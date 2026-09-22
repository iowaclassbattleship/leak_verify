# Leak attribution platform

Two demonstrators for marking artifacts per recipient, so a leaked copy can be
traced back to the person it was issued to. Both run on one host, hold all state
in memory, and never send anything off the machine.

| | What it marks | Port | Docs |
|---|---|---|---|
| **Custodial** | Word, PowerPoint, Excel and PDF documents | 8080 | [custodial/README.md](custodial/README.md) |
| **Provenance** | CSV and other tabular data | 8081 | [provenance/README.md](provenance/README.md) |

They are separate applications with separate frontends, pitched separately. They
share one Go module so they can share code.

Both run from one binary, behind one login, on one port.

```sh
cp config.example.yaml config.yaml   # then edit the users
go run .                             # http://127.0.0.1:8080
go test ./...                        # both test suites
```

`config.yaml` holds the master password and the sign-in list:

```yaml
master: a-password-only-you-have
users:
  marc@northward.ch: a-password
  someone@who-umc.org: another-password
```

Passwords are compared as written, so the file is a secret. It is gitignored,
and the container expects it mounted read-only rather than built in.

## Watching a published deployment

Every action is recorded: who signed in, what they loaded, issued, downloaded
and verified, with the result and how long it took. The record is a 5,000 entry
ring buffer in memory, and it is also echoed to stderr so `docker logs` shows
it.

```sh
curl -u any:MASTER https://host/debug/log            # readable, newest last
curl -u any:MASTER https://host/debug/log?all=1      # including polling and reads
curl -u any:MASTER https://host/debug/log?format=json
```

The master password is separate from the user list on purpose: signing in as a
user does not let you read what everybody else did. Uploaded file names are
deliberately not recorded, only that a file was loaded or verified.

Sign in and the root page offers the two products as tiles. Custodial is at
`/custodial/`, Provenance at `/provenance/`, and their APIs at `/api/doc/` and
`/api/data/`. The wordmark in the masthead goes back to the tiles, and each
product links across to the other.

Every signed-in session gets its own instance of both applications, so two
people using the same deployment never see each other's documents, tables or
issuance logs.

## What survives a restart

Two things outlive the process, so a copy issued last week can still be named
when it turns up:

- **The marking key**, derived from the `secret` in `config.yaml` and the user's
  address. It is never written to disk. Changing the secret invalidates every
  copy ever issued.
- **The issuance log**, one JSON file per user under `-data` (default `data/`),
  holding mark ID, recipient, techniques and time.

Marked files are not stored. Marking is deterministic in the key, the source,
the mark ID and the techniques, so a copy regenerates from those four. The
consequence is that verifying an old copy needs the same source loaded again:
the built-in samples are seeded from the user's key and come back identical,
but an uploaded document or CSV has to be uploaded again. A copy restored from
the log has no download, and says so.

Everything else is in memory and goes when the session expires, after 8 hours
or 2 hours idle.

The frontends are plain HTML, CSS and ES modules served straight from disk, so
editing one only needs a browser refresh. Go changes need a restart.

## Deployment

```sh
docker build -t attribution .
docker run -p 8080:8080 -v ./config.yaml:/app/config.yaml:ro attribution
```

or `docker compose up`, which mounts the same file. Set `SECURE_COOKIE=1` when a
TLS-terminating proxy sits in front, so the session cookie is only sent over
HTTPS. The image is about 24 MB and runs as a non-root user.

## Layout

```
main.go       the single entry point
internal/app/ login, sessions, and the router that joins the two products
common/       code both applications use
  codec/      the error-correcting mark codec and the attribution rule
  webapp/     JSON API and static file plumbing
  web/        the house stylesheet, wordmark, file-type logos, login page
custodial/    document attribution
  internal/document/  PDF pipeline: typesetting, parsing, the marking layers
  internal/office/    Open XML pipeline: docx, pptx, xlsx
  server/             HTTP API, one instance per session
  web/                frontend
provenance/   dataset attribution
  internal/tabular/   schema profiling, marking, detection, attacks
  server/             HTTP API, one instance per session
  web/                frontend
```

`common/codec` is the piece that matters: both products encode a 16-bit mark ID
the same way, and both apply the same attribution rule, so a claim made by one
means the same thing as a claim made by the other. See
[custodial/README.md](custodial/README.md#codec-commoncodec) for how it works.

Each application's `internal/` directory is private to it, so neither can reach
into the other's pipeline by accident. Anything genuinely shared has to move to
`common/` deliberately.

The project brief is in [CLAUDE.md](CLAUDE.md).
