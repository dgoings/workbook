package webui

import (
	"encoding/base64"
	"net/http"
	pathpkg "path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dgoings/workbook/internal/core"
)

// bodySecurityPolicy is the content policy a relic body opened by itself is
// served under. It is neither the page's policy nor an attachment's.
//
// A body is text a teammate wrote, and an HTML one is meant to be drawn as a
// page, its scripts included, so it cannot be refused script the way an
// attachment is. What it is refused instead is everything a script could
// reach:
//
//   - `sandbox allow-scripts` gives the document an opaque origin. It reads no
//     storage or cookie of the board's, and any request it did make would
//     carry `Origin: null`, which GuardSameOrigin refuses. Without
//     allow-popups, allow-forms, allow-same-origin or allow-top-navigation it
//     opens no window, submits no form and moves no other browsing context.
//   - `default-src 'none'` with no connect-src leaves it no fetch, XHR,
//     WebSocket, EventSource or beacon, and no frame; the inline script and
//     style it is drawn with are the only sources it has, with images, fonts
//     and media inline as data: URLs.
//   - `form-action`, `base-uri` and `frame-ancestors` do not fall back to
//     default-src, so each is closed by name: no form goes anywhere, no
//     <base> re-points a relative address, and no page frames the body.
const bodySecurityPolicy = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; media-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'none'"

// serveRelicBody answers the current body of one part by itself, for a tab of
// its own: an HTML body as a page, other text as text, and anything that is not
// UTF-8 as a file to save.
func (handler *handler) serveRelicBody(writer http.ResponseWriter, request *http.Request) {
	if !handler.relicsReadable() {
		http.NotFound(writer, request)
		return
	}
	_, request, err := handler.vocabulary(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	view, err := handler.RelicPart(request.Context(), request.PathValue("id"), request.PathValue("part"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	body := view.Part.Body
	var content []byte
	isText := false
	switch {
	case body != nil && body.Text != nil:
		content, isText = []byte(*body.Text), true
	case body != nil && body.Base64 != nil:
		content, err = base64.StdEncoding.DecodeString(*body.Base64)
		if err != nil {
			handler.writeError(writer, core.Errorf(core.CategoryCorruptData,
				"the body of part %s of relic %s cannot be decoded: %v", view.Part.ID, view.Relic, err))
			return
		}
	default:
		handler.writeError(writer, core.Errorf(core.CategoryNotFound,
			"part %s of relic %s has no body", view.Part.ID, view.Relic))
		return
	}
	media, disposition := relicBodyDelivery(body.Format, content, isText)
	if disposition != "inline" {
		disposition = contentDisposition(disposition, view.Part.ID+"."+relicBodyExtension(body.Format))
	}
	header := writer.Header()
	header.Set("Content-Type", media)
	header.Set("Content-Disposition", disposition)
	// Each stated here rather than inherited from writeSecurityHeaders, which
	// has already set the page's policy and nosniff: this answer is somebody
	// else's document, and the rules it is held to are the ones this route
	// relies on.
	header.Set("Content-Security-Policy", bodySecurityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("ETag", strconv.Quote(body.Version))
	header.Set("Content-Length", strconv.Itoa(len(content)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

// relicBodyDelivery decides the media type and the disposition together, so no
// edit can move one without the other. Only UTF-8 text is drawn: an html or
// htm label as a page, under bodySecurityPolicy, and any other label as plain
// text. Everything else is bytes to save.
func relicBodyDelivery(format string, content []byte, isText bool) (mediaType, disposition string) {
	if !isText || !utf8.Valid(content) {
		return core.DefaultAttachmentMedia, "attachment"
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "html", "htm":
		return "text/html; charset=utf-8", "inline"
	default:
		return "text/plain; charset=utf-8", "inline"
	}
}

// relicBodyExtension is the file name extension a saved body is given: its
// format label, which is somebody's text, with any path separator in it made an
// underscore, or bin for a body with no label. contentDisposition cleans the
// rest of the name.
func relicBodyExtension(format string) string {
	if format == "" {
		return "bin"
	}
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' {
			return '_'
		}
		return r
	}, format)
}

// relicBodyPathShaped reports the address of one part's body, each name in it
// naming something.
func relicBodyPathShaped(path string) bool {
	segments := strings.Split(path, "/")
	return relicBodyShape(segments) && relicBodySegment(segments[2]) && relicBodySegment(segments[4])
}

// relicBodyShape reports the segments of /relics/<id>/parts/<part>/body,
// whatever the two names are.
func relicBodyShape(segments []string) bool {
	return len(segments) == 6 && segments[0] == "" && segments[1] == "relics" &&
		segments[3] == "parts" && segments[5] == "body"
}

func relicBodySegment(segment string) bool {
	return !unnamedSegment(segment) && !strings.Contains(segment, "#")
}

// malformedRelicBodyPath reports a request for a body whose path holds a
// segment naming nothing, or a "#", which no ID holds. It is answered here
// because the mux would otherwise redirect it to the cleaned path, which can be
// another part's body. A path counts as a body's when it ends in "body" as sent
// or has the body route's shape once cleaned.
func malformedRelicBodyPath(path string) bool {
	if !strings.HasPrefix(path, "/relics/") {
		return false
	}
	segments := strings.Split(path, "/")
	if !strings.Contains(path, "#") && !slices.ContainsFunc(segments[2:], unnamedSegment) {
		return false
	}
	return segments[len(segments)-1] == "body" || relicBodyShape(strings.Split(pathpkg.Clean(path), "/"))
}
