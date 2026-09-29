package testrepo

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// CopyTree copies a directory's contents onto target, which already exists.
// Files, directories and symlinks travel with their modes; nothing is
// hardlinked, so a test that writes to its copy cannot reach the template.
//
// This is how a package that mints one template repository for the whole suite
// hands a private repository to each test: a copy costs a few milliseconds
// where a mint costs several git processes. It lives here, beside InitAt, so
// that the two halves of that pattern — minting the template and copying it —
// have one definition for every package that uses them.
func CopyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(destination, info.Mode().Perm())
		case entry.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, destination)
		case entry.Type().IsRegular():
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(destination, contents, info.Mode().Perm())
		default:
			return fmt.Errorf("%s is neither a file, a directory nor a symlink", path)
		}
	})
}
