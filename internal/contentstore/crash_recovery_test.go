package contentstore

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	contentCrashHelperEnvironment = "MAILWISP_CONTENT_CRASH_HELPER"
	contentCrashRootEnvironment   = "MAILWISP_CONTENT_CRASH_ROOT"
	contentCrashStageEnvironment  = "MAILWISP_CONTENT_CRASH_STAGE"
)

var contentCrashPayload = []byte(strings.Repeat("Fast mail. Zero trace.\r\n", 4096))

func TestContentStoreCrashRecovery(t *testing.T) {
	tests := []struct {
		stage         string
		wantObject    bool
		wantStaging   int
		wantOrphanFix int64
	}{
		{stage: "write", wantStaging: 1},
		{stage: string(putStageFileSynced), wantStaging: 1},
		{stage: string(putStageObjectLinked), wantObject: true, wantStaging: 1, wantOrphanFix: 1},
	}
	for _, test := range tests {
		t.Run(test.stage, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "content")
			killContentStoreHelperAtStage(t, root, test.stage)

			store, err := Open(root, Options{MaxBytes: 1 << 20})
			if err != nil {
				t.Fatalf("Open(after crash) error = %v", err)
			}
			ref := refForText(string(contentCrashPayload))
			verifyErr := store.Verify(context.Background(), ref)
			if test.wantObject && verifyErr != nil {
				t.Fatalf("Verify(after %s crash) error = %v", test.stage, verifyErr)
			}
			if !test.wantObject && !errors.Is(verifyErr, os.ErrNotExist) {
				t.Fatalf("Verify(after %s crash) error = %v, want not exist", test.stage, verifyErr)
			}
			entries, err := os.ReadDir(store.stagingRoot)
			if err != nil {
				t.Fatalf("ReadDir(staging) error = %v", err)
			}
			if len(entries) != test.wantStaging {
				t.Fatalf("staging files after %s crash = %d, want %d", test.stage, len(entries), test.wantStaging)
			}

			if test.wantOrphanFix > 0 {
				summary, err := store.Reconcile(context.Background(), &fakeContentCatalog{}, ReconcileOptions{
					BatchSize:     1,
					RepairOrphans: true,
				}, nil)
				if err != nil {
					t.Fatalf("Reconcile(after %s crash) error = %v", test.stage, err)
				}
				if summary.RepairedOrphans != test.wantOrphanFix || summary.Unresolved() != 0 {
					t.Fatalf("Reconcile(after %s crash) summary = %+v", test.stage, summary)
				}
			}
			removed, err := store.PruneStaging(context.Background(), time.Now().Add(time.Hour))
			if err != nil {
				t.Fatalf("PruneStaging(after %s crash) error = %v", test.stage, err)
			}
			if removed != test.wantStaging {
				t.Fatalf("PruneStaging(after %s crash) removed = %d, want %d", test.stage, removed, test.wantStaging)
			}
			if _, err := store.OpenContent(ref); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("OpenContent(after recovery from %s) error = %v, want not exist", test.stage, err)
			}
		})
	}
}

func TestContentStoreCrashHelper(t *testing.T) {
	if os.Getenv(contentCrashHelperEnvironment) != "1" {
		return
	}
	root := os.Getenv(contentCrashRootEnvironment)
	target := os.Getenv(contentCrashStageEnvironment)
	store, err := Open(root, Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Open(helper) error = %v", err)
	}
	signalAndBlock := func(stage string) {
		_, _ = fmt.Fprintf(os.Stdout, "CRASH_STAGE=%s\n", stage)
		_ = os.Stdout.Sync()
		// Keep a runtime timer pending until the parent force-terminates this
		// process. A bare select{} can make the standalone test helper look
		// globally deadlocked, allowing the Go runtime to exit before the
		// parent calls Process.Kill. On Windows that race surfaces as
		// TerminateProcess: Access is denied for the already-exited process.
		time.Sleep(time.Hour)
	}

	var source io.Reader = bytes.NewReader(contentCrashPayload)
	if target == "write" {
		source = &crashDuringWriteReader{payload: contentCrashPayload, signal: func() { signalAndBlock(target) }}
	} else {
		store.putObserver = func(stage putStage) {
			if string(stage) == target {
				signalAndBlock(target)
			}
		}
	}
	if _, err := store.Put(context.Background(), source); err != nil {
		t.Fatalf("Put(helper) error = %v", err)
	}
	t.Fatal("crash helper completed without reaching target stage")
}

type crashDuringWriteReader struct {
	payload []byte
	offset  int
	signal  func()
}

func (r *crashDuringWriteReader) Read(buffer []byte) (int, error) {
	if r.offset > 0 {
		r.signal()
	}
	if r.offset >= len(r.payload) {
		return 0, io.EOF
	}
	limit := len(buffer)
	if limit > 4096 {
		limit = 4096
	}
	remaining := len(r.payload) - r.offset
	if limit > remaining {
		limit = remaining
	}
	copy(buffer[:limit], r.payload[r.offset:r.offset+limit])
	r.offset += limit
	return limit, nil
}

func killContentStoreHelperAtStage(t *testing.T, root, stage string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestContentStoreCrashHelper$", "-test.v")
	command.Env = append(os.Environ(),
		contentCrashHelperEnvironment+"=1",
		contentCrashRootEnvironment+"="+root,
		contentCrashStageEnvironment+"="+stage,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start crash helper: %v", err)
	}

	reached := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "CRASH_STAGE="+stage {
				reached <- nil
				return
			}
		}
		if err := scanner.Err(); err != nil {
			reached <- err
			return
		}
		reached <- errors.New("crash helper exited before target stage")
	}()

	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	select {
	case err := <-reached:
		if err != nil {
			killErr := command.Process.Kill()
			waitErr := command.Wait()
			t.Fatalf("wait for crash stage %q: %v; cleanup kill=%v; wait=%v; stderr=%s", stage, err, killErr, waitErr, stderr.String())
		}
	case <-timeout.C:
		killErr := command.Process.Kill()
		waitErr := command.Wait()
		t.Fatalf("wait for crash stage %q: timeout; cleanup kill=%v; wait=%v; stderr=%s", stage, killErr, waitErr, stderr.String())
	}

	if err := command.Process.Kill(); err != nil {
		waitErr := command.Wait()
		t.Fatalf("force-terminate crash helper at %q: %v; wait=%v; stderr=%s", stage, err, waitErr, stderr.String())
	}
	waitErr := command.Wait()
	if waitErr == nil {
		t.Fatalf("crash helper at %q exited successfully, want forced termination", stage)
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("wait for force-terminated crash helper at %q: %v", stage, waitErr)
	}
}
