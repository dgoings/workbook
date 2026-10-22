package testenv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// An executable written with os.WriteFile and run straight away works on
// macOS and fails on Linux whenever a parallel test forks at the wrong moment,
// so the flake only ever shows up in CI. WriteExecutable is the one place that
// knows how to avoid it; the tests below read the module's own Go sources so a
// new test cannot go back to os.WriteFile without being told.

// executableWrite is one call that creates a file with an execute bit set
// somewhere other than WriteExecutable or WriteFile.
type executableWrite struct {
	// File is the repository-relative path of the source file.
	File string
	// Function is the enclosing top-level function.
	Function string
	// Line is the position of the call.
	Line int
	// Call is the function called, for the report.
	Call string
}

func (w executableWrite) String() string {
	return fmt.Sprintf("%s:%d: %s creates an executable with %s", w.File, w.Line, w.Function, w.Call)
}

// Mutation witness: without this test the next fake git or stub script is
// written with os.WriteFile, passes everywhere but Linux under load, and
// brings the "text file busy" flake back.
func TestNoSourceWritesAnExecutableWithoutTheForkLock(t *testing.T) {
	found, _ := scanRepositoryForExecutableWrites(t)
	if len(found) == 0 {
		return
	}
	var report strings.Builder
	for _, write := range found {
		fmt.Fprintf(&report, "\n\t%s", write)
	}
	t.Fatalf("%d call(s) write an executable without testenv.WriteExecutable:%s"+
		"\n\nOn Linux, a parallel test that forks while the file is open for writing hands the"+
		"\ndescriptor to its child, and running the file then fails with \"text file busy\"."+
		"\nWrite it with testenv.WriteExecutable, or testenv.WriteFile where no *testing.T is at hand.",
		len(found), report.String())
}

// Mutation witness: a walk that reaches nothing finds nothing and reads exactly
// like a repository with no unsafe writes.
func TestExecutableWriteScanReachesTheFilesThatRunHelpers(t *testing.T) {
	_, files := scanRepositoryForExecutableWrites(t)

	// Each of these wrote a helper with os.WriteFile and then ran it; the
	// first is where the flake was seen, and the last is not a _test.go file.
	for _, sentinel := range []string{
		"internal/gitstore/sync_plan_test.go",
		"scripts/publish_release_test.go",
		"cmd/workbook-bench/main_test.go",
		"internal/perf/proctest/proctest.go",
	} {
		if !slices.Contains(files, sentinel) {
			t.Errorf("the scan never parsed %s, so it is not reading this repository's helper writes", sentinel)
		}
	}
	if len(files) < 50 {
		t.Errorf("the scan parsed %d Go files, want the whole module's sources", len(files))
	}
}

// Mutation witness: a scanner that misses one spelling of an executable mode
// lets that spelling through forever.
func TestScanExecutableWritesFindsEverySpellingOfAnExecutableMode(t *testing.T) {
	source := []byte(`package sample

import (
	"io/fs"
	goos "os"
	"testing"
)

func TestOctal(t *testing.T) {
	_ = goos.WriteFile("a", nil, 0o755)
	_ = goos.WriteFile("b", nil, 0755)
	_ = goos.WriteFile("c", nil, 0o700)
	_ = goos.WriteFile("d", nil, 0o744)
}

func TestConverted(t *testing.T) {
	_ = goos.WriteFile("e", nil, fs.FileMode(0o755))
	_ = goos.WriteFile("f", nil, (0o755))
	_ = goos.WriteFile("g", nil, goos.ModePerm)
}

func TestOpened(t *testing.T) {
	file, _ := goos.OpenFile("h", goos.O_CREATE|goos.O_WRONLY, 0o755)
	_ = file
}
`)

	found, err := scanExecutableWrites(token.NewFileSet(), "sample_test.go", source)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var lines []int
	for _, write := range found {
		lines = append(lines, write.Line)
	}
	if want := []int{10, 11, 12, 13, 17, 18, 19, 23}; !slices.Equal(lines, want) {
		t.Fatalf("found = %v, want a finding on each of lines %v", found, want)
	}
	if found[0].Function != "TestOctal" || found[0].Call != "goos.WriteFile" {
		t.Errorf("first finding = %+v, want goos.WriteFile in TestOctal", found[0])
	}
	if found[7].Function != "TestOpened" || found[7].Call != "goos.OpenFile" {
		t.Errorf("last finding = %+v, want goos.OpenFile in TestOpened", found[7])
	}
}

// Mutation witness: a scanner that flags every write, or production code that
// can never import a *testing.T helper, pressures the next contributor into
// working around the guard instead of heeding it.
func TestScanExecutableWritesIgnoresWritesThatCannotBeRun(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		source string
	}{
		{
			name: "modes without an execute bit, or not known until run time",
			source: `package sample

import (
	"os"
	"testing"
)

func TestData(t *testing.T, mode os.FileMode) {
	_ = os.WriteFile("a", nil, 0o644)
	_ = os.WriteFile("b", nil, 0600)
	_ = os.WriteFile("c", nil, mode)
	_ = os.Chmod("directory", 0o755)
	_ = os.Mkdir("directory", 0o755)
}
`,
		},
		{
			name: "WriteFile from a package other than os",
			source: `package sample

import (
	"testing"

	"example.com/os"
)

func TestElsewhere(t *testing.T) {
	_ = os.WriteFile("a", nil, 0o755)
}
`,
		},
		{
			name: "production source that never sees a *testing.T",
			source: `package sample

import "os"

func install(path string) error {
	return os.WriteFile(path, nil, 0o755)
}
`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			found, err := scanExecutableWrites(token.NewFileSet(), "sample.go", []byte(testCase.source))
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if len(found) != 0 {
				t.Fatalf("found = %v, want no findings", found)
			}
		})
	}
}

func scanRepositoryForExecutableWrites(t *testing.T) ([]executableWrite, []string) {
	t.Helper()
	fileSet := token.NewFileSet()
	var found []executableWrite
	files, err := walkGoSources(repositoryRoot(t), func(relative string, source []byte) error {
		writes, err := scanExecutableWrites(fileSet, relative, source)
		found = append(found, writes...)
		return err
	})
	if err != nil {
		t.Fatalf("scan repository sources: %v", err)
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].File != found[j].File {
			return found[i].File < found[j].File
		}
		return found[i].Line < found[j].Line
	})
	return found, files
}

// scanExecutableWrites reports every os.WriteFile or os.OpenFile call whose
// mode is a constant with an execute bit: an octal literal, the same converted
// to a FileMode, or os.ModePerm. Only source importing testing is considered,
// because WriteExecutable takes a *testing.T and production code cannot use it.
//
// A mode held in a variable is not followed, which is what lets WriteFile
// itself and testrepo.CopyTree pass a mode through, and neither is a file
// created without an execute bit and then made executable with Chmod; both
// remain a matter of review.
func scanExecutableWrites(fileSet *token.FileSet, name string, source []byte) ([]executableWrite, error) {
	file, err := parser.ParseFile(fileSet, name, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	osNames := importedAs(file, "os")
	if len(osNames) == 0 || len(importedAs(file, "testing")) == 0 {
		return nil, nil
	}
	var found []executableWrite
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 3 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			receiver, ok := selector.X.(*ast.Ident)
			if !ok || !osNames[receiver.Name] {
				return true
			}
			if selector.Sel.Name != "WriteFile" && selector.Sel.Name != "OpenFile" {
				return true
			}
			if !executableMode(call.Args[2], osNames) {
				return true
			}
			found = append(found, executableWrite{
				File:     name,
				Function: function.Name.Name,
				Line:     fileSet.Position(call.Pos()).Line,
				Call:     receiver.Name + "." + selector.Sel.Name,
			})
			return true
		})
	}
	return found, nil
}

// executableMode reports whether a mode argument is a constant with any
// execute bit set.
func executableMode(expression ast.Expr, osNames map[string]bool) bool {
	switch mode := expression.(type) {
	case *ast.ParenExpr:
		return executableMode(mode.X, osNames)
	case *ast.CallExpr:
		// A conversion such as fs.FileMode(0o755) or os.FileMode(0o755).
		return len(mode.Args) == 1 && executableMode(mode.Args[0], osNames)
	case *ast.SelectorExpr:
		receiver, ok := mode.X.(*ast.Ident)
		return ok && osNames[receiver.Name] && mode.Sel.Name == "ModePerm"
	case *ast.BasicLit:
		if mode.Kind != token.INT {
			return false
		}
		// Base 0 reads the literal as Go does: 0755 and 0o755 are both octal.
		value, err := strconv.ParseUint(mode.Value, 0, 32)
		return err == nil && value&0o111 != 0
	}
	return false
}
