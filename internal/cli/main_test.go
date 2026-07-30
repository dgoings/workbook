package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates the user-global configuration Workbook reads from the
// environment. Without this, running the suite would read and write the
// developer's real ~/.config/workbook/config.json.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "workbook-cli-home")
	if err != nil {
		panic("create isolated home: " + err.Error())
	}
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	code := m.Run()

	os.RemoveAll(home)
	os.Exit(code)
}
