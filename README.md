# gworkspace

Module Go `go.naturallyfunny.dev/gworkspace` — reusable public library untuk akses
Google Workspace (Calendar + Gmail + Contacts) atas nama seorang user. Dirancang
sebagai interface-based library agar dapat dipakai lintas project, tidak terikat ke
satu database atau satu aplikasi.

Satu OAuth connect per user mencakup seluruh Workspace: Calendar, Gmail, dan
Contacts berbagi satu refresh token yang di-grant terhadap gabungan scope
per-domain (`CalendarRequiredScopes` + `GmailRequiredScopes` +
`ContactsRequiredScopes`, sesuai kebutuhan konsumen).

## Tujuan Pemakaian (baca sebelum audit)

Library ini dipakai sebagai **tool yang dipanggil oleh AI agent**, bukan backend
high-throughput. Pola traffic-nya: panggilan sporadik, satu aksi per intent user
(baca event hari ini, kirim email, cari contact), volume rendah. Konteks ini
menentukan trade-off di bawah — **jangan menilai repo ini dengan standar service
high-throughput.** Lihat "Design Decisions".

## Struktur

```
client.go       Client, TokenStore, ErrNotConnected/ErrMissingScopes, checkScopes,
                NewClient, OAuth (AuthURL/Exchange/Connect/Disconnect), TokenSource
calendar.go     Calendar, CalendarRequiredScopes, Event/EventQuery/EventInput,
                NewCalendar, GetEvents, AddEvent
gmail.go        Gmail, GmailRequiredScopes, Message/Label, MessageQuery/LabelQuery,
                ReadMessages, SendEmail, GetLabels, ApplyLabel, CreateLabel,
                GetMessagesByLabel
contact.go      Contacts, ContactsRequiredScopes, Contact/ContactInput/ContactQuery,
                GetContacts, AddContact
postgres/
  store.go      implementasi TokenStore di atas Querier (pgxpool), NewTokenStore/WithAutoMigrate
  migrations/   SQL files, di-embed via //go:embed
firestore/
  store.go      implementasi TokenStore di atas Cloud Firestore (satu dokumen per owner,
                doc ID = owner), NewTokenStore/WithCollection — tanpa migrasi
```

## Cara Pakai

```go
cfg := &oauth2.Config{
    ClientID:     clientID,
    ClientSecret: clientSecret,
    RedirectURL:  redirectURL,
    Endpoint:     google.Endpoint,
    // Gabungkan scope domain yang dipakai; konstruktor domain fail-fast
    // kalau ada yang kurang.
    Scopes: slices.Concat(gworkspace.CalendarRequiredScopes,
        gworkspace.GmailRequiredScopes, gworkspace.ContactsRequiredScopes),
}
store, err := postgres.NewTokenStore(ctx, pool, postgres.WithAutoMigrate())
client := gworkspace.NewClient(store, cfg)

// Alternatif: token di Cloud Firestore (alias salah satu import firestore;
// tidak ada migrasi):
//
//	import (
//	    gcfs "cloud.google.com/go/firestore"
//	    "go.naturallyfunny.dev/gworkspace/firestore"
//	)
//
//	fs, err := gcfs.NewClient(ctx, projectID)
//	store := firestore.NewTokenStore(fs)

// Connect flow (sekali per user):
url := client.AuthURL(state)            // arahkan user ke sini
err = client.Connect(ctx, owner, code)  // di callback
err = client.Disconnect(ctx, owner)     // hapus refresh token tersimpan

// Pemakaian:
cal, err := gworkspace.NewCalendar(client)
gm, err  := gworkspace.NewGmail(client)
con, err := gworkspace.NewContacts(client)

events, err   := cal.GetEvents(ctx, owner, gworkspace.EventQuery{Query: "standup"})
err            = gm.SendEmail(ctx, owner, "bob@example.com", "Hi", "body")
contacts, err := con.GetContacts(ctx, owner, gworkspace.ContactQuery{})
```

User belum connect → method mengembalikan `gworkspace.ErrNotConnected`
(`errors.Is`), supaya konsumen bisa merutekan ke flow login.

## Migrations

Dijalankan **lewat koneksi yang sama** yang di-inject ke `NewTokenStore` (tanpa DSN,
tanpa `golang-migrate`): tiap file `migrations/*.sql` di-`Exec` urut nama. Karena
itu setiap statement **wajib idempoten** (`IF NOT EXISTS` / `IF EXISTS`) — file
di-apply ulang setiap start. Naming: `000N_deskripsi.up.sql`. Tidak ada `.down.sql`
(file `.sql` apa pun di folder ini akan dieksekusi). Jangan edit migration yang
sudah di-commit; tambahkan file baru.

Panggil via `WithAutoMigrate()` saat konstruksi, atau eksplisit dengan
`store.Migrate(ctx)`.

## Design Decisions (sengaja — bukan temuan audit)

Trade-off berikut ditimbang sadar untuk use-case "tool AI agent, traffic rendah".

- **Token refresh per-request** (`xxxFor`, client.go). Tiap panggilan bikin
  `*Service` Google baru dari refresh token lalu dibuang. Aman untuk traffic
  rendah. Caching `*Service`/access-token per-user ditunda sampai ada kebutuhan
  throughput nyata.
- **Limit consumer-controlled, tanpa paginasi.** Query structs (`EventQuery`,
  `MessageQuery`, `LabelQuery`, `ContactQuery`) expose `Limit int`: nol atau
  negatif → no cap (API default berlaku); positif → explicit cap. Paginasi
  konfigurabel ditunda.
- **Refresh token plaintext** (`postgres/store.go`). Enkripsi-at-rest adalah
  tanggung jawab **konsumen** lewat implementasi `TokenStore`-nya sendiri, bukan
  library.
- **Sentinel minimal.** Hanya `ErrNotConnected` dan `ErrMissingScopes`. Library
  tidak membungkus error Google API dengan sentinel buatan sendiri — consumer
  yang perlu branch pada kode HTTP inspect langsung via
  `errors.As(err, &googleapi.Error{})`.

## Conventions

- `TokenStore` interface didefinisikan di `client.go` — consumer-defined interface.
- `postgres.NewTokenStore(ctx, db, opts...)` — ambil `Querier` sempit (pgxpool
  memenuhinya), migrasi lewat koneksi itu sendiri. Tanpa DSN kedua.
- `firestore.NewTokenStore(client, opts...)` — di atas `*firestore.Client` milik
  konsumen; doc ID = owner, koleksi via `WithCollection`.
- Tidak ada `pkg/` — flat structure.
- Nol referensi ke aplikasi konsumen apa pun; user diidentifikasi lewat
  `owner string` opaque.
