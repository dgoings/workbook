# Packed repository sync acceptance provenance

Date: 2026-07-30

## Source and environment

- Source commit: `d65ea61f8f7e42d3560717b495fe6128a70812bb`
- Go: `go version go1.26.5 darwin/arm64`
- Git: `git version 2.50.1 (Apple Git-155)`
- OS architecture: `arm64`

## Frozen binaries

| Binary | SHA-256 |
| --- | --- |
| `/private/tmp/workbook-packed-repository-acceptance` | `248730e1dcb3cc7b19359245466fc7b2dad9bfcc4ff5b25113aa85f3c41cb5cf` |
| `/private/tmp/workbook-bench-packed-repository-acceptance` | `e39a9b1582b78249a9df62edf6d62ead61055cbcd756fc8e8fd016f94f32da7c` |

The source commit was recorded as the exact 40-character output of `git rev-parse
HEAD` before either binary was built. The product binary embeds that literal commit.

```sh
WB_PACKED_SOURCE_COMMIT=$(git rev-parse HEAD)
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -ldflags "-X main.commit=$WB_PACKED_SOURCE_COMMIT" -o /private/tmp/workbook-packed-repository-acceptance ./cmd/workbook
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false -o /private/tmp/workbook-bench-packed-repository-acceptance ./cmd/workbook-bench
shasum -a 256 /private/tmp/workbook-packed-repository-acceptance /private/tmp/workbook-bench-packed-repository-acceptance
```

Neither binary was rebuilt between object formats.

## One-shot acceptance scope

Each acceptance invocation ran once, with the frozen binaries, 500 total task
refs (475 active and 25 tombstoned), 20 operations per task, one sample, and a
60-second timeout. The reports selected only `sync-initial-local-bare` and
`sync-unchanged-local-bare`; no unrelated acceptance family was run.

- [SHA-1 JSON evidence](2026-07-30-packed-repository-sync-acceptance-sha1.json)
  and [Markdown evidence](2026-07-30-packed-repository-sync-acceptance-sha1.md)
- [SHA-256 JSON evidence](2026-07-30-packed-repository-sync-acceptance-sha256.json)
  and [Markdown evidence](2026-07-30-packed-repository-sync-acceptance-sha256.md)
