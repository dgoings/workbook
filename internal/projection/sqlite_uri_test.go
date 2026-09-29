package projection

import (
	"net/url"
	"runtime"
	"testing"
)

// A path with no volume is left exactly as it was, on every platform, so a
// Unix cache path, absolute or relative, opens the way it always has.
func TestSQLiteURIPathLeavesAPathWithoutAVolumeAlone(t *testing.T) {
	for _, path := range []string{"/home/wb/repo/.git/workbook/cache.sqlite", "relative/cache.sqlite"} {
		if got := sqliteURIPath(path); got != path {
			t.Errorf("sqliteURIPath(%q) = %q, want it unchanged", path, got)
		}
	}
}

// Left as it was, a Windows path made file://C:%5CUsers%5C…, and SQLite
// refused it with "invalid uri authority". SQLite's form is file:///C:/….
func TestSQLiteURIPathSpellsAWindowsDriveAsSQLiteReadsIt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive letters are a volume only on Windows")
	}
	dsn := &url.URL{Scheme: "file", Path: sqliteURIPath(`C:\Users\wb\repo\.git\workbook\cache.sqlite`)}
	if got, want := dsn.String(), "file:///C:/Users/wb/repo/.git/workbook/cache.sqlite"; got != want {
		t.Fatalf("DSN = %q, want %q", got, want)
	}
}
