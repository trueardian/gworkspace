package gworkspace

import (
	"context"
	"fmt"
	"time"

	calendarv3 "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

// CalendarRequiredScopes are the OAuth scopes needed for all Calendar methods.
// Include these in cfg.Scopes when building an auth.Client used with NewCalendar.
var CalendarRequiredScopes = []string{
	calendarv3.CalendarScope,
}

// Calendar provides Google Calendar access for a Workspace owner.
type Calendar struct {
	client *Client
}

// NewCalendar builds a Calendar that uses client to obtain per-owner OAuth tokens.
// Returns an error if client does not carry CalendarRequiredScopes.
func NewCalendar(client *Client) (*Calendar, error) {
	if err := checkScopes(client.Scopes(), CalendarRequiredScopes); err != nil {
		return nil, err
	}
	return &Calendar{client: client}, nil
}

// EventInput describes an event to create. CalendarID defaults to "primary".
// Guests are added as attendees by email address.
type EventInput struct {
	CalendarID  string
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	Guests      []string
}

// Event is a calendar event. Start and End are the event's instants; for all-day
// events they are the date at midnight UTC.
type Event struct {
	ID          string
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	Attendees   []string // attendee email addresses
	HTMLLink    string
}

// EventQuery filters a GetEvents call. The zero value lists upcoming events on
// the primary calendar. CalendarID defaults to "primary"; Query is a free-text
// search. TimeMin defaults to now (so the zero query is forward-looking); set it
// to a past time to include history. TimeMax is open unless set. Limit sets the
// maximum results returned; zero or negative applies no cap (Google Calendar
// API default applies).
type EventQuery struct {
	CalendarID string
	Query      string
	TimeMin    time.Time
	TimeMax    time.Time
	Limit      int
}

const defaultCalendarID = "primary"

// GetEvents returns events for the owner matching q, ordered by start time.
// Returns ErrNotConnected if the owner has not connected.
func (c *Calendar) GetEvents(ctx context.Context, owner string, q EventQuery) ([]Event, error) {
	svc, err := c.calendarFor(ctx, owner)
	if err != nil {
		return nil, err
	}

	calendarID := q.CalendarID
	if calendarID == "" {
		calendarID = defaultCalendarID
	}

	call := svc.Events.List(calendarID).
		SingleEvents(true).
		OrderBy("startTime").
		Context(ctx)
	if q.Query != "" {
		call = call.Q(q.Query)
	}
	// OrderBy("startTime") with no lower bound would return events from the
	// earliest available date; default TimeMin to now so the zero query is
	// forward-looking (matching Google's own quickstart).
	timeMin := q.TimeMin
	if timeMin.IsZero() {
		timeMin = time.Now()
	}
	call = call.TimeMin(timeMin.Format(time.RFC3339))
	if !q.TimeMax.IsZero() {
		call = call.TimeMax(q.TimeMax.Format(time.RFC3339))
	}
	if q.Limit > 0 {
		call = call.MaxResults(int64(q.Limit))
	}

	res, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("get events: %w", err)
	}
	events := make([]Event, 0, len(res.Items))
	for _, item := range res.Items {
		events = append(events, eventFrom(item))
	}
	return events, nil
}

// AddEvent creates an event for the owner and returns the created event.
// Returns ErrNotConnected if the owner has not connected.
func (c *Calendar) AddEvent(ctx context.Context, owner string, in EventInput) (Event, error) {
	svc, err := c.calendarFor(ctx, owner)
	if err != nil {
		return Event{}, err
	}

	calendarID := in.CalendarID
	if calendarID == "" {
		calendarID = defaultCalendarID
	}

	attendees := make([]*calendarv3.EventAttendee, 0, len(in.Guests))
	for _, g := range in.Guests {
		attendees = append(attendees, &calendarv3.EventAttendee{Email: g})
	}

	event := &calendarv3.Event{
		Summary:     in.Summary,
		Description: in.Description,
		Location:    in.Location,
		Start:       &calendarv3.EventDateTime{DateTime: in.Start.Format(time.RFC3339)},
		End:         &calendarv3.EventDateTime{DateTime: in.End.Format(time.RFC3339)},
		Attendees:   attendees,
	}

	created, err := svc.Events.Insert(calendarID, event).Context(ctx).Do()
	if err != nil {
		return Event{}, fmt.Errorf("add event: %w", err)
	}
	return eventFrom(created), nil
}

func (c *Calendar) calendarFor(ctx context.Context, owner string) (*calendarv3.Service, error) {
	ts, err := c.client.TokenSource(ctx, owner)
	if err != nil {
		return nil, err
	}
	return calendarv3.NewService(ctx, option.WithTokenSource(ts))
}

func eventFrom(e *calendarv3.Event) Event {
	attendees := make([]string, 0, len(e.Attendees))
	for _, a := range e.Attendees {
		attendees = append(attendees, a.Email)
	}
	return Event{
		ID:          e.Id,
		Summary:     e.Summary,
		Description: e.Description,
		Location:    e.Location,
		Start:       parseEventTime(e.Start),
		End:         parseEventTime(e.End),
		Attendees:   attendees,
		HTMLLink:    e.HtmlLink,
	}
}

// parseEventTime reads an EventDateTime, which carries either DateTime (RFC3339,
// for timed events) or Date (yyyy-mm-dd, for all-day events). Returns the zero
// time when neither parses.
func parseEventTime(dt *calendarv3.EventDateTime) time.Time {
	if dt == nil {
		return time.Time{}
	}
	if dt.DateTime != "" {
		if t, err := time.Parse(time.RFC3339, dt.DateTime); err == nil {
			return t
		}
	}
	if dt.Date != "" {
		if t, err := time.Parse("2006-01-02", dt.Date); err == nil {
			return t
		}
	}
	return time.Time{}
}
