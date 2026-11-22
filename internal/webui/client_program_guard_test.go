package webui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	const unsettled = "promise(s) its checks started still unsettled"
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
		{
			name: "checks that never schedule anything",
			program: `if (1 + 1 !== 2) throw new Error("arithmetic");
process.stdout.write("reached the end\n");`,
		},
		{
			name: "an async function started and not awaited inside a timeout",
			program: `async function checks() { await new Promise(() => {}); }
setTimeout(() => { checks(); }, 0);`,
			fails: unsettled,
		},
		{
			name:    "a promise chain started in a timeout that returns nothing",
			program: `setTimeout(() => { new Promise(() => {}).then(() => { process.stdout.write("checked\n"); }); }, 0);`,
			fails:   unsettled,
		},
		{
			name:    "checks stalled in require(\"timers\").setTimeout",
			program: `require("timers").setTimeout(async () => { await new Promise(() => {}); }, 0);`,
			fails:   stalled,
		},
		{
			name:    "checks stalled in setImmediate",
			program: `setImmediate(async () => { await new Promise(() => {}); });`,
			fails:   stalled,
		},
		{
			name:    "checks stalled in require(\"timers\").setImmediate",
			program: `require("timers").setImmediate(async () => { await new Promise(() => {}); });`,
			fails:   stalled,
		},
		{
			name:    "checks stalled in queueMicrotask",
			program: `queueMicrotask(async () => { await new Promise(() => {}); });`,
			fails:   stalled,
		},
		{
			name:    "checks stalled in process.nextTick",
			program: `process.nextTick(async () => { await new Promise(() => {}); });`,
			fails:   stalled,
		},
		{
			name:    "a bare async function stalled at the top level",
			program: `(async () => { await new Promise(() => {}); process.stdout.write("checked\n"); })();`,
			fails:   unsettled,
		},
		{
			name: "process.exit(0) before the checks' first await",
			program: `setTimeout(async () => {
  process.exit(0);
  await new Promise((resolve) => setTimeout(resolve, 0));
  throw new Error("the check failed");
}, 0);`,
			fails: "client program called process.exit(0) before it was finished",
		},
		{
			name: "process.exit(0) at the top level before the program's last line",
			program: `process.exit(0);
throw new Error("never reached");`,
			fails: "client program ended before its last line ran",
		},
		{
			// An exit that names a failure is the program's own answer, and the
			// guard leaves it alone.
			name:    "process.exit(1) from a check",
			program: `setTimeout(async () => { console.error("the check failed"); process.exit(1); }, 0);`,
			fails:   "the check failed",
		},
		{
			// What nodeCommand's caller requires is the marker, not a clean exit:
			// a program that takes the guard's listeners away exits 0 and still
			// fails.
			name: "a program that silences the guard",
			program: `process.removeAllListeners("beforeExit"); process.removeAllListeners("exit");
setTimeout(async () => { await new Promise(() => {}); }, 0);`,
			fails: "client program exited 0 without reaching its own end",
		},
		{
			// A test holds a request open by handing the page a promise that
			// never settles. The page waiting on it is not a stalled check, and
			// neither is a promise the page itself made and left pending.
			name: "a request the checks hold open for the page",
			program: pageShapedProgram(`
  globalThis.pageWaits = (answer) => { answer.then(() => { throw new Error("never answered"); }); };
  globalThis.pageHangs = async () => { await new Promise(() => {}); };
`, `setTimeout(async () => {
  pageWaits(new Promise(() => {}));
  pageHangs();
  await new Promise((resolve) => setTimeout(resolve, 0));
  process.stdout.write("reached the end\n");
}, 0);`),
		},
		{
			// But the same promise awaited by the checks themselves is a stall.
			name: "checks awaiting the page's request that never answers",
			program: pageShapedProgram(`
  globalThis.pageHangs = async () => { await new Promise(() => {}); };
`, `setTimeout(async () => {
  await pageHangs();
  process.stdout.write("reached the end\n");
}, 0);`),
			fails: stalled,
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
			// The guard's verdict is in the output; the caller's own, for a
			// program that never let the guard give one, is in the error.
			if said := string(output) + "\n" + err.Error(); !strings.Contains(said, scenario.fails) {
				t.Fatalf("output does not say %q\n%s", scenario.fails, said)
			}
		})
	}
}

// pageShapedProgram is a program with page code in it where the guard looks
// for the page's script: between the first lines of boot.js and the last lines
// of events.js, with just enough of a document and a window for those lines to
// run. page is the page's own code and checks follow it, as in every client
// test.
func pageShapedProgram(page, checks string) string {
	head, tail := pageScriptBounds()
	return `globalThis.document = { createElement: () => ({ setAttribute() {} }), querySelector: () => null };
globalThis.window = { setInterval() {} };
globalThis.renderSyncAge = () => {}; globalThis.refresh = () => {};
` + head + "\n" + page + "\n" + tail + "\n" + checks
}

// The guard holds only for a program that reaches node through
// nodeCommandContext. A test that starts node on its own runs its program
// unguarded, and a check stalled on a promise nobody settles passes again
// without a word. These tests read the module's Go sources for the ways a
// test could plausibly do that by accident, listed at scanNodeLaunches. It is
// a tripwire, not a proof: it reads syntax one file at a time and follows no
// value between functions, so a launch written to get past it gets past it.

// nodeLaunch is one call that starts a process where the guard cannot see it.
type nodeLaunch struct {
	File     string
	Function string
	Line     int
	Call     string
}

func (launch nodeLaunch) String() string {
	return fmt.Sprintf("%s:%d: %s starts a process with %s", launch.File, launch.Line, launch.Function, launch.Call)
}

// guardedLaunchers are the only functions in this package allowed to start a
// process. nodeCommandContext is the one that starts client programs, behind
// clientProgramGuard. driveBrowser starts Chrome and the DevTools driver that
// talks to it, which is not a client program: what it runs is the page in a
// real browser, its answer must arrive as JSON before a deadline, and a page
// that never answers fails at that deadline
// (TestBrowserDriverStopsAPageThatNeverAnswersAtItsDeadline).
var guardedLaunchers = map[string]bool{"nodeCommandContext": true, "driveBrowser": true}

func TestEveryClientProgramStartsBehindTheGuard(t *testing.T) {
	t.Parallel()
	found, files := scanModuleForNodeLaunches(t)
	for _, want := range []string{"internal/webui/handler_test.go", "internal/webui/browser_harness_test.go", "scripts/check_ci_capabilities_test.go"} {
		if !slices.Contains(files, want) {
			t.Fatalf("the scan never read %s, so it proves nothing about the sources it skipped (read %d files)", want, len(files))
		}
	}
	if len(found) == 0 {
		return
	}
	var report strings.Builder
	for _, launch := range found {
		fmt.Fprintf(&report, "\n\t%s", launch)
	}
	t.Fatalf("%d call(s) start node or another process outside nodeCommandContext:%s"+
		"\n\nA client program started that way runs without clientProgramGuard, so one whose checks"+
		"\nstall on a promise nobody settles exits cleanly and its test passes. Start it with"+
		"\nnodeCommand, or nodeCommandContext when it needs a deadline of its own.",
		len(found), report.String())
}

// Mutation witness: a scanner that misses the shape the guard was once
// bypassed by reports a clean module forever.
func TestNodeLaunchScanFindsAClientProgramStartedOnItsOwn(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, file, source string
		want               []string
	}{
		{
			name: "a webui test starting node itself",
			file: "internal/webui/sample_test.go",
			source: `package webui
import ("context"; "os/exec"; "strings"; "testing")
func TestSample(t *testing.T) {
	node := requireNode(t)
	command := exec.CommandContext(context.Background(), node, "-")
	command.Stdin = strings.NewReader("program")
}`,
			want: []string{"TestSample", "TestSample"},
		},
		{
			name: "a webui test with exec imported under another name",
			file: "internal/webui/sample_test.go",
			source: `package webui
import (run "os/exec"; "testing")
func TestSample(t *testing.T) { _ = run.Command(requireNode(t), "-") }`,
			want: []string{"TestSample"},
		},
		{
			name: "an exec.Cmd built as a literal",
			file: "internal/webui/sample_test.go",
			source: `package webui
import ("os/exec"; "testing")
func TestValue(t *testing.T) { _ = exec.Cmd{Path: requireNode(t)} }
func TestPointer(t *testing.T) { _ = &exec.Cmd{Path: requireNode(t)} }
func TestNew(t *testing.T) { command := new(exec.Cmd); _ = command }`,
			want: []string{"TestValue", "TestPointer", "TestNew"},
		},
		{
			name: "exec.Command taken as a value",
			file: "internal/webui/sample_test.go",
			source: `package webui
import ("os/exec"; "testing")
func TestSample(t *testing.T) { run := exec.Command; _ = run(requireNode(t), "-") }`,
			want: []string{"TestSample"},
		},
		{
			name: "a package-level function literal",
			file: "internal/webui/sample_test.go",
			source: `package webui
import "os/exec"
var startNode = func(node string) *exec.Cmd { return exec.Command(node, "-") }`,
			want: []string{"package-level startNode"},
		},
		{
			name: "os.StartProcess and its syscall kin",
			file: "internal/webui/sample_test.go",
			source: `package webui
import ("os"; "syscall"; "testing")
func TestOS(t *testing.T) { _, _ = os.StartProcess(requireNode(t), nil, nil) }
func TestSyscall(t *testing.T) { _ = syscall.Exec(requireNode(t), nil, nil) }`,
			want: []string{"TestOS", "TestSyscall"},
		},
		{
			name: "a method that shares a guarded launcher's name",
			file: "internal/webui/sample_test.go",
			source: `package webui
import "os/exec"
type browser struct{}
func (browser) driveBrowser(node string) { _ = exec.Command(node, "-") }`,
			want: []string{"method driveBrowser"},
		},
		{
			name: "a command pointed at another program",
			file: "internal/webui/sample_test.go",
			source: `package webui
import ("os/exec"; "strings"; "testing")
func repoint(command *exec.Cmd, node string) {
	command.Stdin = strings.NewReader("program")
	command.Args = []string{node, "-"}
}
func TestPath(t *testing.T) {
	command := nodeCommand(requireNode(t), "")
	command.Path = "/usr/bin/node"
}
func TestURL(t *testing.T) { request := newRequest(); request.URL.Path = "/x"; request.Path = "/y" }`,
			want: []string{"repoint", "repoint", "TestPath"},
		},
		{
			name: "another package naming node through a variable or a shell",
			file: "internal/other/sample_test.go",
			source: `package other
import ("os/exec"; "testing")
func TestVariable(t *testing.T) { name := "node"; _ = exec.Command(name, "-e", "1") }
func TestShell(t *testing.T) { _ = exec.Command("sh", "-c", "node -e 1") }
func TestElse(t *testing.T) { _ = exec.Command("sh", "-c", "nodes are fine") }`,
			want: []string{"TestVariable", "TestShell"},
		},
		{
			name: "the guarded launchers",
			file: "internal/webui/sample_test.go",
			source: `package webui
import ("context"; "os/exec")
func nodeCommandContext(ctx context.Context, node, program string) *exec.Cmd { return exec.CommandContext(ctx, node, "-") }
func driveBrowser(node, chrome string) { _ = exec.Command(chrome); _ = exec.Command(node, "-") }`,
		},
		{
			name: "another package running a node it resolved",
			file: "internal/other/sample_test.go",
			source: `package other
import ("os/exec"; "testing")
func TestSample(t *testing.T) {
	binary, err := exec.LookPath("node")
	if err != nil { t.Fatal(err) }
	_ = exec.Command(binary, "-e", "1")
}
func TestLiteral(t *testing.T) { _ = exec.Command("node", "-e", "1") }`,
			want: []string{"TestSample", "TestLiteral"},
		},
		{
			name: "another package only probing for node",
			file: "scripts/sample_test.go",
			source: `package scripts
import ("os/exec"; "testing")
func TestSample(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil { t.Skip() }
	_ = exec.Command("./check")
}`,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			found, err := scanNodeLaunches(token.NewFileSet(), scenario.file, []byte(scenario.source))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, launch := range found {
				got = append(got, launch.Function)
			}
			if !slices.Equal(got, scenario.want) {
				t.Fatalf("scan found %v, want %v", found, scenario.want)
			}
		})
	}
}

func scanModuleForNodeLaunches(t *testing.T) ([]nodeLaunch, []string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s, the module root this scan expects: %v", root, err)
	}
	fileSet := token.NewFileSet()
	var found []nodeLaunch
	var files []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" {
				return fs.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		launches, err := scanNodeLaunches(fileSet, relative, source)
		if err != nil {
			return err
		}
		found = append(found, launches...)
		files = append(files, relative)
		return nil
	})
	if err != nil {
		t.Fatalf("scan module sources: %v", err)
	}
	return found, files
}

// scanNodeLaunches reports the places in one source file that could start a
// client program where clientProgramGuard cannot see it.
//
// It is a tripwire against an accident, not a proof: it reads one file's
// syntax, follows no value across functions or files, and someone set on
// starting node around the guard can. What it catches is the ways a person
// writing a test here could plausibly reach for a process without meaning to
// go around nodeCommand:
//
// In this package, wherever it appears outside the two top-level functions
// guardedLaunchers names (a method of the same name does not count), and in a
// package-level variable's initializer as well as in a function:
//
//   - a call of exec.Command or exec.CommandContext, under any name os/exec is
//     imported as, whatever it starts — the package's programs are handed
//     around as strings, so the only line that can be held is the one that
//     starts the process;
//   - either of those taken as a value rather than called, as in
//     `run := exec.Command`, since the call it makes later is out of sight;
//   - an exec.Cmd built directly, as `exec.Cmd{...}`, `&exec.Cmd{...}` or
//     `new(exec.Cmd)`;
//   - os.StartProcess, and syscall's StartProcess, ForkExec and Exec;
//   - an assignment to a Stdin or Args field, or to the Path of a name bound
//     to a command, which is how a command would be pointed at another
//     program. nodeCommand's own result has no such field to assign: it is a
//     clientCommand, which exposes only CombinedOutput and Output, so that
//     accident does not compile.
//
// Elsewhere in the module, where no client program has ever been run, a call
// of exec.Command or exec.CommandContext counts when what it starts is node by
// name, a name the same function bound to "node" or to exec.LookPath("node"),
// or a shell whose command line runs node as a word. A function that only
// probes for node, as the capability preflight's test does, is left alone. A
// helper that resolves node in one function and starts it in another is not
// followed.
func scanNodeLaunches(fileSet *token.FileSet, name string, source []byte) ([]nodeLaunch, error) {
	file, err := parser.ParseFile(fileSet, name, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	imported := func(path string) map[string]bool {
		names := map[string]bool{}
		for _, specification := range file.Imports {
			if specification.Path.Value != strconv.Quote(path) {
				continue
			}
			if specification.Name != nil {
				names[specification.Name.Name] = true
			} else {
				names[path[strings.LastIndex(path, "/")+1:]] = true
			}
		}
		return names
	}
	execNames, osNames, syscallNames := imported("os/exec"), imported("os"), imported("syscall")
	inThisPackage := strings.HasPrefix(name, "internal/webui/")
	if len(execNames) == 0 && !(inThisPackage && (len(osNames) > 0 || len(syscallNames) > 0)) {
		return nil, nil
	}
	// What a selector names, as "exec.Command", when its receiver is one of
	// the import names given.
	qualified := func(expression ast.Expr, receivers map[string]bool, names ...string) string {
		selector, ok := expression.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || !receivers[receiver.Name] || !slices.Contains(names, selector.Sel.Name) {
			return ""
		}
		return receiver.Name + "." + selector.Sel.Name
	}
	execCall := func(node ast.Node, names ...string) (*ast.CallExpr, string) {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return nil, ""
		}
		called := qualified(call.Fun, execNames, names...)
		if called == "" {
			return nil, ""
		}
		return call, called
	}
	var found []nodeLaunch
	report := func(function string, at ast.Node, what string) {
		found = append(found, nodeLaunch{name, function, fileSet.Position(at.Pos()).Line, what})
	}
	// scanThisPackage reports every way in this package to start a process,
	// inside one function body or initializer.
	scanThisPackage := func(function string, body ast.Node) {
		called := map[ast.Expr]bool{}
		// The names bound to a command: what exec or one of the guarded
		// helpers returned. A Path assigned on one of these is a command
		// pointed at another binary; a Path assigned on anything else is
		// usually a URL's.
		commands := map[string]bool{}
		ast.Inspect(body, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok || len(assignment.Rhs) != 1 || len(assignment.Lhs) != 1 {
				return true
			}
			target, ok := assignment.Lhs[0].(*ast.Ident)
			call, isCall := assignment.Rhs[0].(*ast.CallExpr)
			if !ok || !isCall {
				return true
			}
			if helper, ok := call.Fun.(*ast.Ident); ok && guardedLaunchers[helper.Name] || ok && helper.Name == "nodeCommand" {
				commands[target.Name] = true
			}
			if qualified(call.Fun, execNames, "Command", "CommandContext") != "" {
				commands[target.Name] = true
			}
			return true
		})
		ast.Inspect(body, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CallExpr:
				called[node.Fun] = true
				if what := qualified(node.Fun, execNames, "Command", "CommandContext"); what != "" {
					report(function, node, what)
				}
				if what := qualified(node.Fun, osNames, "StartProcess"); what != "" {
					report(function, node, what)
				}
				if what := qualified(node.Fun, syscallNames, "StartProcess", "ForkExec", "Exec"); what != "" {
					report(function, node, what)
				}
				if identifier, ok := node.Fun.(*ast.Ident); ok && identifier.Name == "new" && len(node.Args) == 1 {
					if what := qualified(node.Args[0], execNames, "Cmd"); what != "" {
						report(function, node, "new("+what+")")
					}
				}
			case *ast.SelectorExpr:
				if called[node] {
					return true
				}
				if what := qualified(node, execNames, "Command", "CommandContext"); what != "" {
					report(function, node, what+" taken as a value")
				}
				if what := qualified(node, osNames, "StartProcess"); what != "" {
					report(function, node, what+" taken as a value")
				}
				if what := qualified(node, syscallNames, "StartProcess", "ForkExec", "Exec"); what != "" {
					report(function, node, what+" taken as a value")
				}
			case *ast.CompositeLit:
				if what := qualified(node.Type, execNames, "Cmd"); what != "" {
					report(function, node, "an "+what+" literal")
				}
			case *ast.AssignStmt:
				for _, target := range node.Lhs {
					selector, ok := target.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					receiver, _ := selector.X.(*ast.Ident)
					if slices.Contains([]string{"Stdin", "Args"}, selector.Sel.Name) ||
						selector.Sel.Name == "Path" && receiver != nil && commands[receiver.Name] {
						report(function, selector, "an assignment to ."+selector.Sel.Name)
					}
				}
			}
			return true
		})
	}
	// scanElsewhere reports, in another package, a call that starts node.
	scanElsewhere := func(function string, body ast.Node) {
		// The names this function bound to "node" or to exec.LookPath("node").
		resolved := map[string]bool{}
		ast.Inspect(body, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok || len(assignment.Rhs) != 1 || len(assignment.Lhs) == 0 {
				return true
			}
			target, ok := assignment.Lhs[0].(*ast.Ident)
			if !ok || target.Name == "_" {
				return true
			}
			if literal, ok := assignment.Rhs[0].(*ast.BasicLit); ok && literal.Value == strconv.Quote("node") {
				resolved[target.Name] = true
			}
			call, _ := execCall(assignment.Rhs[0], "LookPath")
			if call == nil || len(call.Args) != 1 {
				return true
			}
			if literal, ok := call.Args[0].(*ast.BasicLit); ok && literal.Value == strconv.Quote("node") {
				resolved[target.Name] = true
			}
			return true
		})
		ast.Inspect(body, func(node ast.Node) bool {
			call, called := execCall(node, "Command", "CommandContext")
			if call == nil {
				return true
			}
			program := 0
			if strings.HasSuffix(called, ".CommandContext") {
				program = 1
			}
			if len(call.Args) <= program {
				return true
			}
			switch argument := call.Args[program].(type) {
			case *ast.BasicLit:
				if argument.Value == strconv.Quote("node") {
					report(function, call, called)
					return true
				}
			case *ast.Ident:
				if resolved[argument.Name] {
					report(function, call, called)
					return true
				}
			}
			// A shell told to run node: any later argument whose words
			// include node.
			for _, argument := range call.Args[program+1:] {
				literal, ok := argument.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				text, err := strconv.Unquote(literal.Value)
				if err == nil && slices.Contains(strings.Fields(text), "node") {
					report(function, call, called+" running a shell command that names node")
					return true
				}
			}
			return true
		})
	}
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if declaration.Body == nil {
				continue
			}
			function := declaration.Name.Name
			if declaration.Recv != nil {
				function = "method " + function
			}
			if inThisPackage {
				// Only the top-level functions by these names are the guarded
				// ones; a method that shares a name is not one of them.
				if declaration.Recv == nil && guardedLaunchers[declaration.Name.Name] {
					continue
				}
				scanThisPackage(function, declaration.Body)
			} else {
				scanElsewhere(function, declaration.Body)
			}
		case *ast.GenDecl:
			for _, specification := range declaration.Specs {
				values, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for at, value := range values.Values {
					function := "a package-level initializer"
					if at < len(values.Names) {
						function = "package-level " + values.Names[at].Name
					}
					if inThisPackage {
						scanThisPackage(function, value)
					} else {
						scanElsewhere(function, value)
					}
				}
			}
		}
	}
	return found, nil
}
