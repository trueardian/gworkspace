package gworkspace

import (
	"context"
	"fmt"

	"google.golang.org/api/option"
	peoplev1 "google.golang.org/api/people/v1"
)

// ContactsRequiredScopes are the OAuth scopes needed for all Contacts methods.
// Include these in cfg.Scopes when building an auth.Client used with NewContacts.
var ContactsRequiredScopes = []string{
	peoplev1.ContactsScope,
}

const personFields = "names,emailAddresses,phoneNumbers"

// ContactQuery filters a GetContacts call. Limit sets the maximum results
// returned; zero or negative applies no cap (People API default applies).
type ContactQuery struct {
	Limit int
}

// Contacts provides Google Contacts access for a Workspace owner.
type Contacts struct {
	client *Client
}

// NewContacts builds a Contacts client that uses client to obtain per-owner OAuth tokens.
// Returns an error if client does not carry ContactsRequiredScopes.
func NewContacts(client *Client) (*Contacts, error) {
	if err := checkScopes(client.Scopes(), ContactsRequiredScopes); err != nil {
		return nil, err
	}
	return &Contacts{client: client}, nil
}

// Contact is a person in the owner's Google Contacts. ResourceName is the People
// API identifier (e.g. "people/c123").
type Contact struct {
	ResourceName string
	Name         string
	Emails       []string
	Phones       []string
}

// ContactInput describes a contact to create.
type ContactInput struct {
	Name   string
	Emails []string
	Phones []string
}

// GetContacts returns the owner's contacts.
// Returns ErrNotConnected if the owner has not connected.
func (c *Contacts) GetContacts(ctx context.Context, owner string, q ContactQuery) ([]Contact, error) {
	svc, err := c.peopleFor(ctx, owner)
	if err != nil {
		return nil, err
	}
	call := svc.People.Connections.List("people/me").
		PersonFields(personFields).
		Context(ctx)
	if q.Limit > 0 {
		call = call.PageSize(int64(q.Limit))
	}
	res, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("get contacts: %w", err)
	}
	contacts := make([]Contact, 0, len(res.Connections))
	for _, p := range res.Connections {
		contacts = append(contacts, contactFrom(p))
	}
	return contacts, nil
}

// AddContact creates a contact for the owner and returns it.
// Returns ErrNotConnected if the owner has not connected.
func (c *Contacts) AddContact(ctx context.Context, owner string, in ContactInput) (Contact, error) {
	svc, err := c.peopleFor(ctx, owner)
	if err != nil {
		return Contact{}, err
	}

	person := &peoplev1.Person{}
	if in.Name != "" {
		person.Names = []*peoplev1.Name{{GivenName: in.Name}}
	}
	for _, e := range in.Emails {
		person.EmailAddresses = append(person.EmailAddresses, &peoplev1.EmailAddress{Value: e})
	}
	for _, p := range in.Phones {
		person.PhoneNumbers = append(person.PhoneNumbers, &peoplev1.PhoneNumber{Value: p})
	}

	// Request personFields on create too: without it the People API returns the
	// person without computed fields like Names[0].DisplayName, so contactFrom
	// would yield an empty Name on the created contact.
	created, err := svc.People.CreateContact(person).PersonFields(personFields).Context(ctx).Do()
	if err != nil {
		return Contact{}, fmt.Errorf("add contact: %w", err)
	}
	return contactFrom(created), nil
}

func (c *Contacts) peopleFor(ctx context.Context, owner string) (*peoplev1.Service, error) {
	ts, err := c.client.TokenSource(ctx, owner)
	if err != nil {
		return nil, err
	}
	return peoplev1.NewService(ctx, option.WithTokenSource(ts))
}

func contactFrom(p *peoplev1.Person) Contact {
	contact := Contact{ResourceName: p.ResourceName}
	if len(p.Names) > 0 {
		contact.Name = p.Names[0].DisplayName
	}
	for _, e := range p.EmailAddresses {
		contact.Emails = append(contact.Emails, e.Value)
	}
	for _, ph := range p.PhoneNumbers {
		contact.Phones = append(contact.Phones, ph.Value)
	}
	return contact
}
