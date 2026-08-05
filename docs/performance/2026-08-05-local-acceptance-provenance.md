# 2026-08-05 sync watcher local acceptance provenance

The SHA-1 and SHA-256 local acceptance reports were produced from one source
commit and the same two binaries. Neither binary was rebuilt between object
formats, and neither invocation was retried, tuned, or replaced.

- Version: `0.3.0-sync-watcher`, an unreleased build of the sync watcher branch
- Source commit: `0bf8982e8ba0e09bb77b83be6c77523b8647f471`
- Measured Workbook binary:
  `/private/tmp/claude-502/.../scratchpad/bench/workbook`
- Measured Workbook binary SHA-256:
  `adcadbcbba17a764464f43cd4c5e7ed841160120af7b4581728619f3265f4852`
- Benchmark harness binary:
  `/private/tmp/claude-502/.../scratchpad/bench/workbook-bench`
- Benchmark harness binary SHA-256:
  `3f34a31d5b26d0b0fbf69d0d009ae43240f83211f91cf0ec3e54681125271af2`

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

One commit landed after the binaries were built, `20a7c4c`, which edits only
`docs/superpowers/specs/` and changes neither binary. It is recorded here rather
than left for a reader to discover from a mismatch between this file and the
branch tip.

Host toolchain, as recorded in each report's `environment` block:

- `go version go1.26.5 darwin/arm64`
- `git version 2.50.1 (Apple Git-155)`
- macOS, arm64

The report `environment` block records the operating system, architecture, Git
version, and Go version, but not the CPU model or concurrent host load.
Comparisons against the 2026-08-02 v0.3.0 evidence are therefore indicative
rather than controlled, and the tables below compare scenarios measured in the
same run wherever the comparison carries weight.
