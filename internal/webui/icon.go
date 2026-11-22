package webui

import (
	"embed"
	"encoding/json"
	"net/http"
)

// The board's icon and name, for a shortcut saved to a phone's home screen:
// the Workbench app's own artwork at the three sizes phones ask for, and a web
// manifest naming the board after its project.
//
// The manifest's display is "browser", and that is the whole of what it says
// about how a shortcut opens: a shortcut that opened as an app window of its
// own would keep a sign-in of its own, apart from the phone's browser, which
// is the reason there is no wrapper app around the board either. Chrome on
// Android opens a shortcut made with "Create shortcut" in a tab, signed in the
// way Chrome is; its "Install app" is offered whatever a manifest says. iOS and iPadOS 26 do not consult it: a site added to the Home Screen
// opens as a web app with its own cookies unless the reader switches "Open as
// Web App" off when adding it, which the documentation tells them to do.
//
// The icons are files of this package rather than fragments of the page, so
// they sit outside assets/: everything under assets/ is joined into the one
// document the board serves.

//go:embed icons/*.png
var icons embed.FS

// iconFiles are the icons the board serves, by the address each is served at.
// The 180-pixel one is the Apple touch icon, drawn square and opaque because
// iOS draws its own rounded corners and fills any transparency with black; the
// other two are the manifest's, the artwork as Workbench ships it.
var iconFiles = map[string]string{
	"/icons/apple-touch-icon.png": "icons/apple-touch-icon.png",
	"/icons/icon-192.png":         "icons/icon-192.png",
	"/icons/icon-512.png":         "icons/icon-512.png",
}

// manifestPath is where the web manifest is served.
const manifestPath = "/manifest.webmanifest"

// iconCacheControl lets a browser keep an icon for a day without asking
// again. An icon is the same bytes for every board this build serves and says
// nothing about the project, so a day of it costs nothing but a stale icon
// after an upgrade that changed it. A hosted board says private too, as every
// answer it gives does: nothing behind the sign-in is for a shared cache.
const iconCacheControl = "max-age=86400"

// webManifest is the manifest's document. Members a browser does not need for
// a shortcut that opens in the browser — colors, orientation, an app ID — are
// left out rather than guessed.
type webManifest struct {
	Name      string         `json:"name"`
	ShortName string         `json:"short_name"`
	StartURL  string         `json:"start_url"`
	Scope     string         `json:"scope"`
	Display   string         `json:"display"`
	Icons     []manifestIcon `json:"icons"`
}

type manifestIcon struct {
	Source string `json:"src"`
	Sizes  string `json:"sizes"`
	Type   string `json:"type"`
}

// serveIcon answers one of iconFiles.
func (handler *handler) serveIcon(writer http.ResponseWriter, request *http.Request) {
	name, known := iconFiles[request.URL.Path]
	if !known {
		http.NotFound(writer, request)
		return
	}
	content, err := icons.ReadFile(name)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	cacheControl := iconCacheControl
	if handler.Hosted {
		cacheControl = "private, " + iconCacheControl
	}
	writer.Header().Set("Cache-Control", cacheControl)
	writer.Header().Set("Content-Type", "image/png")
	_, _ = writer.Write(content)
}

// serveManifest answers the web manifest, named for this board's project the
// way the page's title is. It is read again on every request rather than
// cached, because the name is the project's and a project can be renamed:
// no-cache lets a browser keep it and asks it to check first, and a hosted
// board keeps the private, no-store it gives every answer that carries
// project data.
func (handler *handler) serveManifest(writer http.ResponseWriter, request *http.Request) {
	vocabulary, _, err := handler.vocabulary(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	name := withoutDirectionControls(projectName(vocabulary.Display))
	manifest := webManifest{
		Name: name, ShortName: name, StartURL: "/", Scope: "/", Display: "browser",
		Icons: []manifestIcon{
			{Source: "/icons/icon-192.png", Sizes: "192x192", Type: "image/png"},
			{Source: "/icons/icon-512.png", Sizes: "512x512", Type: "image/png"},
		},
	}
	if !handler.Hosted {
		writer.Header().Set("Cache-Control", "no-cache")
	}
	writer.Header().Set("Content-Type", "application/manifest+json")
	_ = json.NewEncoder(writer).Encode(manifest)
}
