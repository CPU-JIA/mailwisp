//go:build integration

package postgres

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	backuppkg "mailwisp/internal/backup"
	"mailwisp/internal/contentstore"
	"mailwisp/internal/message"
)

func TestBackupToolBundleRoundTrip(t *testing.T) {
	assertPostgreSQLBackupTools(t)

	sourcePool := newIntegrationPool(t)
	resetIntegrationDatabase(t, sourcePool)
	sourceStore, err := contentstore.Open(filepath.Join(t.TempDir(), "source-content"), contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open(source) error = %v", err)
	}
	sourceRepository := newIntegrationRepository(t, sourcePool)
	inboxID := createInbox(t, sourcePool, "backup@example.com")
	receiver, err := message.NewReceiver(sourceStore, sourceRepository)
	if err != nil {
		t.Fatalf("message.NewReceiver() error = %v", err)
	}
	raw := []byte("From: sender@example.net\r\nTo: backup@example.com\r\nSubject: Backup\r\n\r\nFast mail. Zero trace.\r\n")
	receipt, err := receiver.Receive(context.Background(), message.ReceiveRequest{
		EnvelopeSender: "sender@example.net",
		Recipients:     []message.InboxID{inboxID},
		Raw:            bytes.NewReader(raw),
	})
	if err != nil {
		t.Fatalf("Receive(source) error = %v", err)
	}
	compatibilityRepository, err := NewCloudflareTempRepository(sourcePool)
	if err != nil {
		t.Fatal(err)
	}
	compatibilityInboxID, err := compatibilityRepository.EnsureInboxID(context.Background(), inboxID)
	if err != nil {
		t.Fatalf("EnsureInboxID(source) error = %v", err)
	}
	compatibilityMessageIDs, err := compatibilityRepository.EnsureMessageIDs(context.Background(), inboxID, []message.MessageID{receipt.Messages[0].ID})
	if err != nil {
		t.Fatalf("EnsureMessageIDs(source) error = %v", err)
	}
	compatibilityMessageID := compatibilityMessageIDs[receipt.Messages[0].ID]

	sourceTool, err := NewBackupTool(integrationDataSourceName, sourcePool)
	if err != nil {
		t.Fatalf("NewBackupTool(source) error = %v", err)
	}
	bundleRoot := filepath.Join(t.TempDir(), "mailwisp-backup")
	createdAt := time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC)
	created, err := backuppkg.Create(context.Background(), bundleRoot, createdAt, sourceTool, sourceStore)
	if err != nil {
		t.Fatalf("backup.Create() error = %v", err)
	}
	if _, err := backuppkg.Verify(context.Background(), bundleRoot); err != nil {
		t.Fatalf("backup.Verify() error = %v", err)
	}
	sourcePool.Close()

	t.Cleanup(func() { recreateIntegrationSchema(t) })
	dropIntegrationSchema(t)

	restorePool := newIntegrationPool(t)
	restoreTool, err := NewBackupTool(integrationDataSourceName, restorePool)
	if err != nil {
		t.Fatalf("NewBackupTool(restore) error = %v", err)
	}
	restoredContentRoot := filepath.Join(t.TempDir(), "restored-content")
	restored, err := backuppkg.Restore(context.Background(), bundleRoot, restoredContentRoot, restoreTool)
	if err != nil {
		t.Fatalf("backup.Restore() error = %v", err)
	}
	if restored != created {
		t.Fatalf("restored manifest = %+v, want %+v", restored, created)
	}
	assertCounts(t, restorePool, 1, 1)
	restoredCompatibilityRepository, err := NewCloudflareTempRepository(restorePool)
	if err != nil {
		t.Fatal(err)
	}
	restoredInboxID, err := restoredCompatibilityRepository.EnsureInboxID(context.Background(), inboxID)
	if err != nil || restoredInboxID != compatibilityInboxID {
		t.Fatalf("EnsureInboxID(restored) = %d, error = %v", restoredInboxID, err)
	}
	restoredMessageIDs, err := restoredCompatibilityRepository.EnsureMessageIDs(context.Background(), inboxID, []message.MessageID{receipt.Messages[0].ID})
	if err != nil || restoredMessageIDs[receipt.Messages[0].ID] != compatibilityMessageID {
		t.Fatalf("EnsureMessageIDs(restored) = %+v, error = %v", restoredMessageIDs, err)
	}

	restoredStore, err := contentstore.Open(restoredContentRoot, contentstore.Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("contentstore.Open(restored) error = %v", err)
	}
	if err := restoredStore.Verify(context.Background(), receipt.Content); err != nil {
		t.Fatalf("Verify(restored content) error = %v", err)
	}
	file, err := restoredStore.OpenContent(receipt.Content)
	if err != nil {
		t.Fatalf("OpenContent(restored) error = %v", err)
	}
	gotRaw, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read restored content error = %v, close = %v", readErr, closeErr)
	}
	if !bytes.Equal(gotRaw, raw) {
		t.Fatalf("restored content = %q, want %q", gotRaw, raw)
	}
}

func assertPostgreSQLBackupTools(t *testing.T) {
	t.Helper()
	for _, name := range []string{pgDumpExecutable, pgRestoreExecutable} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("%s is required for integration backup verification: %v", name, err)
		}
	}
}

func dropIntegrationSchema(t *testing.T) {
	t.Helper()
	connection, err := pgx.Connect(context.Background(), integrationDataSourceName)
	if err != nil {
		t.Fatalf("connect to drop integration schema: %v", err)
	}
	defer connection.Close(context.Background())
	if _, err := connection.Exec(context.Background(), "DROP SCHEMA public CASCADE"); err != nil {
		t.Fatalf("drop integration schema: %v", err)
	}
	if _, err := connection.Exec(context.Background(), "CREATE SCHEMA public"); err != nil {
		t.Fatalf("create empty integration schema: %v", err)
	}
}

func recreateIntegrationSchema(t *testing.T) {
	t.Helper()
	dropIntegrationSchema(t)
	if err := Migrate(context.Background(), integrationDataSourceName); err != nil {
		t.Fatalf("restore integration schema migrations: %v", err)
	}
}
