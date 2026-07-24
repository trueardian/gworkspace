package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go.naturallyfunny.dev/gworkspace"
)

// fakeRow is a pgx.Row whose Scan returns a preset error or copies preset
// string values into the destinations. Enough to exercise the mapping logic
// without a database.
type fakeRow struct {
	scanErr error
	values  []string
}

func (r fakeRow) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	for i, d := range dest {
		if i >= len(r.values) {
			break
		}
		if p, ok := d.(*string); ok {
			*p = r.values[i]
		}
	}
	return nil
}

// fakeQuerier returns programmed results and records the SQL it was asked to run.
type fakeQuerier struct {
	row     pgx.Row
	tag     pgconn.CommandTag
	execErr error

	execSQL []string
}

func (q *fakeQuerier) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	q.execSQL = append(q.execSQL, sql)
	return q.tag, q.execErr
}

func (q *fakeQuerier) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return q.row
}

func TestGetRefreshToken(t *testing.T) {
	tests := []struct {
		name      string
		row       fakeRow
		want      string
		wantErrIs error // errors.Is target; nil means "no error"
	}{
		{name: "found", row: fakeRow{values: []string{"refresh-xyz"}}, want: "refresh-xyz"},
		{name: "no rows maps to ErrNotConnected", row: fakeRow{scanErr: pgx.ErrNoRows}, wantErrIs: gworkspace.ErrNotConnected},
		{name: "other error is wrapped, not ErrNotConnected", row: fakeRow{scanErr: errors.New("boom")}, wantErrIs: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &TokenStore{db: &fakeQuerier{row: tt.row}}
			got, err := s.GetRefreshToken(context.Background(), "owner")

			switch {
			case tt.wantErrIs != nil:
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("err = %v, want errors.Is %v", err, tt.wantErrIs)
				}
			case tt.row.scanErr != nil:
				// A non-sentinel scan error must surface as a wrapped error, and
				// must NOT be mistaken for ErrNotConnected.
				if err == nil {
					t.Fatal("err = nil, want wrapped error")
				}
				if errors.Is(err, gworkspace.ErrNotConnected) {
					t.Fatal("unexpected ErrNotConnected for a non-ErrNoRows failure")
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != tt.want {
					t.Fatalf("token = %q, want %q", got, tt.want)
				}
			}
		})
	}
}

func TestDeleteRefreshToken(t *testing.T) {
	tests := []struct {
		name      string
		tag       pgconn.CommandTag
		execErr   error
		wantErrIs error
		wantOK    bool
	}{
		{name: "row deleted", tag: pgconn.NewCommandTag("DELETE 1"), wantOK: true},
		{name: "nothing to delete maps to ErrNotConnected", tag: pgconn.NewCommandTag("DELETE 0"), wantErrIs: gworkspace.ErrNotConnected},
		{name: "exec error is wrapped", execErr: errors.New("boom"), wantErrIs: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &TokenStore{db: &fakeQuerier{tag: tt.tag, execErr: tt.execErr}}
			err := s.DeleteRefreshToken(context.Background(), "owner")

			switch {
			case tt.wantOK:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			case tt.wantErrIs != nil:
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("err = %v, want errors.Is %v", err, tt.wantErrIs)
				}
			default: // exec error
				if err == nil || errors.Is(err, gworkspace.ErrNotConnected) {
					t.Fatalf("err = %v, want a wrapped non-sentinel error", err)
				}
			}
		})
	}
}

func TestSaveRefreshToken(t *testing.T) {
	q := &fakeQuerier{tag: pgconn.NewCommandTag("INSERT 0 1")}
	s := &TokenStore{db: q}
	if err := s.SaveRefreshToken(context.Background(), "owner", "refresh-xyz"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(q.execSQL) != 1 || !strings.Contains(q.execSQL[0], "ON CONFLICT") {
		t.Fatalf("expected a single upsert (ON CONFLICT) exec, got %v", q.execSQL)
	}

	failing := &TokenStore{db: &fakeQuerier{execErr: errors.New("boom")}}
	if err := failing.SaveRefreshToken(context.Background(), "owner", "x"); err == nil {
		t.Fatal("err = nil, want wrapped exec error")
	}
}

func TestNewTokenStoreNilQuerierPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewTokenStore(nil) did not panic")
		}
	}()
	_, _ = NewTokenStore(context.Background(), nil)
}

func TestNewTokenStoreValidatesSchema(t *testing.T) {
	// A healthy schema: `SELECT ... LIMIT 0` yields no row, i.e. pgx.ErrNoRows.
	ok := &fakeQuerier{row: fakeRow{scanErr: pgx.ErrNoRows}}
	if _, err := NewTokenStore(context.Background(), ok); err != nil {
		t.Fatalf("healthy schema: unexpected error: %v", err)
	}

	// A missing table/column surfaces as a non-ErrNoRows scan error and must
	// fail construction loudly rather than deferring to the first query.
	broken := &fakeQuerier{row: fakeRow{scanErr: errors.New(`relation "gworkspace_tokens" does not exist`)}}
	if _, err := NewTokenStore(context.Background(), broken); err == nil {
		t.Fatal("broken schema: err = nil, want schema-validation error")
	}
}

func TestNewTokenStoreAutoMigrateAppliesEmbeddedFiles(t *testing.T) {
	q := &fakeQuerier{tag: pgconn.NewCommandTag("CREATE TABLE")}
	if _, err := NewTokenStore(context.Background(), q, WithAutoMigrate()); err != nil {
		t.Fatalf("auto-migrate: unexpected error: %v", err)
	}
	// The embedded migrations/*.sql must have been executed in order.
	if len(q.execSQL) == 0 {
		t.Fatal("auto-migrate ran no migrations")
	}
	if !strings.Contains(strings.Join(q.execSQL, "\n"), "CREATE TABLE") {
		t.Fatalf("migrations did not create the table: %v", q.execSQL)
	}

	// A failing migration aborts construction.
	failing := &fakeQuerier{execErr: errors.New("boom")}
	if _, err := NewTokenStore(context.Background(), failing, WithAutoMigrate()); err == nil {
		t.Fatal("auto-migrate with failing exec: err = nil, want error")
	}
}
