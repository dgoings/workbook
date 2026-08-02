# 2026-08-02 v0.3.0 local acceptance provenance

The SHA-1 and SHA-256 local acceptance reports were produced from one release
commit and the same two binaries. Neither binary was rebuilt between object
formats, and neither invocation was retried, tuned, or replaced.

- Release: `v0.3.0`
- Source commit: `fc1dfd9dddfc76682a815bd03c8810914a6f26fa`
- Measured Workbook binary:
  `/private/tmp/claude-502/.../scratchpad/perf/workbook`
- Measured Workbook binary SHA-256:
  `5ac43bd881938201e80e20d2fb29f73f00f9350289acd77e598d3634dfa1e0d3`
- Benchmark harness binary:
  `/private/tmp/claude-502/.../scratchpad/perf/workbook-bench`
- Benchmark harness binary SHA-256:
  `3ad73a5d8df99c9bc141b1f5bc1e77ff67b52deaf22b90178f41955b43f78333`

The measured binary was installed by `scripts/install.sh`, so it is stamped from
`git describe` and reports `v0.3.0` with the exact commit above rather than
`dev (unknown)`, which the harness rejects before fixture construction.

Build commands:

```sh
scripts/install.sh <destination> workbook
go build -o <destination>/workbook-bench ./cmd/workbook-bench
```

Independent checksum command:

```sh
shasum -a 256 <destination>/workbook <destination>/workbook-bench
```

Host toolchain, as recorded in each report's `environment` block:

- `go version go1.26.5 darwin/arm64`
- `git version 2.50.1 (Apple Git-155)`
- macOS 26.5.2, arm64, Apple M5 Pro, 18 cores

The report `environment` block records the operating system, architecture, Git
version, and Go version, but not the CPU model or concurrent host load.
Comparisons against evidence from an earlier date are therefore indicative
rather than controlled.
