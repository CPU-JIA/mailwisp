package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"mailwisp/internal/message"
)

func TestRetentionSweepUsesBoundedBatchesAndDeletesQueuedContent(t *testing.T) {
	repository := &retentionRepositoryStub{
		batches:   []retentionBatch{{deleted: 2}, {deleted: 1}},
		deletions: []message.ContentDeletion{{Content: deletionRef("a"), Generation: 1}},
	}
	content := &retentionContentStub{}
	job := newTestRetention(t, repository, content, 2)

	summary, err := job.Sweep(context.Background())
	if err != nil || summary.InboxesDeleted != 3 || summary.ContentDeleted != 1 || summary.ContentPending != 0 || repository.cleanupCalls != 2 || len(content.deleted) != 1 || len(repository.deletions) != 0 {
		t.Fatalf("Sweep() = %+v, %v, cleanup calls=%d deleted=%+v queue=%+v", summary, err, repository.cleanupCalls, content.deleted, repository.deletions)
	}
}

func TestRetentionSweepLeavesFailureQueuedAndContinues(t *testing.T) {
	repository := &retentionRepositoryStub{deletions: []message.ContentDeletion{
		{Content: deletionRef("a"), Generation: 1},
		{Content: deletionRef("b"), Generation: 1},
	}}
	content := &retentionContentStub{failKey: deletionRef("a").Key, failOnce: true}
	job := newTestRetention(t, repository, content, 10)

	first, err := job.Sweep(context.Background())
	if err == nil || first.ContentDeleted != 1 || first.ContentPending != 1 || len(repository.deletions) != 1 || repository.deletions[0].Content.Key != deletionRef("a").Key {
		t.Fatalf("first Sweep() = %+v, %v, queue=%+v", first, err, repository.deletions)
	}
	second, err := job.Sweep(context.Background())
	if err != nil || second.ContentDeleted != 1 || second.ContentPending != 0 || len(repository.deletions) != 0 {
		t.Fatalf("second Sweep() = %+v, %v, queue=%+v", second, err, repository.deletions)
	}
}

func TestRetentionSweepRetainsReusedContentAndAcknowledgesExactGeneration(t *testing.T) {
	ref := deletionRef("c")
	repository := &retentionRepositoryStub{
		deletions:  []message.ContentDeletion{{Content: ref, Generation: 7}},
		referenced: map[string]bool{ref.Key: true},
	}
	content := &retentionContentStub{}
	job := newTestRetention(t, repository, content, 10)

	summary, err := job.Sweep(context.Background())
	if err != nil || summary.ContentDeleted != 0 || len(content.deleted) != 0 || len(repository.deletions) != 0 {
		t.Fatalf("Sweep() = %+v, %v, deleted=%+v queue=%+v", summary, err, content.deleted, repository.deletions)
	}
}

func newTestRetention(t *testing.T, repository *retentionRepositoryStub, content *retentionContentStub, batchSize int) *Retention {
	t.Helper()
	job, err := NewRetention(repository, content, slog.New(slog.NewTextHandler(io.Discard, nil)), RetentionOptions{BatchSize: batchSize, Interval: time.Minute, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func deletionRef(fill string) message.ContentRef {
	return message.ContentRef{Key: "sha256/" + repeat(fill, 64), SizeBytes: 1}
}

func repeat(value string, count int) string {
	result := ""
	for range count {
		result += value
	}
	return result
}

type retentionBatch struct {
	deleted int
	refs    []message.ContentRef
}

type retentionRepositoryStub struct {
	batches      []retentionBatch
	cleanupCalls int
	deletions    []message.ContentDeletion
	referenced   map[string]bool
}

func (r *retentionRepositoryStub) CleanupExpiredInboxes(context.Context, int) (int, []message.ContentRef, error) {
	if r.cleanupCalls >= len(r.batches) {
		r.cleanupCalls++
		return 0, nil, nil
	}
	batch := r.batches[r.cleanupCalls]
	r.cleanupCalls++
	return batch.deleted, batch.refs, nil
}

func (r *retentionRepositoryStub) ListContentDeletions(_ context.Context, limit int) ([]message.ContentDeletion, error) {
	if limit > len(r.deletions) {
		limit = len(r.deletions)
	}
	return append([]message.ContentDeletion(nil), r.deletions[:limit]...), nil
}

func (r *retentionRepositoryStub) ContentReferenced(_ context.Context, key string) (bool, error) {
	return r.referenced[key], nil
}

func (r *retentionRepositoryStub) AcknowledgeContentDeletion(_ context.Context, key string, generation int64) (bool, error) {
	for index, deletion := range r.deletions {
		if deletion.Content.Key == key && deletion.Generation == generation {
			r.deletions = append(r.deletions[:index], r.deletions[index+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (r *retentionRepositoryStub) CountContentDeletions(context.Context) (int, error) {
	return len(r.deletions), nil
}

type retentionContentStub struct {
	deleted  []message.ContentRef
	failKey  string
	failOnce bool
}

func (s *retentionContentStub) DeleteUnreferenced(ctx context.Context, ref message.ContentRef, referenced func(context.Context, string) (bool, error)) (bool, error) {
	retained, err := referenced(ctx, ref.Key)
	if err != nil || retained {
		return false, err
	}
	if s.failOnce && ref.Key == s.failKey {
		s.failOnce = false
		return false, errors.New("injected physical deletion failure")
	}
	s.deleted = append(s.deleted, ref)
	return true, nil
}
