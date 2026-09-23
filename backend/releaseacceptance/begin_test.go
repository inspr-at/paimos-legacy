package releaseacceptance

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

var errBusyFixture = errors.New("database is locked (5) (SQLITE_BUSY)")

func recordingWait(waits *[]time.Duration, fail error) waitFunc {
	return func(_ context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return fail
	}
}

func TestBeginTxRetriesBusyThenSucceeds(t *testing.T) {
	f := openFixture(t)
	calls := 0
	var waits []time.Duration
	begin := func(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
		calls++
		if calls <= 2 {
			return nil, errBusyFixture
		}
		return f.svc.DB.BeginTx(ctx, opts)
	}
	tx, err := beginTxWith(context.Background(), begin, recordingWait(&waits, nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if calls != 3 {
		t.Fatalf("begin calls=%d", calls)
	}
	if len(waits) != 2 || waits[0] != beginBusyBackoff[0] || waits[1] != beginBusyBackoff[1] {
		t.Fatalf("waits=%v", waits)
	}
}

func TestBeginTxGivesUpAfterBoundedBusyAttempts(t *testing.T) {
	calls := 0
	var waits []time.Duration
	begin := func(context.Context, *sql.TxOptions) (*sql.Tx, error) {
		calls++
		return nil, errBusyFixture
	}
	_, err := beginTxWith(context.Background(), begin, recordingWait(&waits, nil))
	if !errors.Is(err, errBusyFixture) {
		t.Fatalf("err=%v", err)
	}
	if calls != beginBusyAttempts || len(waits) != beginBusyAttempts-1 {
		t.Fatalf("calls=%d waits=%d", calls, len(waits))
	}
}

func TestBeginTxDoesNotRetryOtherErrors(t *testing.T) {
	other := errors.New("no such table: release_records")
	calls := 0
	var waits []time.Duration
	begin := func(context.Context, *sql.TxOptions) (*sql.Tx, error) {
		calls++
		return nil, other
	}
	_, err := beginTxWith(context.Background(), begin, recordingWait(&waits, nil))
	if !errors.Is(err, other) || calls != 1 || len(waits) != 0 {
		t.Fatalf("err=%v calls=%d waits=%d", err, calls, len(waits))
	}
}

func TestBeginTxStopsWhenContextEndsDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	begin := func(context.Context, *sql.TxOptions) (*sql.Tx, error) {
		calls++
		return nil, errBusyFixture
	}
	waits := 0
	wait := func(_ context.Context, _ time.Duration) error {
		waits++
		cancel()
		return context.Canceled
	}
	_, err := beginTxWith(ctx, begin, wait)
	// One begin, one interrupted backoff, no second begin: without the retry
	// loop waits stays 0, and without the context check calls reaches 2.
	if !errors.Is(err, errBusyFixture) || calls != 1 || waits != 1 {
		t.Fatalf("err=%v calls=%d waits=%d", err, calls, waits)
	}
}
