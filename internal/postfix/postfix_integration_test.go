//go:build integration && linux

package postfix_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"mailwisp/internal/contentstore"
	"mailwisp/internal/lmtp"
	"mailwisp/internal/message"
)

const (
	postfixImage       = "mailwisp/postfix-integration:3.11.5-r0"
	postfixPackageSpec = "postfix=3.11.5-r0"
	postfixVersion     = "3.11.5"
	postfixSMTPPort    = "25/tcp"
	testRecipient      = "inbox@example.test"
	testEnvelopeSender = "sender@outside.test"
)

var queuedAsPattern = regexp.MustCompile(`queued as ([A-F0-9]+)`)

func TestPostfixLMTPRetrySemantics(t *testing.T) {
	lmtpPort := reserveTCPPort(t)
	postfix := startPostfix(t, lmtpPort)

	assertPostfixVersion(t, postfix.container)
	t.Run("队列跨Postfix重启后重投", func(t *testing.T) {
		testQueueSurvivesRestart(t, postfix, lmtpPort)
	})
	t.Run("LMTP临时失败保留队列并重试", func(t *testing.T) {
		testTemporaryFailureRetries(t, postfix, lmtpPort)
	})
	t.Run("确认丢失允许独立重复消息并复用内容", func(t *testing.T) {
		testLostAcknowledgementDuplicates(t, postfix, lmtpPort)
	})
	t.Run("未知收件人永久拒绝", func(t *testing.T) {
		testUnknownRecipientIsPermanent(t, postfix, lmtpPort)
	})
}

func testQueueSurvivesRestart(t *testing.T, postfix *postfixFixture, lmtpPort int) {
	raw := rawMessage("queue-restart", "Queue restart", "durable queue survives restart")
	queueID := submitSMTP(t, postfix.smtpAddress(t), testEnvelopeSender, testRecipient, raw)
	waitForQueueID(t, postfix.container, queueID, true)

	stopTimeout := 10 * time.Second
	stopContext, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()
	if err := postfix.container.Stop(stopContext, &stopTimeout); err != nil {
		t.Fatalf("stop Postfix container: %v", err)
	}
	startContext, startCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer startCancel()
	if err := postfix.container.Start(startContext); err != nil {
		t.Fatalf("restart Postfix container: %v", err)
	}
	waitForSMTP(t, postfix.smtpAddress(t))
	waitForQueueID(t, postfix.container, queueID, true)

	resolver := newStaticResolver(testRecipient)
	receiver := newRecordingReceiver(0)
	service := startLMTP(t, lmtpPort, resolver, receiver, nil)
	defer service.stop(t)
	flushQueue(t, postfix.container)

	attempt := receiver.next(t)
	assertAttempt(t, attempt, testEnvelopeSender, raw)
	waitForQueueID(t, postfix.container, queueID, false)
}

func testTemporaryFailureRetries(t *testing.T, postfix *postfixFixture, lmtpPort int) {
	resolver := newStaticResolver(testRecipient)
	receiver := newRecordingReceiver(1)
	service := startLMTP(t, lmtpPort, resolver, receiver, nil)
	defer service.stop(t)

	raw := rawMessage("temporary-failure", "Temporary failure", "retry after LMTP 451")
	queueID := submitSMTP(t, postfix.smtpAddress(t), testEnvelopeSender, testRecipient, raw)
	first := receiver.next(t)
	assertAttempt(t, first, testEnvelopeSender, raw)
	waitForQueueID(t, postfix.container, queueID, true)

	flushQueue(t, postfix.container)
	second := receiver.next(t)
	assertAttempt(t, second, testEnvelopeSender, raw)
	if !bytes.Equal(first.raw, second.raw) {
		t.Fatal("Postfix retry changed the queued raw message")
	}
	waitForQueueID(t, postfix.container, queueID, false)
}

func testLostAcknowledgementDuplicates(t *testing.T, postfix *postfixFixture, lmtpPort int) {
	resolver := newStaticResolver(testRecipient)
	contentRoot := t.TempDir()
	store, err := contentstore.Open(contentRoot, contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("open content store: %v", err)
	}
	repository := newMemoryRepository()
	receiver, err := message.NewReceiver(store, repository)
	if err != nil {
		t.Fatalf("create durable receiver: %v", err)
	}
	service := startLMTP(t, lmtpPort, resolver, receiver, wrapFirstDeliveryAcknowledgement)
	defer service.stop(t)

	raw := rawMessage("lost-ack", "Lost acknowledgement", "commit succeeds before acknowledgement is lost")
	queueID := submitSMTP(t, postfix.smtpAddress(t), testEnvelopeSender, testRecipient, raw)
	first := repository.next(t)
	waitForQueueID(t, postfix.container, queueID, true)

	flushQueue(t, postfix.container)
	second := repository.next(t)
	waitForQueueID(t, postfix.container, queueID, false)

	if first.delivery.Content != second.delivery.Content {
		t.Fatalf("duplicate content refs differ: first=%+v second=%+v", first.delivery.Content, second.delivery.Content)
	}
	if len(first.messages) != 1 || len(second.messages) != 1 {
		t.Fatalf("duplicate message counts = %d/%d, want 1/1", len(first.messages), len(second.messages))
	}
	if first.messages[0].ID == second.messages[0].ID {
		t.Fatalf("duplicate deliveries reused message ID %q", first.messages[0].ID)
	}
	if count := countContentObjects(t, contentRoot); count != 1 {
		t.Fatalf("physical content object count = %d, want 1", count)
	}
	if err := store.Verify(context.Background(), first.delivery.Content); err != nil {
		t.Fatalf("verify reused content object: %v", err)
	}
}

func testUnknownRecipientIsPermanent(t *testing.T, postfix *postfixFixture, lmtpPort int) {
	resolver := newStaticResolver(testRecipient)
	receiver := newRecordingReceiver(0)
	service := startLMTP(t, lmtpPort, resolver, receiver, nil)
	defer service.stop(t)

	const unknownRecipient = "missing@example.test"
	raw := rawMessage("unknown-recipient", "Unknown recipient", "this delivery must not be retried")
	queueID := submitSMTP(t, postfix.smtpAddress(t), "", unknownRecipient, raw)
	waitForUnknownLookup(t, resolver)
	waitForQueueID(t, postfix.container, queueID, false)
	waitForContainerLog(t, postfix.container, queueID, "550 5.1.1", "status=bounced")

	select {
	case attempt := <-receiver.attempts:
		t.Fatalf("message receiver was called for permanently rejected recipient: %+v", attempt)
	default:
	}
}

type postfixFixture struct {
	container   testcontainers.Container
	queueVolume string
}

func startPostfix(t *testing.T, lmtpPort int) *postfixFixture {
	t.Helper()
	queueVolume := "mailwisp-postfix-queue-" + uuid.NewString()
	request := testcontainers.ContainerRequest{
		Image:           postfixImage,
		ImagePlatform:   "linux/amd64",
		AlwaysPullImage: false,
		Env: map[string]string{
			"MAILWISP_LMTP_PORT": fmt.Sprintf("%d", lmtpPort),
		},
		ExposedPorts:    []string{postfixSMTPPort},
		HostAccessPorts: []int{lmtpPort},
		Mounts: testcontainers.Mounts(
			testcontainers.VolumeMount(queueVolume, "/var/spool/postfix"),
		),
		WaitingFor: wait.ForListeningPort(postfixSMTPPort).WithStartupTimeout(2 * time.Minute),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: request,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start pinned Postfix integration image %q: %v", postfixImage, err)
	}
	fixture := &postfixFixture{container: container, queueVolume: queueVolume}
	t.Cleanup(func() {
		fixture.writeEvidence(t)
		terminateContext, terminateCancel := context.WithTimeout(context.Background(), time.Minute)
		defer terminateCancel()
		if err := container.Terminate(terminateContext, testcontainers.RemoveVolumes(queueVolume)); err != nil {
			t.Errorf("terminate Postfix integration container: %v", err)
		}
	})
	return fixture
}

func (f *postfixFixture) smtpAddress(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host, err := f.container.Host(ctx)
	if err != nil {
		t.Fatalf("resolve Postfix host: %v", err)
	}
	port, err := f.container.MappedPort(ctx, postfixSMTPPort)
	if err != nil {
		t.Fatalf("resolve Postfix SMTP port: %v", err)
	}
	return net.JoinHostPort(host, port.Port())
}

func (f *postfixFixture) writeEvidence(t *testing.T) {
	t.Helper()
	if !t.Failed() {
		return
	}
	root := os.Getenv("MAILWISP_POSTFIX_EVIDENCE_DIR")
	if root == "" {
		return
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Errorf("create Postfix evidence directory: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	writeEvidenceFile(t, root, "postqueue.txt", execOutputBestEffort(ctx, f.container, "postqueue", "-p"))
	writeEvidenceFile(t, root, "postconf.txt", execOutputBestEffort(ctx, f.container, "postconf", "-n"))
	logs, err := containerLogs(ctx, f.container)
	if err != nil {
		logs = []byte("read container logs: " + err.Error())
	}
	writeEvidenceFile(t, root, "container.log", logs)
}

func writeEvidenceFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Errorf("write Postfix evidence %q: %v", name, err)
	}
}

func assertPostfixVersion(t *testing.T, container testcontainers.Container) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if got := strings.TrimSpace(string(execOutput(t, ctx, container, "postconf", "-h", "mail_version"))); got != postfixVersion {
		t.Fatalf("Postfix version = %q, want %q", got, postfixVersion)
	}
	execOutput(t, ctx, container, "apk", "info", "--exists", postfixPackageSpec)
}

func flushQueue(t *testing.T, container testcontainers.Container) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	execOutput(t, ctx, container, "postqueue", "-f")
}

func waitForQueueID(t *testing.T, container testcontainers.Container, queueID string, present bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output := execOutput(t, ctx, container, "postqueue", "-p")
		cancel()
		contains := bytes.Contains(output, []byte(queueID))
		if contains == present {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue presence for %s = %t, want %t\n%s", queueID, contains, present, output)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func execOutput(t *testing.T, ctx context.Context, container testcontainers.Container, command ...string) []byte {
	t.Helper()
	exitCode, reader, err := container.Exec(ctx, command)
	if err != nil {
		t.Fatalf("execute %q in Postfix container: %v", command, err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read %q output: %v", command, err)
	}
	if exitCode != 0 {
		t.Fatalf("execute %q exit code = %d\n%s", command, exitCode, output)
	}
	return output
}

func execOutputBestEffort(ctx context.Context, container testcontainers.Container, command ...string) []byte {
	exitCode, reader, err := container.Exec(ctx, command)
	if err != nil {
		return []byte(err.Error())
	}
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		return append(output, []byte("\nread output: "+readErr.Error())...)
	}
	if exitCode != 0 {
		return append(output, []byte(fmt.Sprintf("\nexit code: %d", exitCode))...)
	}
	return output
}

func submitSMTP(t *testing.T, address, sender, recipient string, raw []byte) string {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatalf("connect to Postfix SMTP: %v", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatalf("set SMTP deadline: %v", err)
	}
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	expectSMTP(t, reader, 220)
	smtpCommand(t, reader, writer, 250, "EHLO test.integration")
	smtpCommand(t, reader, writer, 250, "MAIL FROM:<%s>", sender)
	smtpCommand(t, reader, writer, 250, "RCPT TO:<%s>", recipient)
	smtpCommand(t, reader, writer, 354, "DATA")
	writeSMTPData(t, writer, raw)
	response := expectSMTP(t, reader, 250)
	match := queuedAsPattern.FindStringSubmatch(response)
	if len(match) != 2 {
		t.Fatalf("SMTP acceptance response has no queue ID: %q", response)
	}
	smtpCommand(t, reader, writer, 221, "QUIT")
	return match[1]
}

func smtpCommand(t *testing.T, reader *bufio.Reader, writer *bufio.Writer, wantCode int, format string, arguments ...any) string {
	t.Helper()
	if _, err := fmt.Fprintf(writer, format+"\r\n", arguments...); err != nil {
		t.Fatalf("write SMTP command: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush SMTP command: %v", err)
	}
	return expectSMTP(t, reader, wantCode)
}

func writeSMTPData(t *testing.T, writer *bufio.Writer, raw []byte) {
	t.Helper()
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	for _, line := range strings.Split(normalized, "\n") {
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		if _, err := fmt.Fprintf(writer, "%s\r\n", line); err != nil {
			t.Fatalf("write SMTP DATA: %v", err)
		}
	}
	if _, err := io.WriteString(writer, ".\r\n"); err != nil {
		t.Fatalf("finish SMTP DATA: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush SMTP DATA: %v", err)
	}
}

func expectSMTP(t *testing.T, reader *bufio.Reader, wantCode int) string {
	t.Helper()
	var response strings.Builder
	wantPrefix := fmt.Sprintf("%03d", wantCode)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SMTP response %d: %v", wantCode, err)
		}
		response.WriteString(line)
		if len(line) < 4 || line[:3] != wantPrefix {
			t.Fatalf("SMTP response = %q, want code %d", line, wantCode)
		}
		if line[3] == ' ' {
			return response.String()
		}
		if line[3] != '-' {
			t.Fatalf("invalid SMTP response separator in %q", line)
		}
	}
}

func waitForSMTP(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			reader := bufio.NewReader(connection)
			line, readErr := reader.ReadString('\n')
			_ = connection.Close()
			if readErr == nil && strings.HasPrefix(line, "220 ") {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Postfix SMTP did not become ready at %s: %v", address, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func rawMessage(id, subject, body string) []byte {
	return []byte(fmt.Sprintf("From: sender@outside.test\r\nTo: inbox@example.test\r\nSubject: %s\r\nMessage-ID: <%s@mailwisp.integration>\r\nX-MailWisp-Test-ID: %s\r\n\r\n%s\r\n", subject, id, id, body))
}

type staticResolver struct {
	acceptedAddress string
	inboxID         message.InboxID
	mu              sync.Mutex
	unknownLookups  int
}

func newStaticResolver(address string) *staticResolver {
	return &staticResolver{acceptedAddress: strings.ToLower(address), inboxID: message.InboxID(uuid.NewString())}
}

func (r *staticResolver) ResolveInbox(_ context.Context, address string) (message.InboxID, error) {
	if strings.ToLower(address) == r.acceptedAddress {
		return r.inboxID, nil
	}
	r.mu.Lock()
	r.unknownLookups++
	r.mu.Unlock()
	return "", message.ErrInboxNotFound
}

func waitForUnknownLookup(t *testing.T, resolver *staticResolver) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resolver.mu.Lock()
		lookups := resolver.unknownLookups
		resolver.mu.Unlock()
		if lookups > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Postfix did not ask the Go LMTP server to resolve the unknown recipient")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type recordedAttempt struct {
	envelopeSender string
	recipients     []message.InboxID
	raw            []byte
}

type recordingReceiver struct {
	mu                sync.Mutex
	temporaryFailures int
	attempts          chan recordedAttempt
}

func newRecordingReceiver(temporaryFailures int) *recordingReceiver {
	return &recordingReceiver{temporaryFailures: temporaryFailures, attempts: make(chan recordedAttempt, 8)}
}

func (r *recordingReceiver) Receive(_ context.Context, request message.ReceiveRequest) (message.Receipt, error) {
	raw, err := io.ReadAll(request.Raw)
	if err != nil {
		return message.Receipt{}, err
	}
	attempt := recordedAttempt{
		envelopeSender: request.EnvelopeSender,
		recipients:     append([]message.InboxID(nil), request.Recipients...),
		raw:            append([]byte(nil), raw...),
	}
	r.attempts <- attempt
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.temporaryFailures > 0 {
		r.temporaryFailures--
		return message.Receipt{}, errors.New("injected temporary persistence failure")
	}
	return message.Receipt{}, nil
}

func (r *recordingReceiver) next(t *testing.T) recordedAttempt {
	t.Helper()
	select {
	case attempt := <-r.attempts:
		return attempt
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for LMTP delivery attempt")
		return recordedAttempt{}
	}
}

func assertAttempt(t *testing.T, attempt recordedAttempt, sender string, raw []byte) {
	t.Helper()
	if attempt.envelopeSender != sender {
		t.Fatalf("LMTP envelope sender = %q, want %q", attempt.envelopeSender, sender)
	}
	if len(attempt.recipients) != 1 {
		t.Fatalf("LMTP recipient count = %d, want 1", len(attempt.recipients))
	}
	for _, marker := range [][]byte{
		bytes.TrimSpace(raw),
		[]byte("X-MailWisp-Test-ID:"),
	} {
		if !bytes.Contains(attempt.raw, marker) {
			t.Fatalf("LMTP raw message does not contain expected marker %q\n%s", marker, attempt.raw)
		}
	}
}

type lmtpService struct {
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
}

func startLMTP(t *testing.T, port int, resolver lmtp.InboxResolver, receiver lmtp.MessageReceiver, wrap func(net.Listener) net.Listener) *lmtpService {
	t.Helper()
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("listen for Go LMTP integration server: %v", err)
	}
	if wrap != nil {
		listener = wrap(listener)
	}
	options := lmtp.DefaultOptions("mailwisp.postfix.integration")
	options.SessionTimeout = 30 * time.Second
	options.DeliveryTimeout = 10 * time.Second
	server, err := lmtp.NewServer(options, resolver, receiver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		_ = listener.Close()
		t.Fatalf("create Go LMTP integration server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &lmtpService{cancel: cancel, done: make(chan error, 1)}
	go func() {
		service.done <- server.Serve(ctx, listener)
	}()
	t.Cleanup(func() { service.stop(t) })
	return service
}

func (s *lmtpService) stop(t *testing.T) {
	t.Helper()
	s.once.Do(func() {
		s.cancel()
		select {
		case err := <-s.done:
			if err != nil {
				t.Errorf("stop Go LMTP integration server: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Go LMTP integration server did not stop")
		}
	})
}

type deliveryAckDropListener struct {
	net.Listener
	once sync.Once
}

func wrapFirstDeliveryAcknowledgement(listener net.Listener) net.Listener {
	return &deliveryAckDropListener{Listener: listener}
}

func (l *deliveryAckDropListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	drop := false
	l.once.Do(func() { drop = true })
	if !drop {
		return connection, nil
	}
	return &deliveryAckDropConn{Conn: connection}, nil
}

type deliveryAckDropConn struct {
	net.Conn
}

func (c *deliveryAckDropConn) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte(" delivered\r\n")) {
		_ = c.Conn.Close()
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(data)
}

type recordedCommit struct {
	delivery message.Delivery
	messages []message.StoredMessage
}

type memoryRepository struct {
	commits chan recordedCommit
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{commits: make(chan recordedCommit, 4)}
}

func (r *memoryRepository) CommitDelivery(_ context.Context, delivery message.Delivery) ([]message.StoredMessage, error) {
	messages := make([]message.StoredMessage, len(delivery.Recipients))
	for index, inboxID := range delivery.Recipients {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("create message ID: %w", err)
		}
		messages[index] = message.StoredMessage{ID: message.MessageID(id.String()), InboxID: inboxID}
	}
	delivery.Recipients = append([]message.InboxID(nil), delivery.Recipients...)
	r.commits <- recordedCommit{delivery: delivery, messages: append([]message.StoredMessage(nil), messages...)}
	return messages, nil
}

func (r *memoryRepository) next(t *testing.T) recordedCommit {
	t.Helper()
	select {
	case commit := <-r.commits:
		return commit
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for durable delivery commit")
		return recordedCommit{}
	}
}

func countContentObjects(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(filepath.Join(root, "objects", "sha256"), func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk content objects: %v", err)
	}
	return count
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve LMTP port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved LMTP port: %v", err)
	}
	return port
}

func waitForContainerLog(t *testing.T, container testcontainers.Container, markers ...string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		logs, err := containerLogs(ctx, container)
		cancel()
		if err == nil {
			matched := true
			for _, marker := range markers {
				if !bytes.Contains(logs, []byte(marker)) {
					matched = false
					break
				}
			}
			if matched {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Postfix logs did not contain markers %q: %v", markers, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func containerLogs(ctx context.Context, container testcontainers.Container) ([]byte, error) {
	reader, err := container.Logs(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
