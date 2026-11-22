package webui

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"
)

// The board is one document and one html/template, kept as several files so
// that unrelated changes to it do not all edit the same file. The files are
// not templates of their own and are not parsed one at a time: they are joined,
// in exactly the order pageFragments names, into one source, and that source is
// parsed once. Joining text before the parse is the one composition that cannot
// change what html/template sees, so the page served is byte for byte the page
// a single file would have produced, and every escaping context — the card
// template, the stylesheet, the markup, the script — is the one its text sits
// in after the join, not one a fragment would have alone.
//
// That is also why most fragments are not valid on their own: page/head.html
// ends by opening the <style> element the style/ files fill, page/body.html
// opens by closing it and ends by opening the <script> element, and every
// script/ file is a run of statements inside the one function that script is.
// A cut falls only between two complete top-level statements, rules or
// elements, so each file holds whole pieces of one concern.
//
// Adding a fragment means creating the file under assets/ and naming it in
// pageFragments at the place its text belongs. A file that exists but is not
// named, or is named but does not exist, stops `workbook serve` when it builds
// its handler rather than serving a page with a piece missing, and the tests in
// page_test.go fail on the same mismatch before that.

//go:embed assets
var assets embed.FS

// pageFragments is the page, in order, as paths under assets/.
var pageFragments = []string{
	// The card template the board's columns render each task with.
	"page/card.html",
	// The document head, up to and including the opening <style> tag.
	"page/head.html",
	// The stylesheet, in cascade order: tokens first, then the page chrome,
	// the configuration route, the board, a task's page, rendered markdown,
	// the width and motion queries, and the dark scheme.
	"style/theme.css",
	"style/chrome.css",
	"style/config.css",
	"style/board.css",
	"style/task.css",
	"style/markdown.css",
	"style/layout.css",
	"style/dark.css",
	// The closing </style>, the theme and priority-ink blocks the server
	// composes, the body's markup, and the opening <script> tag.
	"page/body.html",
	// The script, one function from boot.js's opening line to events.js's
	// closing one.
	"script/boot.js",
	"script/preferences.js",
	"script/cards.js",
	"script/filters.js",
	"script/fields.js",
	"script/creation.js",
	"script/relationships.js",
	"script/markdown.js",
	"script/thread.js",
	"script/attachments.js",
	"script/history.js",
	"script/set-aside.js",
	"script/task-form.js",
	"script/mutations.js",
	"script/config-statuses.js",
	"script/config-priorities.js",
	"script/config-keys.js",
	"script/config-display.js",
	"script/failures.js",
	"script/toolbar.js",
	"script/route.js",
	"script/drag.js",
	"script/phone.js",
	"script/render.js",
	"script/events.js",
	// The closing </script>, </body> and </html>.
	"page/tail.html",
}

// pageSource is the board's template source, composed once, the first time a
// handler is built. Composing it at package initialization would charge every
// workbook command for a page only `workbook serve` draws, and would let one
// stray file under assets/ in a source build stop commands that never touch
// the board.
var pageSource = sync.OnceValue(func() string {
	source, err := composePage(assets, pageFragments)
	if err != nil {
		panic(err)
	}
	return source
})

// composePage joins fragments, read from the assets directory of fsys, in the
// order given. It refuses a list that names a file twice, names a file that is
// not there, or leaves out a file that is.
func composePage(fsys fs.FS, fragments []string) (string, error) {
	root, err := fs.Sub(fsys, "assets")
	if err != nil {
		return "", err
	}
	present, err := fragmentFiles(root)
	if err != nil {
		return "", err
	}
	named := make(map[string]bool, len(fragments))
	var source strings.Builder
	for _, name := range fragments {
		if named[name] {
			return "", fmt.Errorf("webui: page fragment %s is listed twice", name)
		}
		named[name] = true
		content, err := fs.ReadFile(root, name)
		if err != nil {
			return "", fmt.Errorf("webui: page fragment %s is listed but cannot be read: %w", name, err)
		}
		source.Write(content)
	}
	var unlisted []string
	for _, name := range present {
		if !named[name] {
			unlisted = append(unlisted, name)
		}
	}
	if len(unlisted) > 0 {
		return "", fmt.Errorf("webui: page fragments %s are under assets/ but not in pageFragments", strings.Join(unlisted, ", "))
	}
	return source.String(), nil
}

// fragmentFiles lists every regular file under root, sorted.
func fragmentFiles(root fs.FS) ([]string, error) {
	var files []string
	err := fs.WalkDir(root, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files = append(files, name)
		}
		return nil
	})
	slices.Sort(files)
	return files, err
}
