package release_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/cli"
	"github.com/dgoings/workbook/internal/release"
)

func TestVersionCommandReportsInjectedBuildMetadataAsJSON(t *testing.T) {
	previousVersion, previousCommit := release.Version, release.Commit
	t.Cleanup(func() {
		release.Version = previousVersion
		release.Commit = previousCommit
	})
	release.Version = "0.1.0"
	release.Commit = "abc123"

	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"version", "--json"}, t.TempDir(), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("workbook version --json exit code = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("workbook version --json stderr = %q, want empty", stderr.String())
	}

	var result struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
		Command string `json:"command"`
		Data    struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode version JSON: %v; output = %q", err, stdout.String())
	}
	if result.Format != "workbook.result" || result.Version != 1 || result.Command != "version" {
		t.Fatalf("version JSON envelope = %#v, want workbook version result envelope", result)
	}
	if result.Data.Version != "0.1.0" || result.Data.Commit != "abc123" {
		t.Fatalf("version JSON data = %#v, want version 0.1.0 and commit abc123", result.Data)
	}
}

func TestVersionCommandReportsDevelopmentDefaultsAsJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"version", "--json"}, t.TempDir(), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("workbook version --json exit code = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("workbook version --json stderr = %q, want empty", stderr.String())
	}

	var result struct {
		Data struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode version JSON: %v; output = %q", err, stdout.String())
	}
	if result.Data.Version != "dev" || result.Data.Commit != "unknown" {
		t.Fatalf("version JSON data = %#v, want development defaults", result.Data)
	}
}

func TestRenderFormulaUsesImmutableDarwinArchives(t *testing.T) {
	formula, err := release.RenderFormula(
		"0.1.0",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"dgoings/workbook",
	)
	if err != nil {
		t.Fatalf("render formula: %v", err)
	}

	for _, want := range []string{
		"class Workbook < Formula",
		"\n  depends_on :macos\n\n  on_macos do",
		"on_macos do",
		"on_arm do",
		"https://github.com/dgoings/workbook/releases/download/v0.1.0/workbook_0.1.0_darwin_arm64.tar.gz",
		"sha256 \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"",
		"on_intel do",
		"https://github.com/dgoings/workbook/releases/download/v0.1.0/workbook_0.1.0_darwin_amd64.tar.gz",
		"sha256 \"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"",
		"bin.install \"workbook\"",
		"test do",
		"workbook version",
	} {
		if !strings.Contains(formula, want) {
			t.Errorf("formula missing %q:\n%s", want, formula)
		}
	}
}

func TestRenderFormulaRejectsMissingChecksums(t *testing.T) {
	_, err := release.RenderFormula(
		"0.1.0",
		"",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"dgoings/workbook",
	)
	if err == nil {
		t.Fatal("RenderFormula succeeded without an arm64 checksum")
	}
}

func TestReleaseScriptCreatesVerifiedDarwinArchives(t *testing.T) {
	root, script := releasePaths(t)
	outputDirectory := t.TempDir()
	command := exec.Command(script, "0.1.0", outputDirectory)
	command.Dir = root
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "gocache"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release script: %v\n%s", err, output)
	}

	archiveNames := []string{
		"workbook_0.1.0_darwin_arm64.tar.gz",
		"workbook_0.1.0_darwin_amd64.tar.gz",
	}
	for _, archiveName := range archiveNames {
		assertExecutableOnlyArchive(t, filepath.Join(outputDirectory, archiveName))
	}

	entries, err := os.ReadDir(outputDirectory)
	if err != nil {
		t.Fatalf("read release output: %v", err)
	}
	gotNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotNames = append(gotNames, entry.Name())
	}
	wantNames := []string{
		"checksums.txt",
		"workbook_0.1.0_darwin_amd64.tar.gz",
		"workbook_0.1.0_darwin_arm64.tar.gz",
	}
	if strings.Join(gotNames, "\n") != strings.Join(wantNames, "\n") {
		t.Fatalf("release files = %q, want %q", gotNames, wantNames)
	}

	checksums, err := os.ReadFile(filepath.Join(outputDirectory, "checksums.txt"))
	if err != nil {
		t.Fatalf("read checksums: %v", err)
	}
	var wantChecksums strings.Builder
	for _, archiveName := range wantNames[1:] {
		contents, err := os.ReadFile(filepath.Join(outputDirectory, archiveName))
		if err != nil {
			t.Fatalf("read archive %s: %v", archiveName, err)
		}
		fmt.Fprintf(&wantChecksums, "%x  %s\n", sha256.Sum256(contents), archiveName)
	}
	if string(checksums) != wantChecksums.String() {
		t.Fatalf("checksums.txt = %q, want %q", checksums, wantChecksums.String())
	}
}

func TestReleaseScriptProducesDeterministicArtifacts(t *testing.T) {
	root, script := releasePaths(t)
	firstOutputDirectory := t.TempDir()
	secondOutputDirectory := t.TempDir()
	for _, outputDirectory := range []string{firstOutputDirectory, secondOutputDirectory} {
		command := exec.Command(script, "0.1.0", outputDirectory)
		command.Dir = root
		command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "gocache"))
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("release script: %v\n%s", err, output)
		}
	}

	for _, name := range []string{
		"workbook_0.1.0_darwin_amd64.tar.gz",
		"workbook_0.1.0_darwin_arm64.tar.gz",
		"checksums.txt",
	} {
		first, err := os.ReadFile(filepath.Join(firstOutputDirectory, name))
		if err != nil {
			t.Fatalf("read first %s: %v", name, err)
		}
		second, err := os.ReadFile(filepath.Join(secondOutputDirectory, name))
		if err != nil {
			t.Fatalf("read second %s: %v", name, err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("release artifact %s differs between identical builds", name)
		}
	}
}

func assertExecutableOnlyArchive(t *testing.T, archivePath string) {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive %s: %v", archivePath, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("open gzip archive %s: %v", archivePath, err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	tarReader := tar.NewReader(reader)
	header, err := tarReader.Next()
	if err != nil {
		t.Fatalf("read archive member %s: %v", archivePath, err)
	}
	if header.Name != "workbook" || header.Typeflag != tar.TypeReg || header.FileInfo().Mode().Perm()&0o111 == 0 {
		t.Fatalf("archive member = %#v, want executable regular workbook", header)
	}
	if header.Uid != 0 || header.Gid != 0 || header.Uname != "root" || header.Gname != "root" {
		t.Fatalf("archive owner = uid %d gid %d user %q group %q, want normalized root ownership", header.Uid, header.Gid, header.Uname, header.Gname)
	}
	if _, err := io.Copy(io.Discard, tarReader); err != nil {
		t.Fatalf("read archive payload %s: %v", archivePath, err)
	}
	if extra, err := tarReader.Next(); err != io.EOF {
		t.Fatalf("archive extra member = %#v, %v; want EOF", extra, err)
	}
}

func releasePaths(t *testing.T) (string, string) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine release test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	return root, filepath.Join(root, "scripts", "release.sh")
}
