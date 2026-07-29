# SHA-1 tip-focused synchronization acceptance attempt

Date: 2026-07-29

The single permitted SHA-1 acceptance command selected all seven remote
topologies at 500 tasks by 20 operations, one sample, and a 60-second command
timeout. The harness stopped during `sync-small-changed-ref-set` before report
assembly, so it did not create the requested JSON or generated Markdown report.
The command was not retried.

The failure was a harness-oracle defect, not a product-command failure:
the fixture changes five local-ahead tasks and five remote-ahead tasks, but the
oracle incorrectly expected every task after the first five to be
`fast-forwarded`. Workbook correctly returned `unchanged` for tasks 10 through
499. The oracle now scopes `fast-forwarded` to indices 5 through 9 and has
coverage above ten tasks.

Because the runner is fail-fast, no trustworthy complete SHA-1 timing or process
table exists for this attempt. This note preserves that missing evidence rather
than substituting a rerun.
