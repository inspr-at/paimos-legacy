package releaseacceptance

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// PAI-1057: the database opens with `_txlock=immediate`, so BeginTx issues
// BEGIN IMMEDIATE and waits up to busy_timeout (5s) for the writer lock. Under
// sustained write contention that wait can be exhausted while nothing has been
// written yet, and the caller received a hard SQLITE_BUSY. Retrying BEGIN a few
// times with a short backoff is safe for every transaction in this package,
// because the failure happens before any statement runs; the per-attempt
// busy_timeout still bounds each wait. Retries stop as soon as the context is
// done or a non-busy error appears.
const beginBusyAttempts = 4

var beginBusyBackoff = [beginBusyAttempts - 1]time.Duration{50 * time.Millisecond, 200 * time.Millisecond, 800 * time.Millisecond}

type beginFunc func(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)

type waitFunc func(ctx context.Context, d time.Duration) error

func (s *Service) beginTx(ctx context.Context) (*sql.Tx, error) {
	return beginTxWith(ctx, s.DB.BeginTx, sleepContext)
}

func beginTxWith(ctx context.Context, begin beginFunc, wait waitFunc) (*sql.Tx, error) {
	var lastErr error
	for attempt := 0; attempt < beginBusyAttempts; attempt++ {
		tx, err := begin(ctx, nil)
		if err == nil {
			return tx, nil
		}
		if !isSQLiteBusy(err) || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
		if attempt < len(beginBusyBackoff) {
			if werr := wait(ctx, beginBusyBackoff[attempt]); werr != nil {
				return nil, lastErr
			}
		}
	}
	return nil, lastErr
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isSQLiteBusy matches the driver's SQLITE_BUSY surface the same way the
// handlers package does (modernc formats it as "database is locked (5)
// (SQLITE_BUSY)").
func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked")
}
