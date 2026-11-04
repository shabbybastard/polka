# Polka

[![CI](https://img.shields.io/github/actions/workflow/status/shabbybastard/polka/ci.yml?branch=main&label=CI&style=flat-square)](https://github.com/shabbybastard/polka/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/shabbybastard/polka?include_prereleases&style=flat-square&label=release)](https://github.com/shabbybastard/polka/releases)

**Your home e-book library — fast, private, and beautiful.**

Polka (Russian for *"bookshelf"*) is a self-hosted library for e-book
collection. Point it at a folder of books — or import a huge `.inpx` catalog —
and get a polished web interface with search, covers, annotations, an online
reader, reading lists, ratings, recommendations, and an OPDS feed for every
mobile reading app.

![Polka home page — personal shelves with covers, search, and reading progress](docs/screenshot-home.png)

---

## Highlights

- **Built for big collections.** A 690,000-book catalog imports in about a
  minute and stays instant to browse and search (SQLite + FTS5 under the hood).
- **Read in your browser.** A clean, newspaper-style reader for FB2, EPUB and
  TXT with continuous scrolling, themes (light / sepia / dark), adjustable
  fonts, footnotes, live tables of contents, and per-user reading progress.
  PDF opens in a dedicated reader with the same progress tracking.
- **Find your next book.** Personal shelves: *Reading now*, *Want to read*,
  *Continue the series*, and *For you* — recommendations scored from your
  ratings, lists, and reading history. Plus "similar books" on every book page,
  optionally enriched by external sources (FantLab, TasteDive).
- **Search everything.** Titles, authors, series, genres — and ISBN: paste an
  ISBN in any format and Polka finds the book locally or resolves it via
  Open Library / Google Books.
- **Reading lists & ratings.** Build custom lists, mark books you want to read,
  rate books 1–5 — average ratings are visible to everyone on your server.
  External ratings (LiveLib, Google Books, Open Library) can be enabled per
  source by the admin. External genres (LiveLib genres and Open Library
  subjects) can be merged into the catalog; both sources are off by default
  and disabling one removes the genres it contributed.
- **Admin tools.** Edit a book's title, authors, EPUB metadata and cover, change
  user display names, hide the home-page hero banner, reset a reader's progress
  when a position is no longer wanted, and bulk-load external genres for the
  whole catalog or only for books without genres (throttled server-side job).
- **Curated collections.** Well-known book lists — *100 Books of the Century*
  (Le Monde), BBC's *The Big Read*, the Norwegian Book Club's *100 best books*
  and more — ship with Polka and appear as home-page shelves showing which of
  the listed books you already have. Add your own as a JSON list of
  "author + title". See [`collections/`](collections/README.md).
- **OPDS catalog.** Connect Moon+ Reader, KOReader, FBReader, or any other
  OPDS-capable app: new books, browse by author, series and genre, your
  "reading now" shelf, and search.
- **E-ink reader progress sync.** Polka speaks the KOReader sync protocol
  (kosync): KOReader on PocketBook, Kindle, Kobo, reMarkable and friends
  pushes the reading position straight to your server, and books read on
  the device show their progress on Polka's "Reading now" shelf. Each user
  sets a device password in the top-bar "E-reader" dialog.
- **Send to your e-reader.** Configure SMTP once and e-mail any book straight
  to your Kindle / Kobo / PocketBook from its page (the SMTP password is stored
  encrypted).
- **Multi-user.** Accounts with admin/reader roles, per-user progress, lists
  and ratings. Or run it fully open on a trusted home network.
- **Add books from the browser.** Upload fb2 / epub / pdf / djvu / txt / mobi;
  metadata is extracted automatically and three-level duplicate detection
  (file hash → text hash → fuzzy metadata) keeps your collection clean.
- **Two languages, three themes.** English and Russian UI, switchable in one
  click; genre names and the OPDS feed are localized too. The interface
  follows the system theme or can be forced to a light or graphite-gray dark
  palette from the top bar.
- **Mobile-friendly + PWA.** Add Polka to your phone's home screen and it
  behaves like an app.
- **Desktop apps that don't need a server.** Native-feel clients for Windows
  and Linux work fully standalone: your library, reader, lists and ratings
  live right on your machine — no server, no account, no internet required.
  Optionally connect them to your Polka server later and get two-way sync of
  progress, ratings, and lists, including offline copies of selected books.

## Quick start

### Docker (recommended for servers)

```sh
docker run -d --name polka \
  -p 12791:12791 \
  -v polka-data:/data \
  -v /path/to/your/books:/books \
  ghcr.io/shabbybastard/polka:latest
```

Open `http://localhost:12791`, sign in with the bootstrap admin account
(created on first run — see the container log: `docker logs polka`), and start
exploring. Or use [docker-compose.yml](docker-compose.yml):

```sh
docker compose up -d
```

### Desktop app — no server required

If you just want a personal library on one computer, skip the server entirely:
install the desktop app from the [releases](../../releases) page
(`polka-setup-<version>.exe` on Windows, `.deb`/`.rpm` on Linux) and launch
**Polka**. It opens in its own window, stores everything locally, and works
offline. You can connect it to a server later at any time — nothing to
reconfigure.

### Prebuilt binaries and installers

Grab the latest [release](../../releases):

| Platform | What you get |
|----------|--------------|
| Windows  | `polka-setup-<version>.exe` — installer for the desktop app (server included) |
| Linux    | `.deb` / `.rpm` packages (amd64, arm64), plus standalone binaries |
| Raspberry Pi | 64-bit OS → `polka-linux-arm64` (or the arm64 `.deb` / Docker); 32-bit OS → `polka-linux-arm` |
| Any      | `polka` server binary — single file, no dependencies |

Run the server directly:

```sh
polka serve --library-dir /path/to/books
```

### Build from source

Requires Go 1.26+ and Node 20+.

```sh
git clone https://github.com/shabbybastard/polka
cd polka
make build        # builds web UI + bin/polka + bin/polka-desktop
./bin/polka serve --library-dir /path/to/books
```

To pull a feature from the original project later, add its repository as a
remote and merge it deliberately:

```sh
git remote add upstream https://github.com/vestigiumincaligne/polka
git fetch upstream --tags
git merge upstream/main        # or cherry-pick a single feature
```

## Importing a large catalog

Polka understands `.inpx` index files (MyHomeLib / Flibusta format) — both
from the command line and from the web UI (*Manage → Import*):

```sh
polka import --inpx collection.inpx --data-dir /path/to/data --library-dir /path/to/archives
```

You can also import an inpx that already sits on the server (e.g. a
mounted or NFS collection) straight from the web UI — paste its path in
*Manage → Import* instead of uploading. The book archives are looked up
under `--library-dir`.

Book files may sit in plain folders or inside **ZIP or 7z archives** (the
usual Flibusta layout, e.g. `f.fb2-…​.7z`) — Polka reads them directly, no
unpacking needed.

Hundreds of thousands of records import in about a minute. Re-importing
replaces the catalog but **never touches user data** — accounts, progress,
ratings and lists live in a separate database and survive re-imports.

## The desktop apps

`polka-desktop` opens Polka in its own window (WebView2 on Windows, Chromium
app window on Linux) and can work two ways:

- **Standalone (default)** — a personal library on your computer. No server,
  no sign-in, works completely offline.
- **Connected** — point it at your Polka server (*Server* page in the app):
  the full catalog is available online, selected books are downloaded for
  offline reading, and progress / ratings / lists sync both ways automatically.

## Editing EPUB metadata

Administrators can open a book and edit its EPUB metadata directly in Polka:
title, authors, language, publisher, year, ISBN, description and cover. The
cover is resized to 1600 px and stored as a compact JPEG.

This works for bare `.epub` files; books inside ZIP/7z archives stay read-only.
The original file is copied to `<data-dir>/epub-backups/<book-id>/` before the
atomic replacement, and backups older than seven days are removed once a day
at 03:00 server time.

## OPDS

Point your reading app at `http://your-server:12791/opds` — no converting or
copying files, the books open straight on the device. Authentication is HTTP
Basic with your Polka account. The catalog has new books, **browse by author,
by series, and by genre** (alphabetical), search, and a personal "Reading now"
feed; downloads work in fb2, zip, epub, and pdf. The admin can turn OPDS on or
off and copy the connection address in *Manage → OPDS*.

## E-ink readers (PocketBook, Kindle, Kobo…)

Install [KOReader](https://koreader.rocks) on the device, then:

1. **Books**: add the OPDS catalog `http://your-server:12791/opds`
   (your Polka login and password) — browse, search and download right
   on the device. PocketBook owners can also use Polka's *Send to
   e-reader* button with their `@pbsync.com` address.
2. **Progress sync**: in Polka's top bar open *E-reader*, set a device
   password; on the device: *Tools → Progress sync*, server address =
   your Polka URL, username = your Polka login, password = the device
   password. Positions of books downloaded through Polka then follow you
   between the device, the web reader and other KOReader devices.

## Configuration

Everything has a sensible default. The most useful flags of `polka serve`:

| Flag | Default | Meaning |
|------|---------|---------|
| `--addr` | `:12791` | listen address |
| `--data-dir` | `~/.polka` | database, settings, covers cache |
| `--library-dir` | — | folder with books (or inpx archives) |
| `--auth` | `required` | `required` (accounts), `public` (open access) or `demo` (ephemeral guest sessions); the desktop app sets `desktop` internally |

External rating/recommendation sources are configured in the web UI
(*Manage → External ratings / Similar books*) and are **off by default**
except local recommendations — nothing is queried without the admin's
consent. External genre sources are a separate switch and are also off by
default.

## Tech notes

- Single Go binary; the React frontend is embedded at build time.
- SQLite (pure-Go driver) with WAL and FTS5 — no database server to run.
- User data (accounts, sessions, progress, ratings, lists) is stored
  separately from the catalog and survives re-imports; so do curated
  collections, which are re-matched against the catalog after every import.
- Sync between desktop clients and the server is state-based with
  last-write-wins per record. Forgetting reading progress writes a timestamped
  tombstone, so the removal propagates to other devices instead of being
  restored from an older snapshot.
- External genres are cached together with ratings; enabling a source after
  the cache was filled without genres refreshes the affected book lazily.

## About this fork

This repository is a fork of
[vestigiumincaligne/polka](https://github.com/vestigiumincaligne/polka).
The original Polka was created by its authors and contributors; all credit for
the upstream project belongs to them. Please support the original project and
report upstream bugs there.

The current fork state is based on upstream v0.1.22. Upstream features will be pulled in
selectively when they are worth taking.

Fork releases use the version pattern `v<upstream>-fork.<n>`
(current: `v0.1.22-fork.4`). The binary reports the same version without the
leading `v`, deb/rpm packages use `0.1.22+fork.4`, and the version for local
`make build` is stored in [`VERSION`](VERSION); release tags should match it.

## Neuroslop warning

The original code has been modified by AI.

## License

[MIT](LICENSE)
