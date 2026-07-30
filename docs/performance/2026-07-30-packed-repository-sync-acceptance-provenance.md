# Packed repository sync acceptance provenance

Date: 2026-07-30

## Source and environment

- Source commit: `e889e9f9c452e0d00c47cf1b427221d69489a303`
- Go: `go version go1.26.5 darwin/arm64`
- Git: `git version 2.50.1 (Apple Git-155)`
- OS architecture: `arm64`

## Frozen binaries

| Binary | SHA-256 |
| --- | --- |
| `/private/tmp/workbook-packed-repository-acceptance` | `d8f7e963ba94d8564ae6fa3772c2ec73731eeeba500e696d169aecf075fe3b12` |
| `/private/tmp/workbook-bench-packed-repository-acceptance` | `6eaa33fba8f3f1011eb5faf0fd33a320893fd53edfe5f9c9ee33579d818ce2c6` |

The source commit was recorded as the exact 40-character output of `git rev-parse
HEAD` before either binary was built. The product binary embeds that literal
commit.

```sh
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -ldflags "-X main.commit=e889e9f9c452e0d00c47cf1b427221d69489a303" -o /private/tmp/workbook-packed-repository-acceptance ./cmd/workbook
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -o /private/tmp/workbook-bench-packed-repository-acceptance ./cmd/workbook-bench
shasum -a 256 /private/tmp/workbook-packed-repository-acceptance /private/tmp/workbook-bench-packed-repository-acceptance
```

Neither binary was rebuilt between object formats.

## One-shot acceptance scope

Each acceptance invocation ran once, with the frozen binaries, 500 total task
refs (475 active and 25 tombstoned), 20 operations per task, one sample, and a
60-second timeout. The reports selected only `sync-initial-local-bare` and
`sync-unchanged-local-bare`; no unrelated acceptance family was run.

The corrected sync-only dispatcher uses a fresh packed-repository path and does
not run projection measurements. Its real regression checks every canonical ref
in its fixture immediately before and after both sync commands, proving that the
requested ancestry depth is retained through the measurement.

- [SHA-1 JSON evidence](2026-07-30-packed-repository-sync-acceptance-sha1.json)
  and [Markdown evidence](2026-07-30-packed-repository-sync-acceptance-sha1.md)
- [SHA-256 JSON evidence](2026-07-30-packed-repository-sync-acceptance-sha256.json)
  and [Markdown evidence](2026-07-30-packed-repository-sync-acceptance-sha256.md)
