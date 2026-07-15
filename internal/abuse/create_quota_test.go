package abuse

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServiceConsumesNormalizedDailyIdentity(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{}
	service, err := NewService(repository, make([]byte, 32), 3)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 7, 15, 23, 0, 0, 0, time.FixedZone("CST", 8*60*60)) }
	first, err := service.Consume(context.Background(), "2001:db8::1")
	if err != nil {
		t.Fatal(err)
	}
	firstDigest := repository.digest
	second, err := service.Consume(context.Background(), "2001:0db8:0:0:0:0:0:1")
	if err != nil {
		t.Fatal(err)
	}
	if repository.digest != firstDigest {
		t.Fatal("equivalent IPv6 forms produced different quota identities")
	}
	if !repository.bucket.Equal(time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)) || first.ResetAt.Hour() != 0 || second.Remaining != 1 {
		t.Fatalf("decisions first=%+v second=%+v bucket=%s", first, second, repository.bucket)
	}
}

func TestServiceReturnsLimitDecisionAndValidationErrors(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{err: ErrDailyCreateQuotaExceeded, used: 2}
	service, err := NewService(repository, make([]byte, 32), 2)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := service.Consume(context.Background(), "192.0.2.1")
	if !errors.Is(err, ErrDailyCreateQuotaExceeded) || decision.Remaining != 0 {
		t.Fatalf("Consume(limit) = %+v, %v", decision, err)
	}
	if _, err := service.Consume(context.Background(), "not-an-ip"); !errors.Is(err, ErrInvalidClientAddress) {
		t.Fatalf("Consume(invalid IP) error = %v", err)
	}
	if _, err := NewService(nil, make([]byte, 32), 1); err == nil {
		t.Fatal("NewService(nil repository) error = nil")
	}
	if _, err := NewService(repository, make([]byte, 31), 1); err == nil {
		t.Fatal("NewService(short key) error = nil")
	}
}

func TestServiceNormalizesMappedIPv4AndSeparatesKeys(t *testing.T) {
	t.Parallel()

	firstRepository := &repositoryStub{}
	first, err := NewService(firstRepository, make([]byte, 32), 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Consume(context.Background(), "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	canonicalDigest := firstRepository.digest
	if _, err := first.Consume(context.Background(), "::ffff:192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	if firstRepository.digest != canonicalDigest {
		t.Fatal("IPv4 and IPv4-mapped IPv6 produced different quota identities")
	}

	secondRepository := &repositoryStub{}
	secondKey := make([]byte, 32)
	secondKey[0] = 1
	second, err := NewService(secondRepository, secondKey, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Consume(context.Background(), "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	if secondRepository.digest == canonicalDigest {
		t.Fatal("different HMAC keys produced the same quota identity")
	}
}

type repositoryStub struct {
	digest IdentityDigest
	bucket time.Time
	used   int
	err    error
}

func (r *repositoryStub) ConsumeInboxCreate(_ context.Context, digest IdentityDigest, bucket time.Time, _ int) (int, error) {
	r.digest = digest
	r.bucket = bucket
	if r.err != nil {
		return r.used, r.err
	}
	r.used++
	return r.used, nil
}
