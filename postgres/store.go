// Package postgres provides a PostgreSQL-backed implementation of
// gworkspace.TokenStore over a pgx connection pool.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go.trueardian.com/gworkspace"
)

//go:embed migrations
var migrationFiles embed.FS

// Querier is the narrow slice of pgx the TokenStore needs. *pgxpool.Pool satisfies it,
// as does a pgx.Conn or a transaction. Taking this instead of a concrete pool
// keeps the TokenStore decoupled from how the consumer built its connection and makes
// it trivial to fake in tests.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TokenStore implements gworkspace.TokenStore backed by PostgreSQL.
type TokenStore struct {
	db          Querier
	autoMigrate bool
}

// Option configures a TokenStore at construction.
type Option func(*TokenStore)

// WithAutoMigrate runs the embedded migrations against the injected connection
// when the TokenStore is built. Migrations are idempotent (every statement is
// IF [NOT] EXISTS), so this is safe to enable on every startup.
func WithAutoMigrate() Option {
	return func(s *TokenStore) { s.autoMigrate = true }
}

// NewTokenStore builds a TokenStore over an existing connection (typically a *pgxpool.Pool
// the consumer already owns). Migrations run through that same connection — there
// is no separate DSN to pass. Pass WithAutoMigrate to migrate on construction.
func NewTokenStore(ctx context.Context, db Querier, opts ...Option) (*TokenStore, error) {
	if db == nil {
		panic("gworkspace/postgres: NewTokenStore called with nil Querier")
	}
	s := &TokenStore{db: db}
	for _, opt := range opts {
		opt(s)
	}

	if s.autoMigrate {
		// Migrating creates the schema, so there is nothing left to validate.
		if err := s.migrate(ctx); err != nil {
			return nil, fmt.Errorf("gworkspace/postgres: auto-migrate: %w", err)
		}
	} else if err := s.validateSchema(ctx); err != nil {
		// Consumer manages the schema; fail fast if it is missing.
		return nil, err
	}

	return s, nil
}

func (s *TokenStore) GetRefreshToken(ctx context.Context, owner string) (string, error) {
	var token string
	err := s.db.QueryRow(ctx,
		`SELECT refresh_token FROM gworkspace_tokens WHERE owner = $1`,
		owner,
	).Scan(&token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", gworkspace.ErrNotConnected
		}
		return "", fmt.Errorf("gworkspace/postgres: get refresh token: %w", err)
	}
	return token, nil
}

// SaveRefreshToken inserts or replaces the refresh token for owner.
func (s *TokenStore) SaveRefreshToken(ctx context.Context, owner, refreshToken string) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO gworkspace_tokens (owner, refresh_token)
		 VALUES ($1, $2)
		 ON CONFLICT (owner) DO UPDATE SET refresh_token = EXCLUDED.refresh_token`,
		owner, refreshToken,
	)
	if err != nil {
		return fmt.Errorf("gworkspace/postgres: save refresh token: %w", err)
	}
	return nil
}

// DeleteRefreshToken removes the owner's refresh token, returning
// ErrNotConnected when there was none to remove — matching GetRefreshToken.
func (s *TokenStore) DeleteRefreshToken(ctx context.Context, owner string) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM gworkspace_tokens WHERE owner = $1`,
		owner,
	)
	if err != nil {
		return fmt.Errorf("gworkspace/postgres: delete refresh token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return gworkspace.ErrNotConnected
	}
	return nil
}

// Migrate runs the embedded migrations against the injected connection. It is
// exposed for consumers that prefer to migrate explicitly rather than via
// WithAutoMigrate. Idempotent.
func (s *TokenStore) Migrate(ctx context.Context) error {
	return s.migrate(ctx)
}

func (s *TokenStore) migrate(ctx context.Context) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	// Apply in filename order (0001_, 0002_, ...) so later migrations see the
	// schema the earlier ones established.
	sort.Strings(names)
	for _, name := range names {
		content, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if _, err := s.db.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("execute %s: %w", name, err)
		}
	}
	return nil
}

// validateSchema fails fast if the expected table or its columns are missing,
// turning a misconfigured database into a clear startup error instead of a
// confusing one on the first GetRefreshToken.
func (s *TokenStore) validateSchema(ctx context.Context) error {
	var owner, refreshToken string
	err := s.db.QueryRow(ctx,
		`SELECT owner, refresh_token FROM gworkspace_tokens LIMIT 0`,
	).Scan(&owner, &refreshToken)
	// LIMIT 0 returns no row, so a healthy schema yields ErrNoRows; any other
	// error means the table or one of the named columns is missing.
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("gworkspace/postgres: schema validation: %w", err)
	}
	return nil
}

var _ gworkspace.TokenStore = (*TokenStore)(nil)
