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
	"sort"
	"strings"
	"testing"
)

// A bare t.Skip is invisible to both halves of the capability guard:
// RequireCapabilitiesVariable only changes behavior where MissingCapability is
// called, and scripts/skipreport only fails a run for skips carrying
// MissingCapabilityPrefix. A test that probes for a tool with exec.LookPath and
// then skips silently drops its coverage on any machine missing that tool,
// including CI. The tests below read the repository's own test sources so the
// convention is enforced structurally rather than remembered by reviewers.

// bareSkip is one place a test function probes the environment with
// exec.LookPath and disposes of the result with t.Skip instead of
// MissingCapability.
type bareSkip struct {
	// File is the repository-relative path of the test file.
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
func TestNoTestFilePairsACapabilityProbeWithABareSkip(t *testing.T) {
	found := scanRepositoryForBareSkips(t)

	unexpected := withoutExceptions(found, bareSkipExceptions)
	if len(unexpected) == 0 {
		return
	}
	var report strings.Builder
	for _, skip := range unexpected {
		fmt.Fprintf(&report, "\n\t%s", skip)
	}
	t.Fatalf("%d test function(s) report a missing tool with a bare skip instead of testenv.MissingCapability:%s"+
		"\n\nA bare skip is invisible to %s and to scripts/skipreport, so the coverage disappears silently."+
		"\nCall testenv.MissingCapability instead, or add a bareSkipException naming why the skip is not a capability report.",
		len(unexpected), report.String(), RequireCapabilitiesVariable)
}

// Mutation witness: an exception that outlives the code it excused turns into a
// permanent hole in the guard, because nothing else reports that the function
// it names no longer skips at all.
func TestBareSkipExceptionsAreStillNeeded(t *testing.T) {
	if len(bareSkipExceptions) == 0 {
		return
	}
	found := scanRepositoryForBareSkips(t)

	for _, exception := range bareSkipExceptions {
		if exception.Reason == "" {
			t.Errorf("exception for %s in %s records no reason", exception.Function, exception.File)
		}
		if !matchesAny(found, exception) {
			t.Errorf("exception for %s in %s no longer matches a bare skip; delete it", exception.Function, exception.File)
		}
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

// scanRepositoryForBareSkips parses every _test.go file in the module. Parsing
// costs a few milliseconds over the whole tree, so the guard stays cheap enough
// to run with the ordinary suite.
func scanRepositoryForBareSkips(t *testing.T) []bareSkip {
	t.Helper()
	root := repositoryRoot(t)
	fileSet := token.NewFileSet()
	var found []bareSkip
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
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		skips, err := scanBareCapabilitySkips(fileSet, filepath.ToSlash(relative), source)
		if err != nil {
			return err
		}
		found = append(found, skips...)
		return nil
	})
	if err != nil {
		t.Fatalf("scan repository test sources: %v", err)
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].File != found[j].File {
			return found[i].File < found[j].File
		}
		return found[i].Line < found[j].Line
	})
	return found
}

// scanBareCapabilitySkips reports every skip call in a function that also
// probes for a tool with exec.LookPath. The pairing is judged per enclosing
// top-level function rather than per if-statement, because the probe and the
// skip are written apart as often as together; a function that probes and skips
// for genuinely unrelated reasons takes a bareSkipException.
func scanBareCapabilitySkips(fileSet *token.FileSet, name string, source []byte) ([]bareSkip, error) {
	file, err := parser.ParseFile(fileSet, name, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
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
				if receiver, ok := selector.X.(*ast.Ident); ok && receiver.Name == "exec" {
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
