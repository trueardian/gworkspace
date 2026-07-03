// Package firestore provides a Cloud Firestore-backed implementation of
// gworkspace.TokenStore.
//
// Each owner maps to one document in a single collection (DefaultCollection
// unless overridden with WithCollection); the owner string is the document ID,
// so lookups are direct reads rather than queries. Firestore is schemaless, so
// unlike the postgres sibling there is nothing to migrate — the collection
// appears with the first saved token.
package firestore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.naturallyfunny.dev/gworkspace"
)

// DefaultCollection is the collection tokens live in unless WithCollection
// overrides it. It matches the table name used by the postgres sibling.
const DefaultCollection = "gworkspace_tokens"

// tokenDoc is the stored shape of one owner's token document.
type tokenDoc struct {
	RefreshToken string    `firestore:"refresh_token"`
	CreatedAt    time.Time `firestore:"created_at"`
}

// TokenStore implements gworkspace.TokenStore backed by Cloud Firestore.
type TokenStore struct {
	client     *firestore.Client
	collection string
}

// Option configures a TokenStore at construction.
type Option func(*TokenStore)

// WithCollection stores tokens in the named collection instead of
// DefaultCollection. Use it when one Firestore database hosts several
// environments or apps.
func WithCollection(name string) Option {
	return func(s *TokenStore) { s.collection = name }
}

// NewTokenStore builds a TokenStore over an existing *firestore.Client the
// consumer already owns (and stays responsible for closing). No I/O happens
// here — Firestore needs no schema, so there is no migrate/validate step.
func NewTokenStore(client *firestore.Client, opts ...Option) *TokenStore {
	if client == nil {
		panic("gworkspace/firestore: NewTokenStore called with nil client")
	}
	s := &TokenStore{client: client, collection: DefaultCollection}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *TokenStore) GetRefreshToken(ctx context.Context, owner string) (string, error) {
	ref, err := s.doc(owner)
	if err != nil {
		return "", err
	}
	snap, err := ref.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return "", gworkspace.ErrNotConnected
	}
	if err != nil {
		return "", fmt.Errorf("gworkspace/firestore: get refresh token: %w", err)
	}
	var doc tokenDoc
	if err := snap.DataTo(&doc); err != nil {
		return "", fmt.Errorf("gworkspace/firestore: decode token for %q: %w", owner, err)
	}
	return doc.RefreshToken, nil
}

// SaveRefreshToken inserts or replaces the refresh token for owner. Like the
// postgres sibling's upsert, created_at is written once on first save and left
// untouched on replacement, so it keeps meaning "when the owner first
// connected". The read-then-write runs in a transaction so concurrent saves
// serialize instead of racing on that first write.
func (s *TokenStore) SaveRefreshToken(ctx context.Context, owner, refreshToken string) error {
	ref, err := s.doc(owner)
	if err != nil {
		return err
	}
	err = s.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		_, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return tx.Set(ref, map[string]any{
				"refresh_token": refreshToken,
				"created_at":    firestore.ServerTimestamp,
			})
		}
		if err != nil {
			return err
		}
		return tx.Update(ref, []firestore.Update{
			{Path: "refresh_token", Value: refreshToken},
		})
	})
	if err != nil {
		return fmt.Errorf("gworkspace/firestore: save refresh token: %w", err)
	}
	return nil
}

// DeleteRefreshToken removes the owner's token document. The Exists
// precondition makes Firestore reject deleting a missing document instead of
// succeeding silently, so absence maps to ErrNotConnected like GetRefreshToken.
// Firestore reports the failed precondition as NotFound; FailedPrecondition is
// matched too in case the server ever reports it as such.
func (s *TokenStore) DeleteRefreshToken(ctx context.Context, owner string) error {
	ref, err := s.doc(owner)
	if err != nil {
		return err
	}
	_, err = ref.Delete(ctx, firestore.Exists)
	if code := status.Code(err); code == codes.NotFound || code == codes.FailedPrecondition {
		return gworkspace.ErrNotConnected
	}
	if err != nil {
		return fmt.Errorf("gworkspace/firestore: delete refresh token: %w", err)
	}
	return nil
}

// doc resolves owner to its document reference, rejecting owners that cannot
// be Firestore document IDs.
func (s *TokenStore) doc(owner string) (*firestore.DocumentRef, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	return s.client.Collection(s.collection).Doc(owner), nil
}

// validateOwner enforces Firestore's document-ID constraints on the opaque
// owner string. The owner is used verbatim as the document ID — no escaping —
// so the rare owner that violates a constraint is rejected loudly here instead
// of corrupting a document path or failing server-side with an opaque error.
func validateOwner(owner string) error {
	switch {
	case owner == "":
		return errors.New("gworkspace/firestore: owner is empty")
	case owner == "." || owner == "..":
		return fmt.Errorf("gworkspace/firestore: owner %q is a reserved document ID", owner)
	case strings.Contains(owner, "/"):
		return fmt.Errorf("gworkspace/firestore: owner %q contains '/', not allowed in a document ID", owner)
	case len(owner) > 1500:
		return fmt.Errorf("gworkspace/firestore: owner exceeds Firestore's 1500-byte document ID limit (%d bytes)", len(owner))
	case len(owner) >= 4 && strings.HasPrefix(owner, "__") && strings.HasSuffix(owner, "__"):
		return fmt.Errorf("gworkspace/firestore: owner %q matches Firestore's reserved __*__ document ID pattern", owner)
	}
	return nil
}

var _ gworkspace.TokenStore = (*TokenStore)(nil)
