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
	source := `package sample

import (
	"io/fs"
	goos "os"
	"testing"
)

const executable = 0o755

const (
	readable   = 0o644
	searchable = readable | 0o111
)

func TestOctal(t *testing.T) {
	_ = goos.WriteFile("a", nil, 0o755) // flagged
	_ = goos.WriteFile("b", nil, 0755)  // flagged
	_ = goos.WriteFile("c", nil, 0o700) // flagged
	_ = goos.WriteFile("d", nil, 0o744) // flagged
}

func TestConverted(t *testing.T) {
	_ = goos.WriteFile("e", nil, fs.FileMode(0o755)) // flagged
	_ = goos.WriteFile("f", nil, (0o755))            // flagged
	_ = goos.WriteFile("g", nil, goos.ModePerm)      // flagged
	_ = goos.WriteFile("h", nil, fs.ModePerm)        // flagged
}

func TestNamed(t *testing.T) {
	const local fs.FileMode = 0o700
	_ = goos.WriteFile("i", nil, executable)            // flagged
	_ = goos.WriteFile("j", nil, local)                 // flagged
	_ = goos.WriteFile("k", nil, searchable)            // flagged
	_ = goos.WriteFile("l", nil, fs.FileMode(executable)) // flagged
}

func TestCombined(t *testing.T) {
	_ = goos.WriteFile("m", nil, 0o644|0o111)         // flagged
	_ = goos.WriteFile("n", nil, executable&^0o022)   // flagged
	_ = goos.WriteFile("o", nil, goos.ModePerm&0o750) // flagged
}

func TestOpened(t *testing.T) {
	file, _ := goos.OpenFile("p", goos.O_CREATE|goos.O_WRONLY, 0o755) // flagged
	_ = file
}
`

	found, err := scanExecutableWrites(token.NewFileSet(), "sample_test.go", []byte(source))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var lines []int
	for _, write := range found {
		lines = append(lines, write.Line)
	}
	if want := linesMarked(source, "// flagged"); !slices.Equal(lines, want) {
		t.Fatalf("found = %v, want a finding on each of lines %v", found, want)
	}
	if found[0].Function != "TestOctal" || found[0].Call != "goos.WriteFile" {
		t.Errorf("first finding = %+v, want goos.WriteFile in TestOctal", found[0])
	}
	if last := found[len(found)-1]; last.Function != "TestOpened" || last.Call != "goos.OpenFile" {
		t.Errorf("last finding = %+v, want goos.OpenFile in TestOpened", last)
	}
}

// Mutation witness: a scanner that reads only _test.go files misses the helper
// packages, such as internal/perf/proctest, that write executables for tests
// from an ordinary source file.
func TestScanExecutableWritesReadsHelperSourcesOutsideTestFiles(t *testing.T) {
	source := `package helper

import (
	"os"
	"testing"
)

func FakeTool(t testing.TB, path string) {
	if err := os.WriteFile(path, nil, 0o700); err != nil { // flagged
		t.Fatal(err)
	}
}
`
	found, err := scanExecutableWrites(token.NewFileSet(), "internal/sample/helper/helper.go", []byte(source))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 1 || found[0].Function != "FakeTool" || found[0].Line != linesMarked(source, "// flagged")[0] {
		t.Fatalf("found = %v, want the os.WriteFile in FakeTool", found)
	}
}

// linesMarked returns the 1-based numbers of the lines of source containing
// marker, so an expected finding is written beside the line it belongs to.
func linesMarked(source, marker string) []int {
	var lines []int
	for index, line := range strings.Split(source, "\n") {
		if strings.Contains(line, marker) {
			lines = append(lines, index+1)
		}
	}
	return lines
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

const data = 0o644

func TestData(t *testing.T, mode os.FileMode) {
	const private os.FileMode = 0o600
	_ = os.WriteFile("a", nil, 0o644)
	_ = os.WriteFile("b", nil, 0600)
	_ = os.WriteFile("c", nil, mode)
	_ = os.WriteFile("d", nil, data)
	_ = os.WriteFile("e", nil, private)
	_ = os.WriteFile("f", nil, 0o755&^0o111)
	_ = os.WriteFile("g", nil, undefined)
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
// mode is a constant expression with an execute bit set. The expression may be
// an integer literal, os.ModePerm or fs.ModePerm, a constant declared in the
// same file, a conversion such as fs.FileMode(...), or any of these combined
// with |, &, &^, ^ or +. Only source importing testing is considered, whether
// or not it is a _test.go file, because WriteExecutable takes a *testing.T and
// production code cannot use it.
//
// Three spellings are not followed and remain a matter of review: a mode held
// in a variable (which is what lets WriteFile itself and testrepo.CopyTree pass
// one through), a constant declared in another file or package, and a file
// created without an execute bit and then made executable with Chmod.
func scanExecutableWrites(fileSet *token.FileSet, name string, source []byte) ([]executableWrite, error) {
	file, err := parser.ParseFile(fileSet, name, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	osNames := importedAs(file, "os")
	if len(osNames) == 0 || len(importedAs(file, "testing")) == 0 {
		return nil, nil
	}
	modes := modeEvaluator{
		permNames: importedAs(file, "io/fs"),
		constants: fileConstants(file),
	}
	for osName := range osNames {
		modes.permNames[osName] = true
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
			if !modes.executable(call.Args[2]) {
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

// fileConstants maps each constant name declared anywhere in file, at package
// level or inside a function, to the expressions it is given. A name declared
// in more than one scope keeps every value, and the guard treats it as
// executable if any of them is, which can only err toward a finding.
func fileConstants(file *ast.File) map[string][]ast.Expr {
	constants := make(map[string][]ast.Expr)
	ast.Inspect(file, func(node ast.Node) bool {
		declaration, ok := node.(*ast.GenDecl)
		if !ok || declaration.Tok != token.CONST {
			return true
		}
		for _, specification := range declaration.Specs {
			value := specification.(*ast.ValueSpec)
			// A spec without values repeats the previous one's expression,
			// usually with iota; no file mode is spelled that way.
			if len(value.Values) != len(value.Names) {
				continue
			}
			for index, constant := range value.Names {
				constants[constant.Name] = append(constants[constant.Name], value.Values[index])
			}
		}
		return true
	})
	return constants
}

// modeEvaluator folds the constant expressions a file can spell a mode with.
type modeEvaluator struct {
	// permNames are the local names of os and io/fs, the packages whose
	// ModePerm is 0o777.
	permNames map[string]bool
	// constants are the file's own constant declarations.
	constants map[string][]ast.Expr
}

// executable reports whether a mode argument is a constant expression with
// any execute bit set.
func (m modeEvaluator) executable(expression ast.Expr) bool {
	for _, value := range m.values(expression, 0) {
		if value&0o111 != 0 {
			return true
		}
	}
	return false
}

// values returns every value expression can take, or none when it is not a
// constant the evaluator can fold. A name declared in several scopes yields
// one value per declaration, so a binary expression yields every combination.
func (m modeEvaluator) values(expression ast.Expr, depth int) []uint64 {
	// A constant defined in terms of itself does not compile, but the guard
	// may read source that does not, so it bounds the lookups.
	if depth > 16 {
		return nil
	}
	switch mode := expression.(type) {
	case *ast.ParenExpr:
		return m.values(mode.X, depth+1)
	case *ast.CallExpr:
		// A conversion such as fs.FileMode(0o755) or os.FileMode(executable).
		if len(mode.Args) != 1 {
			return nil
		}
		return m.values(mode.Args[0], depth+1)
	case *ast.SelectorExpr:
		receiver, ok := mode.X.(*ast.Ident)
		if ok && m.permNames[receiver.Name] && mode.Sel.Name == "ModePerm" {
			return []uint64{0o777}
		}
		return nil
	case *ast.Ident:
		var values []uint64
		for _, definition := range m.constants[mode.Name] {
			values = append(values, m.values(definition, depth+1)...)
		}
		return values
	case *ast.BinaryExpr:
		var values []uint64
		for _, left := range m.values(mode.X, depth+1) {
			for _, right := range m.values(mode.Y, depth+1) {
				if value, ok := foldMode(mode.Op, left, right); ok {
					values = append(values, value)
				}
			}
		}
		return values
	case *ast.BasicLit:
		if mode.Kind != token.INT {
			return nil
		}
		// Base 0 reads the literal as Go does: 0755 and 0o755 are both octal.
		value, err := strconv.ParseUint(mode.Value, 0, 32)
		if err != nil {
			return nil
		}
		return []uint64{value}
	}
	return nil
}

// foldMode applies the bitwise and additive operators a mode is built with.
func foldMode(operator token.Token, left, right uint64) (uint64, bool) {
	switch operator {
	case token.OR:
		return left | right, true
	case token.AND:
		return left & right, true
	case token.AND_NOT:
		return left &^ right, true
	case token.XOR:
		return left ^ right, true
	case token.ADD:
		return left + right, true
	}
	return 0, false
}
