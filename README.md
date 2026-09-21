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

```sh
go run ./custodial     # http://127.0.0.1:8080
go run ./provenance    # http://127.0.0.1:8081
go test ./...          # both test suites
```

Run from the repository root. Each app finds its own `web/` directory, or takes
`-web` to point somewhere else. The frontends are plain HTML, CSS and ES modules
served straight from disk, so editing one only needs a browser refresh.

## Layout

```
common/       code both applications use
  codec/      the error-correcting mark codec and the attribution rule
  webapp/     JSON API and static file plumbing
custodial/    document attribution
  internal/document/  PDF pipeline: typesetting, parsing, the marking layers
  internal/office/    Open XML pipeline: docx, pptx, xlsx
  internal/server/    HTTP API
  web/                frontend
provenance/   dataset attribution
  internal/tabular/   schema profiling, marking, detection, attacks
  internal/server/    HTTP API
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
