//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"mailwisp/internal/auth"
	"mailwisp/internal/message"
	"mailwisp/migrations"
)

func TestInboxCapabilityMigrationUpgradesExistingInboxData(t *testing.T) {
	dropIntegrationSchema(t)
	t.Cleanup(func() { recreateIntegrationSchema(t) })

	config, err := pgx.ParseConfig(integrationDataSourceName)
	if err != nil {
		t.Fatalf("pgx.ParseConfig() error = %v", err)
	}
	database := stdlib.OpenDB(*config)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = database.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrations.FS)
	if err != nil {
		t.Fatalf("goose.NewProvider() error = %v", err)
	}
	if _, err := provider.UpTo(context.Background(), 2); err != nil {
		t.Fatalf("apply migrations through version 2: %v", err)
	}
	inboxID := "018f26e5-8f04-7b44-8ba2-4a8f434dcb12"
	if _, err := database.ExecContext(context.Background(), `
		INSERT INTO inboxes (id, address) VALUES ($1::uuid, 'capability-upgrade@example.com')
	`, inboxID); err != nil {
		t.Fatalf("insert version 2 Inbox: %v", err)
	}
	if _, err := provider.UpTo(context.Background(), 3); err != nil {
		t.Fatalf("apply migration 3: %v", err)
	}
	var inboxCount, tableCount int
	if err := database.QueryRowContext(context.Background(), `
		SELECT
			(SELECT count(*) FROM inboxes WHERE id = $1::uuid),
			(SELECT count(*) FROM information_schema.tables
			 WHERE table_schema = 'public' AND table_name = 'inbox_capabilities')
	`, inboxID).Scan(&inboxCount, &tableCount); err != nil {
		t.Fatalf("inspect version 3 upgrade: %v", err)
	}
	if inboxCount != 1 || tableCount != 1 {
		t.Fatalf("upgrade counts = Inbox %d, capability table %d", inboxCount, tableCount)
	}
}

func TestInboxCapabilityIssueAuthenticateAndRevoke(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewInboxCapabilityRepository(pool)
	if err != nil {
		t.Fatalf("NewInboxCapabilityRepository() error = %v", err)
	}
	service, err := auth.NewCapabilityService(repository)
	if err != nil {
		t.Fatalf("auth.NewCapabilityService() error = %v", err)
	}
	inboxExpiresAt := time.Now().UTC().Add(2 * time.Hour)
	inboxID := createInboxWithState(t, pool, "capability@example.com", "active", &inboxExpiresAt)
	scopes, _ := auth.NewScopeSet(auth.ScopeInboxRead, auth.ScopeMessageRead)
	issued, err := service.Issue(context.Background(), inboxID, scopes, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	principal, err := service.Authenticate(context.Background(), issued.Plaintext, auth.ScopeMessageRead)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if principal.InboxID != inboxID || principal.KID != issued.KID || principal.Scopes != scopes {
		t.Fatalf("Authenticate() principal = %+v", principal)
	}

	parsed, err := auth.ParseToken(issued.Plaintext)
	if err != nil {
		t.Fatalf("ParseToken() error = %v", err)
	}
	wantDigest, _ := parsed.Digest()
	var storedKID, storedRow string
	var storedDigest []byte
	var scopeMask int64
	if err := pool.QueryRow(context.Background(), `
		SELECT kid, secret_digest, scope_mask, row_to_json(capability)::text
		FROM inbox_capabilities AS capability
		WHERE id = $1::uuid
	`, string(issued.ID)).Scan(&storedKID, &storedDigest, &scopeMask, &storedRow); err != nil {
		t.Fatalf("read stored capability: %v", err)
	}
	var gotDigest auth.Digest
	copy(gotDigest[:], storedDigest)
	secretSuffix := issued.Plaintext[len(issued.Plaintext)-43:]
	if storedKID != issued.KID || !auth.EqualDigest(gotDigest, wantDigest) || scopeMask != int64(scopes.Mask()) {
		t.Fatalf("stored capability = KID %q digest length %d scope %d", storedKID, len(storedDigest), scopeMask)
	}
	if strings.Contains(storedRow, issued.Plaintext) || strings.Contains(storedRow, secretSuffix) {
		t.Fatal("database row contains capability plaintext or encoded secret")
	}

	if err := service.Revoke(context.Background(), issued.Plaintext); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), issued.Plaintext); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("Authenticate(revoked) error = %v", err)
	}
	if err := repository.RevokeCapability(context.Background(), issued.ID, time.Now().UTC()); err != nil {
		t.Fatalf("RevokeCapability(idempotent) error = %v", err)
	}
}

func TestInboxCapabilityRejectsUnavailableSubjectAndExcessLifetime(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewInboxCapabilityRepository(pool)
	if err != nil {
		t.Fatalf("NewInboxCapabilityRepository() error = %v", err)
	}
	service, err := auth.NewCapabilityService(repository)
	if err != nil {
		t.Fatalf("auth.NewCapabilityService() error = %v", err)
	}
	scopes, _ := auth.NewScopeSet(auth.ScopeInboxRead)
	now := time.Now().UTC()
	inboxExpiresAt := now.Add(time.Hour)
	inboxID := createInboxWithState(t, pool, "short-capability@example.com", "active", &inboxExpiresAt)
	if _, err := service.Issue(context.Background(), inboxID, scopes, now.Add(2*time.Hour)); !errors.Is(err, auth.ErrCapabilityLifetime) {
		t.Fatalf("Issue(excess lifetime) error = %v", err)
	}
	disabledID := createInboxWithState(t, pool, "disabled-capability@example.com", "disabled", nil)
	if _, err := service.Issue(context.Background(), disabledID, scopes, now.Add(time.Hour)); !errors.Is(err, auth.ErrCapabilitySubjectUnavailable) {
		t.Fatalf("Issue(disabled Inbox) error = %v", err)
	}
}

func TestInboxCapabilityRotationIsAtomicAndFenced(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewInboxCapabilityRepository(pool)
	if err != nil {
		t.Fatalf("NewInboxCapabilityRepository() error = %v", err)
	}
	service, err := auth.NewCapabilityService(repository)
	if err != nil {
		t.Fatalf("auth.NewCapabilityService() error = %v", err)
	}
	inboxID := createInbox(t, pool, "rotate-capability@example.com")
	scopes, _ := auth.NewScopeSet(auth.ScopeInboxRead, auth.ScopeMessageRead)
	issued, err := service.Issue(context.Background(), inboxID, scopes, time.Now().UTC().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	current, err := repository.FindCapabilityByKID(context.Background(), issued.KID)
	if err != nil {
		t.Fatalf("FindCapabilityByKID() error = %v", err)
	}

	rotatedAt := time.Now().UTC()
	replacements := make([]auth.ReplacementCapability, 2)
	for index := range replacements {
		token, err := auth.GenerateToken(auth.TokenCapability)
		if err != nil {
			t.Fatalf("GenerateToken(%d) error = %v", index, err)
		}
		digest, err := token.Digest()
		if err != nil {
			t.Fatalf("Digest(%d) error = %v", index, err)
		}
		replacements[index] = auth.ReplacementCapability{KID: token.KID(), Digest: digest, CreatedAt: rotatedAt}
	}

	type rotationResult struct {
		record auth.CapabilityRecord
		err    error
	}
	results := make(chan rotationResult, 2)
	var workers sync.WaitGroup
	for _, replacement := range replacements {
		workers.Add(1)
		go func() {
			defer workers.Done()
			record, err := repository.RotateCapability(context.Background(), current.ID, replacement, rotatedAt)
			results <- rotationResult{record: record, err: err}
		}()
	}
	workers.Wait()
	close(results)
	successes, fenced := 0, 0
	var replacementRecord auth.CapabilityRecord
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			replacementRecord = result.record
		case errors.Is(result.err, auth.ErrCapabilityAlreadyRotated):
			fenced++
		default:
			t.Errorf("RotateCapability() unexpected error = %v", result.err)
		}
	}
	if successes != 1 || fenced != 1 {
		t.Fatalf("rotation outcomes = success %d, fenced %d", successes, fenced)
	}
	if replacementRecord.InboxID != inboxID || replacementRecord.Scopes != scopes || !replacementRecord.ExpiresAt.Equal(issued.ExpiresAt) {
		t.Fatalf("replacement record = %+v", replacementRecord)
	}
	var oldRevokedAt *time.Time
	var replacementCount int
	if err := pool.QueryRow(context.Background(), `
		SELECT current.revoked_at,
		       (SELECT count(*) FROM inbox_capabilities WHERE rotated_from_id = current.id)
		FROM inbox_capabilities AS current
		WHERE current.id = $1::uuid
	`, string(current.ID)).Scan(&oldRevokedAt, &replacementCount); err != nil {
		t.Fatalf("inspect capability rotation: %v", err)
	}
	if oldRevokedAt == nil || replacementCount != 1 {
		t.Fatalf("rotation persistence = revoked %v, replacements %d", oldRevokedAt, replacementCount)
	}
}

func TestInboxCapabilityKIDAndScopeConstraints(t *testing.T) {
	pool := newIntegrationPool(t)
	resetIntegrationDatabase(t, pool)
	repository, err := NewInboxCapabilityRepository(pool)
	if err != nil {
		t.Fatalf("NewInboxCapabilityRepository() error = %v", err)
	}
	inboxID := createInbox(t, pool, "constraint-capability@example.com")
	capability := newIntegrationCapability(t, inboxID, time.Now().UTC().Add(30*time.Minute))
	if _, err := repository.CreateCapability(context.Background(), capability); err != nil {
		t.Fatalf("CreateCapability() error = %v", err)
	}
	duplicate := newIntegrationCapability(t, inboxID, time.Now().UTC().Add(30*time.Minute))
	duplicate.KID = capability.KID
	if _, err := repository.CreateCapability(context.Background(), duplicate); !errors.Is(err, auth.ErrCapabilityKIDConflict) {
		t.Fatalf("CreateCapability(duplicate KID) error = %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO inbox_capabilities (inbox_id, kid, secret_digest, scope_mask, created_at, expires_at)
		VALUES ($1::uuid, 'abcdefabcdefabcdefabcdef', decode(repeat('00', 32), 'hex'), 64, now(), now() + interval '1 hour')
	`, string(inboxID)); err == nil {
		t.Fatal("direct insert with unknown scope mask succeeded")
	}
}

func newIntegrationCapability(t *testing.T, inboxID message.InboxID, expiresAt time.Time) auth.NewCapability {
	t.Helper()
	token, err := auth.GenerateToken(auth.TokenCapability)
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	digest, err := token.Digest()
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	scopes, _ := auth.NewScopeSet(auth.ScopeInboxRead)
	return auth.NewCapability{
		InboxID: inboxID, KID: token.KID(), Digest: digest, Scopes: scopes,
		CreatedAt: time.Now().UTC(), ExpiresAt: expiresAt.UTC(),
	}
}
