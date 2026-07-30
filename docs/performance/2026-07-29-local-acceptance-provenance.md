# 2026-07-29 local acceptance provenance

The SHA-1 and SHA-256 local acceptance reports were produced from one reviewed
source commit and the same two binaries. Neither binary was rebuilt between
object formats.

- Source commit: `50dfcdcd09a7a70dd27617ec195e5d08d280a5a4`
- Measured Workbook binary:
  `/private/tmp/workbook-local-acceptance`
- Measured Workbook binary SHA-256:
  `30bd9bb943d0e49a0ddb346ec042e5a97f550e75f1a263a2b7c0234f0c8e2f5e`
- Benchmark harness binary:
  `/private/tmp/workbook-bench-local-acceptance`
- Benchmark harness binary SHA-256:
  `d1dfebcde228a3be6928937ca2a4338bc9b064b5442c393249e3f56ce07bc9fb`

Build commands:

```sh
GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false \
  -ldflags "-X main.commit=50dfcdcd09a7a70dd27617ec195e5d08d280a5a4" \
  -o /private/tmp/workbook-local-acceptance ./cmd/workbook

GOCACHE=/private/tmp/workbook-gocache go build -buildvcs=false \
  -o /private/tmp/workbook-bench-local-acceptance ./cmd/workbook-bench
```

Independent checksum command:

```sh
shasum -a 256 \
  /private/tmp/workbook-local-acceptance \
  /private/tmp/workbook-bench-local-acceptance
```
