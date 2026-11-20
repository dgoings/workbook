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

// The fragment list and the files on disk name the same set. Composing the page
// already refuses an embedded file that is not listed, but go:embed leaves out
// a name that begins with "." or "_", so a fragment named with a leading "_"
// would be missing from the embedded tree and invisible to that check. Reading
// the directory itself is what catches it. Names that begin with "." are passed
// over: Finder and editors write them (.DS_Store, a swap file beside an open
// fragment), go:embed never embeds them, and no fragment is named that way.
func TestPageFragmentsMatchAssetsDirectory(t *testing.T) {
	t.Parallel()
	var onDisk []string
	err := fs.WalkDir(os.DirFS("assets"), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !strings.HasPrefix(path.Base(name), ".") {
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

// The real list composes, and to the source the handler parses.
func TestPageSourceComposesTheEmbeddedFragments(t *testing.T) {
	t.Parallel()
	source, err := composePage(assets, pageFragments)
	if err != nil {
		t.Fatal(err)
	}
	if source != pageSource() {
		t.Fatal("pageSource() is not the composition of pageFragments")
	}
}

// The list keeps the shape of the document: the card template and the head,
// then the stylesheet, then the body, then the script, then the closing tags.
// Order within the stylesheet is the cascade and order within the script is
// its declaration order, neither of which a test can judge, but a fragment
// filed under the wrong element changes what html/template takes its text to
// be, and the script's first and last files hold the function's two ends.
func TestPageFragmentsKeepTheDocumentShape(t *testing.T) {
	t.Parallel()
	var styles, scripts []string
	for _, name := range pageFragments {
		switch {
		case strings.HasPrefix(name, "style/"):
			styles = append(styles, name)
		case strings.HasPrefix(name, "script/"):
			scripts = append(scripts, name)
		}
	}
	if len(styles) == 0 || len(scripts) == 0 {
		t.Fatalf("pageFragments has %d style and %d script fragments, want some of each", len(styles), len(scripts))
	}
	if scripts[0] != "script/boot.js" || scripts[len(scripts)-1] != "script/events.js" {
		t.Errorf("script fragments run from %s to %s, want script/boot.js to script/events.js", scripts[0], scripts[len(scripts)-1])
	}
	want := slices.Concat(
		[]string{"page/card.html", "page/head.html"},
		styles,
		[]string{"page/body.html"},
		scripts,
		[]string{"page/tail.html"},
	)
	if !slices.Equal(pageFragments, want) {
		t.Errorf("pageFragments = %v, want the card and head, the style fragments, the body, the script fragments, then the tail: %v", pageFragments, want)
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
