# 2026-08-05 sync watcher local acceptance provenance

The SHA-1 and SHA-256 local acceptance reports were produced from one source
commit and the same two binaries. Neither binary was rebuilt between object
formats, and neither invocation was retried, tuned, or replaced.

- Version: `0.3.0-sync-watcher`, an unreleased build of the sync watcher branch
- Source commit: `9d485885c1f7f677d79183d7f63cfb2e2b2aa6ba`
- Measured Workbook binary:
  `/private/tmp/claude-502/.../scratchpad/bench/workbook`
- Measured Workbook binary SHA-256:
  `0d3ffc504cced428853d41a361fd6135bbae744115c0b40c03105f7bb773f8c8`
- Benchmark harness binary:
  `/private/tmp/claude-502/.../scratchpad/bench/workbook-bench`
- Benchmark harness binary SHA-256:
  `978320693c672263c17bbeda228ff9582224d2020dc5305bff6aaf6fd3e125ff`

This is branch evidence rather than a release measurement, so the measured
binary was stamped by hand with the same `-ldflags` `scripts/install.sh` uses
rather than installed through it. The stamp matters because the harness rejects
`unknown` as a commit before fixture construction.

Build commands:

```sh
go build -ldflags "-X main.version=0.3.0-sync-watcher -X main.commit=<commit>" \
  -o <destination>/workbook ./cmd/workbook
go build -o <destination>/workbook-bench ./cmd/workbook-bench
```

Independent checksum command:

```sh
shasum -a 256 <destination>/workbook <destination>/workbook-bench
```

These reports replace an earlier pair measured before
`WB-01KZ1JCYZCPD156TCXMRB4Z6ZB` landed. That work rewrote projection refresh to
walk operation chains, so the earlier numbers described a tree that no longer
exists and were re-measured rather than carried forward. The measured values
barely moved, which is itself worth recording: the refresh rewrite does not
reach the timed mutation path.

Only documentation changed after the binaries were built — this file, the
evidence section of `README.md`, and the design spec. Neither binary is
affected. It is recorded here rather than left for a reader to discover from a
mismatch between this file and the branch tip.

Host toolchain, as recorded in each report's `environment` block:

- `go version go1.26.5 darwin/arm64`
- `git version 2.50.1 (Apple Git-155)`
- macOS, arm64

The report `environment` block records the operating system, architecture, Git
version, and Go version, but not the CPU model or concurrent host load.
Comparisons against the 2026-08-02 v0.3.0 evidence are therefore indicative
rather than controlled, and the tables below compare scenarios measured in the
same run wherever the comparison carries weight.
