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

// The guard holds only for a program that reaches node through
// nodeCommandContext. A test that starts node on its own runs its program
// unguarded, and a check stalled on a promise nobody settles passes again
// without a word. These tests read the module's Go sources so that a new test
// cannot start a client program any other way without being told.

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
			want: []string{"TestSample"},
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

// scanNodeLaunches reports the calls in one source file that start a client
// program where clientProgramGuard cannot see it.
//
// In this package every exec.Command and exec.CommandContext outside
// guardedLaunchers counts, whatever it starts: the package's programs are
// handed around as strings, so the only line that can be held is the one that
// starts the process. Elsewhere in the module, where no client program has ever
// been run, a call counts when it starts node by name or starts a path the same
// function resolved with exec.LookPath("node"); a function that only probes for
// node, as the capability preflight's test does, is left alone.
func scanNodeLaunches(fileSet *token.FileSet, name string, source []byte) ([]nodeLaunch, error) {
	file, err := parser.ParseFile(fileSet, name, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	execNames := map[string]bool{}
	for _, specification := range file.Imports {
		if specification.Path.Value != strconv.Quote("os/exec") {
			continue
		}
		if specification.Name != nil {
			execNames[specification.Name.Name] = true
		} else {
			execNames["exec"] = true
		}
	}
	if len(execNames) == 0 {
		return nil, nil
	}
	inThisPackage := strings.HasPrefix(name, "internal/webui/")
	execCall := func(node ast.Node, names ...string) (*ast.CallExpr, string) {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return nil, ""
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return nil, ""
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || !execNames[receiver.Name] || !slices.Contains(names, selector.Sel.Name) {
			return nil, ""
		}
		return call, receiver.Name + "." + selector.Sel.Name
	}
	var found []nodeLaunch
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		// The names this function bound to exec.LookPath("node").
		resolved := map[string]bool{}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok || len(assignment.Rhs) != 1 || len(assignment.Lhs) == 0 {
				return true
			}
			call, _ := execCall(assignment.Rhs[0], "LookPath")
			if call == nil || len(call.Args) != 1 {
				return true
			}
			if literal, ok := call.Args[0].(*ast.BasicLit); ok && literal.Value == strconv.Quote("node") {
				if target, ok := assignment.Lhs[0].(*ast.Ident); ok && target.Name != "_" {
					resolved[target.Name] = true
				}
			}
			return true
		})
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, called := execCall(node, "Command", "CommandContext")
			if call == nil {
				return true
			}
			if inThisPackage {
				if !guardedLaunchers[function.Name.Name] {
					found = append(found, nodeLaunch{name, function.Name.Name, fileSet.Position(call.Pos()).Line, called})
				}
				return true
			}
			program := 0
			if called == "exec.CommandContext" || strings.HasSuffix(called, ".CommandContext") {
				program = 1
			}
			if len(call.Args) <= program {
				return true
			}
			switch argument := call.Args[program].(type) {
			case *ast.BasicLit:
				if argument.Value == strconv.Quote("node") {
					found = append(found, nodeLaunch{name, function.Name.Name, fileSet.Position(call.Pos()).Line, called})
				}
			case *ast.Ident:
				if resolved[argument.Name] {
					found = append(found, nodeLaunch{name, function.Name.Name, fileSet.Position(call.Pos()).Line, called})
				}
			}
			return true
		})
	}
	return found, nil
}
