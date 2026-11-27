package webui

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// The relic reads a board is built with. Every one of them only reads, and
// every route over them is a GET.
type (
	// RelicLister returns every relic, deleted and retired ones included, in
	// the order the list shows them.
	RelicLister func(context.Context) ([]core.RelicSummary, error)
	// RelicShower reads one relic by ID or prefix, with its removed parts when
	// removed is set. The board sends none of its body text.
	RelicShower func(ctx context.Context, id string, removed bool) (core.RelicDocument, error)
	// RelicPartReader reads one part of a relic with its body.
	RelicPartReader func(ctx context.Context, relicID, partID string) (core.RelicPartView, error)
	// RelicHistoryLister reads the history of a relic, or of one part
	// addressed as <relic>#<part>.
	RelicHistoryLister func(context.Context, string, core.RelicHistoryOptions) (core.RelicHistory, error)
	// RelicDiffer compares versions of a relic, or of one part addressed as
	// <relic>#<part>.
	RelicDiffer func(context.Context, string, []string, core.RelicDiffOptions) (core.RelicDiff, error)
	// TaskRelicReader reads the links relics have to one task.
	TaskRelicReader func(ctx context.Context, taskID string) ([]core.TaskRelicLink, []core.Warning, error)
)

// The window of changes a history answers with when the request names none, and
// a diff's lines of context: the default is the command's.
const (
	relicHistoryLimit     = 100
	relicDiffContext      = 3
	relicDiffContextLimit = 1000
)

// RelicsDocument lists the relics the filters keep. Types is every type among
// the relics the retired and deleted filters keep, whatever the type filter.
type RelicsDocument struct {
	Format  string              `json:"format"`
	Version int                 `json:"version"`
	Relics  []core.RelicSummary `json:"relics"`
	Types   []string            `json:"types"`
}

// RelicViewDocument is one relic without the text of any body.
type RelicViewDocument struct {
	Format   string             `json:"format"`
	Version  int                `json:"version"`
	Relic    core.RelicDocument `json:"relic"`
	Warnings []core.Warning     `json:"warnings,omitempty"`
}

// RelicPartDocument is one part of a relic with its body.
type RelicPartDocument struct {
	Format  string                 `json:"format"`
	Version int                    `json:"version"`
	Relic   string                 `json:"relic"`
	Head    string                 `json:"head"`
	Part    core.RelicDocumentPart `json:"part"`
}

// RelicHistoryDocument is the history of a relic or of one part.
type RelicHistoryDocument struct {
	Format  string            `json:"format"`
	Version int               `json:"version"`
	History core.RelicHistory `json:"history"`
}

// RelicDiffDocument is the comparison of two points of a relic's history.
type RelicDiffDocument struct {
	Format  string         `json:"format"`
	Version int            `json:"version"`
	Diff    core.RelicDiff `json:"diff"`
}

// TaskRelicsDocument lists the links relics have to one task.
type TaskRelicsDocument struct {
	Format   string               `json:"format"`
	Version  int                  `json:"version"`
	Task     string               `json:"task"`
	Relics   []core.TaskRelicLink `json:"relics"`
	Warnings []core.Warning       `json:"warnings,omitempty"`
}

// relicsReadable is whether this board can show relics at all: it takes the
// list, one relic and one part together, since a view of any of them leads to
// the others.
func (handler *handler) relicsReadable() bool {
	return handler.ListRelics != nil && handler.ShowRelic != nil && handler.RelicPart != nil
}

func (handler *handler) serveRelics(writer http.ResponseWriter, request *http.Request) {
	if !handler.relicsReadable() {
		http.NotFound(writer, request)
		return
	}
	query := request.URL.Query()
	retired, err := relicStateFilter("retired", query.Get("retired"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	deleted, err := relicStateFilter("deleted", query.Get("deleted"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	_, request, err = handler.vocabulary(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	summaries, err := handler.ListRelics(request.Context())
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	relicType := query.Get("type")
	relics, types := []core.RelicSummary{}, []string{}
	for _, summary := range summaries {
		if !retired(summary.Retired) || !deleted(summary.Deleted) {
			continue
		}
		types = append(types, summary.Type)
		if relicType == "" || summary.Type == relicType {
			relics = append(relics, summary)
		}
	}
	slices.Sort(types)
	writeJSON(writer, http.StatusOK, RelicsDocument{
		Format: "workbook.relics", Version: 1, Relics: relics, Types: slices.Compact(types),
	})
}

// relicStateFilter reads the retired or deleted filter, and reports whether it
// keeps a relic that is or is not in that state. By default it keeps only the
// relics that are not.
func relicStateFilter(name, value string) (func(bool) bool, error) {
	switch value {
	case "":
		return func(is bool) bool { return !is }, nil
	case "include":
		return func(bool) bool { return true }, nil
	case "only":
		return func(is bool) bool { return is }, nil
	default:
		return nil, core.Errorf(core.CategoryValidation, "%s must be \"include\" or \"only\", not %q", name, value)
	}
}

func (handler *handler) serveRelic(writer http.ResponseWriter, request *http.Request) {
	if !handler.relicsReadable() {
		http.NotFound(writer, request)
		return
	}
	removed := request.URL.Query().Get("removed")
	if removed != "" && removed != "include" {
		handler.writeError(writer, core.Errorf(core.CategoryValidation, "removed must be \"include\", not %q", removed))
		return
	}
	_, request, err := handler.vocabulary(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	document, err := handler.ShowRelic(request.Context(), request.PathValue("id"), removed == "include")
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, RelicViewDocument{
		Format: "workbook.relic-view", Version: 1,
		Relic:    withoutBodyText(document),
		Warnings: handler.disclosed(document.Warnings),
	})
}

// withoutBodyText is the document with no body's text, whatever its reader
// filled in: a relic may hold megabytes of bodies, and the part route sends
// each one when it is shown. The reader's own document is left as it was.
func withoutBodyText(document core.RelicDocument) core.RelicDocument {
	document.Parts = slices.Clone(document.Parts)
	for index, part := range document.Parts {
		if part.Body == nil {
			continue
		}
		body := *part.Body
		body.Text, body.Base64 = nil, nil
		document.Parts[index].Body = &body
	}
	return document
}

func (handler *handler) serveRelicPart(writer http.ResponseWriter, request *http.Request) {
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
	writeJSON(writer, http.StatusOK, RelicPartDocument{
		Format: "workbook.relic-part", Version: 1, Relic: view.Relic, Head: view.Head, Part: view.Part,
	})
}

// relicAddress is the relic a history or diff route names, or its one part when
// the request asks for one.
func relicAddress(request *http.Request) string {
	address := request.PathValue("id")
	if part := request.URL.Query().Get("part"); part != "" {
		address += "#" + part
	}
	return address
}

func (handler *handler) serveRelicHistory(writer http.ResponseWriter, request *http.Request) {
	if !handler.relicsReadable() || handler.RelicHistory == nil {
		http.NotFound(writer, request)
		return
	}
	options, err := relicHistoryOptions(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	_, request, err = handler.vocabulary(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	history, err := handler.RelicHistory(request.Context(), relicAddress(request), options)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	history.Truncated = handler.disclosedTruncation(history.Truncated)
	writeJSON(writer, http.StatusOK, RelicHistoryDocument{Format: "workbook.relic-history", Version: 1, History: history})
}

func relicHistoryOptions(request *http.Request) (core.RelicHistoryOptions, error) {
	query := request.URL.Query()
	options := core.RelicHistoryOptions{Limit: relicHistoryLimit}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 {
			return options, core.Errorf(core.CategoryValidation, "limit must be a whole number of 1 or more, not %q", value)
		}
		options.Limit = limit
	}
	switch value := query.Get("all"); value {
	case "":
	case "1":
		options.All = true
	default:
		return options, core.Errorf(core.CategoryValidation, "all must be \"1\", not %q", value)
	}
	return options, nil
}

func (handler *handler) serveRelicDiff(writer http.ResponseWriter, request *http.Request) {
	if !handler.relicsReadable() || handler.RelicDiff == nil {
		http.NotFound(writer, request)
		return
	}
	versions, options, err := relicDiffRequest(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	_, request, err = handler.vocabulary(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	diff, err := handler.RelicDiff(request.Context(), relicAddress(request), versions, options)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, RelicDiffDocument{Format: "workbook.relic-diff", Version: 1, Diff: diff})
}

// relicDiffRequest reads the versions to compare, in order, and the lines of
// context. The context is bounded here, so no request reaches the comparison
// with a number large enough to overflow its arithmetic.
func relicDiffRequest(request *http.Request) ([]string, core.RelicDiffOptions, error) {
	query := request.URL.Query()
	options := core.RelicDiffOptions{Context: relicDiffContext}
	var versions []string
	from, to := query.Get("from"), query.Get("to")
	switch {
	case from != "" && to != "":
		versions = []string{from, to}
	case from != "":
		versions = []string{from}
	case to != "":
		return nil, options, core.Errorf(core.CategoryValidation, "to needs from: give both versions, or from alone")
	}
	switch value := query.Get("context"); value {
	case "":
	case "all":
		options.Context = -1
	default:
		lines, err := strconv.Atoi(value)
		if err != nil || lines < 0 || lines > relicDiffContextLimit {
			return nil, options, core.Errorf(core.CategoryValidation,
				"context must be \"all\" or a whole number from 0 to %d, not %q", relicDiffContextLimit, value)
		}
		options.Context = lines
	}
	return versions, options, nil
}

// serveTaskRelics answers the relics linked to a task. It has a route of its
// own so that reading a task, which the board does again after every change,
// reads no relic.
func (handler *handler) serveTaskRelics(writer http.ResponseWriter, request *http.Request) {
	if handler.TaskRelics == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task relic reading is not configured"))
		return
	}
	_, request, err := handler.vocabulary(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	id := request.PathValue("id")
	links, warnings, err := handler.TaskRelics(request.Context(), id)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	if links == nil {
		links = []core.TaskRelicLink{}
	}
	writeJSON(writer, http.StatusOK, TaskRelicsDocument{
		Format: "workbook.task-relics", Version: 1, Task: id, Relics: links, Warnings: handler.disclosed(warnings),
	})
}

// disclosedTruncation is a history's stopping point as this board may show it. A
// read that stopped at a damaged change says so in Git's or the filesystem's
// words, which a hosted board keeps to its log, as it does for an error. A
// change written by a newer Workbook is described in Workbook's own sentence.
func (handler *handler) disclosedTruncation(truncation *core.HistoryTruncation) *core.HistoryTruncation {
	if truncation == nil || !handler.Hosted || truncation.Category == core.CategoryNewerWriter {
		return truncation
	}
	shown := *truncation
	reference := handler.logDetail(truncation.Message)
	shown.Message = "The history cannot be read past this change. The server's log has the detail under reference " + reference + "."
	return &shown
}

// relicAPIPath reports whether a path has the shape of a relic route.
func relicAPIPath(path string) bool {
	if path == "/api/relics" {
		return true
	}
	rest, ok := strings.CutPrefix(path, "/api/relics/")
	if !ok {
		return false
	}
	segments := strings.Split(rest, "/")
	switch {
	case slices.ContainsFunc(segments, unnamedSegment):
		return false
	case len(segments) == 1:
		return true
	case len(segments) == 2:
		return segments[1] == "history" || segments[1] == "diff"
	default:
		return len(segments) == 3 && segments[1] == "parts"
	}
}

func taskRelicsPathID(path string) string {
	id := taskCollectionPathID(path, "/relics")
	if unnamedSegment(id) {
		return ""
	}
	return id
}

// unnamedSegment reports a path segment that names nothing.
func unnamedSegment(segment string) bool {
	return segment == "" || segment == "." || segment == ".."
}

// malformedRelicPath reports a request under a relic route whose path holds a
// segment naming nothing. It is answered here because the mux would otherwise
// redirect it to the cleaned path, which is some other route's address. A "#"
// is refused too: no ID holds one, and in an address it separates a relic from
// a part, so one inside a segment would turn a read of a relic into a read of
// a part.
func malformedRelicPath(path string) bool {
	segments := strings.Split(path, "/")
	switch {
	case strings.HasPrefix(path, "/api/relics/"):
		return slices.ContainsFunc(segments[3:], func(segment string) bool {
			return unnamedSegment(segment) || strings.Contains(segment, "#")
		})
	case strings.HasPrefix(path, "/api/tasks/") && len(segments) == 5 && segments[4] == "relics":
		return unnamedSegment(segments[3])
	case strings.HasPrefix(path, "/relics/"):
		return malformedRelicBodyPath(path)
	default:
		return false
	}
}
