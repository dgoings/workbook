package projection

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
)

// No test in this package must stay serial: there is no t.Setenv, os.Setenv,
// Chdir, PATH shim, port or cross-test global for one to protect.
//
// TestMain isolates the user-global and system git configuration that every
// one of this package's 27 test repositories would otherwise read. A
// developer's real ~/.gitconfig carries settings these tests do not intend to
// depend on but that would be live in every repository they build, and
// background auto-gc or maintenance spawned by any of those repositories can
// outlive its test and race t.TempDir's cleanup. A per-test t.Setenv would
// normally cover this, but it panics once a test calls t.Parallel, so the
// environment is replaced once here, before m.Run.
//
// TestMain also mints the package's one fixture template here, after the
// environment above and before m.Run, for the same reason gitstore's
// writeTemplate does: the mint runs under the configuration every copy of it
// will be read under, and a mint that cannot succeed says so once instead of
// failing every test that copies it.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "workbook-projection-home")
	if err != nil {
		panic("create isolated home: " + err.Error())
	}

	gitconfig := filepath.Join(home, "gitconfig")
	contents := "[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n[receive]\n\tautogc = false\n"
	if err := os.WriteFile(gitconfig, []byte(contents), 0o600); err != nil {
		panic("write isolated gitconfig: " + err.Error())
	}

	os.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	templateDir = filepath.Join(home, "template")
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		panic("create template directory: " + err.Error())
	}
	config, err := mintWorkbookTemplate(templateDir)
	if err != nil {
		panic("mint workbook template: " + err.Error())
	}
	templateConfig = config

	code := m.Run()

	os.RemoveAll(home)
	os.Exit(code)
}

// templateDir holds the one fixture this package mints once and every test
// that wants an initialized project copies with testrepo.CopyTree:
// initializeWorkbook, which 27 tests call. It lives under the isolated home
// TestMain creates, so that removing the home removes it too, and so that it
// is minted against the same user-global git configuration every test runs
// against.
//
// templateConfig is the core.ProjectConfig that mint produced, returned by
// initializeWorkbook instead of a second call to Repository.Init: the project
// ID and identity commit are fixed, so a template minted once is
// byte-identical to what every call site used to build for itself.
var (
	templateDir    string
	templateConfig core.ProjectConfig
)

// mintWorkbookTemplate builds the initialized project initializeWorkbook
// copies, in dir, which must already exist.
//
// TestMain calls this once, before m.Run, so the mint happens in a quiet
// process under the environment TestMain has just arranged. It takes no
// *testing.T so that this one definition serves both the mint and, if a test
// ever needs to compare a copy against a fresh mint, that test.
func mintWorkbookTemplate(dir string) (core.ProjectConfig, error) {
	if err := testrepo.InitAt(dir); err != nil {
		return core.ProjectConfig{}, err
	}
	repository, err := gitstore.Open(context.Background(), dir)
	if err != nil {
		return core.ProjectConfig{}, err
	}
	config, _, err := repository.Init(context.Background(), "WB", core.IDSourceFunc(func() (string, error) {
		return testConfig().ProjectID, nil
	}))
	return config, err
}
