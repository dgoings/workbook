package gitstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// Git for Windows installs with core.autocrlf=true, so the tracked
// .workbook/config.json arrives in a Windows checkout with CRLF line endings
// even though it was committed with LF. The file is otherwise byte-for-byte
// canonical, and refusing it made every Windows clone of a Workbook project
// unusable.
func TestReadConfigFileAcceptsACRLFCheckoutOfACanonicalConfig(t *testing.T) {
	want := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	}
	canonical, err := encodeConfig(want)
	if err != nil {
		t.Fatal(err)
	}
	crlf := bytes.ReplaceAll(canonical, []byte("\n"), []byte("\r\n"))
	if bytes.Equal(crlf, canonical) {
		t.Fatal("canonical configuration has no line endings to convert")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, crlf, 0o644); err != nil {
		t.Fatal(err)
	}

	got, found, err := readConfigFile(path, "Workbook configuration")
	if err != nil {
		t.Fatalf("readConfigFile(CRLF) error = %v", err)
	}
	if !found {
		t.Fatal("readConfigFile(CRLF) found = false")
	}
	if got.ProjectID != want.ProjectID || got.Key != want.Key {
		t.Fatalf("readConfigFile(CRLF) = %#v, want %#v", got, want)
	}
}

// Folding CRLF is the only leniency: a configuration that differs from the
// canonical encoding in any other way is still refused.
func TestReadConfigFileStillRefusesANonCanonicalConfig(t *testing.T) {
	canonical, err := encodeConfig(core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, append(canonical, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readConfigFile(path, "Workbook configuration"); core.CategoryOf(err) != core.CategoryCorruptData {
		t.Fatalf("readConfigFile(trailing newline) error = %v, want corrupt data", err)
	}
}
