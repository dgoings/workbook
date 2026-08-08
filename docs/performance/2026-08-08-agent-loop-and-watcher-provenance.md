# 2026-08-08 agent hot loop and sync watcher provenance

The four reports on this date — an agent hot loop and read-side pair, and a sync
watcher steady-state pair — were produced from one source commit and the same two
binaries. Neither binary was rebuilt between object formats or between families,
and no invocation was retried, tuned, or replaced.

- Version: `0.4.2-bench-agent-loop`, an unreleased build of the
  `perf/bench-agent-loop-watcher` branch
- Source commit: `e7d4508fe8f7164bce97599e91703027e5111c2d`
- Measured Workbook binary:
  `/private/tmp/claude-502/.../scratchpad/bench/workbook`
- Measured Workbook binary SHA-256:
  `8b68a5941a743167e144420bc8a74999a42533f76988852d1f6531b82bb0a655`
- Benchmark harness binary:
  `/private/tmp/claude-502/.../scratchpad/bench/workbook-bench`
- Benchmark harness binary SHA-256:
  `dd0f0663a403686e35400d803be158b5466ba7611587499843e0f08a8f98ef33`

This is branch evidence rather than a release measurement, so the measured
binary was stamped by hand with the same `-ldflags` `scripts/install.sh` uses
rather than installed through it. The stamp matters because the harness rejects
`unknown` as a commit before fixture construction.

Build commands:

```sh
go build -buildvcs=false \
  -ldflags "-X main.version=0.4.2-bench-agent-loop -X main.commit=<commit>" \
  -o <destination>/workbook ./cmd/workbook
go build -buildvcs=false -o <destination>/workbook-bench ./cmd/workbook-bench
```

Independent checksum command:

```sh
shasum -a 256 <destination>/workbook <destination>/workbook-bench
```

Two things changed on the branch after the binaries were built: documentation —
this file and the evidence sections of both `README.md` files — and one `_test.go`
file, which removed a `-short` skip. Go does not compile test files into either
executable, so neither binary is affected and neither was rebuilt. Both are
recorded here rather than left for a reader to discover from a mismatch between
this file and the branch tip.

## This is a focused run, not a full local acceptance set

The hot loop pair selects seven scenarios rather than the eighteen a whole local
acceptance run now covers, and the choice is deliberate rather than a shortcut
that happened to fit:

| Scenario | Why it is in this run |
| --- | --- |
| `cli-next` | New. The agent's acquire step, and the reason the run exists. |
| `cli-show` | New. The agent's read step. |
| `api-tasks` | New. The board's read side. |
| `api-update` | The standing target miss this run is asked to decide. |
| `cli-update` | The 200 ms local mutation anchor. Without it in the same run, `cli-show`'s local classification rests on a comparison across hosts. |
| `cli-update-autosync` | The 1,000 ms synchronized anchor. `cli-next`'s whole target argument is that its pre-answer fetch puts it in this class, and that claim is only worth reading if both were measured together. |
| `cli-list` | The existing whole-board read, so `cli-show` and `cli-list` sit side by side and the single-task/whole-board distinction is visible within one run rather than asserted. |

The eleven omitted scenarios — the four bursts, `cli-create`, `cli-delete`,
`cli-depend`, `cli-free`, `cli-move`, `cli-restore`, and `cli-update-watched` —
are unchanged by this branch and were measured on 2026-08-05. Every
`(scenario, sample)` pair builds its own 525-task fixture, so a full
eighteen-scenario pair costs several times what this one did and would not have
sharpened any claim this branch makes. Focused evidence has precedent here: the
2026-07-29 tip-focused, 2026-07-30 packed-repository, and 2026-08-07
bounded-validation runs are all scenario subsets.

The consequence is worth stating plainly rather than leaving implicit: this pair
is **not** a v0.4.2 local acceptance set and does not refresh the standing miss
list. The next full acceptance run does that.

## Host load during measurement

These numbers were measured on a shared development host that other agents were
running full `go test` suites on. The harness spawns a Git process per unit of
work and the host's endpoint-security scanner inspects every one of them, so this
workload is unusually sensitive to what else is running.

The driver therefore waited for the host's one-minute load average to fall below
10 before it started, rather than measuring on demand. It had been above 30 for
the preceding several minutes. The load at each family's start and end, as the
driver recorded it:

| Family | Started | Load at start | Load at end |
| --- | --- | ---: | ---: |
| Hot loop, SHA-1 | 15:27:28 | 8.42 | 4.35 |
| Hot loop, SHA-256 | 15:33:49 | 4.35 | 6.17 |
| Watcher, SHA-1 | 15:39:47 | 6.17 | 5.16 |
| Watcher, SHA-256 | 15:46:08 | 5.16 | 1.67 |

That is a mitigation, not a control. It is still visible in the results, where
scenarios this branch does not touch are slower than their 2026-08-05
counterparts — `cli-update` by 71% in SHA-1 and 52% in SHA-256,
`cli-update-autosync` by more than 100% in both — and it is recorded here rather
than smoothed away.

The two families are not equally affected. The hot loop pair measures command
latency and is the sensitive one. The watcher pair's headline numbers are process
counts and a CPU total for a single observed process, which is why all six of its
windows agree exactly on 13 synchronizations and 117 Git processes despite the
load moving underneath them.

It bounds what the numbers can be used for:

- **Sound**: comparisons *within* a single report. `cli-next` against
  `cli-update-autosync`, or `cli-show` against `cli-update` and `cli-list`, are
  scenarios measured minutes apart under the same load, and those are the
  comparisons every claim on this branch actually rests on.
- **Sound**: treating a measured value as an *upper* bound. A contended host does
  not make a command faster.
- **Unsound**: reading a delta against 2026-08-05 as a product change. The
  report `environment` block records the operating system, architecture, Git
  version, and Go version, but not the CPU model or concurrent host load, so a
  cross-run comparison here is indicative at best.
- **Unsound**: reading this run's duration outcomes as a refreshed miss list.
  `cli-update` and `cli-update-autosync` both miss here and both passed on
  2026-08-05, and neither is touched by this branch. `cli-next` misses in SHA-1
  and passes in SHA-256 for the same reason. Those outcomes are evidence about
  the host, not about the product.

The proposed `api-update` target in `docs/performance/README.md` is therefore
derived from the six prior evidence points measured on quieter hosts, with this
run used only as corroboration that the miss is structural rather than a one-off.

Host toolchain, as recorded in each report's `environment` block:

- `go version go1.26.5 darwin/arm64`
- `git version 2.50.1 (Apple Git-155)`
- macOS, arm64
