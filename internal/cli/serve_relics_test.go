package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/syncloop"
	"github.com/dgoings/workbook/internal/testenv"
	"github.com/dgoings/workbook/internal/testrepo"
	"github.com/dgoings/workbook/internal/webui"
)

// servedRelics is a relic of three parts, linked to a task and changed once
// since it was created: an md body, an html body and a body that is not UTF-8.
type servedRelics struct {
	task  core.Task
	relic core.RelicDocument
}

const servedHTMLBody = "<!doctype html>\n<title>Mock</title>\n<p>Drawn as a page.</p>\n"

func servedRelicDocument(t *testing.T) []byte {
	t.Helper()
	return []byte(`{"format":"workbook.relic","version":1,"title":"Archive plan","type":"paper",` +
		`"retired":false,"deleted":false,"properties":{"status":"approved","weight":3},"parts":[` +
		`{"type":"section","fields":{"heading":"Goals"},"body":{"format":"md","bytes":0,"text":"# Goals\n\nKeep it small.\n"}},` +
		`{"type":"mockup","fields":{},"body":{"format":"html","bytes":0,"text":` + mustJSON(t, servedHTMLBody) + `}},` +
		`{"type":"figure","fields":{},"body":{"format":"bin","bytes":0,"base64":"/wD+"}}` +
		`],"links":[]}`)
}

func createServedRelics(t *testing.T, repository string) servedRelics {
	t.Helper()
	task := cliCreateTask(t, repository, "Archive finished tasks")
	created := mustCreateRelic(t, repository, "--from-file", writeRelicDocumentFile(t, servedRelicDocument(t)))
	mustRelic(t, repository, nil, "update", created.Relic.ID, "--link", task.ID, "--rel", "relates-to")
	var shown core.RelicDocument
	mustRelic(t, repository, &shown, "show", created.Relic.ID)
	if len(shown.Parts) != 3 || len(shown.Links) != 1 {
		t.Fatalf("relic has %d parts and %d links, want 3 and 1", len(shown.Parts), len(shown.Links))
	}
	return servedRelics{task: task, relic: shown}
}

// relicRoutePaths is one address of every route that reads relics.
func (fixture servedRelics) relicRoutePaths() []string {
	relic, part := "/api/relics/"+fixture.relic.ID, fixture.relic.Parts[1].ID
	return []string{
		"/api/relics",
		"/api/relics?deleted=include",
		relic,
		relic + "?removed=include",
		relic + "/parts/" + part,
		relic + "/history",
		relic + "/diff",
		"/relics/" + fixture.relic.ID + "/parts/" + part + "/body",
		"/api/tasks/" + fixture.task.ID + "/relics",
	}
}

// boardJSON reads a route that must answer 200 and decodes it.
func boardJSON(t *testing.T, address, path string) map[string]any {
	t.Helper()
	body, status := boardRequest(t, http.MethodGet, "http://"+address+path, "")
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d; body = %s", path, status, body)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode GET %s: %v; body = %s", path, err, body)
	}
	return document
}

// commandData runs a command with --json and returns its data member decoded.
func commandData(t *testing.T, repository string, args ...string) any {
	t.Helper()
	full := append(slices.Clone(args), "--json")
	code, stdout, stderr := run(t, repository, full...)
	if code != 0 || stderr != "" {
		t.Fatalf("%s = (%d, %q, %q)", strings.Join(full, " "), code, stdout, stderr)
	}
	var result struct {
		Data any `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode %s: %v", strings.Join(full, " "), err)
	}
	return result.Data
}

func assertSameJSON(t *testing.T, label string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s =\n%s\nwant\n%s", label, mustJSON(t, got), mustJSON(t, want))
	}
}

func assertBoardError(t *testing.T, address, path string, wantStatus int, wantCategory core.Category) string {
	t.Helper()
	body, status := boardRequest(t, http.MethodGet, "http://"+address+path, "")
	var document errorDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode GET %s: %v; status = %d, body = %s", path, err, status, body)
	}
	if status != wantStatus || document.Format != "workbook.error" || document.Error.Category != wantCategory {
		t.Fatalf("GET %s = %d %s, want %d %s", path, status, body, wantStatus, wantCategory)
	}
	return document.Error.Message
}

func TestServeRelicRoutesAnswerAsTheCommandsDo(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			address := startServeBoard(t, repository)
			// Saved while the board runs, so the routes judge each relic
			// under the schemas of their own request.
			boardJSON(t, address, "/api/relics")
			mustPutSchema(t, repository, "paper", specSchemaOne)
			fixture := createServedRelics(t, repository)
			retired := mustCreateRelic(t, repository, "Retired", "--type", "paper")
			mustRelic(t, repository, nil, "retire", retired.Relic.ID)
			deleted := mustCreateRelic(t, repository, "Deleted", "--type", "paper")
			mustRelic(t, repository, nil, "delete", deleted.Relic.ID)
			relicID, taskID := fixture.relic.ID, fixture.task.ID

			listed := boardJSON(t, address, "/api/relics")["relics"]
			assertSameJSON(t, "/api/relics", listed, commandData(t, repository, "relic", "list"))
			if !strings.Contains(mustJSON(t, listed), `"conformance":"conforms"`) {
				t.Errorf("/api/relics judged no relic under the saved schema: %s", mustJSON(t, listed))
			}
			// relic list --all lists retired relics too, which the route's
			// deleted filter leaves to its retired one.
			all, ok := commandData(t, repository, "relic", "list", "--all").([]any)
			if !ok || len(all) != 3 {
				t.Fatalf("relic list --all = %v, want three relics", all)
			}
			unretired := slices.DeleteFunc(all, func(summary any) bool {
				return summary.(map[string]any)["id"] == retired.Relic.ID
			})
			assertSameJSON(t, "/api/relics?deleted=include",
				boardJSON(t, address, "/api/relics?deleted=include")["relics"], any(unretired))

			assertSameJSON(t, "/api/relics/<id>", boardJSON(t, address, "/api/relics/"+relicID)["relic"],
				commandData(t, repository, "relic", "show", relicID))
			assertSameJSON(t, "/api/relics/<id>?removed=include",
				boardJSON(t, address, "/api/relics/"+relicID+"?removed=include")["relic"],
				commandData(t, repository, "relic", "show", relicID, "--removed"))

			for _, part := range fixture.relic.Parts {
				answer := boardJSON(t, address, "/api/relics/"+relicID+"/parts/"+part.ID)
				shown := commandData(t, repository, "relic", "show", relicID+"#"+part.ID, "--body").(map[string]any)
				assertSameJSON(t, "part "+part.Type, answer["part"], shown["parts"].([]any)[0])
				assertSameJSON(t, "part relic", answer["relic"], shown["id"])
				assertSameJSON(t, "part head", answer["head"], shown["head"])
			}

			history := boardJSON(t, address, "/api/relics/"+relicID+"/history")["history"]
			assertSameJSON(t, "history", history,
				commandData(t, repository, "relic", "show", relicID, "--history", "--limit", "100"))
			var changes core.RelicHistory
			if err := json.Unmarshal([]byte(mustJSON(t, history)), &changes); err != nil {
				t.Fatal(err)
			}
			if len(changes.Changes) != 2 {
				t.Fatalf("history lists %d changes, want 2", len(changes.Changes))
			}
			newest, oldest := changes.Changes[0].Operations, changes.Changes[1].Operations
			from, to := oldest[len(oldest)-1].ID, newest[len(newest)-1].ID
			assertSameJSON(t, "diff",
				boardJSON(t, address, "/api/relics/"+relicID+"/diff?from="+from+"&to="+to)["diff"],
				commandData(t, repository, "relic", "diff", relicID, from, to))

			shownTask := commandData(t, repository, "show", taskID).(map[string]any)
			if links, ok := shownTask["relics"].([]any); !ok || len(links) != 1 {
				t.Fatalf("show %s relics = %v, want one link", taskID, shownTask["relics"])
			}
			assertSameJSON(t, "task relics",
				boardJSON(t, address, "/api/tasks/"+taskID+"/relics")["relics"], shownTask["relics"])
			assertSameJSON(t, "task relics by prefix",
				boardJSON(t, address, "/api/tasks/"+taskID[:len(taskID)-1]+"/relics")["relics"], shownTask["relics"])
			unlinked := cliCreateTask(t, repository, "Nothing links here")
			assertSameJSON(t, "relics of an unlinked task",
				boardJSON(t, address, "/api/tasks/"+unlinked.ID+"/relics")["relics"], any([]any{}))
			for _, unknown := range []string{"WB-" + relicTestID(424242), "nope"} {
				assertBoardError(t, address, "/api/tasks/"+unknown+"/relics", http.StatusNotFound, core.CategoryNotFound)
			}
		})
	}
}

// standInWatcher takes the repository's watcher socket without synchronizing
// anything, so a board started after it runs no synchronization of its own and
// every Git command the board runs is a request's or its start's.
func standInWatcher(t *testing.T, repository string) {
	t.Helper()
	// Not t.TempDir: a socket path has a short limit.
	directory, err := os.MkdirTemp("", "wbw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen as a stand-in watcher: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	commonDir := gitOutput(t, repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	pointer := syncloop.PointerPath(commonDir)
	if err := os.MkdirAll(filepath.Dir(pointer), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf(`{"format":%q,"version":%d,"socket":%q,"pid":%d}`,
		syncloop.PointerFormat, syncloop.PointerVersion, socket, os.Getpid())
	if err := os.WriteFile(pointer, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

// recordedBoard is a workbook serve process whose Git commands are recorded,
// beside a stand-in watcher.
type recordedBoard struct {
	address string
	log     string
}

// startRecordedBoard runs the built workbook with a git first on its PATH that
// records each command it is given and runs the real one. A process of its own
// keeps that PATH, and so the record, to this board.
func startRecordedBoard(t *testing.T, repository string) recordedBoard {
	t.Helper()
	binary := buildWorkbookBinary(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	directory := t.TempDir()
	log := filepath.Join(directory, "git.log")
	testenv.WriteExecutable(t, filepath.Join(directory, "git"), []byte("#!/bin/sh\n"+
		"printf '%s\\n' \"$*\" >> '"+log+"'\nexec '"+realGit+"' \"$@\"\n"))
	standInWatcher(t, repository)

	output := &watcherOutput{}
	command := exec.Command(binary, "serve", "--addr", "127.0.0.1:0")
	command.Dir = repository
	command.Env = append(os.Environ(), "PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		finished := make(chan error, 1)
		go func() { finished <- command.Wait() }()
		select {
		case err := <-finished:
			if err != nil {
				t.Errorf("serve: %v; wrote %q", err, output.String())
			}
		case <-time.After(30 * time.Second):
			_ = command.Process.Kill()
			t.Errorf("serve did not stop; wrote %q", output.String())
		}
	})

	// The banner names the address, and the second line says the board found
	// the stand-in and started no synchronization of its own.
	var address string
	deadline := time.Now().Add(30 * time.Second)
	for {
		written := output.String()
		if _, after, found := strings.Cut(written, "Workbook board: http://"); found &&
			strings.Contains(written, "an external sync watcher owns this repository") {
			address, _, _ = strings.Cut(after, "\n")
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not start beside the stand-in watcher; wrote %q", written)
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitForHTTP(t, "http://"+address+"/healthz")
	return recordedBoard{address: address, log: log}
}

// forget empties the record.
func (board recordedBoard) forget(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(board.log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// subcommands lists the Git subcommand of each recorded command.
func (board recordedBoard) subcommands(t *testing.T) []string {
	t.Helper()
	contents, err := os.ReadFile(board.log)
	if err != nil {
		t.Fatalf("read the Git record: %v", err)
	}
	var subcommands []string
	for _, line := range strings.Split(string(contents), "\n") {
		arguments := strings.Fields(line)
		for len(arguments) > 0 && strings.HasPrefix(arguments[0], "-") {
			if arguments[0] == "-C" || arguments[0] == "-c" {
				arguments = arguments[1:]
			}
			arguments = arguments[min(1, len(arguments)):]
		}
		if len(arguments) > 0 {
			subcommands = append(subcommands, arguments[0])
		}
	}
	return subcommands
}

func TestServeRelicRoutesMoveNoRef(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	fixture := createServedRelics(t, first)
	for _, command := range [][]string{{first, "sync"}, {second, "sync"}} {
		if code, stdout, stderr := run(t, command[0], command[1]); code != 0 {
			t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
		}
	}
	newer := mustCreateRelic(t, second, "Not fetched yet", "--type", "paper")
	mustRelic(t, second, nil, "update", fixture.relic.ID, "--title", "Renamed elsewhere")
	if remote := cliGitOutput(t, first, "ls-remote", "origin", "refs/workbook/relics/*"); !strings.Contains(remote, newer.Relic.ID) {
		t.Fatalf("origin does not hold the newer relic: %q", remote)
	}

	board := startRecordedBoard(t, first)
	before := gitOutput(t, first, "for-each-ref")
	board.forget(t)
	for _, path := range fixture.relicRoutePaths() {
		body, status := boardRequest(t, http.MethodGet, "http://"+board.address+path, "")
		if status != http.StatusOK {
			t.Fatalf("GET %s = %d; body = %s", path, status, body)
		}
		if bytes.Contains(body, []byte(newer.Relic.ID)) || bytes.Contains(body, []byte("Renamed elsewhere")) {
			t.Errorf("GET %s answers what only origin holds: %s", path, body)
		}
	}
	subcommands := board.subcommands(t)
	if after := gitOutput(t, first, "for-each-ref"); after != before {
		t.Errorf("refs after the relic routes =\n%s\nwant\n%s", after, before)
	}
	if !slices.Contains(subcommands, "for-each-ref") {
		t.Fatalf("the Git record holds no ref listing, so it is not recording the board: %q", subcommands)
	}
	for _, subcommand := range subcommands {
		switch subcommand {
		case "fetch", "push", "ls-remote":
			t.Errorf("a relic route ran git %s", subcommand)
		}
	}
}

func TestServeRelicListReadsTheIndexOnly(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	for number := range 49 {
		mustCreateRelic(t, repository, fmt.Sprintf("Relic %d", number), "--type", "paper", "--no-sync")
	}
	parts := make([]string, 200)
	for number := range parts {
		parts[number] = fmt.Sprintf(`{"type":"section","fields":{"heading":"Part %d"},`+
			`"body":{"format":"md","bytes":0,"text":"Text of part %d.\n"}}`, number, number)
	}
	large := mustCreateRelic(t, repository, "--no-sync", "--from-file", writeRelicDocumentFile(t, []byte(
		`{"format":"workbook.relic","version":1,"title":"Large","type":"paper","retired":false,"deleted":false,`+
			`"properties":{},"parts":[`+strings.Join(parts, ",")+`],"links":[]}`)))
	board := startRecordedBoard(t, repository)
	listPath, relicPath := "/api/relics", "/api/relics/"+large.Relic.ID
	// The first reads build the index.
	boardJSON(t, board.address, listPath)
	boardJSON(t, board.address, relicPath)

	board.forget(t)
	if relics := boardJSON(t, board.address, listPath)["relics"].([]any); len(relics) != 50 {
		t.Fatalf("listed %d relics, want 50", len(relics))
	}
	shown := boardJSON(t, board.address, relicPath)["relic"].(map[string]any)
	if got := len(shown["parts"].([]any)); got != 200 {
		t.Fatalf("shown %d parts, want 200", got)
	}
	subcommands := board.subcommands(t)
	if !slices.Contains(subcommands, "for-each-ref") {
		t.Fatalf("the Git record holds no ref listing, so it is not recording the board: %q", subcommands)
	}
	for _, subcommand := range subcommands {
		if subcommand == "cat-file" || subcommand == "rev-list" {
			t.Errorf("a warm read ran git %s; the record is %q", subcommand, subcommands)
		}
	}

	// A body is read from Git, which shows the record would have held it.
	board.forget(t)
	partID := shown["parts"].([]any)[0].(map[string]any)["id"].(string)
	boardJSON(t, board.address, relicPath+"/parts/"+partID)
	if subcommands := board.subcommands(t); !slices.Contains(subcommands, "cat-file") {
		t.Fatalf("reading a body ran %q, want a cat-file", subcommands)
	}
}

// boardAnswers is the status and body of every relic route.
func boardAnswers(t *testing.T, address string, fixture servedRelics) []string {
	t.Helper()
	var answers []string
	for _, path := range fixture.relicRoutePaths() {
		body, status := boardRequest(t, http.MethodGet, "http://"+address+path, "")
		if status != http.StatusOK {
			t.Errorf("GET %s = %d; body = %s", path, status, body)
		}
		answers = append(answers, fmt.Sprintf("%s %d %s", path, status, body))
	}
	return answers
}

func TestServeRelicRoutesAnswerFromGitWhenTheIndexCannot(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustPutSchema(t, repository, "paper", specSchemaOne)
	fixture := createServedRelics(t, repository)
	var want []string
	t.Run("warm", func(t *testing.T) {
		want = boardAnswers(t, startServeBoard(t, repository), fixture)
	})
	if t.Failed() {
		return
	}
	// A directory where the cache file goes is a cache SQLite cannot open.
	cache := relicCachePath(t, repository)
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	got := boardAnswers(t, startServeBoard(t, repository), fixture)
	assertSameOutputs(t, "with no usable cache", got, want)
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("the cache is usable again (%v), so nothing was read without it", err)
	}
}

// withoutRelicOptions leaves a board serving tasks alone.
func withoutRelicOptions(options *webui.Options) {
	options.ListRelics, options.ShowRelic, options.RelicPart = nil, nil, nil
	options.RelicHistory, options.RelicDiff, options.TaskRelics = nil, nil, nil
}

// startServeBoardWith is startServeBoard with the board's options changed
// before its handler is built.
func startServeBoardWith(t *testing.T, repository string, change func(*webui.Options)) string {
	t.Helper()
	reserved := reserveListener(t)
	ctx, cancel := context.WithCancel(withBoardOptions(context.Background(), change))
	result := make(chan error, 1)
	var serveStderr bytes.Buffer
	go func() {
		result <- runServeWith(ctx, reserved.listen, []string{"--addr", reserved.addr}, repository, io.Discard, &serveStderr)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-result; err != nil {
			t.Errorf("runServeWith() error = %v; stderr = %q", err, serveStderr.String())
		}
	})
	waitForHTTP(t, "http://"+reserved.addr+"/healthz")
	return reserved.addr
}

// countRelicReads wraps each relic read the board was given so every call is
// counted, and fails the test for one the board was not given.
func countRelicReads(t *testing.T, calls *atomic.Int64) func(*webui.Options) {
	return func(options *webui.Options) {
		list, show, part := options.ListRelics, options.ShowRelic, options.RelicPart
		history, diff, task := options.RelicHistory, options.RelicDiff, options.TaskRelics
		if list == nil || show == nil || part == nil || history == nil || diff == nil || task == nil {
			t.Error("serve gave the board no reader for at least one relic read")
			return
		}
		options.ListRelics = func(ctx context.Context) ([]core.RelicSummary, error) {
			calls.Add(1)
			return list(ctx)
		}
		options.ShowRelic = func(ctx context.Context, id string, removed bool) (core.RelicDocument, error) {
			calls.Add(1)
			return show(ctx, id, removed)
		}
		options.RelicPart = func(ctx context.Context, relicID, partID string) (core.RelicPartView, error) {
			calls.Add(1)
			return part(ctx, relicID, partID)
		}
		options.RelicHistory = func(ctx context.Context, address string, window core.RelicHistoryOptions) (core.RelicHistory, error) {
			calls.Add(1)
			return history(ctx, address, window)
		}
		options.RelicDiff = func(ctx context.Context, address string, versions []string, diffOptions core.RelicDiffOptions) (core.RelicDiff, error) {
			calls.Add(1)
			return diff(ctx, address, versions, diffOptions)
		}
		options.TaskRelics = func(ctx context.Context, taskID string) ([]core.TaskRelicLink, []core.Warning, error) {
			calls.Add(1)
			return task(ctx, taskID)
		}
	}
}

func TestTaskRoutesReadNoRelic(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	fixture := createServedRelics(t, repository)
	var calls atomic.Int64
	counted := startServeBoardWith(t, repository, countRelicReads(t, &calls))
	without := startServeBoardWith(t, repository, withoutRelicOptions)

	detail := "/api/tasks/" + fixture.task.ID
	for _, path := range []string{"/", "/tasks/" + fixture.task.ID, "/api/tasks", detail} {
		body, status := boardRequest(t, http.MethodGet, "http://"+counted+path, "")
		if status != http.StatusOK {
			t.Fatalf("GET %s = %d; body = %s", path, status, body)
		}
		if got := calls.Load(); got != 0 {
			t.Fatalf("GET %s made %d relic reads, want none", path, got)
		}
		if path == detail {
			want, wantStatus := boardRequest(t, http.MethodGet, "http://"+without+path, "")
			if wantStatus != http.StatusOK || !bytes.Equal(body, want) {
				t.Fatalf("GET %s =\n%s\nwithout relic reads (%d)\n%s", path, body, wantStatus, want)
			}
		}
	}
	// The counters and the board without them are what they say they are.
	boardJSON(t, counted, "/api/relics")
	if calls.Load() != 1 {
		t.Fatalf("GET /api/relics made %d counted reads, want 1", calls.Load())
	}
	if body, status := boardRequest(t, http.MethodGet, "http://"+without+"/api/relics", ""); status != http.StatusNotFound {
		t.Fatalf("GET /api/relics without relic reads = %d; body = %s", status, body)
	}
}

func TestServeDamagedRelicIsReported(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	created := mustCreateRelic(t, repository, "Damaged", "--type", "paper")
	tip := created.Relic.Head
	operation := []byte(gitOutput(t, repository, "cat-file", "blob", tip+":operation.json") + "\n")
	fixture := openRelicFixture(t, repository)
	forged := fixture.forgeRelicCommit(t, tip, operation, []byte("{}\n"), "")
	gitInput(t, repository, nil, "update-ref", "refs/workbook/relics/"+created.Relic.ID, forged, tip)
	address := startServeBoard(t, repository)

	for path, command := range map[string][]string{
		"/api/relics/" + created.Relic.ID: {"relic", "show", created.Relic.ID, "--json"},
		"/api/relics":                     {"relic", "list", "--json"},
	} {
		message := assertBoardError(t, address, path, http.StatusInternalServerError, core.CategoryCorruptData)
		if !strings.Contains(message, created.Relic.ID) {
			t.Errorf("GET %s message = %q, want the relic named", path, message)
		}
		code, stdout, stderr := run(t, repository, command...)
		var refused errorDocument
		if err := json.Unmarshal([]byte(stderr), &refused); err != nil || code != 7 {
			t.Fatalf("%s = (%d, %q, %q), want exit 7", strings.Join(command, " "), code, stdout, stderr)
		}
		if message != refused.Error.Message {
			t.Errorf("GET %s message = %q, want the command's %q", path, message, refused.Error.Message)
		}
	}
}

func TestServeRelicListRetiredFilter(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	live := mustCreateRelic(t, repository, "Live", "--type", "paper")
	retired := mustCreateRelic(t, repository, "Retired", "--type", "paper")
	mustRelic(t, repository, nil, "retire", retired.Relic.ID)
	address := startServeBoard(t, repository)

	// listed is each listed relic's ID, with whether it is marked retired.
	listed := func(path string) map[string]bool {
		t.Helper()
		var document webui.RelicsDocument
		if err := json.Unmarshal([]byte(mustJSON(t, boardJSON(t, address, path))), &document); err != nil {
			t.Fatalf("decode GET %s: %v", path, err)
		}
		found := map[string]bool{}
		for _, summary := range document.Relics {
			found[summary.ID] = summary.Retired
		}
		return found
	}
	for path, want := range map[string]map[string]bool{
		"/api/relics":                 {live.Relic.ID: false},
		"/api/relics?retired=include": {live.Relic.ID: false, retired.Relic.ID: true},
		"/api/relics?retired=only":    {retired.Relic.ID: true},
	} {
		if got := listed(path); !reflect.DeepEqual(got, want) {
			t.Errorf("GET %s lists %v, want %v", path, got, want)
		}
	}
}

func TestServeRelicBodyOpensFromTheRepository(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	fixture := createServedRelics(t, repository)
	address := startServeBoard(t, repository)

	response, err := boardTestClient.Get("http://" + address +
		"/relics/" + fixture.relic.ID + "/parts/" + fixture.relic.Parts[1].ID + "/body")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != servedHTMLBody {
		t.Fatalf("body = %d %q, want 200 %q", response.StatusCode, body, servedHTMLBody)
	}
	const policy = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; media-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'none'"
	if got := response.Header.Values("Content-Security-Policy"); !slices.Equal(got, []string{policy}) {
		t.Errorf("Content-Security-Policy = %q, want %q", got, policy)
	}
	if got := response.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
}
