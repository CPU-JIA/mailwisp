//go:build integration

package postgres

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"mailwisp/internal/contentstore"
	"mailwisp/internal/message"
)

const (
	deliveryCrashHelperEnvironment = "MAILWISP_DELIVERY_CRASH_HELPER"
	deliveryCrashDSNEnvironment    = "MAILWISP_DELIVERY_CRASH_DSN"
	deliveryCrashRootEnvironment   = "MAILWISP_DELIVERY_CRASH_ROOT"
	deliveryCrashInboxEnvironment  = "MAILWISP_DELIVERY_CRASH_INBOX"
	deliveryCrashStageEnvironment  = "MAILWISP_DELIVERY_CRASH_STAGE"
)

var deliveryCrashRaw = []byte("From: sender@example.net\r\nTo: crash@example.com\r\nSubject: Crash Recovery\r\n\r\nFast mail. Zero trace.\r\n")

func TestDeliveryCrashRecovery(t *testing.T) {
	tests := []struct {
		stage        deliveryCommitStage
		wantContent  int
		wantMessages int
	}{
		{stage: deliveryCommitStageBefore},
		{stage: deliveryCommitStageAfter, wantContent: 1, wantMessages: 1},
	}
	for _, test := range tests {
		t.Run(string(test.stage), func(t *testing.T) {
			pool := newIntegrationPool(t)
			resetIntegrationDatabase(t, pool)
			inboxID := createInbox(t, pool, "crash@example.com")
			contentRoot := filepath.Join(t.TempDir(), "content")
			killDeliveryHelperAtStage(t, integrationDataSourceName, contentRoot, inboxID, test.stage)
			waitForDatabaseCounts(t, pool, test.wantContent, test.wantMessages)

			store, err := contentstore.Open(contentRoot, contentstore.Options{MaxBytes: 1 << 20})
			if err != nil {
				t.Fatalf("contentstore.Open(after crash) error = %v", err)
			}
			repository := newIntegrationRepository(t, pool)
			if test.stage == deliveryCommitStageBefore {
				summary, err := store.Reconcile(context.Background(), repository, contentstore.ReconcileOptions{
					BatchSize:     1,
					RepairOrphans: true,
				}, nil)
				if err != nil {
					t.Fatalf("Reconcile(after pre-commit crash) error = %v", err)
				}
				if summary.Orphans != 1 || summary.RepairedOrphans != 1 || summary.Unresolved() != 0 {
					t.Fatalf("Reconcile(after pre-commit crash) summary = %+v", summary)
				}
				assertCounts(t, pool, 0, 0)
				return
			}

			var ref message.ContentRef
			if err := pool.QueryRow(context.Background(), "SELECT content_key, size_bytes FROM mail_contents").Scan(&ref.Key, &ref.SizeBytes); err != nil {
				t.Fatalf("read committed content metadata: %v", err)
			}
			if err := store.Verify(context.Background(), ref); err != nil {
				t.Fatalf("Verify(after post-commit crash) error = %v", err)
			}
			receiver, err := message.NewReceiver(store, repository)
			if err != nil {
				t.Fatalf("message.NewReceiver() error = %v", err)
			}
			if _, err := receiver.Receive(context.Background(), message.ReceiveRequest{
				EnvelopeSender: "sender@example.net",
				Recipients:     []message.InboxID{inboxID},
				Raw:            bytes.NewReader(deliveryCrashRaw),
			}); err != nil {
				t.Fatalf("Receive(retry after post-commit crash) error = %v", err)
			}
			assertCounts(t, pool, 1, 2)
		})
	}
}

func TestDeliveryCrashHelper(t *testing.T) {
	if os.Getenv(deliveryCrashHelperEnvironment) != "1" {
		return
	}
	pool, err := pgxpool.New(context.Background(), os.Getenv(deliveryCrashDSNEnvironment))
	if err != nil {
		t.Fatalf("pgxpool.New(helper) error = %v", err)
	}
	defer pool.Close()
	repository, err := NewDeliveryRepository(pool)
	if err != nil {
		t.Fatalf("NewDeliveryRepository(helper) error = %v", err)
	}
	target := deliveryCommitStage(os.Getenv(deliveryCrashStageEnvironment))
	repository.commitObserver = func(stage deliveryCommitStage) {
		if stage != target {
			return
		}
		_, _ = fmt.Fprintf(os.Stdout, "CRASH_STAGE=%s\n", stage)
		_ = os.Stdout.Sync()
		select {}
	}
	store, err := contentstore.Open(os.Getenv(deliveryCrashRootEnvironment), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open(helper) error = %v", err)
	}
	receiver, err := message.NewReceiver(store, repository)
	if err != nil {
		t.Fatalf("message.NewReceiver(helper) error = %v", err)
	}
	if _, err := receiver.Receive(context.Background(), message.ReceiveRequest{
		EnvelopeSender: "sender@example.net",
		Recipients:     []message.InboxID{message.InboxID(os.Getenv(deliveryCrashInboxEnvironment))},
		Raw:            bytes.NewReader(deliveryCrashRaw),
	}); err != nil {
		t.Fatalf("Receive(helper) error = %v", err)
	}
	t.Fatal("delivery crash helper completed without reaching target stage")
}

func killDeliveryHelperAtStage(
	t *testing.T,
	dsn string,
	contentRoot string,
	inboxID message.InboxID,
	stage deliveryCommitStage,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDeliveryCrashHelper$", "-test.v")
	command.Env = append(os.Environ(),
		deliveryCrashHelperEnvironment+"=1",
		deliveryCrashDSNEnvironment+"="+dsn,
		deliveryCrashRootEnvironment+"="+contentRoot,
		deliveryCrashInboxEnvironment+"="+string(inboxID),
		deliveryCrashStageEnvironment+"="+string(stage),
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start delivery crash helper: %v", err)
	}

	reached := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "CRASH_STAGE="+string(stage) {
				reached <- nil
				return
			}
		}
		if err := scanner.Err(); err != nil {
			reached <- err
			return
		}
		reached <- errors.New("delivery crash helper exited before target stage")
	}()

	select {
	case err := <-reached:
		if err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("wait for delivery crash stage %q: %v; stderr=%s", stage, err, stderr.String())
		}
	case <-ctx.Done():
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("wait for delivery crash stage %q: %v; stderr=%s", stage, ctx.Err(), stderr.String())
	}
	if err := command.Process.Kill(); err != nil {
		_ = command.Wait()
		t.Fatalf("kill delivery crash helper at %q: %v", stage, err)
	}
	if err := command.Wait(); err == nil {
		t.Fatalf("delivery crash helper at %q exited successfully, want forced termination", stage)
	}
}

func waitForDatabaseCounts(t *testing.T, pool *pgxpool.Pool, wantContent, wantMessages int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var contentCount, messageCount int
		contentErr := pool.QueryRow(context.Background(), "SELECT count(*) FROM mail_contents").Scan(&contentCount)
		messageErr := pool.QueryRow(context.Background(), "SELECT count(*) FROM messages").Scan(&messageCount)
		if contentErr == nil && messageErr == nil && contentCount == wantContent && messageCount == wantMessages {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("database counts after crash = %d/%d, errors = %v/%v, want %d/%d", contentCount, messageCount, contentErr, messageErr, wantContent, wantMessages)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
