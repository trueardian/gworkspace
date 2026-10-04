# gworkspace — catatan untuk Claude

Domain client tunggal untuk Google Workspace (Calendar + Gmail + Contacts), satu
OAuth refresh token per user. Library generik `go.trueardian.com/gworkspace`,
dipakai sebagai tool AI agent (traffic rendah). Blueprint: `go.trueardian.com/spotify`
(pola `TokenStore` + OAuth per-user) dan `go.trueardian.com/tuya` (domain client
owner-scoped, consumer-defined interface).

## Prinsip yang mengikat (non-negotiable)

1. **Nol coupling ke aplikasi konsumen.** Tidak ada referensi ke aplikasi
   atau identitas app apa pun. User = `owner string` opaque.
2. **`TokenStore` consumer-defined interface** di `client.go`. Library tidak bangun
   credential atau pool DB sendiri — semua via DI di `NewClient`. Implementasi Postgres
   di subpackage `postgres/`.
3. **Satu refresh token per user untuk seluruh Workspace** (gabungan scope
   per-domain: `CalendarRequiredScopes`/`GmailRequiredScopes`/`ContactsRequiredScopes`).
   Bukan satu token per API.
4. **OAuth refresh per-request** (`TokenSource` → `calendarFor`/`gmailFor`/`peopleFor`).
5. **`ErrNotConnected`** dari `TokenStore.GetRefreshToken`, di-surface oleh `Client`.
   Didefinisikan di `client.go` (root package).
6. Method mengembalikan **tipe domain package ini** (`Event`, `Message`, `Label`,
   `Contact`) — bukan tipe mentah `google.golang.org/api/...`.
7. **Error handling: tiap method urus sendiri** via `fmt.Errorf("op: %w", err)`.
   Tidak ada helper wrapping terpusat. Consumer yang perlu cek kode HTTP pakai
   `errors.As(err, &googleapi.Error{})` langsung — library tidak sembunyikan itu.

## Struktur

```
client.go       Client, TokenStore (Get/Save/DeleteRefreshToken), ErrNotConnected,
                  ErrMissingScopes, checkScopes, NewClient,
                  AuthURL/Exchange/Connect/Disconnect, TokenSource
calendar.go     Calendar, CalendarRequiredScopes, Event/EventQuery/EventInput,
                  NewCalendar, GetEvents, AddEvent
gmail.go        Gmail, GmailRequiredScopes, Message/Label, NewGmail, ReadMessages,
                  SendEmail, GetLabels, ApplyLabel, CreateLabel, GetMessagesByLabel
contact.go      Contacts, ContactsRequiredScopes, Contact/ContactInput, NewContacts,
                  GetContacts, AddContact
postgres/       store.go: TokenStore (gworkspace.TokenStore impl),
                  NewTokenStore/WithAutoMigrate, migrations/
firestore/      store.go: TokenStore di atas Cloud Firestore (satu dokumen per owner,
                  doc ID = owner, koleksi gworkspace_tokens / WithCollection),
                  NewTokenStore(client, opts) — tanpa migrasi, created_at dipertahankan
                  saat replace (transactional)
*_test.go       mapping + ErrNotConnected (tanpa network)
```

## OAuth

Konsumen membangun `*oauth2.Config` (Google endpoint + gabungan `*RequiredScopes`
domain yang dipakai) dan mengirimnya ke `gworkspace.NewClient(store, cfg)`.
Konstruktor domain (`NewCalendar` dst) fail-fast via `checkScopes` kalau
`cfg.Scopes` kurang. `AuthURL` me-request `AccessTypeOffline`
+ `prompt=consent` agar Google mengembalikan refresh token. Tiap call fitur bikin
`*Service` Google baru dari refresh token (per-request, lalu dibuang).

## Konteks proyek (kenapa repo ini ada)

Library generik untuk konsumen yang butuh akses Google Workspace (Calendar, Gmail,
Contacts) per-user dengan satu OAuth token. Dirancang sebagai fondasi ADK toolset
(`adk/gcal`, `adk/gmail`, `adk/contact`). Repo ini greenfield.

## Design Decisions

Ini **keputusan sadar**, bukan technical debt atau TODO yang belum sempat digarap.
Rationale lengkap (decision → constraint → kenapa alternatif "obvious" salah) ada di
README §7 "Design rationale". Ringkas:

- **Refresh per-request** (§7.3): stateless & correct-by-construction untuk use-case
  low-traffic. Dalam satu request, `oauth2.Config.TokenSource` sudah pakai
  `ReuseTokenSource` (access token di-reuse). Cross-request caching = YAGNI: seam-nya
  (`TokenSource`) sudah siap kalau throughput nyata muncul. Jangan tambahkan cache
  spekulatif.
- **Limit consumer-controlled, tanpa paginasi** (query structs, README §5/§9): zero
  atau negatif → no cap (API default); positif → explicit cap. Cukup untuk "top N"
  ala agent. `PageToken` adalah penambahan aditif kalau bulk-export dibutuhkan.
- **Refresh token plaintext** (`postgres/store.go`): enkripsi-at-rest sengaja jadi
  tanggung jawab konsumen lewat implementasi `TokenStore`-nya (§7.5) — bukan lubang
  keamanan yang terlewat.

**Error philosophy** (§7.5): library tidak wrap atau sembunyikan Google API errors
dengan sentinel buatan sendiri (dulu ada `WrapError` + `ErrRateLimited` — sudah
dihapus). Terlalu abstraktif dan tidak idiomatic Go untuk public library. Consumer
punya akses penuh ke `*googleapi.Error` untuk inspect kode HTTP, message, dll.

Kalau reviewer (mis. Codex) menandai salah satu poin di atas sebagai "kekurangan",
itu salah baca konteks: use-case-nya tool AI agent low-traffic, dan tiap poin punya
seam yang jelas untuk di-extend tanpa rewrite.

## Verifikasi

`go build ./...`, `go vet ./...`, `go test ./...` harus bersih. Test = mapping murni
+ jalur `ErrNotConnected`/sentinel (tanpa network). `postgres/store_test.go` pakai
fake `Querier` (tanpa DB) untuk mapping `ErrNoRows`/`RowsAffected==0` → `ErrNotConnected`,
validasi schema, dan auto-migrate (cov ~94%). Pakai conventional commits.

## Prev session

Perubahan yang sudah di-commit:

1. **Flatten `auth/` ke root package.** `auth.Client` bukan sekadar auth utility —
   dia adalah central client yang consumer pegang. Nama subpackage `auth` misleading
   (mendeskripsikan mekanisme, bukan peran). Semua isi `auth/client.go` dipindah ke
   `client.go` (root package); `New` di-rename ke `NewClient`; `auth/postgres/` →
   `postgres/` (root-level subpackage); `auth/` directory dihapus.
2. **`TokenStore` dan `ErrMissingScopes` sekarang di root package** (`client.go`).
   Import `go.trueardian.com/gworkspace/auth` tidak diperlukan lagi.
3. Consumer usage: `c := gworkspace.NewClient(store, cfg)` lalu pass `c` ke
   `NewCalendar`/`NewGmail`/`NewContacts`.

Catatan arah: langkah berikutnya membangun ADK toolset (`gcal`, `gmail`, `contact`)
mengikuti pola `tuya/toolset.go` — interface narrow sisi konsumen, `forAgent(err)`
map sentinel, output `...View`.
