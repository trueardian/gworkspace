# gworkspace

[![Go Reference](https://pkg.go.dev/badge/go.trueardian.com/gworkspace.svg)](https://pkg.go.dev/go.trueardian.com/gworkspace)
[![Go 1.25](https://img.shields.io/badge/go-1.25-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

A small, dependency-injected Go client for one user's Google Workspace —
**Calendar, Gmail, and Contacts behind a single OAuth refresh token** — reached by an
opaque `owner string` you define. Zero coupling to any application, database, or user
model: the library knows *how to call Google*, and nothing about *who your users are
or where you keep their tokens*.

It was built as the foundation for an AI agent's Workspace tools (`adk/gcal`,
`adk/gmail`, `adk/contact`): sporadic calls, one action per user intent — *read
today's events*, *send an email*, *find a contact* — at low volume. That traffic
profile is the premise behind several decisions below, and it is stated up front so a
reviewer weighs the code against the workload it targets, not a high-throughput
service.

```go
c := gworkspace.NewClient(store, cfg) // store: your TokenStore; cfg: *oauth2.Config
cal, _ := gworkspace.NewCalendar(c)   // fails fast if cfg lacks Calendar scopes

events, err := cal.GetEvents(ctx, owner, gworkspace.EventQuery{Query: "standup"})
if errors.Is(err, gworkspace.ErrNotConnected) {
	// owner hasn't completed OAuth — route them through c.AuthURL(state)
}
```

## 1. Install

```sh
go get go.trueardian.com/gworkspace
```

Requires **Go 1.25+** — the module's declared toolchain (`go 1.25` in `go.mod`), the
version it is built, vetted, and tested against. The library source leans on no
bleeding-edge language feature; the only version-sensitive line is the quick-start's
[`slices.Concat`](https://pkg.go.dev/slices#Concat) (Go 1.22+), used to assemble the
scope list. Lowering the directive is therefore mechanical if you must — 1.25 is
simply what CI proves.

## 2. Why this shape

Google Workspace access for a user reduces to a *single* artifact: one refresh token,
granted over the **union** of the scopes the user consented to. Which API you call,
which `*Service` object you build, which short-lived access token you present — all of
it is derivable from that one token, per request.

Google's SDK doesn't present it that way. It makes you re-derive the plumbing three
times — a separate service constructor and scope list for Calendar, Gmail, and
Contacts — and it is silent on the two things that are actually *yours*: **where the
token lives** and **who the user is**. Conflating those with the API calls is what
makes Workspace wiring get copy-pasted between projects.

So the library splits the two concerns the SDK leaves tangled:

- **Identity & persistence — your concern.** Expressed as an opaque `owner string`
  (the library never parses or interprets it) and a `TokenStore` interface you
  implement. This is where "which of my users is this, and where is their token?"
  lives.
- **Workspace access — the library's concern.** One `Client`, constructed once from
  your `*oauth2.Config`, that turns an `owner` into live Calendar / Gmail / Contacts
  services on demand.

The **refresh token is the seam** between the two layers. That is the whole reason
there is a single `Client`, a single token per user covering all of Workspace, and a
consumer-defined `TokenStore` rather than a database the library opens itself. Every
decision in §7 follows from taking that seam seriously.

## 3. Concepts

Pick your entry point by what you have and what you want:

| You have… / you want…                    | Use                                                      | Guarded by                                               |
| ---------------------------------------- | -------------------------------------------------------- | -------------------------------------------------------- |
| A user, but no stored token yet          | `Client.AuthURL(state)` → `Client.Connect(owner, code)`  | `ErrMissingScopes` if the user withholds a permission    |
| A connected `owner`, a Workspace action  | `NewCalendar` / `NewGmail` / `NewContacts` → its methods | scope check at construction · `ErrNotConnected` per call |
| To check or revoke a connection          | `Client.Connected(owner)` / `Client.Disconnect(owner)`   | `ErrNotConnected` (nothing stored)                       |

**Main types**

- **`Client`** — holds your `*oauth2.Config` and `TokenStore`; owns the OAuth flow
  (`AuthURL`, `Exchange`, `Connect`, `Disconnect`, `Connected`) and hands out a fresh
  `oauth2.TokenSource` per request via `TokenSource`.
- **`TokenStore`** — the three-method interface (`Get` / `Save` / `DeleteRefreshToken`)
  you implement, or take from a bundled adapter. Declared in `client.go` because that
  is the package that *uses* it (see §7.1).
- **`Calendar` / `Gmail` / `Contacts`** — the domain clients, each constructed from a
  `Client` and each fail-fast if the `Client` lacks its `*RequiredScopes`.
- **`Event` / `Message` / `Label` / `Contact`** — flat value types this package owns,
  returned instead of the raw `google.golang.org/api/...` structs (§7.4).

**Bundled `TokenStore` adapters** (optional subpackages, each pulling its own heavy
dependency only if you import it):

- `postgres.TokenStore` — over a narrow `pgx` `Querier`; embedded, idempotent migrations.
- `firestore.TokenStore` — over Cloud Firestore; one document per owner, `owner` = doc ID.

**Dependency direction** — the arrows point *inward*, toward the interface:

```
   your app ──▶ gworkspace.Client ──▶ gworkspace.TokenStore  (interface)
                       │                        ▲
                       │                        │ implements
                       ▼                        │
        Calendar / Gmail / Contacts    postgres.TokenStore · firestore.TokenStore
                       │                        │
                       ▼                        ▼
            google.golang.org/api        pgx pool · *firestore.Client  (you own these)
```

## 4. Setup

Three wiring steps: **store → client → domain**.

```go
// 1. Store — you own the connection; the store borrows it (dependency injection).
pool, _ := pgxpool.New(ctx, dsn)
store, _ := postgres.NewTokenStore(ctx, pool, postgres.WithAutoMigrate())

// 2. Client — you own the oauth2.Config. Scopes = the union of the domains you use.
cfg := &oauth2.Config{
	ClientID:     clientID,
	ClientSecret: clientSecret,
	RedirectURL:  "https://app.example.com/oauth/callback",
	Endpoint:     google.Endpoint,
	Scopes: slices.Concat(
		gworkspace.CalendarRequiredScopes,
		gworkspace.GmailRequiredScopes,
		gworkspace.ContactsRequiredScopes,
	),
}
client := gworkspace.NewClient(store, cfg)

// 3. Domain clients — each verifies its scopes against cfg at construction.
cal, err := gworkspace.NewCalendar(client) // err if cfg is missing Calendar scopes
gm, _ := gworkspace.NewGmail(client)
con, _ := gworkspace.NewContacts(client)
```

Behavior worth knowing while wiring:

- **Scopes are additive and checked twice.** Put only the domains you use into
  `cfg.Scopes`; each constructor fail-fasts (`checkScopes`) if its scopes are absent,
  turning a misconfiguration into a startup error rather than a 403 mid-request.
- **`AuthURL` forces a refresh token.** It sets `access_type=offline` *and*
  `prompt=consent`, because Google returns a refresh token only under offline access
  and only when the consent screen is actually shown (§7.6).
- **Firestore instead?** Swap step 1 for `firestore.NewTokenStore(fsClient)` — no
  migration, no other change:

  ```go
  fs, _ := gcfs.NewClient(ctx, projectID) // gcfs "cloud.google.com/go/firestore"
  store := firestore.NewTokenStore(fs)     // you still own fs and close it
  ```

### Scopes

| Domain   | Variable                 | OAuth scope(s)                                  |
| -------- | ------------------------ | ----------------------------------------------- |
| Calendar | `CalendarRequiredScopes` | `.../auth/calendar`                             |
| Gmail    | `GmailRequiredScopes`    | `.../auth/gmail.modify`, `.../auth/gmail.send`  |
| Contacts | `ContactsRequiredScopes` | `.../auth/contacts`                             |

(`...` = `https://www.googleapis.com`.) The values come straight from the Google API
packages, so they track the SDK rather than hard-coded strings.

## 5. Usage

Every feature method takes the `owner` as its second argument and returns this
package's own types. `ErrNotConnected` is the one branch a caller should always
handle — it means "route this user back through OAuth":

```go
events, err := cal.GetEvents(ctx, owner, gworkspace.EventQuery{
	Query: "standup",
	Limit: 10, // 0 or negative → no cap (Google's API default applies)
})
switch {
case errors.Is(err, gworkspace.ErrNotConnected):
	redirectToConnect(w, client.AuthURL(state)) // onboard the user
	return
case err != nil:
	// Google's error is intact — inspect the HTTP status directly if you need to:
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code == http.StatusTooManyRequests {
		backoff()
	}
	return
}

_ = gm.SendEmail(ctx, owner, "bob@example.com", "Hi", "body")
contacts, _ := con.GetContacts(ctx, owner, gworkspace.ContactQuery{})
```

> **Security.** The bundled stores persist the refresh token **in plaintext** — a
> refresh token is a long-lived credential to the user's Workspace. Encryption at rest
> is deliberately the `TokenStore`'s responsibility, not the library's (§7.5): if you
> need envelope encryption, implement `TokenStore` over your KMS. Whatever store you
> use, protect it as you would a password table.

## 6. Connecting accounts

The connect lifecycle is a per-user, one-time flow the `Client` owns end to end; your
app owns only the HTTP routing and the `state` value (which Google echoes back for
CSRF correlation):

```go
// Start: send the user to Google's consent screen.
http.Redirect(w, r, client.AuthURL(state), http.StatusFound)

// Callback: trade the code for a refresh token and persist it.
err := client.Connect(ctx, owner, r.URL.Query().Get("code"))
// errors.Is(err, gworkspace.ErrMissingScopes) → user unchecked a permission; retry.

connected, _ := client.Connected(ctx, owner) // for a UI "Connected ✓" badge
err = client.Disconnect(ctx, owner)          // forget the stored token
```

`Connect` persists exactly one artifact — the refresh token — via
`TokenStore.SaveRefreshToken`. `Disconnect` deletes it and returns `ErrNotConnected`
when there was nothing to forget. Note the asymmetry, stated honestly: `Disconnect`
only forgets the token *on your side*; the grant itself stays listed on the user's
Google Account until they revoke it there. `Connected` likewise reports only what the
store knows — a token revoked at Google still reads as connected until its first
failing use.

**Backing store.** Both adapters keep one record per owner in `gworkspace_tokens`
(`owner`, `refresh_token`, `created_at`) — a table in Postgres, a collection in
Firestore with the owner as the document ID. `created_at` is written once on first
connect and preserved across re-connects (an upsert in Postgres; a transactional
read-then-write in Firestore), so it keeps meaning "when the owner first connected."

### Migrations

The Postgres store runs migrations **through the very connection you inject** — no
second DSN, no `golang-migrate` dependency. Each `migrations/*.sql` file is embedded
via `//go:embed` and `Exec`'d in filename order (`0001_…`, `0002_…`). Because a file
may be re-applied on every startup, **every statement must be idempotent**
(`IF NOT EXISTS` / `IF EXISTS`). Run them with `WithAutoMigrate()` at construction, or
explicitly via `store.Migrate(ctx)`. Never edit a committed migration — add a new
file. Firestore is schemaless, so it has no migration step at all.

## 7. Design rationale

The decisions a generic-library reviewer might flag — each as
**decision → the constraint that justifies it → why the obvious alternative is
wrong** — so intent isn't mistaken for accident.

### 7.1 `TokenStore` is declared where it's *used*, not where it's implemented

**Decision.** The interface lives in `client.go`; `postgres` and `firestore` implement
it and depend on the root package, not the reverse.

**Constraint.** The consumer, not the library, owns the database, the pool, and the
encryption policy. The library only ever *consumes* tokens.

**Why not the obvious thing.** Defining the interface beside each implementation (or
having the library build a pool from a DSN) inverts the dependency and forces every
consumer onto our storage choices. Go's guidance is explicit —
[Effective Go](https://go.dev/doc/effective_go#interfaces) and
[Go Code Review Comments](https://go.dev/wiki/CodeReviewComments#interfaces): *"Go
interfaces generally belong in the package that uses values of the interface type, not
the package that implements those values."* Declaring it here keeps the library
coupled to nothing and testable with a hand-written fake.

### 7.2 The Postgres store takes a two-method `Querier`, not `*pgxpool.Pool`

**Decision.** `postgres.NewTokenStore` accepts `Querier` (`Exec` + `QueryRow`).

**Constraint.** Callers variously hold a pool, a single connection, or want the store
to enlist in an existing transaction.

**Why not the obvious thing.** Taking the concrete `*pgxpool.Pool` would forbid all
three and make unit tests need a live database. *"The bigger the interface, the weaker
the abstraction"* ([Go Proverbs](https://go-proverbs.github.io/)): the narrow one is
satisfied by a pool, a `pgx.Conn`, a `pgx.Tx`, **and** a trivial fake — which is how
`postgres/store_test.go` reaches 94% coverage with no database.

### 7.3 One token for all of Workspace, services rebuilt per request

**Decision.** A single refresh token over the union of scopes; each call builds a fresh
`*Service` via `Client.TokenSource` and discards it.

**Constraint.** A low-traffic agent tool — sporadic, one call per intent.

**Why not the obvious thing.** Caching a `*Service` or access token per owner buys
throughput this workload never spends, and pays for it with a cache to invalidate on
disconnect, a per-owner map to bound and evict, and staleness after revocation. The
stateless path is correct by construction — and not naïve about refreshes:
`oauth2.Config.TokenSource` returns a
[`ReuseTokenSource`](https://pkg.go.dev/golang.org/x/oauth2#ReuseTokenSource), so the
access token is already reused within a request. This is YAGNI, not an unfinished TODO:
if real throughput ever demands it, a per-owner `ReuseTokenSource` cache slots in
behind the unchanged `TokenSource` seam.

### 7.4 Methods return this package's types, never raw `google.golang.org/api` types

**Decision.** `[]Event`, `[]Message`, `[]Label`, `[]Contact` — flat value types.

**Constraint.** The generated Google types are large, pointer-heavy, and leak the
transport (RFC 3339 *vs* date-only `EventDateTime`, base64url message bodies,
`Names[0].DisplayName`).

**Why not the obvious thing.** Returning them directly would bind every caller to the
SDK's version and scatter the fiddly decoding (`parseEventTime`, `decodeBody`) across
call sites. One mapping boundary gives a stable API and one tested place for the
awkward bits.

### 7.5 Two sentinels only; Google's errors pass through untouched

**Decision.** `ErrNotConnected` and `ErrMissingScopes` are the only sentinels;
everything else is `fmt.Errorf("op: %w", err)` over the intact `*googleapi.Error`.

**Constraint.** The SDK already models HTTP failures precisely; the caller keeps full
fidelity via `errors.As(err, &googleapi.Error{})`.

**Why not the obvious thing.** A house taxonomy (`ErrRateLimited`, `ErrNotFound`, …)
mapped from status codes only *subtracts* information — this library had exactly that
(`WrapError` + `ErrRateLimited`) and removed it. The two sentinels that remain encode
states the caller genuinely can't derive otherwise: *not connected*, *scope withheld*.
That is precisely what `%w` is [designed to allow](https://go.dev/blog/go1.13-errors).

### 7.6 OAuth requests offline access *and* forces the consent screen

**Decision.** `AuthURL` sets both `access_type=offline` and `prompt=consent`; `Exchange`
then re-checks the granted `scope` field.

**Constraint.** Google returns a refresh token only under offline access, and only on
an account's *first* authorization — a re-auth of an already-granted account omits it
unless consent is forced
([web-server OAuth guide](https://developers.google.com/identity/protocols/oauth2/web-server#offline)).

**Why not the obvious thing.** Requesting offline access alone silently yields *no*
refresh token for returning users — the single most common Workspace-OAuth bug. Forcing
consent guarantees one every time; verifying the returned scopes catches partial
consent at connect time instead of at the first failing API call.

### 7.7 Firestore keys documents by the owner, validated before any I/O

**Decision.** One document per owner, `owner` used verbatim as the document ID, so a
lookup is a direct `Doc(owner).Get`. `validateOwner` screens the string first.

**Constraint.** The owner is opaque and used unescaped as a Firestore path segment,
which has [hard ID rules](https://firebase.google.com/docs/firestore/quotas#collections_documents_and_fields)
(no `/`, not `.`/`..`, not `__*__`, ≤ 1500 bytes).

**Why not the obvious thing.** Storing the owner as a queried *field* costs an index
and a query where a point-read suffices, and diverges from the Postgres store's
one-row-per-owner shape. Trusting the string instead would let a stray `/` corrupt a
path or fail server-side with an opaque error; validating locally fails loudly and
early. The save runs in a transaction so `created_at` survives concurrent re-connects.

## 8. Testing

Tests are pure mapping logic plus the sentinel / `ErrNotConnected` paths — **no
network, no live backend** — which is exactly the slice that can be asserted
deterministically:

```sh
go build ./...          # clean
go vet ./...            # clean
go test ./... -cover
# ok  …/gworkspace            coverage: 45.3% of statements
# ok  …/gworkspace/firestore  coverage: 13.5% of statements
# ok  …/gworkspace/postgres   coverage: 94.1% of statements
```

What each number honestly reflects:

- **Root (~45%)** — the type mappers (`eventFrom`, `messageFrom`, `contactFrom`,
  `decodeBody`), scope checking, `Connected`, and `ErrNotConnected` propagation across
  all ten feature methods. The uncovered part is each method's live-API leg and the
  interactive OAuth calls (`AuthURL`, `Exchange`, `Connect`) — unreachable without a
  Google endpoint.
- **firestore (13.5%)** — `validateOwner` exhaustively; the read/write paths need a
  real or emulated Firestore and are left to integration testing.
- **postgres (94%)** — a hand-written fake `Querier`: `ErrNoRows → ErrNotConnected`,
  `RowsAffected == 0 → ErrNotConnected` on delete, upsert SQL, schema validation, the
  nil-`Querier` panic, and the auto-migrate path — all without a database, which is
  precisely what §7.1 and §7.2 buy.

## 9. Layout

```
client.go        Client, TokenStore, ErrNotConnected, ErrMissingScopes, checkScopes,
                 NewClient, OAuth (AuthURL/Exchange/Connect/Disconnect/Connected), TokenSource
calendar.go      Calendar, CalendarRequiredScopes, Event/EventQuery/EventInput, GetEvents, AddEvent
gmail.go         Gmail, GmailRequiredScopes, Message/Label, ReadMessages, SendEmail,
                 GetLabels, CreateLabel, ApplyLabel, GetMessagesByLabel
contact.go       Contacts, ContactsRequiredScopes, Contact/ContactInput, GetContacts, AddContact
*_test.go        table-driven mapping + ErrNotConnected paths (no network)

postgres/
  store.go       TokenStore over a narrow pgx Querier; NewTokenStore/WithAutoMigrate
  store_test.go  fake-Querier tests: sentinel mapping, schema validation, auto-migrate
  migrations/    embedded, idempotent SQL (0001_initial.up.sql)
firestore/
  store.go       TokenStore over Cloud Firestore; doc ID = owner; WithCollection
  store_test.go  validateOwner rules
```

## 10. License

MIT © 2026 Ardian. See [LICENSE](LICENSE).
