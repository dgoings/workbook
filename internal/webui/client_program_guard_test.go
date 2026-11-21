package webui

import (
	"strings"
	"testing"
)

// Every client program runs behind clientProgramGuard, which is what makes a
// program that stalls on a promise nobody settles fail instead of exiting
// cleanly with its checks unrun. These are the shapes the package's programs
// take, run through nodeCommand exactly as every client test runs.
func TestClientProgramGuardFailsAProgramThatStopsShortOfItsChecks(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	const stalled = "client program ended with 1 of its checks unfinished"
	for _, scenario := range []struct {
		name    string
		program string
		// fails is the text a failing run must print, or "" when it must pass.
		fails string
	}{
		{
			name: "a timeout's checks that finish",
			program: `setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  process.stdout.write("reached the end\n");
}, 0);`,
		},
		{
			name: "a timeout's checks stalled on a promise nobody settles",
			program: `setTimeout(async () => {
  await new Promise(() => {});
  process.stdout.write("reached the end\n");
}, 0);`,
			fails: stalled,
		},
		{
			name: "a clientProgram's checks stalled on a promise nobody settles",
			program: `clientProgram(async () => {
  await new Promise(() => {});
}).catch((error) => { console.error(error); process.exit(1); });`,
			fails: stalled,
		},
		{
			name: "checks stalled in a timeout the checks set themselves",
			program: `setTimeout(async () => {
  setTimeout(async () => { await new Promise(() => {}); }, 0);
}, 0);`,
			fails: stalled,
		},
		{
			// The guard's own bookkeeping must not count as handling the
			// rejection: a check that throws fails as it always did.
			name: "a timeout's check that throws",
			program: `setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  throw new Error("the check failed");
}, 0);`,
			fails: "the check failed",
		},
		{
			name: "a promisified timeout still waits",
			program: `setTimeout(async () => {
  await require("util").promisify(setTimeout)(1);
  process.stdout.write("reached the end\n");
}, 0);`,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			output, err := nodeCommand(node, scenario.program).CombinedOutput()
			if scenario.fails == "" {
				if err != nil || !strings.Contains(string(output), "reached the end") {
					t.Fatalf("a program that finishes failed or stopped short: %v\n%s", err, output)
				}
				return
			}
			if err == nil {
				t.Fatalf("a program that %s exited cleanly\n%s", scenario.name, output)
			}
			if !strings.Contains(string(output), scenario.fails) {
				t.Fatalf("output does not say %q\n%s", scenario.fails, output)
			}
		})
	}
}
