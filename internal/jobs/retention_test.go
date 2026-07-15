package jobs

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"mailwisp/internal/message"
)

func TestRetentionSweepUsesBoundedBatchesAndDeletesContent(t *testing.T) {
	repository := &retentionRepositoryStub{batches: []retentionBatch{
		{deleted: 2, refs: []message.ContentRef{{Key: "sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
		{deleted: 1},
	}}
	content := &retentionContentStub{}
	job, err := NewRetention(repository, content, slog.New(slog.NewTextHandler(io.Discard, nil)), RetentionOptions{BatchSize: 2, Interval: time.Minute, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := job.Sweep(context.Background())
	if err != nil || summary.InboxesDeleted != 3 || summary.ContentDeleted != 1 || repository.calls != 2 || len(content.deleted) != 1 {
		t.Fatalf("Sweep() = %+v, %v, calls=%d deleted=%+v", summary, err, repository.calls, content.deleted)
	}
}

type retentionBatch struct {
	deleted int
	refs    []message.ContentRef
}

type retentionRepositoryStub struct {
	batches []retentionBatch
	calls   int
}

func (r *retentionRepositoryStub) CleanupExpiredInboxes(context.Context, int) (int, []message.ContentRef, error) {
	batch := r.batches[r.calls]
	r.calls++
	return batch.deleted, batch.refs, nil
}

type retentionContentStub struct{ deleted []message.ContentRef }

func (s *retentionContentStub) Delete(ref message.ContentRef) error {
	s.deleted = append(s.deleted, ref)
	return nil
}
