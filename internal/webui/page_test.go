package webui

import (
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// The fragment list and the files on disk name the same set. Package
// initialization already refuses an embedded file that is not listed, but
// go:embed leaves out a name that begins with "." or "_", so a fragment named
// that way would be missing from the embedded tree and invisible to that check.
// Reading the directory itself is what catches it. .DS_Store is the one name
// passed over, because Finder writes it and go:embed never embeds it.
func TestPageFragmentsMatchAssetsDirectory(t *testing.T) {
	t.Parallel()
	var onDisk []string
	err := fs.WalkDir(os.DirFS("assets"), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && path.Base(name) != ".DS_Store" {
			onDisk = append(onDisk, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := slices.Clone(pageFragments)
	slices.Sort(listed)
	for _, name := range onDisk {
		if _, found := slices.BinarySearch(listed, name); !found {
			t.Errorf("assets/%s exists but is not in pageFragments", name)
		}
	}
	for _, name := range listed {
		if _, found := slices.BinarySearch(onDisk, name); !found {
			t.Errorf("pageFragments names %s, which is not under assets/", name)
		}
	}
}

func TestComposePageJoinsFragmentsInListedOrder(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"assets/b.html":     {Data: []byte("two\n")},
		"assets/a/one.css":  {Data: []byte("one")},
		"assets/c/three.js": {Data: []byte("three\n")},
	}
	got, err := composePage(files, []string{"a/one.css", "b.html", "c/three.js"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "onetwo\nthree\n"; got != want {
		t.Fatalf("composed = %q, want %q", got, want)
	}
}

func TestComposePageRefusesAListOutOfStepWithTheFiles(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"assets/a.html": {Data: []byte("a")},
		"assets/b.js":   {Data: []byte("b")},
	}
	for _, test := range []struct {
		name      string
		fragments []string
		want      string
	}{
		{"unlisted file", []string{"a.html"}, "b.js are under assets/ but not in pageFragments"},
		{"missing file", []string{"a.html", "b.js", "c.css"}, "c.css is listed but cannot be read"},
		{"listed twice", []string{"a.html", "b.js", "a.html"}, "a.html is listed twice"},
	} {
		_, err := composePage(files, test.fragments)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: err = %v, want one containing %q", test.name, err, test.want)
		}
	}
}
