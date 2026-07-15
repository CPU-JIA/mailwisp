package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

const (
	maintenanceLockClass  int32 = 0x4d575350 // MWSP
	maintenanceLockObject int32 = 0x434f4e54 // CONT
)

var (
	// ErrServiceActive indicates that at least one MailWisp serve process holds
	// the shared lease that protects active deliveries.
	ErrServiceActive = errors.New("mailwisp service is active")
)

// MaintenanceLease owns one session-level PostgreSQL advisory lock.
type MaintenanceLease struct {
	mutex  sync.Mutex
	conn   *pgx.Conn
	shared bool
}

// AcquireServiceLease obtains a shared lease for the complete serve lifetime.
// Multiple serve processes may coexist, while exclusive maintenance is blocked.
func AcquireServiceLease(ctx context.Context, dsn string) (*MaintenanceLease, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("postgres DSN is required")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("acquire service lease connection: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock_shared($1, $2)", maintenanceLockClass, maintenanceLockObject); err != nil {
		return nil, errors.Join(fmt.Errorf("acquire service lease: %w", err), conn.Close(context.Background()))
	}
	return &MaintenanceLease{conn: conn, shared: true}, nil
}

// TryAcquireMaintenanceLease obtains an exclusive lease without waiting for
// active serve processes to stop.
func TryAcquireMaintenanceLease(ctx context.Context, dsn string) (*MaintenanceLease, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("postgres DSN is required")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("acquire maintenance lease connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", maintenanceLockClass, maintenanceLockObject).Scan(&acquired); err != nil {
		return nil, errors.Join(fmt.Errorf("acquire maintenance lease: %w", err), conn.Close(context.Background()))
	}
	if !acquired {
		return nil, errors.Join(ErrServiceActive, conn.Close(context.Background()))
	}
	return &MaintenanceLease{conn: conn}, nil
}

// Release unlocks and closes the dedicated connection. Closing the session is
// the final safety net if PostgreSQL rejects the unlock operation.
func (l *MaintenanceLease) Release(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if l.conn == nil {
		return nil
	}

	query := "SELECT pg_advisory_unlock($1, $2)"
	if l.shared {
		query = "SELECT pg_advisory_unlock_shared($1, $2)"
	}
	var unlocked bool
	err := l.conn.QueryRow(ctx, query, maintenanceLockClass, maintenanceLockObject).Scan(&unlocked)
	connection := l.conn
	l.conn = nil
	closeErr := connection.Close(ctx)
	if err != nil {
		return errors.Join(fmt.Errorf("release maintenance lease: %w", err), closeErr)
	}
	if !unlocked {
		return errors.Join(errors.New("maintenance lease was not held by this session"), closeErr)
	}
	return closeErr
}
