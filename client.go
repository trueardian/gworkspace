// Package gworkspace is a reusable client for a user's Google Workspace:
// callers reach Calendar, Gmail, and Contacts by their own opaque owner string.
// Build one Client and pass it to any combination of domain constructors:
//
//	c   := gworkspace.NewClient(store, cfg)
//	cal := gworkspace.NewCalendar(c)
//	gm  := gworkspace.NewGmail(c)
//	con := gworkspace.NewContacts(c)
//
// cfg.Scopes determines which Workspace APIs are accessible. Assemble the
// scope list from the gworkspace.*RequiredScopes variables, e.g.:
//
//	cfg.Scopes = append(gworkspace.CalendarRequiredScopes, gworkspace.GmailRequiredScopes...)
package gworkspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/oauth2"
)

// ErrNotConnected is returned when the owner has not completed the OAuth
// connect flow and therefore has no stored refresh token. Route the user
// through Client.AuthURL / Client.Connect to resolve it.
var ErrNotConnected = errors.New("gworkspace: user not connected")

// checkScopes returns an error if have does not contain every scope in need.
// Used by domain constructors to fail fast when the Client was configured without
// the scopes that capability requires.
func checkScopes(have, need []string) error {
	haveSet := make(map[string]bool, len(have))
	for _, s := range have {
		haveSet[s] = true
	}
	var missing []string
	for _, s := range need {
		if !haveSet[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("gworkspace: missing required scopes: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Client holds an *oauth2.Config and a TokenStore; it builds a fresh token
// source per request from the owner's stored refresh token.
type Client struct {
	tokenStore TokenStore
	oauth2Cfg  *oauth2.Config
}

// TokenStore persists Google Workspace OAuth refresh tokens keyed by owner.
// Implement this interface to provide your own storage backend.
// GetRefreshToken and DeleteRefreshToken must return ErrNotConnected when no
// token exists for the owner. postgres.TokenStore and firestore.TokenStore in
// this module satisfy it.
type TokenStore interface {
	GetRefreshToken(ctx context.Context, owner string) (string, error)
	SaveRefreshToken(ctx context.Context, owner, refreshToken string) error
	DeleteRefreshToken(ctx context.Context, owner string) error
}

// NewClient builds a Client. cfg must carry the Google endpoint and the combined
// scope list for all Workspace APIs the consumer will use.
func NewClient(tokenStore TokenStore, cfg *oauth2.Config) *Client {
	return &Client{tokenStore: tokenStore, oauth2Cfg: cfg}
}

// Scopes returns the OAuth scopes this client was configured with.
// Domain constructors (NewCalendar, NewGmail, NewContacts) call this to verify
// the client carries the scopes they need before accepting it.
func (c *Client) Scopes() []string {
	return c.oauth2Cfg.Scopes
}

// AuthURL returns the Google consent URL the user must visit to grant access.
// state is handed to Google and returned verbatim on the callback; consumers
// use it to correlate the callback with a user and to guard against CSRF.
//
// Offline access plus prompt=consent are requested so Google returns a refresh
// token: without offline access there is no refresh token at all, and without
// forcing the consent screen Google omits it on re-authorization of an account
// that has already granted access.
func (c *Client) AuthURL(state string) string {
	return c.oauth2Cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
}

// ErrMissingScopes is returned by Exchange when Google did not grant all scopes
// that were requested in cfg.Scopes — usually because the user deselected some
// permissions on the consent screen. Trigger a new OAuth flow to re-request.
var ErrMissingScopes = errors.New("gworkspace: google did not grant all requested scopes")

// Exchange completes the OAuth flow by trading the authorization code from the
// Google callback for tokens, returning the refresh token to persist via
// TokenStore.SaveRefreshToken. The code is single-use.
// Returns ErrMissingScopes if Google did not grant all scopes in cfg.Scopes.
func (c *Client) Exchange(ctx context.Context, code string) (string, error) {
	token, err := c.oauth2Cfg.Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("exchange code: %w", err)
	}
	if token.RefreshToken == "" {
		return "", errors.New("gworkspace: exchange returned no refresh token")
	}
	if len(c.oauth2Cfg.Scopes) > 0 {
		granted := strings.Fields(fmt.Sprintf("%s", token.Extra("scope")))
		if missing := missingScopes(c.oauth2Cfg.Scopes, granted); len(missing) > 0 {
			return "", fmt.Errorf("%w: missing %s", ErrMissingScopes, strings.Join(missing, ", "))
		}
	}
	return token.RefreshToken, nil
}

// Connect completes the OAuth flow for owner: it trades code for tokens via
// Exchange and persists the resulting refresh token.
func (c *Client) Connect(ctx context.Context, owner, code string) error {
	refreshToken, err := c.Exchange(ctx, code)
	if err != nil {
		return err
	}
	return c.tokenStore.SaveRefreshToken(ctx, owner, refreshToken)
}

// Disconnect undoes Connect: it removes the owner's stored refresh token, so
// TokenSource returns ErrNotConnected until the owner completes a new OAuth
// flow. Returns ErrNotConnected when the owner was not connected.
//
// Only this side forgets the token — the grant itself stays listed on the
// owner's Google Account until they revoke it there or Google expires it.
func (c *Client) Disconnect(ctx context.Context, owner string) error {
	return c.tokenStore.DeleteRefreshToken(ctx, owner)
}

// Connected reports whether owner currently has a stored refresh token — i.e.
// has completed Connect and not since Disconnected. It only consults the
// TokenStore; it does not verify the grant against Google (a revoked-at-Google
// token still reads as connected until its first failing use).
func (c *Client) Connected(ctx context.Context, owner string) (bool, error) {
	_, err := c.tokenStore.GetRefreshToken(ctx, owner)
	if errors.Is(err, ErrNotConnected) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get token for owner %s: %w", owner, err)
	}
	return true, nil
}

// TokenSource resolves the owner's refresh token and returns an oauth2.TokenSource
// that refreshes access tokens on demand. Returns ErrNotConnected
// when the owner has not connected.
func (c *Client) TokenSource(ctx context.Context, owner string) (oauth2.TokenSource, error) {
	refreshToken, err := c.tokenStore.GetRefreshToken(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("get token for owner %s: %w", owner, err)
	}
	return c.oauth2Cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken}), nil
}

func missingScopes(required, granted []string) []string {
	grantedSet := make(map[string]bool, len(granted))
	for _, s := range granted {
		grantedSet[s] = true
	}
	var missing []string
	for _, s := range required {
		if !grantedSet[s] {
			missing = append(missing, s)
		}
	}
	return missing
}
