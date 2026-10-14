package database_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
)

// spyBeginner hands out the pool's transactions and remembers the last one, so
// a test can write through the same transaction InTx manages.
type spyBeginner struct {
	pool *pgxpool.Pool
	tx   pgx.Tx
}

func (s *spyBeginner) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	s.tx = tx
	return tx, err
}

func newScratchPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := database.NewPool(t.Context(), newDatabase(t), 4)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), "CREATE TABLE scratch (n int PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	return pool
}

func scratchRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM scratch").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInTx(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name      string
		fn        func(spy *spyBeginner) func(*sqlc.Queries) error
		wantErr   error
		wantPanic bool
		wantRows  int
	}{
		{
			name: "commits when fn returns nil",
			fn: func(spy *spyBeginner) func(*sqlc.Queries) error {
				return func(*sqlc.Queries) error {
					_, err := spy.tx.Exec(t.Context(), "INSERT INTO scratch VALUES (1)")
					return err
				}
			},
			wantRows: 1,
		},
		{
			name: "rolls back and returns the error",
			fn: func(spy *spyBeginner) func(*sqlc.Queries) error {
				return func(*sqlc.Queries) error {
					if _, err := spy.tx.Exec(t.Context(), "INSERT INTO scratch VALUES (1)"); err != nil {
						return err
					}
					return errBoom
				}
			},
			wantErr:  errBoom,
			wantRows: 0,
		},
		{
			name: "rolls back and re-panics",
			fn: func(spy *spyBeginner) func(*sqlc.Queries) error {
				return func(*sqlc.Queries) error {
					if _, err := spy.tx.Exec(t.Context(), "INSERT INTO scratch VALUES (1)"); err != nil {
						return err
					}
					panic("kaboom")
				}
			},
			wantPanic: true,
			wantRows:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newScratchPool(t)
			spy := &spyBeginner{pool: pool}

			var recovered any
			var err error
			func() {
				defer func() { recovered = recover() }()
				err = database.InTx(t.Context(), spy, tt.fn(spy))
			}()

			if tt.wantPanic != (recovered != nil) {
				t.Fatalf("panic = %v, want panic %v", recovered, tt.wantPanic)
			}
			if tt.wantPanic && recovered != "kaboom" {
				t.Errorf("recovered %v, want the original panic value", recovered)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := scratchRows(t, pool); got != tt.wantRows {
				t.Errorf("rows after InTx = %d, want %d", got, tt.wantRows)
			}
			// The connection must be back in the pool in a clean state.
			if _, err := pool.Exec(t.Context(), "SELECT 1"); err != nil {
				t.Errorf("pool unusable after InTx: %v", err)
			}
		})
	}
}

func TestInTx_UsesRealQueries(t *testing.T) {
	pool := newMigratedPool(t)
	var got int64
	err := database.InTx(t.Context(), pool, func(q *sqlc.Queries) error {
		var err error
		got, err = q.CountWorkspaces(t.Context())
		return err
	})
	if err != nil || got != 0 {
		t.Fatalf("CountWorkspaces in tx = %d, %v; want 0, nil", got, err)
	}
}

func TestInTx_BeginFailure(t *testing.T) {
	pool := newScratchPool(t)
	pool.Close()
	called := false
	err := database.InTx(t.Context(), pool, func(*sqlc.Queries) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("err = %v, fn called = %v; want an error and no call", err, called)
	}
}

func TestInTx_RollbackSurvivesCancelledContext(t *testing.T) {
	pool := newScratchPool(t)
	spy := &spyBeginner{pool: pool}
	ctx, cancel := context.WithCancel(t.Context())
	err := database.InTx(ctx, spy, func(*sqlc.Queries) error {
		if _, err := spy.tx.Exec(t.Context(), "INSERT INTO scratch VALUES (1)"); err != nil {
			return err
		}
		cancel() // the request died mid-transaction
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := scratchRows(t, pool); got != 0 {
		t.Errorf("rows = %d, want 0", got)
	}
}

func TestNewPool(t *testing.T) {
	t.Run("connects and applies the pool size", func(t *testing.T) {
		pool, err := database.NewPool(t.Context(), newDatabase(t), 3)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		if got := pool.Config().MaxConns; got != 3 {
			t.Errorf("MaxConns = %d, want 3", got)
		}
	})
	t.Run("rejects an unparsable URL", func(t *testing.T) {
		if _, err := database.NewPool(t.Context(), "://nope", 1); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("fails when the server is unreachable", func(t *testing.T) {
		if _, err := database.NewPool(t.Context(), "postgres://127.0.0.1:1/x?connect_timeout=1", 1); err == nil {
			t.Fatal("want an error")
		}
	})
}
