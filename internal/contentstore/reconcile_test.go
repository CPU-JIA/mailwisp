package contentstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"

	"mailwisp/internal/message"
)

func TestReconcileReportsAndRepairsContentFindings(t *testing.T) {
	store, err := Open(t.TempDir(), Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	good, err := store.Put(context.Background(), strings.NewReader("durable"))
	if err != nil {
		t.Fatalf("Put(good) error = %v", err)
	}
	corrupt, err := store.Put(context.Background(), strings.NewReader("corrupt"))
	if err != nil {
		t.Fatalf("Put(corrupt) error = %v", err)
	}
	corruptPath, err := store.pathForKey(corrupt.Key)
	if err != nil {
		t.Fatalf("pathForKey(corrupt) error = %v", err)
	}
	if err := os.WriteFile(corruptPath, []byte("changed"), 0o600); err != nil {
		t.Fatalf("corrupt object: %v", err)
	}
	orphan, err := store.Put(context.Background(), strings.NewReader("orphan"))
	if err != nil {
		t.Fatalf("Put(orphan) error = %v", err)
	}
	missing := refForText("missing")
	catalog := &fakeContentCatalog{refs: []message.ContentRef{good, corrupt, missing}}

	var findings []Finding
	summary, err := store.Reconcile(context.Background(), catalog, ReconcileOptions{BatchSize: 2}, func(finding Finding) error {
		findings = append(findings, finding)
		return nil
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if summary.DatabaseRefs != 3 || summary.StoredObjects != 3 || summary.Missing != 1 || summary.Corrupt != 1 || summary.Orphans != 1 || summary.Unresolved() != 3 {
		t.Fatalf("summary = %+v", summary)
	}
	if got := findingKinds(findings); !equalStrings(got, []string{"corrupt", "missing", "orphan"}) {
		t.Fatalf("finding kinds = %v", got)
	}
	if err := store.Verify(context.Background(), orphan); err != nil {
		t.Fatalf("orphan should remain after report-only scan: %v", err)
	}

	findings = nil
	summary, err = store.Reconcile(context.Background(), catalog, ReconcileOptions{BatchSize: 2, RepairOrphans: true}, func(finding Finding) error {
		findings = append(findings, finding)
		return nil
	})
	if err != nil {
		t.Fatalf("Reconcile(repair) error = %v", err)
	}
	if summary.RepairedOrphans != 1 || summary.Unresolved() != 2 {
		t.Fatalf("repair summary = %+v", summary)
	}
	if len(findings) != 3 {
		t.Fatalf("repair findings = %d, want 3", len(findings))
	}
	if _, err := store.OpenContent(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repaired orphan OpenContent() error = %v, want not exist", err)
	}
	if !findings[2].Repaired {
		t.Fatalf("orphan finding = %+v, want repaired", findings[2])
	}
}

func TestDeleteMissingObjectIsIdempotent(t *testing.T) {
	store, err := Open(t.TempDir(), Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Delete(refForText("never-written")); err != nil {
		t.Fatalf("Delete(missing) error = %v", err)
	}
}

type fakeContentCatalog struct {
	refs []message.ContentRef
}

func (f *fakeContentCatalog) WalkContentRefs(_ context.Context, batchSize int, visit func(message.ContentRef) error) error {
	for start := 0; start < len(f.refs); start += batchSize {
		end := start + batchSize
		if end > len(f.refs) {
			end = len(f.refs)
		}
		for _, ref := range f.refs[start:end] {
			if err := visit(ref); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeContentCatalog) ExistingContentKeys(_ context.Context, keys []string) (map[string]struct{}, error) {
	existing := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		for _, ref := range f.refs {
			if ref.Key == key {
				existing[key] = struct{}{}
				break
			}
		}
	}
	return existing, nil
}

func refForText(value string) message.ContentRef {
	digest := sha256.Sum256([]byte(value))
	return message.ContentRef{Key: "sha256/" + hex.EncodeToString(digest[:]), SizeBytes: int64(len(value))}
}

func findingKinds(findings []Finding) []string {
	kinds := make([]string, 0, len(findings))
	for _, finding := range findings {
		kinds = append(kinds, string(finding.Kind))
	}
	sort.Strings(kinds)
	return kinds
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
