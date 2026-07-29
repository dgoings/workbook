# SHA-1 semantic history validation acceptance attempt

The single SHA-1 acceptance invocation on 2026-07-29 aborted before the
benchmark harness started or assembled a trustworthy report.

The measured Workbook binary had already been built once at:

```text
/private/tmp/workbook-history-validation-acceptance
```

The one-shot command was:

```sh
go run ./cmd/workbook-bench \
  --workbook /private/tmp/workbook-history-validation-acceptance \
  --tasks 500 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha1 \
  --phase acceptance \
  --scenario validate-full-history \
  --scenario validate-cached-unchanged \
  --scenario validate-five-changed \
  --output-json docs/performance/2026-07-29-history-validation-sha1.json \
  --output-markdown docs/performance/2026-07-29-history-validation-sha1.md
```

It exited 1 while `go run` tried to read the sandboxed default Go build cache:

```text
open /Users/dylan.goings/Library/Caches/go-build/77/774ae85a042e6ff359bf4a6b256f75080591996db7a263181668df70004e998f-d: operation not permitted
```

No fixture, measured scenario, JSON report, or generated Markdown report was
produced. The SHA-1 invocation was not retried or replaced.
