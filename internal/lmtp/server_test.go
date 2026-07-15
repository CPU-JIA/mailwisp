package lmtp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mailwisp/internal/message"
)

const (
	firstInboxID  = message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb12")
	secondInboxID = message.InboxID("018f26e5-8f04-7b44-8ba2-4a8f434dcb13")
)

func TestSessionDeliversDotDecodedDataToMultipleRecipients(t *testing.T) {
	t.Parallel()

	resolver := &resolverStub{results: map[string]message.InboxID{
		"first@example.com":  firstInboxID,
		"second@example.com": secondInboxID,
	}}
	var received message.ReceiveRequest
	receiver := &receiverStub{receive: func(_ context.Context, request message.ReceiveRequest) (message.Receipt, error) {
		received = request
		raw, err := io.ReadAll(request.Raw)
		if err != nil {
			return message.Receipt{}, err
		}
		received.Raw = bytes.NewReader(raw)
		return message.Receipt{Messages: []message.StoredMessage{
			{ID: "message-1", InboxID: firstInboxID},
			{ID: "message-2", InboxID: secondInboxID},
		}}, nil
	}}
	server := newTestServer(t, resolver, receiver)

	runSession(t, server, func(client *lmtpClient) {
		client.expectCode(220)
		client.send("LHLO postfix.example")
		capabilities := client.readResponse(250)
		if !strings.Contains(capabilities, "SIZE 1024") || !strings.Contains(capabilities, "8BITMIME") {
			t.Fatalf("LHLO capabilities = %q", capabilities)
		}
		client.send("MAIL FROM:<SENDER@EXAMPLE.NET> SIZE=64 BODY=8BITMIME")
		client.expectCode(250)
		client.send("RCPT TO:<FIRST@EXAMPLE.COM>")
		client.expectCode(250)
		client.send("RCPT TO:<FIRST@EXAMPLE.COM>")
		client.expectCode(250)
		client.send("RCPT TO:<SECOND@EXAMPLE.COM>")
		client.expectCode(250)
		client.send("DATA")
		client.expectCode(354)
		client.writeRaw("Subject: test\r\n\r\n..leading dot\r\nbody\r\n.\r\n")
		client.expectCode(250)
		client.expectCode(250)
		client.send("NOOP")
		client.expectCode(250)
		client.send("QUIT")
		client.expectCode(221)
	})

	if received.EnvelopeSender != "sender@example.net" {
		t.Fatalf("EnvelopeSender = %q", received.EnvelopeSender)
	}
	if len(received.Recipients) != 2 || received.Recipients[0] != firstInboxID || received.Recipients[1] != secondInboxID {
		t.Fatalf("Recipients = %v", received.Recipients)
	}
	raw, err := io.ReadAll(received.Raw)
	if err != nil {
		t.Fatalf("ReadAll(captured raw) error = %v", err)
	}
	wantRaw := "Subject: test\r\n\r\n.leading dot\r\nbody\r\n"
	if string(raw) != wantRaw {
		t.Fatalf("raw message = %q, want %q", raw, wantRaw)
	}
	if resolver.calls != 3 {
		t.Fatalf("resolver calls = %d, want 3", resolver.calls)
	}
}

func TestSessionRejectsUnknownAndTemporaryRecipients(t *testing.T) {
	t.Parallel()

	temporaryErr := errors.New("database unavailable")
	resolver := &resolverStub{
		results: map[string]message.InboxID{"known@example.com": firstInboxID},
		errors: map[string]error{
			"missing@example.com":   message.ErrInboxNotFound,
			"temporary@example.com": temporaryErr,
		},
	}
	receiver := &receiverStub{receive: func(context.Context, message.ReceiveRequest) (message.Receipt, error) {
		return message.Receipt{}, errors.New("receiver must not be called")
	}}
	server := newTestServer(t, resolver, receiver)

	runSession(t, server, func(client *lmtpClient) {
		client.expectCode(220)
		client.send("LHLO client")
		client.readResponse(250)
		client.send("MAIL FROM:<sender@example.com>")
		client.expectCode(250)
		client.send("RCPT TO:<missing@example.com>")
		client.expectCode(550)
		client.send("RCPT TO:<temporary@example.com>")
		client.expectCode(451)
		client.send("DATA")
		client.expectCode(503)
		client.send("QUIT")
		client.expectCode(221)
	})
}

func TestSessionMapsTemporaryDeliveryFailurePerRecipient(t *testing.T) {
	t.Parallel()

	receiveErr := errors.New("postgres commit failed")
	server := newTestServer(t,
		&resolverStub{results: map[string]message.InboxID{
			"first@example.com":  firstInboxID,
			"second@example.com": secondInboxID,
		}},
		&receiverStub{receive: func(_ context.Context, request message.ReceiveRequest) (message.Receipt, error) {
			_, _ = io.ReadAll(request.Raw)
			return message.Receipt{}, receiveErr
		}},
	)

	runSession(t, server, func(client *lmtpClient) {
		client.expectCode(220)
		client.send("LHLO client")
		client.readResponse(250)
		client.send("MAIL FROM:<sender@example.com>")
		client.expectCode(250)
		client.send("RCPT TO:<first@example.com>")
		client.expectCode(250)
		client.send("RCPT TO:<second@example.com>")
		client.expectCode(250)
		client.send("DATA")
		client.expectCode(354)
		client.writeRaw("Subject: retry\r\n\r\nbody\r\n.\r\n")
		client.expectCode(451)
		client.expectCode(451)
		client.send("QUIT")
		client.expectCode(221)
	})
}

func TestSessionDrainsOversizedDataAndContinues(t *testing.T) {
	t.Parallel()

	options := testOptions()
	options.MaxMessageBytes = 10
	server, err := NewServer(options,
		&resolverStub{results: map[string]message.InboxID{"first@example.com": firstInboxID}},
		&receiverStub{receive: func(_ context.Context, request message.ReceiveRequest) (message.Receipt, error) {
			_, err := io.ReadAll(request.Raw)
			return message.Receipt{}, err
		}},
		testLMTPLogger(),
	)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	runSession(t, server, func(client *lmtpClient) {
		client.expectCode(220)
		client.send("LHLO client")
		client.readResponse(250)
		client.send("MAIL FROM:<sender@example.com>")
		client.expectCode(250)
		client.send("RCPT TO:<first@example.com>")
		client.expectCode(250)
		client.send("DATA")
		client.expectCode(354)
		client.writeRaw("12345678901\r\nstill drained\r\n.\r\n")
		client.expectCode(552)
		client.send("NOOP")
		client.expectCode(250)
		client.send("QUIT")
		client.expectCode(221)
	})
}

func TestSessionDrainsOverlongDataLineAndContinues(t *testing.T) {
	t.Parallel()

	options := testOptions()
	options.MaxDataLineBytes = 8
	server, err := NewServer(options,
		&resolverStub{results: map[string]message.InboxID{"first@example.com": firstInboxID}},
		&receiverStub{receive: func(_ context.Context, request message.ReceiveRequest) (message.Receipt, error) {
			_, err := io.ReadAll(request.Raw)
			return message.Receipt{}, err
		}},
		testLMTPLogger(),
	)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	runSession(t, server, func(client *lmtpClient) {
		client.expectCode(220)
		client.send("LHLO client")
		client.readResponse(250)
		client.send("MAIL FROM:<sender@example.com>")
		client.expectCode(250)
		client.send("RCPT TO:<first@example.com>")
		client.expectCode(250)
		client.send("DATA")
		client.expectCode(354)
		client.writeRaw("123456789\r\n.\r\n")
		client.expectCode(554)
		client.send("NOOP")
		client.expectCode(250)
		client.send("QUIT")
		client.expectCode(221)
	})
}

func TestSessionNullSenderStillRejectsNestedMail(t *testing.T) {
	t.Parallel()

	server := newTestServer(t,
		&resolverStub{results: map[string]message.InboxID{"first@example.com": firstInboxID}},
		&receiverStub{},
	)
	runSession(t, server, func(client *lmtpClient) {
		client.expectCode(220)
		client.send("LHLO client")
		client.readResponse(250)
		client.send("MAIL FROM:<>")
		client.expectCode(250)
		client.send("MAIL FROM:<second@example.com>")
		client.expectCode(503)
		client.send("RSET")
		client.expectCode(250)
		client.send("QUIT")
		client.expectCode(221)
	})
}

func TestSessionRejectsDeclaredOversizeBeforeData(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &resolverStub{}, &receiverStub{})
	runSession(t, server, func(client *lmtpClient) {
		client.expectCode(220)
		client.send("LHLO client")
		client.readResponse(250)
		client.send("MAIL FROM:<sender@example.com> SIZE=1025")
		client.expectCode(552)
		client.send("RCPT TO:<first@example.com>")
		client.expectCode(503)
		client.send("QUIT")
		client.expectCode(221)
	})
}

func TestSessionEnforcesRecipientLimitButAcceptsDuplicate(t *testing.T) {
	t.Parallel()

	options := testOptions()
	options.MaxRecipients = 1
	server, err := NewServer(options,
		&resolverStub{results: map[string]message.InboxID{
			"first@example.com":  firstInboxID,
			"alias@example.com":  firstInboxID,
			"second@example.com": secondInboxID,
		}},
		&receiverStub{},
		testLMTPLogger(),
	)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	runSession(t, server, func(client *lmtpClient) {
		client.expectCode(220)
		client.send("LHLO client")
		client.readResponse(250)
		client.send("MAIL FROM:<sender@example.com>")
		client.expectCode(250)
		client.send("RCPT TO:<first@example.com>")
		client.expectCode(250)
		client.send("RCPT TO:<alias@example.com>")
		client.expectCode(250)
		client.send("RCPT TO:<second@example.com>")
		client.expectCode(452)
		client.send("QUIT")
		client.expectCode(221)
	})
}

func TestSessionRecoversAfterOverlongCommand(t *testing.T) {
	t.Parallel()

	options := testOptions()
	options.MaxCommandBytes = 16
	server, err := NewServer(options, &resolverStub{}, &receiverStub{}, testLMTPLogger())
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	runSession(t, server, func(client *lmtpClient) {
		client.expectCode(220)
		client.send(strings.Repeat("X", 32))
		client.expectCode(500)
		client.send("NOOP")
		client.expectCode(250)
		client.send("QUIT")
		client.expectCode(221)
	})
}

func TestServerRejectsSessionsAboveBound(t *testing.T) {
	options := testOptions()
	options.MaxSessions = 1
	server, err := NewServer(options, &resolverStub{}, &receiverStub{}, testLMTPLogger())
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveError := make(chan error, 1)
	go func() { serveError <- server.Serve(ctx, listener) }()

	first, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		cancel()
		t.Fatalf("first Dial() error = %v", err)
	}
	defer first.Close()
	firstReader := bufio.NewReader(first)
	if line, err := firstReader.ReadString('\n'); err != nil || !strings.HasPrefix(line, "220 ") {
		cancel()
		t.Fatalf("first greeting = %q, error = %v", line, err)
	}

	second, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		cancel()
		t.Fatalf("second Dial() error = %v", err)
	}
	secondReader := bufio.NewReader(second)
	line, readErr := secondReader.ReadString('\n')
	_ = second.Close()
	if readErr != nil || !strings.HasPrefix(line, "421 ") {
		cancel()
		t.Fatalf("second response = %q, error = %v", line, readErr)
	}

	cancel()
	select {
	case err := <-serveError:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not stop after cancellation")
	}
}

func TestParseEnvelopePaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		argument  string
		mail      bool
		want      string
		wantSize  int64
		wantError bool
	}{
		{name: "mail", argument: "FROM:<User+tag@Example.COM> SIZE=42 BODY=8BITMIME", mail: true, want: "user+tag@example.com", wantSize: 42},
		{name: "bounce", argument: "FROM:<>", mail: true, want: ""},
		{name: "recipient", argument: "TO:<User@Example.COM>", want: "user@example.com"},
		{name: "SMTPUTF8", argument: "TO:<测试@example.com>", wantError: true},
		{name: "double dot", argument: "TO:<a..b@example.com>", wantError: true},
		{name: "unknown parameter", argument: "FROM:<a@example.com> RET=FULL", mail: true, wantError: true},
		{name: "duplicate size", argument: "FROM:<a@example.com> SIZE=1 SIZE=2", mail: true, wantError: true},
		{name: "recipient parameter", argument: "TO:<a@example.com> NOTIFY=SUCCESS", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got string
			var size int64
			var err error
			if test.mail {
				got, size, err = parseMailFrom(test.argument)
			} else {
				got, err = parseRecipient(test.argument)
			}
			if test.wantError && err == nil {
				t.Fatal("parse error = nil, want error")
			}
			if !test.wantError && (err != nil || got != test.want || size != test.wantSize) {
				t.Fatalf("parse = %q/%d/%v, want %q/%d", got, size, err, test.want, test.wantSize)
			}
		})
	}
}

func TestNewServerValidatesOptions(t *testing.T) {
	t.Parallel()

	valid := testOptions()
	resolver := &resolverStub{}
	receiver := &receiverStub{}
	if _, err := NewServer(valid, resolver, receiver, testLMTPLogger()); err != nil {
		t.Fatalf("NewServer(valid) error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{name: "hostname", mutate: func(options *Options) { options.Hostname = "" }},
		{name: "message bytes", mutate: func(options *Options) { options.MaxMessageBytes = 0 }},
		{name: "command bytes", mutate: func(options *Options) { options.MaxCommandBytes = 0 }},
		{name: "data line bytes", mutate: func(options *Options) { options.MaxDataLineBytes = 0 }},
		{name: "recipients", mutate: func(options *Options) { options.MaxRecipients = 0 }},
		{name: "sessions", mutate: func(options *Options) { options.MaxSessions = 0 }},
		{name: "session timeout", mutate: func(options *Options) { options.SessionTimeout = 0 }},
		{name: "delivery timeout", mutate: func(options *Options) { options.DeliveryTimeout = 0 }},
		{name: "delivery exceeds session", mutate: func(options *Options) { options.DeliveryTimeout = options.SessionTimeout + time.Second }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			test.mutate(&options)
			if _, err := NewServer(options, resolver, receiver, testLMTPLogger()); err == nil {
				t.Fatal("NewServer() error = nil, want error")
			}
		})
	}
	if _, err := NewServer(valid, nil, receiver, testLMTPLogger()); err == nil {
		t.Fatal("NewServer(nil resolver) error = nil, want error")
	}
	if _, err := NewServer(valid, resolver, nil, testLMTPLogger()); err == nil {
		t.Fatal("NewServer(nil receiver) error = nil, want error")
	}
	if _, err := NewServer(valid, resolver, receiver, nil); err == nil {
		t.Fatal("NewServer(nil logger) error = nil, want error")
	}
}

func newTestServer(t *testing.T, resolver InboxResolver, receiver MessageReceiver) *Server {
	t.Helper()
	server, err := NewServer(testOptions(), resolver, receiver, testLMTPLogger())
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func testLMTPLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testOptions() Options {
	return Options{
		Hostname:         "mailwisp.test",
		MaxMessageBytes:  1024,
		MaxCommandBytes:  1024,
		MaxDataLineBytes: 1024,
		MaxRecipients:    2,
		MaxSessions:      2,
		SessionTimeout:   2 * time.Second,
		DeliveryTimeout:  2 * time.Second,
	}
}

func runSession(t *testing.T, server *Server, script func(*lmtpClient)) {
	t.Helper()
	serverConnection, clientConnection := net.Pipe()
	serverError := make(chan error, 1)
	go func() {
		serverError <- server.serveConnection(context.Background(), serverConnection)
	}()
	client := &lmtpClient{
		t:          t,
		connection: clientConnection,
		reader:     bufio.NewReader(clientConnection),
		writer:     bufio.NewWriter(clientConnection),
	}
	script(client)
	_ = clientConnection.Close()
	select {
	case err := <-serverError:
		if err != nil {
			t.Fatalf("serveConnection() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serveConnection() did not stop")
	}
}

type lmtpClient struct {
	t          *testing.T
	connection net.Conn
	reader     *bufio.Reader
	writer     *bufio.Writer
}

func (c *lmtpClient) send(command string) {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.writer, "%s\r\n", command); err != nil {
		c.t.Fatalf("send %q: %v", command, err)
	}
	if err := c.writer.Flush(); err != nil {
		c.t.Fatalf("flush %q: %v", command, err)
	}
}

func (c *lmtpClient) writeRaw(data string) {
	c.t.Helper()
	if _, err := io.WriteString(c.writer, data); err != nil {
		c.t.Fatalf("write raw DATA: %v", err)
	}
	if err := c.writer.Flush(); err != nil {
		c.t.Fatalf("flush raw DATA: %v", err)
	}
}

func (c *lmtpClient) expectCode(want int) string {
	c.t.Helper()
	return c.readResponse(want)
}

func (c *lmtpClient) readResponse(want int) string {
	c.t.Helper()
	var lines []string
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			c.t.Fatalf("read response %d: %v", want, err)
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 4 {
			c.t.Fatalf("short LMTP response %q", line)
		}
		code, err := strconv.Atoi(line[:3])
		if err != nil || code != want {
			c.t.Fatalf("LMTP response = %q, want code %d", line, want)
		}
		lines = append(lines, line)
		if line[3] == ' ' {
			return strings.Join(lines, "\n")
		}
		if line[3] != '-' {
			c.t.Fatalf("invalid LMTP response separator in %q", line)
		}
	}
}

type resolverStub struct {
	mu      sync.Mutex
	results map[string]message.InboxID
	errors  map[string]error
	calls   int
}

func (s *resolverStub) ResolveInbox(_ context.Context, address string) (message.InboxID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if err := s.errors[address]; err != nil {
		return "", err
	}
	if inboxID, ok := s.results[address]; ok {
		return inboxID, nil
	}
	return "", message.ErrInboxNotFound
}

type receiverStub struct {
	receive func(context.Context, message.ReceiveRequest) (message.Receipt, error)
}

func (s *receiverStub) Receive(ctx context.Context, request message.ReceiveRequest) (message.Receipt, error) {
	if s.receive == nil {
		return message.Receipt{}, errors.New("unexpected receiver call")
	}
	return s.receive(ctx, request)
}
