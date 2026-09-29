package webui

// Tests that stay serial, and why. Unlike internal/gitstore, nothing here
// calls t.Setenv, os.Setenv, t.Chdir or runs git, and no test needs its own
// process-global environment — there is no TestMain in this file for that
// reason.
//
//	TestServeStopsCleanlyWhenContextIsCancelled — server_test.go:14
//	TestServeLetsAnInFlightRequestFinish — shutdown_test.go:106
//
// Both call http.Get, which goes through http.DefaultClient and its
// process-global keep-alive connection pool (http.DefaultTransport). Neither
// closes its transport's idle connections when it is done, so running these
// in parallel with the rest of the package could let a connection pooled
// against a listener that has since shut down answer either of these, or let
// a connection either of these pools answer a later test on a listener that
// no longer exists. Giving each its own http.Client with its own Transport
// (closed in a t.Cleanup) would let them run parallel; until that lands,
// they stay serial. Nothing else in the package needs to be serial:
// no test writes a package-level variable, sets an environment variable,
// changes the working directory, or shares a filesystem path, a fixed port,
// or a git repository with another test.
//
// Four t.Run groups keep their subtests serial rather than parallel, because
// each shares a mutable counter or observer across the subtests in its
// closure with cumulative or exact-contents assertions:
// TestHandlerDependencyMutationsRequireEmptyRequestBodies (handler_test.go
// ~325), TestHandlerRejectsEncodedDependencyPathAliases (handler_test.go
// ~482), TestNewHandlerRoutesEveryOptionToItsOwnRoute (options_test.go ~18),
// and TestShippedPriorityColorsClearAAAgainstTheCard (priority_chip_test.go
// ~265). Their parent tests still run in parallel against the rest of the
// package; only their subtests are left without it.
