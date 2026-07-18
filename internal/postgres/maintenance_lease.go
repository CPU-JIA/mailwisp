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
	// ErrServiceActive indicates that one MailWisp serve or maintenance process
	// already owns the singleton content lifecycle lease.
	ErrServiceActive = errors.New("mailwisp service is active")
)

// MaintenanceLease owns one session-level PostgreSQL advisory lock.
type MaintenanceLease struct {
	mutex sync.Mutex
	conn  *pgx.Conn
}

// AcquireServiceLease obtains the singleton application lease for the complete
// serve lifetime. The filesystem content profile deliberately permits one Go
// process with bounded internal concurrency, not multiple replicas.
func AcquireServiceLease(ctx context.Context, dsn string) (*MaintenanceLease, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("postgres DSN is required")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("acquire service lease connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", maintenanceLockClass, maintenanceLockObject).Scan(&acquired); err != nil {
		return nil, errors.Join(fmt.Errorf("acquire service lease: %w", err), conn.Close(context.Background()))
	}
	if !acquired {
		return nil, errors.Join(ErrServiceActive, conn.Close(context.Background()))
	}
	return &MaintenanceLease{conn: conn}, nil
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

	var unlocked bool
	err := l.conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1, $2)", maintenanceLockClass, maintenanceLockObject).Scan(&unlocked)
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
