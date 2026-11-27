package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

const (
	routeRelicID = "WB-R-01M8EVZ9BQR7GNFYPYHSVT6FRC"
	routePartID  = "01M8EVZ9BQ20NDMR0F09K3CGXB"
	routeTaskID  = "WB-01M7ZFJEH48C8WTN09GT4T1DVC"
)

// relicRouteCalls records what the board asked its relic capabilities for.
type relicRouteCalls struct {
	list    int
	show    []string
	removed []bool
	part    []string
	history []string
	options []core.RelicHistoryOptions
	diff    []string
	between [][]string
	context []int
	tasks   []string
}

func (calls *relicRouteCalls) count() int {
	return calls.list + len(calls.show) + len(calls.part) + len(calls.history) + len(calls.diff) + len(calls.tasks)
}

// relicRouteOptions is a board with every relic capability, each recording its
// arguments in calls and answering err when one is given.
func relicRouteOptions(calls *relicRouteCalls, err error) Options {
	return Options{
		ListRelics: func(context.Context) ([]core.RelicSummary, error) {
			calls.list++
			return []core.RelicSummary{
				{ID: "WB-R-1", Type: "spec", Title: "Live spec"},
				{ID: "WB-R-2", Type: "plan", Title: "Live plan"},
				{ID: "WB-R-3", Type: "spec", Title: "Retired spec", Retired: true},
				{ID: "WB-R-4", Type: "note", Title: "Deleted note", Deleted: true},
			}, err
		},
		ShowRelic: func(_ context.Context, id string, removed bool) (core.RelicDocument, error) {
			calls.show = append(calls.show, id)
			calls.removed = append(calls.removed, removed)
			return routeRelicDocument(), err
		},
		RelicPart: func(_ context.Context, relicID, partID string) (core.RelicPartView, error) {
			calls.part = append(calls.part, relicID+" "+partID)
			text := "# Goals\n"
			return core.RelicPartView{Relic: routeRelicID, Head: "abc", Part: core.RelicDocumentPart{
				ID: routePartID, Type: "section",
				Body: &core.RelicDocumentBody{Format: "md", Version: "v1", Bytes: 8, Text: &text},
			}}, err
		},
		RelicHistory: func(_ context.Context, address string, options core.RelicHistoryOptions) (core.RelicHistory, error) {
			calls.history = append(calls.history, address)
			calls.options = append(calls.options, options)
			return core.RelicHistory{Relic: core.RelicSummary{ID: routeRelicID}, Total: 1, Showing: 1,
				Changes: []core.RelicChange{{Commit: "c1", Summary: "relic.create"}}}, err
		},
		RelicDiff: func(_ context.Context, address string, versions []string, options core.RelicDiffOptions) (core.RelicDiff, error) {
			calls.diff = append(calls.diff, address)
			calls.between = append(calls.between, versions)
			calls.context = append(calls.context, options.Context)
			return core.RelicDiff{Relic: routeRelicID, To: core.RelicDiffPoint{Version: "v2"}}, err
		},
		TaskRelics: func(_ context.Context, taskID string) ([]core.TaskRelicLink, []core.Warning, error) {
			calls.tasks = append(calls.tasks, taskID)
			return []core.TaskRelicLink{{Relic: routeRelicID, Title: "Compaction", Type: "spec", Link: "l1", Rel: "specifies"}},
				[]core.Warning{{Code: core.WarningRelicLinksUnread, Message: "relic WB-R-9 cannot be read"}}, err
		},
	}
}

func routeRelicDocument() core.RelicDocument {
	text, encoded := "secret text", "c2VjcmV0"
	return core.RelicDocument{
		Format: core.RelicDocumentFormat, Version: 1, ID: routeRelicID, Head: "abc", Title: "Compaction", Type: "spec",
		Parts: []core.RelicDocumentPart{
			{ID: "p1", Type: "section", Body: &core.RelicDocumentBody{Format: "md", Version: "v1", Bytes: 11, Text: &text}},
			{ID: "p2", Type: "section", Body: &core.RelicDocumentBody{Format: "bin", Version: "v2", Bytes: 6, Base64: &encoded}},
			{ID: "p3", Type: "section"},
		},
		Warnings: []core.Warning{{Code: core.WarningRelicLinkTargetsUnread, Message: "the tasks cannot be read"}},
	}
}

// relicRouteAnswer reads a 200 JSON answer's members, checking its envelope.
func relicRouteAnswer(t *testing.T, handler http.Handler, target, format string) map[string]json.RawMessage {
	t.Helper()
	response := requestJSON(t, handler, http.MethodGet, target, "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s = %d; body = %s", target, response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("GET %s Content-Type = %q", target, got)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &members); err != nil {
		t.Fatalf("GET %s: %v; body = %s", target, err, response.Body.String())
	}
	if string(members["format"]) != `"`+format+`"` || string(members["version"]) != "1" {
		t.Fatalf("GET %s format %s version %s, want %q version 1", target, members["format"], members["version"], format)
	}
	return members
}

func relicRouteRefusal(t *testing.T, handler http.Handler, target string, status int, category core.Category) ErrorBody {
	t.Helper()
	response := requestJSON(t, handler, http.MethodGet, target, "")
	if response.Code != status {
		t.Fatalf("GET %s = %d, want %d; body = %s", target, response.Code, status, response.Body.String())
	}
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("GET %s: %v; body = %s", target, err, response.Body.String())
	}
	if document.Format != "workbook.error" || document.Version != 1 || document.Error.Category != category {
		t.Fatalf("GET %s = %#v, want workbook.error v1, %s", target, document, category)
	}
	return document.Error
}

func TestRelicListRouteFilters(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		query string
		ids   []string
		types []string
	}{
		{"", []string{"WB-R-1", "WB-R-2"}, []string{"plan", "spec"}},
		{"?retired=include", []string{"WB-R-1", "WB-R-2", "WB-R-3"}, []string{"plan", "spec"}},
		{"?retired=only", []string{"WB-R-3"}, []string{"spec"}},
		{"?deleted=include", []string{"WB-R-1", "WB-R-2", "WB-R-4"}, []string{"note", "plan", "spec"}},
		{"?deleted=only", []string{"WB-R-4"}, []string{"note"}},
		{"?retired=include&deleted=include", []string{"WB-R-1", "WB-R-2", "WB-R-3", "WB-R-4"}, []string{"note", "plan", "spec"}},
		{"?type=plan", []string{"WB-R-2"}, []string{"plan", "spec"}},
		{"?type=nothing", []string{}, []string{"plan", "spec"}},
	} {
		handler := NewHandler(relicRouteOptions(&relicRouteCalls{}, nil))
		members := relicRouteAnswer(t, handler, "/api/relics"+test.query, "workbook.relics")
		var relics []core.RelicSummary
		var types []string
		if err := json.Unmarshal(members["relics"], &relics); err != nil {
			t.Fatalf("%q relics: %v", test.query, err)
		}
		if err := json.Unmarshal(members["types"], &types); err != nil {
			t.Fatalf("%q types: %v", test.query, err)
		}
		ids := []string{}
		for _, relic := range relics {
			ids = append(ids, relic.ID)
		}
		if relics == nil || !reflect.DeepEqual(ids, test.ids) || !reflect.DeepEqual(types, test.types) {
			t.Errorf("%q: relics %v types %v, want %v and %v", test.query, ids, types, test.ids, test.types)
		}
	}
	calls := &relicRouteCalls{}
	handler := NewHandler(relicRouteOptions(calls, nil))
	for _, query := range []string{"?retired=yes", "?deleted=1"} {
		relicRouteRefusal(t, handler, "/api/relics"+query, http.StatusBadRequest, core.CategoryValidation)
	}
	if calls.count() != 0 {
		t.Errorf("a refused filter listed relics: %#v", calls)
	}

	empty := NewHandler(Options{
		ListRelics: func(context.Context) ([]core.RelicSummary, error) { return nil, nil },
		ShowRelic:  relicRouteOptions(calls, nil).ShowRelic,
		RelicPart:  relicRouteOptions(calls, nil).RelicPart,
	})
	members := relicRouteAnswer(t, empty, "/api/relics", "workbook.relics")
	if string(members["relics"]) != "[]" || string(members["types"]) != "[]" {
		t.Errorf("no relics = relics %s types %s, want empty lists", members["relics"], members["types"])
	}
}

func TestRelicViewCarriesNoBodyText(t *testing.T) {
	t.Parallel()
	calls := &relicRouteCalls{}
	document := routeRelicDocument()
	options := relicRouteOptions(calls, nil)
	show := options.ShowRelic
	options.ShowRelic = func(ctx context.Context, id string, removed bool) (core.RelicDocument, error) {
		if _, err := show(ctx, id, removed); err != nil {
			return core.RelicDocument{}, err
		}
		return document, nil
	}
	handler := NewHandler(options)

	members := relicRouteAnswer(t, handler, "/api/relics/"+routeRelicID, "workbook.relic-view")
	var relic map[string]any
	if err := json.Unmarshal(members["relic"], &relic); err != nil {
		t.Fatal(err)
	}
	parts, _ := relic["parts"].([]any)
	if relic["id"] != routeRelicID || len(parts) != 3 {
		t.Fatalf("relic = %s", members["relic"])
	}
	for index, want := range []map[string]any{
		{"format": "md", "version": "v1", "bytes": float64(11)},
		{"format": "bin", "version": "v2", "bytes": float64(6)},
	} {
		body := parts[index].(map[string]any)["body"]
		if !reflect.DeepEqual(body, any(want)) {
			t.Errorf("part %d body = %#v, want %#v alone", index, body, want)
		}
	}
	whole, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{`"text"`, `"base64"`, "secret text", "c2VjcmV0"} {
		if strings.Contains(string(whole), leaked) {
			t.Errorf("answer carries %q: %s", leaked, whole)
		}
	}
	if document.Parts[0].Body.Text == nil || document.Parts[1].Body.Base64 == nil {
		t.Error("the route changed the reader's document")
	}
	var warnings []core.Warning
	if err := json.Unmarshal(members["warnings"], &warnings); err != nil || !reflect.DeepEqual(warnings, document.Warnings) {
		t.Errorf("warnings = %s, %v; want the document's", members["warnings"], err)
	}

	relicRouteAnswer(t, handler, "/api/relics/WB-R-01?removed=include", "workbook.relic-view")
	if !reflect.DeepEqual(calls.show, []string{routeRelicID, "WB-R-01"}) || !reflect.DeepEqual(calls.removed, []bool{false, true}) {
		t.Errorf("show calls = %v removed %v", calls.show, calls.removed)
	}
	relicRouteRefusal(t, handler, "/api/relics/WB-R-01?removed=yes", http.StatusBadRequest, core.CategoryValidation)
}

func TestRelicPartRouteAnswersOnePart(t *testing.T) {
	t.Parallel()
	calls := &relicRouteCalls{}
	handler := NewHandler(relicRouteOptions(calls, nil))
	members := relicRouteAnswer(t, handler, "/api/relics/WB-R-01/parts/01M8", "workbook.relic-part")
	if string(members["relic"]) != `"`+routeRelicID+`"` || string(members["head"]) != `"abc"` {
		t.Errorf("relic %s head %s", members["relic"], members["head"])
	}
	var part core.RelicDocumentPart
	if err := json.Unmarshal(members["part"], &part); err != nil {
		t.Fatal(err)
	}
	if part.ID != routePartID || part.Body == nil || part.Body.Text == nil || *part.Body.Text != "# Goals\n" {
		t.Errorf("part = %s, want it with its body text", members["part"])
	}
	if !reflect.DeepEqual(calls.part, []string{"WB-R-01 01M8"}) || calls.count() != 1 {
		t.Errorf("calls = %#v", calls)
	}
}

func TestRelicHistoryRouteOptions(t *testing.T) {
	t.Parallel()
	calls := &relicRouteCalls{}
	handler := NewHandler(relicRouteOptions(calls, nil))
	members := relicRouteAnswer(t, handler, "/api/relics/WB-R-01/history", "workbook.relic-history")
	var history core.RelicHistory
	if err := json.Unmarshal(members["history"], &history); err != nil || history.Total != 1 || len(history.Changes) != 1 {
		t.Errorf("history = %s, %v", members["history"], err)
	}
	relicRouteAnswer(t, handler, "/api/relics/WB-R-01/history?all=1", "workbook.relic-history")
	relicRouteAnswer(t, handler, "/api/relics/WB-R-01/history?part=01M8&limit=7", "workbook.relic-history")
	if want := []string{"WB-R-01", "WB-R-01", "WB-R-01#01M8"}; !reflect.DeepEqual(calls.history, want) {
		t.Errorf("addresses = %v, want %v", calls.history, want)
	}
	if want := []core.RelicHistoryOptions{{Limit: 100}, {Limit: 100, All: true}, {Limit: 7}}; !reflect.DeepEqual(calls.options, want) {
		t.Errorf("options = %#v, want %#v", calls.options, want)
	}
	for _, query := range []string{"?limit=0", "?limit=-1", "?limit=x", "?all=yes"} {
		relicRouteRefusal(t, handler, "/api/relics/WB-R-01/history"+query, http.StatusBadRequest, core.CategoryValidation)
	}
	if len(calls.history) != 3 {
		t.Errorf("a refused request read history: %v", calls.history)
	}
}

func TestRelicDiffRouteOptions(t *testing.T) {
	t.Parallel()
	calls := &relicRouteCalls{}
	handler := NewHandler(relicRouteOptions(calls, nil))
	for _, query := range []string{"", "?from=a", "?from=a&to=b&context=all", "?part=01M8&context=0", "?context=1000"} {
		members := relicRouteAnswer(t, handler, "/api/relics/WB-R-01/diff"+query, "workbook.relic-diff")
		var diff core.RelicDiff
		if err := json.Unmarshal(members["diff"], &diff); err != nil || diff.Relic != routeRelicID || diff.To.Version != "v2" {
			t.Errorf("diff = %s, %v", members["diff"], err)
		}
	}
	if want := []string{"WB-R-01", "WB-R-01", "WB-R-01", "WB-R-01#01M8", "WB-R-01"}; !reflect.DeepEqual(calls.diff, want) {
		t.Errorf("addresses = %v, want %v", calls.diff, want)
	}
	versions := make([]string, len(calls.between))
	for index, between := range calls.between {
		versions[index] = strings.Join(between, ",")
	}
	if want := []string{"", "a", "a,b", "", ""}; !reflect.DeepEqual(versions, want) {
		t.Errorf("versions = %q, want %q", versions, want)
	}
	if want := []int{3, 3, -1, 0, 1000}; !reflect.DeepEqual(calls.context, want) {
		t.Errorf("context = %v, want %v", calls.context, want)
	}
	for _, query := range []string{"?to=b", "?context=1001", "?context=-2", "?context=-1", "?context=x", "?context=99999999999999999999"} {
		relicRouteRefusal(t, handler, "/api/relics/WB-R-01/diff"+query, http.StatusBadRequest, core.CategoryValidation)
	}
	if len(calls.diff) != 5 {
		t.Errorf("a refused request compared versions: %v", calls.diff)
	}
}

func TestTaskRelicsRoute(t *testing.T) {
	t.Parallel()
	calls := &relicRouteCalls{}
	handler := NewHandler(relicRouteOptions(calls, nil))
	members := relicRouteAnswer(t, handler, "/api/tasks/"+routeTaskID+"/relics", "workbook.task-relics")
	var links []core.TaskRelicLink
	var warnings []core.Warning
	if err := json.Unmarshal(members["relics"], &links); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(members["warnings"], &warnings); err != nil {
		t.Fatal(err)
	}
	if string(members["task"]) != `"`+routeTaskID+`"` || len(links) != 1 || links[0].Relic != routeRelicID ||
		len(warnings) != 1 || warnings[0].Message != "relic WB-R-9 cannot be read" {
		t.Errorf("task %s relics %s warnings %s", members["task"], members["relics"], members["warnings"])
	}
	if !reflect.DeepEqual(calls.tasks, []string{routeTaskID}) {
		t.Errorf("tasks asked = %v", calls.tasks)
	}

	none := NewHandler(Options{TaskRelics: func(context.Context, string) ([]core.TaskRelicLink, []core.Warning, error) {
		return nil, nil, nil
	}})
	members = relicRouteAnswer(t, none, "/api/tasks/"+routeTaskID+"/relics", "workbook.task-relics")
	if _, warned := members["warnings"]; string(members["relics"]) != "[]" || warned {
		t.Errorf("no links = %v, want an empty list and no warnings", members)
	}

	unconfigured := NewHandler(Options{})
	refusal := relicRouteRefusal(t, unconfigured, "/api/tasks/"+routeTaskID+"/relics",
		http.StatusInternalServerError, core.CategoryOperational)
	if !strings.Contains(refusal.Message, "not configured") {
		t.Errorf("refusal = %q", refusal.Message)
	}
}

var relicRoutePaths = []string{
	"/api/relics",
	"/api/relics/WB-R-01",
	"/api/relics/WB-R-01/parts/01M8",
	"/api/relics/WB-R-01/history",
	"/api/relics/WB-R-01/diff",
}

func TestRelicRoutesRefuseOtherMethods(t *testing.T) {
	t.Parallel()
	calls := &relicRouteCalls{}
	handler := NewHandler(relicRouteOptions(calls, nil))
	paths := append([]string{"/api/tasks/" + routeTaskID + "/relics"}, relicRoutePaths...)
	for _, path := range paths {
		for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
			response := requestJSON(t, handler, method, path, "{}")
			if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
				t.Errorf("%s %s = %d Allow %q, want 405 Allow GET", method, path, response.Code, response.Header().Get("Allow"))
			}
		}
	}
	if calls.count() != 0 {
		t.Errorf("a refused method called a capability: %#v", calls)
	}
}

func TestRelicRoutesMapCoreErrors(t *testing.T) {
	t.Parallel()
	paths := append([]string{"/api/tasks/" + routeTaskID + "/relics"}, relicRoutePaths...)
	for _, path := range paths {
		missing := NewHandler(relicRouteOptions(&relicRouteCalls{}, core.Errorf(core.CategoryNotFound, "no relic matches %q", "WB-R-01")))
		if got := relicRouteRefusal(t, missing, path, http.StatusNotFound, core.CategoryNotFound); got.Message != `no relic matches "WB-R-01"` {
			t.Errorf("%s message = %q", path, got.Message)
		}

		corrupt := core.Errorf(core.CategoryCorruptData, "relic WB-R-01 in /srv/boards/acme/.git is damaged")
		local := NewHandler(relicRouteOptions(&relicRouteCalls{}, corrupt))
		got := relicRouteRefusal(t, local, path, http.StatusInternalServerError, core.CategoryCorruptData)
		if got.Message != "relic WB-R-01 in /srv/boards/acme/.git is damaged" {
			t.Errorf("%s local message = %q", path, got.Message)
		}

		log := &lockedBuffer{}
		options := relicRouteOptions(&relicRouteCalls{}, corrupt)
		options.Hosted, options.ServerLog = true, log
		got = relicRouteRefusal(t, NewHandler(options), path, http.StatusInternalServerError, core.CategoryCorruptData)
		match := referencePattern.FindStringSubmatch(got.Message)
		if match == nil || got.Reference != match[1] || strings.Contains(got.Message, "/srv") {
			t.Errorf("%s hosted answer = %#v, want a short sentence and a reference", path, got)
		} else if logged := log.String(); !strings.Contains(logged, match[1]) || !strings.Contains(logged, "/srv/boards/acme/.git") {
			t.Errorf("%s server log = %q", path, logged)
		}
	}
}

// A hosted board keeps the server's words out of a relic answer's warnings and
// out of the note saying where a history stops, as it does out of its errors.
func TestHostedRelicRoutesKeepServerDetailOutOfWarningsAndTruncation(t *testing.T) {
	t.Parallel()
	detail := errors.New(serverDetail).Error()
	var category core.Category
	log := &lockedBuffer{}
	options := relicRouteOptions(&relicRouteCalls{}, nil)
	options.ShowRelic = func(context.Context, string, bool) (core.RelicDocument, error) {
		document := routeRelicDocument()
		document.Warnings = []core.Warning{{Code: core.WarningRelicLinkTargetsUnread, Message: detail}}
		return document, nil
	}
	options.TaskRelics = func(context.Context, string) ([]core.TaskRelicLink, []core.Warning, error) {
		return nil, []core.Warning{{Code: core.WarningRelicLinksUnread, Message: detail}}, nil
	}
	options.RelicHistory = func(context.Context, string, core.RelicHistoryOptions) (core.RelicHistory, error) {
		return core.RelicHistory{Truncated: &core.HistoryTruncation{Commit: "c9", Message: detail, Category: category}}, nil
	}
	options.Hosted, options.ServerLog = true, log
	handler := NewHandler(options)
	for _, path := range []string{"/api/relics/WB-R-01", "/api/tasks/" + routeTaskID + "/relics", "/api/relics/WB-R-01/history"} {
		response := requestJSON(t, handler, http.MethodGet, path, "")
		body := response.Body.String()
		if response.Code != http.StatusOK || strings.Contains(body, "/srv/boards") || strings.Contains(body, "fatal:") {
			t.Errorf("%s = %d %s", path, response.Code, body)
		}
		if match := referencePattern.FindStringSubmatch(body); match == nil || !strings.Contains(log.String(), match[1]) {
			t.Errorf("%s = %s, want a reference the log holds", path, body)
		}
	}
	// Workbook's own sentence about a newer writer is the person's to read.
	category, detail = core.CategoryNewerWriter, "upgrade workbook to read it"
	response := requestJSON(t, handler, http.MethodGet, "/api/relics/WB-R-01/history", "")
	if !strings.Contains(response.Body.String(), detail) {
		t.Errorf("newer-writer truncation = %s", response.Body.String())
	}

	options.Hosted = false
	detail, category = serverDetail, ""
	for _, path := range []string{"/api/relics/WB-R-01", "/api/tasks/" + routeTaskID + "/relics", "/api/relics/WB-R-01/history"} {
		if response := requestJSON(t, NewHandler(options), http.MethodGet, path, ""); !strings.Contains(response.Body.String(), "missing.git") {
			t.Errorf("local %s lost its detail: %s", path, response.Body.String())
		}
	}
}

func TestRelicRoutesAbsentWithoutCapabilities(t *testing.T) {
	t.Parallel()
	status := func(handler http.Handler, path string) int {
		t.Helper()
		return requestJSON(t, handler, http.MethodGet, path, "").Code
	}
	none := NewHandler(Options{})
	for _, path := range relicRoutePaths {
		if got := status(none, path); got != http.StatusNotFound {
			t.Errorf("GET %s = %d with no relic options, want 404", path, got)
		}
	}
	// Reading relics takes the list, show and part reads together.
	for name, drop := range map[string]func(*Options){
		"list": func(options *Options) { options.ListRelics = nil },
		"show": func(options *Options) { options.ShowRelic = nil },
		"part": func(options *Options) { options.RelicPart = nil },
	} {
		calls := &relicRouteCalls{}
		options := relicRouteOptions(calls, nil)
		drop(&options)
		partial := NewHandler(options)
		for _, path := range relicRoutePaths {
			if got := status(partial, path); got != http.StatusNotFound {
				t.Errorf("GET %s = %d without %s, want 404", path, got, name)
			}
		}
		if calls.count() != 0 {
			t.Errorf("without %s a capability was called: %#v", name, calls)
		}
	}
	options := relicRouteOptions(&relicRouteCalls{}, nil)
	options.RelicHistory, options.RelicDiff = nil, nil
	readOnly := NewHandler(options)
	for path, want := range map[string]int{
		"/api/relics": 200, "/api/relics/WB-R-01/history": 404, "/api/relics/WB-R-01/diff": 404,
	} {
		if got := status(readOnly, path); got != want {
			t.Errorf("GET %s = %d without history and diff, want %d", path, got, want)
		}
	}
}

func TestRelicsReadableNeedsListShowAndPart(t *testing.T) {
	t.Parallel()
	options := relicRouteOptions(&relicRouteCalls{}, nil)
	if !(&handler{Options: options}).relicsReadable() {
		t.Error("a board with every relic capability does not read relics")
	}
	options.RelicPart = nil
	if (&handler{Options: options}).relicsReadable() {
		t.Error("a board without the part read says it reads relics")
	}
}

func TestMalformedRelicPaths(t *testing.T) {
	t.Parallel()
	calls := &relicRouteCalls{}
	handler := NewHandler(relicRouteOptions(calls, nil))
	for _, path := range []string{
		"/api/relics/",
		"/api/relics//parts/x",
		"/api/relics/a/parts/",
		"/api/relics/a/parts/..",
		"/api/relics/a/parts/.",
		"/api/relics/a/parts/b/c",
		"/api/relics/../tasks",
		"/api/relics/%2E%2E/history",
		"/api/relics/a%23b",
		"/api/relics/a/parts/b%23c",
		"/api/relics/a/unknown",
		"/api/tasks//relics",
		"/api/tasks/../relics",
		"/api/tasks/%2E/relics",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d (Location %q), want 404", path, response.Code, response.Header().Get("Location"))
		}
	}
	if calls.count() != 0 {
		t.Errorf("a malformed path called a capability: %#v", calls)
	}
}

// Every relic route hands its read the vocabulary the request resolved, from
// which serve builds the read's service.
func TestRelicRoutesReadUnderTheRequestsVocabulary(t *testing.T) {
	t.Parallel()
	saw := 0
	check := func(ctx context.Context) {
		if state, ok := VocabularyFrom(ctx); ok && state.Head == "head-1" {
			saw++
		}
	}
	inner := relicRouteOptions(&relicRouteCalls{}, nil)
	options := Options{
		Vocabulary: func(context.Context) (VocabularyState, error) { return VocabularyState{Head: "head-1"}, nil },
		ListRelics: func(ctx context.Context) ([]core.RelicSummary, error) { check(ctx); return inner.ListRelics(ctx) },
		ShowRelic: func(ctx context.Context, id string, removed bool) (core.RelicDocument, error) {
			check(ctx)
			return inner.ShowRelic(ctx, id, removed)
		},
		RelicPart: func(ctx context.Context, relicID, partID string) (core.RelicPartView, error) {
			check(ctx)
			return inner.RelicPart(ctx, relicID, partID)
		},
		RelicHistory: func(ctx context.Context, address string, o core.RelicHistoryOptions) (core.RelicHistory, error) {
			check(ctx)
			return inner.RelicHistory(ctx, address, o)
		},
		RelicDiff: func(ctx context.Context, address string, versions []string, o core.RelicDiffOptions) (core.RelicDiff, error) {
			check(ctx)
			return inner.RelicDiff(ctx, address, versions, o)
		},
		TaskRelics: func(ctx context.Context, id string) ([]core.TaskRelicLink, []core.Warning, error) {
			check(ctx)
			return inner.TaskRelics(ctx, id)
		},
	}
	handler := NewHandler(options)
	paths := append([]string{"/api/tasks/" + routeTaskID + "/relics"}, relicRoutePaths...)
	for _, path := range paths {
		if response := requestJSON(t, handler, http.MethodGet, path, ""); response.Code != http.StatusOK {
			t.Errorf("GET %s = %d", path, response.Code)
		}
	}
	if saw != len(paths) {
		t.Errorf("%d of %d reads had the request's vocabulary", saw, len(paths))
	}
}
