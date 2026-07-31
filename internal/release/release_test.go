package release_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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

func TestRenderFormulaUsesImmutablePlatformArchives(t *testing.T) {
	formula, err := release.RenderFormula("0.1.0", "dgoings/workbook", fixtureArchives())
	if err != nil {
		t.Fatalf("render formula: %v", err)
	}

	for _, want := range []string{
		"# typed: strict\n# frozen_string_literal: true",
		"class Workbook < Formula",
		"on_macos do",
		"https://github.com/dgoings/workbook/releases/download/v0.1.0/workbook_0.1.0_darwin_arm64.tar.gz",
		"sha256 \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"",
		"https://github.com/dgoings/workbook/releases/download/v0.1.0/workbook_0.1.0_darwin_amd64.tar.gz",
		"sha256 \"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"",
		"on_linux do",
		"https://github.com/dgoings/workbook/releases/download/v0.1.0/workbook_0.1.0_linux_arm64.tar.gz",
		"sha256 \"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\"",
		"https://github.com/dgoings/workbook/releases/download/v0.1.0/workbook_0.1.0_linux_amd64.tar.gz",
		"sha256 \"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\"",
		"bin.install \"workbook\"",
		"test do",
		"workbook version",
	} {
		if !strings.Contains(formula, want) {
			t.Errorf("formula missing %q:\n%s", want, formula)
		}
	}
	// Production mutation: leaving the macOS-only dependency in place makes
	// Homebrew refuse the formula on Linux even though Linux archives ship.
	if strings.Contains(formula, "depends_on :macos") {
		t.Errorf("formula still restricts installation to macOS:\n%s", formula)
	}
	if macos, linux := strings.Index(formula, "on_macos do"), strings.Index(formula, "on_linux do"); macos > linux {
		t.Errorf("formula platform blocks are out of order:\n%s", formula)
	}
}

func TestRenderFormulaComparesTheTestVersionAsAString(t *testing.T) {
	formula, err := release.RenderFormula("0.1.0", "dgoings/workbook", fixtureArchives())
	if err != nil {
		t.Fatalf("render formula: %v", err)
	}

	// Production mutation: passing the bare `version` to assert_match hands
	// Homebrew a Version object, which does not respond to `=~`, so
	// `brew test` aborts with a Minitest assertion error instead of running
	// the released binary.
	if !strings.Contains(formula, `assert_match version.to_s, shell_output("#{bin}/workbook version")`) {
		t.Errorf("formula does not compare the test version as a string:\n%s", formula)
	}
}

func TestRenderFormulaRejectsMissingChecksums(t *testing.T) {
	// Production mutation: rendering with a blank checksum for any platform
	// publishes a formula that cannot install on it.
	for platform, blank := range map[string]func(*release.FormulaArchives){
		"darwin arm64": func(a *release.FormulaArchives) { a.DarwinARM64 = "" },
		"darwin amd64": func(a *release.FormulaArchives) { a.DarwinAMD64 = "" },
		"linux arm64":  func(a *release.FormulaArchives) { a.LinuxARM64 = "" },
		"linux amd64":  func(a *release.FormulaArchives) { a.LinuxAMD64 = "" },
	} {
		t.Run(platform, func(t *testing.T) {
			archives := fixtureArchives()
			blank(&archives)
			_, err := release.RenderFormula("0.1.0", "dgoings/workbook", archives)
			if err == nil {
				t.Fatalf("RenderFormula succeeded without a %s checksum", platform)
			}
			if !strings.Contains(err.Error(), platform) {
				t.Fatalf("RenderFormula error = %v, want the %s platform named", err, platform)
			}
		})
	}
}

func fixtureArchives() release.FormulaArchives {
	return release.FormulaArchives{
		DarwinARM64: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		DarwinAMD64: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		LinuxARM64:  "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		LinuxAMD64:  "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}
}

func TestRenderFormulaRejectsUnsafeVersions(t *testing.T) {
	for _, version := range []string{
		"",
		"0.1",
		"01.2.3",
		"1.02.3",
		"1.2.03",
		"1.2.3-alpha",
		"1.2.3+build",
		"1.2.3/../../formula",
		"1.2.3\"",
	} {
		t.Run(version, func(t *testing.T) {
			_, err := release.RenderFormula(version, "dgoings/workbook", fixtureArchives())
			if err == nil {
				t.Fatalf("RenderFormula(%q) succeeded, want safe SemVer rejection", version)
			}
		})
	}
}

func TestCompiledBinaryReportsLinkerInjectedVersion(t *testing.T) {
	root, _ := releasePaths(t)
	binary := filepath.Join(t.TempDir(), "workbook")
	build := exec.Command(
		"go", "build",
		"-buildvcs=false",
		"-trimpath",
		"-ldflags", "-X main.version=9.8.7 -X main.commit=abc123",
		"-o", binary,
		"./cmd/workbook",
	)
	build.Dir = root
	build.Env = environmentWith(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "gocache"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build linker-injected workbook: %v\n%s", err, output)
	}

	command := exec.Command(binary, "version", "--json")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("run linker-injected workbook: %v", err)
	}
	var result struct {
		Data release.Metadata `json:"data"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode linker-injected version output: %v; output = %q", err, output)
	}
	if result.Data.Version != "9.8.7" || result.Data.Commit != "abc123" {
		t.Fatalf("linker-injected metadata = %#v, want version 9.8.7 and commit abc123", result.Data)
	}
}

func TestReleaseScriptRejectsUnsafeVersionsBeforeCreatingArtifacts(t *testing.T) {
	root, script := releasePaths(t)
	for _, version := range []string{
		"v0.1.0",
		"0.1",
		"01.2.3",
		"1.2.3-alpha",
		"1.2.3/../../escape",
		"1.2.3;touch-pwned",
	} {
		t.Run(version, func(t *testing.T) {
			outputDirectory := filepath.Join(t.TempDir(), "dist")
			command := exec.Command(script, version, outputDirectory)
			command.Dir = root
			command.Env = environmentWith(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "gocache"))
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("release script accepted unsafe version %q; output = %q", version, output)
			}
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 2 {
				t.Fatalf("release script error = %v, want exit code 2; output = %q", err, output)
			}
			if entries, readErr := os.ReadDir(outputDirectory); readErr == nil && len(entries) != 0 {
				t.Fatalf("release script created files for unsafe version %q: %v", version, entries)
			} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("read output directory: %v", readErr)
			}
		})
	}
}

func TestReleaseScriptCreatesVerifiedPlatformArchives(t *testing.T) {
	// Production mutation: building only darwin archives leaves the Homebrew
	// formula pointing at Linux downloads that were never published.
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
		"workbook_0.1.0_linux_arm64.tar.gz",
		"workbook_0.1.0_linux_amd64.tar.gz",
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
		"workbook_0.1.0_linux_amd64.tar.gz",
		"workbook_0.1.0_linux_arm64.tar.gz",
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
	hostileToolDirectory := t.TempDir()
	for _, name := range []string{"gzip", "tar", "touch"} {
		path := filepath.Join(hostileToolDirectory, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 91\n"), 0o700); err != nil {
			t.Fatalf("write hostile %s: %v", name, err)
		}
	}
	environments := [][]string{
		environmentWith(os.Environ(),
			"GOCACHE="+filepath.Join(t.TempDir(), "gocache"),
			"TZ=America/Detroit",
			"SOURCE_DATE_EPOCH=1712345678",
		),
		environmentWith(os.Environ(),
			"GOCACHE="+filepath.Join(t.TempDir(), "gocache"),
			"TZ=Asia/Tokyo",
			"SOURCE_DATE_EPOCH=946684800",
			"PATH="+hostileToolDirectory+string(os.PathListSeparator)+os.Getenv("PATH"),
		),
	}
	for index, outputDirectory := range []string{firstOutputDirectory, secondOutputDirectory} {
		command := exec.Command(script, "0.1.0", outputDirectory)
		command.Dir = root
		command.Env = environments[index]
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("release script: %v\n%s", err, output)
		}
	}

	for _, name := range []string{
		"workbook_0.1.0_darwin_amd64.tar.gz",
		"workbook_0.1.0_darwin_arm64.tar.gz",
		"workbook_0.1.0_linux_amd64.tar.gz",
		"workbook_0.1.0_linux_arm64.tar.gz",
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
	if header.ModTime.Unix() != 0 {
		t.Fatalf("archive member modification time = %v, want Unix epoch", header.ModTime)
	}
	if _, err := io.Copy(io.Discard, tarReader); err != nil {
		t.Fatalf("read archive payload %s: %v", archivePath, err)
	}
	if extra, err := tarReader.Next(); err != io.EOF {
		t.Fatalf("archive extra member = %#v, %v; want EOF", extra, err)
	}
}

func environmentWith(base []string, values ...string) []string {
	replacements := make(map[string]string, len(values))
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		replacements[key] = value
	}
	environment := make([]string, 0, len(base)+len(values))
	for _, value := range base {
		key, _, _ := strings.Cut(value, "=")
		if _, replaced := replacements[key]; !replaced {
			environment = append(environment, value)
		}
	}
	for _, value := range values {
		environment = append(environment, value)
	}
	return environment
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

func TestRenderFormulaDirectsUpgradersToRerunSetup(t *testing.T) {
	// Production mutation: shipping a formula without caveats would leave a
	// `brew upgrade` silent about the per-project refresh Homebrew cannot do.
	formula, err := release.RenderFormula("0.2.0", "dgoings/workbook", fixtureArchives())
	if err != nil {
		t.Fatalf("render formula: %v", err)
	}

	for _, want := range []string{"def caveats", "workbook setup", "workbook docs status"} {
		if !strings.Contains(formula, want) {
			t.Errorf("formula missing %q:\n%s", want, formula)
		}
	}
	install := strings.Index(formula, "bin.install")
	caveats := strings.Index(formula, "def caveats")
	test := strings.Index(formula, "test do")
	if !(install < caveats && caveats < test) {
		t.Fatalf("caveats are not between install and test:\n%s", formula)
	}
}
