package gworkspace

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/oauth2"
)

type fakeTokenStore struct {
	token string
	err   error
	saved map[string]string
}

func (f *fakeTokenStore) GetRefreshToken(_ context.Context, _ string) (string, error) {
	return f.token, f.err
}

func (f *fakeTokenStore) SaveRefreshToken(_ context.Context, owner, refreshToken string) error {
	if f.saved == nil {
		f.saved = map[string]string{}
	}
	f.saved[owner] = refreshToken
	return nil
}

func (f *fakeTokenStore) DeleteRefreshToken(_ context.Context, owner string) error {
	if f.err != nil {
		return f.err
	}
	delete(f.saved, owner)
	return nil
}

// TokenSource must surface ErrNotConnected from the store so domain clients
// (Calendar, Gmail, Contacts) can propagate it to callers without touching
// the network.
func TestTokenSourceNotConnected(t *testing.T) {
	c := NewClient(&fakeTokenStore{err: ErrNotConnected}, &oauth2.Config{})
	_, err := c.TokenSource(context.Background(), "owner")
	if !errors.Is(err, ErrNotConnected) {
		t.Errorf("TokenSource err = %v, want ErrNotConnected", err)
	}
}

// Disconnect must surface ErrNotConnected from the store so callers can tell
// "nothing to disconnect" apart from infrastructure failures.
func TestDisconnectNotConnected(t *testing.T) {
	c := NewClient(&fakeTokenStore{err: ErrNotConnected}, &oauth2.Config{})
	err := c.Disconnect(context.Background(), "owner")
	if !errors.Is(err, ErrNotConnected) {
		t.Errorf("Disconnect err = %v, want ErrNotConnected", err)
	}
}

// Connected maps the store's three outcomes for the UI status check: a stored
// token is true, ErrNotConnected is false-without-error, anything else is an
// infrastructure error.
func TestConnected(t *testing.T) {
	tests := []struct {
		name    string
		store   *fakeTokenStore
		want    bool
		wantErr bool
	}{
		{name: "connected", store: &fakeTokenStore{token: "refresh-xyz"}, want: true},
		{name: "not connected", store: &fakeTokenStore{err: ErrNotConnected}, want: false},
		{name: "store failure", store: &fakeTokenStore{err: errors.New("boom")}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(tt.store, &oauth2.Config{})
			got, err := c.Connected(context.Background(), "owner")
			if (err != nil) != tt.wantErr {
				t.Fatalf("Connected err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Connected = %v, want %v", got, tt.want)
			}
		})
	}
}
