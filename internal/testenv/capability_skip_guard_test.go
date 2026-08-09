package testenv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A bare t.Skip is invisible to both halves of the capability guard:
// RequireCapabilitiesVariable only changes behavior where MissingCapability is
// called, and scripts/skipreport only fails a run for skips carrying
// MissingCapabilityPrefix. A test that probes for a tool with exec.LookPath and
// then skips silently drops its coverage on any machine missing that tool,
// including CI. The tests below read the repository's own Go sources so the
// convention is enforced structurally rather than remembered by reviewers.

// bareSkip is one place a test function probes the environment with
// exec.LookPath and disposes of the result with t.Skip instead of
// MissingCapability.
type bareSkip struct {
	// File is the repository-relative path of the source file.
	File string
	// Function is the enclosing top-level function, which is the unit an
	// exception is granted to.
	Function string
	// Line is the position of the skip call.
	Line int
	// Call is the skip method named at that position, for the report.
	Call string
}

func (s bareSkip) String() string {
	return fmt.Sprintf("%s:%d: %s calls %s after probing with exec.LookPath", s.File, s.Line, s.Function, s.Call)
}

// bareSkipException exempts one function from the guard. Grant one only when
// the skip is unrelated to what the function probes for: a skip that stands in
// for a missing tool belongs in MissingCapability, which skips locally and
// fails wherever RequireCapabilitiesVariable is set. Entries are checked
// against the scan by TestBareSkipExceptionsAreStillNeeded, so one left behind
// after the code changes fails rather than quietly widening the guard.
type bareSkipException struct {
	// File is the repository-relative path the exception applies to.
	File string
	// Function is the enclosing top-level function it applies to.
	Function string
	// Reason says why this skip is not a capability report.
	Reason string
}

// bareSkipExceptions is the complete list of functions allowed to pair an
// exec.LookPath probe with a bare skip. It is empty: every capability probe in
// this repository reports through MissingCapability.
var bareSkipExceptions []bareSkipException

// Mutation witness: without this test a contributor can reintroduce the
// pattern the capability marker replaced -- exec.LookPath followed by t.Skip --
// and CI stays green while the tests it guards stop running.
func TestNoTestSourcePairsACapabilityProbeWithABareSkip(t *testing.T) {
	scan := scanRepositoryForBareSkips(t)

	unexpected := withoutExceptions(scan.Found, bareSkipExceptions)
	if len(unexpected) == 0 {
		return
	}
	var report strings.Builder
	for _, skip := range unexpected {
		fmt.Fprintf(&report, "\n\t%s", skip)
	}
	t.Fatalf("%d test function(s) report a missing tool with a bare skip instead of testenv.MissingCapability:%s"+
		"\n\nA bare skip is invisible to %s, and scripts/skipreport lists it in the job summary without failing the"+
		"\nrun, so the coverage disappears while CI stays green."+
		"\nCall testenv.MissingCapability instead, or add a bareSkipException naming why the skip is not a capability report.",
		len(unexpected), report.String(), RequireCapabilitiesVariable)
}

// Mutation witness: the walk is the only thing joining the parser to this
// repository, and the test above finds nothing whether the tree is clean or the
// walk reached no file at all. A root that resolves somewhere empty, a prune
// that swallows the module, or an extension filter that matches nothing all
// report a clean repository forever.
func TestScanRepositoryReachesThisRepositorysCapabilityProbes(t *testing.T) {
	scan := scanRepositoryForBareSkips(t)

	// Each of these holds a capability probe today, in a different directory,
	// and the last is deliberately not a _test.go file.
	for _, sentinel := range []string{
		"internal/webui/handler_test.go",
		"scripts/check_ci_capabilities_test.go",
		"internal/testrepo/repository.go",
	} {
		if !slices.Contains(scan.Files, sentinel) {
			t.Errorf("the scan never parsed %s, so it is not reading this repository's capability probes", sentinel)
		}
	}
	// A prune that kept only the sentinels' directories would still satisfy the
	// check above, so require the scan to have covered the module broadly.
	if len(scan.Files) < 50 {
		t.Errorf("the scan parsed %d Go files, want the whole module's sources", len(scan.Files))
	}
}

// Mutation witness: an exception that outlives the code it excused turns into a
// permanent hole in the guard, because nothing else reports that the function
// it names no longer skips at all.
func TestBareSkipExceptionsAreStillNeeded(t *testing.T) {
	if len(bareSkipExceptions) == 0 {
		return
	}
	scan := scanRepositoryForBareSkips(t)

	for _, problem := range exceptionProblems(scan.Found, bareSkipExceptions) {
		t.Error(problem)
	}
}

// Mutation witness: bareSkipExceptions is empty, so the test above returns
// before reaching exceptionProblems and the stale-exception behavior documented
// in README.md would otherwise be exercised for the first time by whoever adds
// the first exception.
func TestExceptionProblemsReportsUnreasonedAndUnmatchedEntries(t *testing.T) {
	found := []bareSkip{
		{File: "internal/sample/sample_test.go", Function: "TestExcused", Line: 10, Call: "t.Skip"},
	}
	for _, testCase := range []struct {
		name      string
		exception bareSkipException
		want      []string
	}{
		{
			name:      "matched and reasoned",
			exception: bareSkipException{File: "internal/sample/sample_test.go", Function: "TestExcused", Reason: "skips for a reason of its own"},
		},
		{
			name:      "no reason recorded",
			exception: bareSkipException{File: "internal/sample/sample_test.go", Function: "TestExcused"},
			want:      []string{"records no reason"},
		},
		{
			name:      "function no longer skips",
			exception: bareSkipException{File: "internal/sample/sample_test.go", Function: "TestConverted", Reason: "skips for a reason of its own"},
			want:      []string{"no longer matches"},
		},
		{
			name:      "same file, different function",
			exception: bareSkipException{File: "internal/sample/sample_test.go", Function: "TestNeverExisted", Reason: "skips for a reason of its own"},
			want:      []string{"no longer matches"},
		},
		{
			name:      "unreasoned and unmatched",
			exception: bareSkipException{File: "internal/other/other_test.go", Function: "TestExcused"},
			want:      []string{"records no reason", "no longer matches"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			problems := exceptionProblems(found, []bareSkipException{testCase.exception})

			if len(problems) != len(testCase.want) {
				t.Fatalf("problems = %v, want %d problem(s)", problems, len(testCase.want))
			}
			for i, want := range testCase.want {
				if !strings.Contains(problems[i], want) {
					t.Errorf("problem %d = %q, want it to mention %q", i, problems[i], want)
				}
			}
		})
	}
}

// Mutation witness: a scanner that misses the pairing reports a clean
// repository forever, which is exactly how the guard would fail.
func TestScanBareCapabilitySkipsFindsAProbeDisposedOfWithASkip(t *testing.T) {
	source := []byte(`package sample

import (
	"os/exec"
	"testing"
)

func TestNeedsNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
	_ = node
}

func TestNeedsGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is unavailable: %v", err)
	}
}
`)

	found, err := scanBareCapabilitySkips(token.NewFileSet(), "sample_test.go", source)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("found = %v, want one skip in each function", found)
	}
	if found[0].Function != "TestNeedsNode" || found[0].Call != "t.Skip" || found[0].Line != 11 {
		t.Errorf("first finding = %+v, want t.Skip in TestNeedsNode at line 11", found[0])
	}
	if found[1].Function != "TestNeedsGit" || found[1].Call != "t.Skipf" || found[1].Line != 18 {
		t.Errorf("second finding = %+v, want t.Skipf in TestNeedsGit at line 18", found[1])
	}
}

// Mutation witness: a scanner that flags every skip, or every probe, makes the
// guard unusable and pressures the next contributor into a blanket exception.
func TestScanBareCapabilitySkipsAcceptsMarkedAndUnrelatedSkips(t *testing.T) {
	source := []byte(`package sample

import (
	"os/exec"
	"testing"

	"github.com/dgoings/workbook/internal/testenv"
)

func TestReportsTheMissingCapability(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		testenv.MissingCapability(t, "node is required to execute the embedded client behavior")
	}
}

func TestSkipsForAnUnrelatedReason(t *testing.T) {
	if testing.Short() {
		t.Skip("builds four platform archives; skipped in -short mode")
	}
}

func helperWithoutASkip(t *testing.T) string {
	path, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	return path
}
`)

	found, err := scanBareCapabilitySkips(token.NewFileSet(), "sample_test.go", source)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("found = %v, want no findings", found)
	}
}

// Mutation witness: the probe is recognized by the name os/exec is bound to in
// the file, not by the spelling "exec", so renaming the import cannot walk a
// capability skip past the guard.
func TestScanBareCapabilitySkipsResolvesTheOSExecImportName(t *testing.T) {
	source := []byte(`package sample

import (
	goexec "os/exec"
	"testing"
)

func TestNeedsNode(t *testing.T) {
	if _, err := goexec.LookPath("node"); err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
}
`)

	found, err := scanBareCapabilitySkips(token.NewFileSet(), "sample_test.go", source)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 1 || found[0].Function != "TestNeedsNode" {
		t.Fatalf("found = %v, want the skip in TestNeedsNode", found)
	}
}

// Mutation witness: a package of its own named exec, or a LookPath method on
// some other value, is not a capability probe, and flagging one would push a
// contributor toward an exception that excuses a real skip later.
func TestScanBareCapabilitySkipsIgnoresSourceThatCannotProbe(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		source string
	}{
		{
			name: "LookPath on something other than os/exec",
			source: `package sample

import (
	"testing"

	"example.com/exec"
)

func TestNeedsNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to execute the embedded client behavior")
	}
}
`,
		},
		{
			name: "production source that never sees a *testing.T",
			source: `package sample

import "os/exec"

type walker struct{}

func (w walker) Skip() {}

func find(w walker) {
	if _, err := exec.LookPath("node"); err != nil {
		w.Skip()
	}
}
`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			found, err := scanBareCapabilitySkips(token.NewFileSet(), "sample.go", []byte(testCase.source))
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if len(found) != 0 {
				t.Fatalf("found = %v, want no findings", found)
			}
		})
	}
}

// Mutation witness: an exception matched by file alone would excuse every skip
// added to that file afterwards, including the next unmarked one.
func TestWithoutExceptionsExcusesOnlyTheNamedFunction(t *testing.T) {
	found := []bareSkip{
		{File: "internal/sample/sample_test.go", Function: "TestExcused", Line: 10, Call: "t.Skip"},
		{File: "internal/sample/sample_test.go", Function: "TestUnexcused", Line: 20, Call: "t.Skip"},
		{File: "internal/other/other_test.go", Function: "TestExcused", Line: 30, Call: "t.Skip"},
	}
	exceptions := []bareSkipException{
		{File: "internal/sample/sample_test.go", Function: "TestExcused", Reason: "skips for a reason of its own"},
	}

	kept := withoutExceptions(found, exceptions)

	if len(kept) != 2 || kept[0].Function != "TestUnexcused" || kept[1].File != "internal/other/other_test.go" {
		t.Fatalf("kept = %v, want the unexcused skip and the same function in another file", kept)
	}
}

// Mutation witness: the walk is what the guard's per-file tests cannot reach.
// This one builds a tree with a violation in each place the guard is supposed
// to look and one in each place it is supposed to leave alone, so a filter, a
// prune, or a relative path that stops working fails here.
func TestScanTreeReadsTestSourcesAndPrunesForeignTrees(t *testing.T) {
	bareSkipSource := `package sample

import (
	"os/exec"
	"testing"
)

func RequireNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required")
	}
}
`
	root := t.TempDir()
	write := func(relative, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", relative, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relative, err)
		}
	}
	write("go.mod", "module sample\n")
	write("internal/sample/sample_test.go", bareSkipSource)
	// A capability helper outside any _test.go file, the shape internal/testrepo
	// and internal/syncloop/watchertest already use.
	write("internal/helper/helper.go", bareSkipSource)
	write("internal/helper/helper.md", "not Go source\n")
	// Hidden directories hold no module source, and a nested module -- including
	// a sibling worktree checked out beneath the root -- is somebody else's tree.
	write(".hidden/hidden_test.go", bareSkipSource)
	write("nested/go.mod", "module nested\n")
	write("nested/nested_test.go", bareSkipSource)

	scan, err := scanTree(root)
	if err != nil {
		t.Fatalf("scan tree: %v", err)
	}

	wantFiles := []string{"internal/helper/helper.go", "internal/sample/sample_test.go"}
	if !slices.Equal(scan.Files, wantFiles) {
		t.Errorf("parsed files = %v, want %v", scan.Files, wantFiles)
	}
	if len(scan.Found) != 2 {
		t.Fatalf("found = %v, want the skip in each scanned file", scan.Found)
	}
	if scan.Found[0].File != "internal/helper/helper.go" || scan.Found[0].Function != "RequireNode" {
		t.Errorf("first finding = %+v, want RequireNode in internal/helper/helper.go", scan.Found[0])
	}
	if scan.Found[1].File != "internal/sample/sample_test.go" || scan.Found[1].Call != "t.Skip" {
		t.Errorf("second finding = %+v, want t.Skip in internal/sample/sample_test.go", scan.Found[1])
	}
}

// exceptionProblems reports what is wrong with a set of exceptions measured
// against a scan: an entry that records no reason, and an entry that matches no
// bare skip and is therefore excusing nothing.
func exceptionProblems(found []bareSkip, exceptions []bareSkipException) []string {
	var problems []string
	for _, exception := range exceptions {
		if exception.Reason == "" {
			problems = append(problems,
				fmt.Sprintf("exception for %s in %s records no reason", exception.Function, exception.File))
		}
		if !matchesAny(found, exception) {
			problems = append(problems,
				fmt.Sprintf("exception for %s in %s no longer matches a bare skip; delete it", exception.Function, exception.File))
		}
	}
	return problems
}

func matchesAny(found []bareSkip, exception bareSkipException) bool {
	for _, skip := range found {
		if skip.File == exception.File && skip.Function == exception.Function {
			return true
		}
	}
	return false
}

// withoutExceptions drops the skips an exception covers, keyed by file and
// enclosing function so an exception cannot silently spread to a skip added to
// a different function later.
func withoutExceptions(found []bareSkip, exceptions []bareSkipException) []bareSkip {
	var kept []bareSkip
	for _, skip := range found {
		allowed := false
		for _, exception := range exceptions {
			if skip.File == exception.File && skip.Function == exception.Function {
				allowed = true
				break
			}
		}
		if !allowed {
			kept = append(kept, skip)
		}
	}
	return kept
}

// repositoryScan is one pass over a tree: the bare skips it found and the files
// it parsed. The file list is what the walk itself is checked against, because
// a walk that reaches nothing finds nothing and reads exactly like a clean
// repository.
type repositoryScan struct {
	// Found lists every bare capability skip, sorted by file and line.
	Found []bareSkip
	// Files lists the parsed sources as repository-relative slash paths.
	Files []string
}

func scanRepositoryForBareSkips(t *testing.T) repositoryScan {
	t.Helper()
	scan, err := scanTree(repositoryRoot(t))
	if err != nil {
		t.Fatalf("scan repository sources: %v", err)
	}
	return scan
}

// scanTree parses every Go source under root, skipping hidden directories and
// nested modules. It reads more than _test.go files because a capability probe
// is as likely to live in a helper package: internal/testrepo and
// internal/syncloop/watchertest both take a *testing.T outside any test file.
// Parsing costs a few milliseconds over the whole tree, so the guard stays
// cheap enough to run with the ordinary suite.
func scanTree(root string) (repositoryScan, error) {
	fileSet := token.NewFileSet()
	var scan repositoryScan
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			// Hidden directories hold no module source, and a nested module --
			// including a sibling worktree checked out beneath the root -- is
			// somebody else's tree to police.
			if strings.HasPrefix(entry.Name(), ".") || containsModule(path) {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(entry.Name()) != ".go" {
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
		skips, err := scanBareCapabilitySkips(fileSet, relative, source)
		if err != nil {
			return err
		}
		scan.Files = append(scan.Files, relative)
		scan.Found = append(scan.Found, skips...)
		return nil
	})
	if err != nil {
		return repositoryScan{}, err
	}
	sort.Strings(scan.Files)
	sort.Slice(scan.Found, func(i, j int) bool {
		if scan.Found[i].File != scan.Found[j].File {
			return scan.Found[i].File < scan.Found[j].File
		}
		return scan.Found[i].Line < scan.Found[j].Line
	})
	return scan, nil
}

// scanBareCapabilitySkips reports every skip call in a function that also
// probes for a tool with exec.LookPath. Only source importing both os/exec and
// testing is considered: without the first there is no probe, and without the
// second there is no *testing.T to skip, so a Skip method on some unrelated
// value cannot be mistaken for one.
//
// The os/exec import name is resolved per file, so an aliased import still
// reads as a probe. The pairing is judged per enclosing top-level function
// rather than per if-statement, because the probe and the skip are written
// apart as often as together; a function that probes and skips for genuinely
// unrelated reasons takes a bareSkipException. A probe and a skip split across
// two functions -- a helper that skips on behalf of its caller -- needs
// whole-package type information to follow and remains a matter of review.
func scanBareCapabilitySkips(fileSet *token.FileSet, name string, source []byte) ([]bareSkip, error) {
	file, err := parser.ParseFile(fileSet, name, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	probeNames := importedAs(file, "os/exec")
	if len(probeNames) == 0 || len(importedAs(file, "testing")) == 0 {
		return nil, nil
	}
	var found []bareSkip
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		probes := false
		var skips []*ast.SelectorExpr
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "LookPath":
				if receiver, ok := selector.X.(*ast.Ident); ok && probeNames[receiver.Name] {
					probes = true
				}
			case "Skip", "Skipf", "SkipNow":
				skips = append(skips, selector)
			}
			return true
		})
		if !probes {
			continue
		}
		for _, skip := range skips {
			found = append(found, bareSkip{
				File:     name,
				Function: function.Name.Name,
				Line:     fileSet.Position(skip.Sel.Pos()).Line,
				Call:     skipCallName(skip),
			})
		}
	}
	return found, nil
}

// importedAs returns the local names a file binds to an import path. A file
// that does not import the path at all yields none, which is how a source that
// cannot probe for a tool is recognized without type checking it.
func importedAs(file *ast.File, path string) map[string]bool {
	quoted := strconv.Quote(path)
	names := make(map[string]bool)
	for _, specification := range file.Imports {
		if specification.Path.Value != quoted {
			continue
		}
		if specification.Name != nil {
			names[specification.Name.Name] = true
			continue
		}
		names[path[strings.LastIndex(path, "/")+1:]] = true
	}
	return names
}

func skipCallName(selector *ast.SelectorExpr) string {
	if receiver, ok := selector.X.(*ast.Ident); ok {
		return receiver.Name + "." + selector.Sel.Name
	}
	return selector.Sel.Name
}

func containsModule(directory string) bool {
	_, err := os.Stat(filepath.Join(directory, "go.mod"))
	return err == nil
}

// repositoryRoot walks up from this source file to the directory holding
// go.mod, so the scan covers the whole module wherever the checkout lives.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine the path of the capability skip guard")
	}
	directory := filepath.Dir(filename)
	for {
		if containsModule(directory) {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatalf("no go.mod above %s", filepath.Dir(filename))
		}
		directory = parent
	}
}
