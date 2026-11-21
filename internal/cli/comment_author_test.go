package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// Only a comment's author may change its text, on the command line and on the
// board; anybody may remove one, and the thread says who did. The rule is
// asserted identity: the author is the name the adding pack recorded, and the
// person editing is WORKBOOK_ACTOR, or else the checkout's `user.email`.

const (
	commentBob   = "bob@example.com"
	commentAlice = "alice@example.com"
)

// workbookRefs is every Workbook ref and what it points at, so a test can say
// that a refused command moved nothing.
func workbookRefs(t *testing.T, repository string) string {
	t.Helper()
	return cliGitOutput(t, repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/workbook/")
}

// commentedTask is a task bob filed and commented on, as bob.
func commentedTask(t *testing.T, repository string, bodies ...string) (core.Task, []string) {
	t.Helper()
	task := mustRunAs(t, repository, commentBob, "create", "Discussed task")
	for _, body := range bodies {
		task = mustRunAs(t, repository, commentBob, "update", task.ID, "--comment", body)
	}
	ids := make([]string, 0, len(task.Comments))
	for _, comment := range task.Comments {
		ids = append(ids, comment.ID)
	}
	return task, ids
}

// showDetailAs reads `show --json` as person.
func showDetailAs(t *testing.T, repository, person, taskID string) core.TaskDetail {
	t.Helper()
	code, stdout, stderr := runAs(t, repository, person, "show", taskID, "--json")
	if code != 0 {
		t.Fatalf("show as %s code = %d; stderr = %q", person, code, stderr)
	}
	var detail core.TaskDetail
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &detail); err != nil {
		t.Fatalf("decode show as %s: %v", person, err)
	}
	return detail
}

func TestEditingAnotherPersonsCommentIsRefusedOnTheCommandLine(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task, ids := commentedTask(t, repository, "bob's words")
	before := workbookRefs(t, repository)

	code, stdout, stderr := runAs(t, repository, commentAlice,
		"update", task.ID, "--edit-comment", ids[0], "--comment", "alice's words", "--no-sync", "--json")
	if code != 5 || stdout != "" {
		t.Fatalf("alice editing bob's comment code = %d, stdout = %q; want 5 and nothing", code, stdout)
	}
	assertJSONError(t, stderr, core.CategoryValidation, "")
	for _, want := range []string{ids[0], commentBob, "only its author may edit it", "anyone may remove it"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("refusal = %q, want it to mention %q", stderr, want)
		}
	}
	if after := workbookRefs(t, repository); after != before {
		t.Fatalf("a refused edit moved refs:\nbefore %s\nafter  %s", before, after)
	}
	if got := showDetailAs(t, repository, commentBob, task.ID).Comments[0].Body; got != "bob's words" {
		t.Fatalf("body after the refusal = %q, want bob's words", got)
	}

	edited := mustRunAs(t, repository, commentBob,
		"update", task.ID, "--edit-comment", ids[0], "--comment", "bob's better words")
	if got := edited.Comments[0]; got.Body != "bob's better words" || got.Author != commentBob || !got.Edited() {
		t.Fatalf("bob's own edit = %#v, want it recorded", got)
	}
}

// Without WORKBOOK_ACTOR the checkout's `user.email` is the person; with it,
// the variable is, even in the author's own checkout.
func TestWorkbookActorDecidesWhoMayEditAComment(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	gitOutput(t, repository, "config", "user.email", commentBob)
	task, ids := commentedTask(t, repository, "first")

	code, _, stderr := runWithActorEnvironment(t, repository, nil,
		"update", task.ID, "--edit-comment", ids[0], "--comment", "edited as the checkout", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("editing as the checkout's own address code = %d; stderr = %q", code, stderr)
	}
	before := workbookRefs(t, repository)
	code, _, stderr = runAs(t, repository, commentAlice,
		"update", task.ID, "--edit-comment", ids[0], "--comment", "edited as alice", "--no-sync", "--json")
	if code != 5 || !strings.Contains(stderr, "only its author may edit it") {
		t.Fatalf("WORKBOOK_ACTOR=alice in bob's checkout code = %d; stderr = %q; want the refusal", code, stderr)
	}
	if after := workbookRefs(t, repository); after != before {
		t.Fatalf("a refused edit moved refs:\nbefore %s\nafter  %s", before, after)
	}

	// And `show --json` answers "may I edit this" for the same person.
	if detail := showDetailAs(t, repository, commentAlice, task.ID); detail.Comments[0].Editable {
		t.Fatalf("show as alice = %#v, want bob's comment not editable", detail.Comments[0])
	}
	code, stdout, stderr := runWithActorEnvironment(t, repository, nil, "show", task.ID, "--json")
	if code != 0 {
		t.Fatalf("show as the checkout code = %d; stderr = %q", code, stderr)
	}
	var detail core.TaskDetail
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &detail); err != nil {
		t.Fatal(err)
	}
	if !detail.Comments[0].Editable {
		t.Fatalf("show as the checkout's own address = %#v, want bob's comment editable", detail.Comments[0])
	}
}

func TestAnyoneMayRemoveACommentAndTheThreadSaysWho(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task, ids := commentedTask(t, repository, "a remark that is about to go", "a remark that stays")

	removed := mustRunAs(t, repository, commentAlice, "update", task.ID, "--remove-comment", ids[0])
	if len(removed.Comments) != 1 || removed.Comments[0].ID != ids[1] {
		t.Fatalf("comments after alice's removal = %#v, want only the second", removed.Comments)
	}

	code, stdout, stderr := runAs(t, repository, commentBob, "show", task.ID)
	if code != 0 {
		t.Fatalf("show code = %d; stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "about to go") {
		t.Fatalf("show prints the removed text:\n%s", stdout)
	}
	if !strings.Contains(stdout, "a remark that stays") {
		t.Fatalf("show lost the live comment:\n%s", stdout)
	}
	var trace string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "\t"+ids[0]+"\t") {
			trace = line
		}
	}
	if !strings.HasPrefix(trace, "\t"+ids[0]+"\t"+commentBob+"\t") || !strings.Contains(trace, "(removed ") ||
		!strings.HasSuffix(trace, " by "+commentAlice+")") {
		t.Fatalf("removed comment's line = %q, want it to name bob as author and alice as remover:\n%s", trace, stdout)
	}
	if strings.Index(stdout, trace) > strings.Index(stdout, "a remark that stays") {
		t.Fatalf("the removed comment is not in its place in the thread:\n%s", stdout)
	}

	detail := showDetailAs(t, repository, commentBob, task.ID)
	if len(detail.RemovedComments) != 1 {
		t.Fatalf("removedComments = %#v, want one", detail.RemovedComments)
	}
	got := detail.RemovedComments[0]
	if got.ID != ids[0] || got.Author != commentBob || got.RemovedBy != commentAlice || got.RemovedAt.IsZero() || got.CreatedAt.IsZero() {
		t.Fatalf("removedComments[0] = %#v, want bob's comment removed by alice", got)
	}
	if !detail.Comments[0].Editable {
		t.Fatalf("bob's comments as bob reads them = %#v, want editable", detail.Comments)
	}
	if other := showDetailAs(t, repository, commentAlice, task.ID); other.Comments[0].Editable {
		t.Fatalf("bob's comments as alice reads them = %#v, want not editable", other.Comments)
	}
	// The JSON says it in so many words, with no member left for a client to
	// infer: the removed entry carries no body, and every live entry an
	// explicit `editable`.
	_, stdout, _ = runAs(t, repository, commentAlice, "show", task.ID, "--json")
	var raw struct {
		Data struct {
			Comments        []map[string]any `json:"comments"`
			RemovedComments []map[string]any `json:"removedComments"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatal(err)
	}
	if _, has := raw.Data.RemovedComments[0]["body"]; has {
		t.Fatalf("removedComments[0] = %v carries a body", raw.Data.RemovedComments[0])
	}
	if editable, has := raw.Data.Comments[0]["editable"]; !has || editable != false {
		t.Fatalf("comments[0] = %v, want editable:false spelled out", raw.Data.Comments[0])
	}
}

// craftForeignCommentEdit records, with Git plumbing, an edit of a comment by
// somebody other than its author — what a clone running a build from before
// the rule writes, and what this build refuses to. The commit names the pack's
// actor, as Workbook's own commits do.
func craftForeignCommentEdit(t *testing.T, repository, taskID, commentID, actor, body string) {
	t.Helper()
	ctx := context.Background()
	opened, err := gitstore.Open(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	config, err := opened.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	parent, err := opened.Get(ctx, config, taskID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := core.CryptoULIDSource{}.New()
	if err != nil {
		t.Fatal(err)
	}
	pack := core.NewOperationPack(config.ProjectID, taskID, parent.State.History.Generation, actor,
		parent.State.LogicalClock+1, time.Now().UTC().Truncate(time.Second),
		[]core.Operation{{ID: id, Type: core.OperationCommentEdit, CommentID: commentID, Body: body}})
	state, err := core.Apply(&parent.State, pack)
	if err != nil {
		t.Fatalf("Apply(crafted edit) error = %v", err)
	}
	packBytes, err := core.EncodeDocument(pack)
	if err != nil {
		t.Fatal(err)
	}
	stateBytes, err := core.EncodeDocument(state)
	if err != nil {
		t.Fatal(err)
	}
	plumb := func(input []byte, args ...string) string {
		command := exec.Command("git", append([]string{"-C", repository}, args...)...)
		command.Stdin = bytes.NewReader(input)
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME="+actor, "GIT_AUTHOR_EMAIL="+actor,
			"GIT_COMMITTER_NAME="+actor, "GIT_COMMITTER_EMAIL="+actor)
		output, err := command.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(output))
	}
	packBlob := plumb(packBytes, "hash-object", "-w", "--stdin")
	stateBlob := plumb(stateBytes, "hash-object", "-w", "--stdin")
	tree := plumb([]byte("100644 blob "+packBlob+"\toperation.json\n100644 blob "+stateBlob+"\tstate.json\n"), "mktree")
	commit := plumb([]byte("workbook: update comment edited"), "commit-tree", tree, "-p", parent.Head)
	plumb(nil, "update-ref", "refs/workbook/tasks/"+taskID, commit, parent.Head)
}

// threadOf is what a clone shows of a task's thread, with the parts that are
// the same claim on every clone: the head, the live comments as one reader
// reads them, and the removals.
type threadOf struct {
	Head     string
	Comments []core.ThreadComment
	Removed  []core.RemovedComment
}

func threadAs(t *testing.T, repository, person, taskID string) threadOf {
	t.Helper()
	detail := showDetailAs(t, repository, person, taskID)
	return threadOf{Head: detail.Head, Comments: detail.Comments, Removed: detail.RemovedComments}
}

func syncClone(t *testing.T, repository string) {
	t.Helper()
	if code, _, stderr := run(t, repository, "sync", "--json"); code != 0 {
		t.Fatalf("sync code = %d; stderr = %q", code, stderr)
	}
}

// An edit by somebody other than the author that is already in history — or
// that arrives from a clone running an older build — is folded as recorded:
// the thread shows the new text under the original author and names who made
// the edit. It reads the same after the cache is thrown away and rebuilt, and
// on a second clone that fetched it.
func TestANonAuthorEditInHistoryProjectsTheSameEverywhere(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	task, ids := commentedTask(t, first, "bob's words")
	craftForeignCommentEdit(t, first, task.ID, ids[0], commentAlice, "words alice put there")
	syncClone(t, first)
	syncClone(t, second)

	want := threadAs(t, first, commentBob, task.ID)
	got := want.Comments[0]
	if got.Author != commentBob || got.Body != "words alice put there" || got.EditedBy != commentAlice || !got.Editable {
		t.Fatalf("the foreign edit shows as %#v; want alice's text under bob, naming alice, editable by bob", got)
	}
	code, stdout, stderr := runAs(t, first, commentBob, "show", task.ID)
	if code != 0 || !strings.Contains(stdout, " by "+commentAlice+")") {
		t.Fatalf("show code = %d, stderr = %q; want the edit to name alice:\n%s", code, stderr, stdout)
	}

	if code, _, stderr := run(t, first, "rebuild", "--json"); code != 0 {
		t.Fatalf("rebuild code = %d; stderr = %q", code, stderr)
	}
	if rebuilt := threadAs(t, first, commentBob, task.ID); !reflect.DeepEqual(rebuilt, want) {
		t.Fatalf("after a rebuild:\n%#v\nwant\n%#v", rebuilt, want)
	}
	if fetched := threadAs(t, second, commentBob, task.ID); !reflect.DeepEqual(fetched, want) {
		t.Fatalf("on the second clone:\n%#v\nwant\n%#v", fetched, want)
	}

	// The rule still holds for new edits on either clone: alice cannot edit
	// it again, and bob can take his comment back.
	code, _, stderr = runAs(t, second, commentAlice,
		"update", task.ID, "--edit-comment", ids[0], "--comment", "again", "--no-sync", "--json")
	if code != 5 || !strings.Contains(stderr, "only its author may edit it") {
		t.Fatalf("alice editing again code = %d; stderr = %q", code, stderr)
	}
	mustRunAs(t, second, commentBob, "update", task.ID, "--edit-comment", ids[0], "--comment", "bob's words")
	if back := threadAs(t, second, commentBob, task.ID).Comments[0]; back.EditedBy != commentBob || back.EditedByOther() {
		t.Fatalf("after bob's own edit = %#v, want the latest edit to be bob's", back)
	}
}

// Two clones race an edit and a removal of the same comment, in both orders.
// Whichever reaches origin first, both clones end on the same head with the
// comment removed and the thread naming who removed it — a removal is not
// undone by an edit — and neither person's command was refused.
func TestTwoClonesRacingAnEditAndARemovalConverge(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"removal first", "edit first"} {
		t.Run(order, func(t *testing.T) {
			t.Parallel()
			first, second := cliSyncRepositories(t)
			task, ids := commentedTask(t, first, "contested words")
			syncClone(t, first)
			syncClone(t, second)

			mustRunAs(t, first, commentBob, "update", task.ID, "--edit-comment", ids[0], "--comment", "bob's revision")
			mustRunAs(t, second, commentAlice, "update", task.ID, "--remove-comment", ids[0])
			if order == "removal first" {
				syncClone(t, second)
				syncClone(t, first)
				syncClone(t, second)
			} else {
				syncClone(t, first)
				syncClone(t, second)
				syncClone(t, first)
			}

			one := threadAs(t, first, commentBob, task.ID)
			two := threadAs(t, second, commentBob, task.ID)
			if !reflect.DeepEqual(one, two) {
				t.Fatalf("the clones disagree:\nfirst  %#v\nsecond %#v", one, two)
			}
			if len(one.Comments) != 0 || len(one.Removed) != 1 || one.Removed[0].ID != ids[0] ||
				one.Removed[0].RemovedBy != commentAlice || one.Removed[0].Author != commentBob {
				t.Fatalf("thread = %#v, want bob's comment removed by alice", one)
			}
		})
	}
}

// The board answers for the person on each request: it refuses alice's edit
// of bob's comment with a client error and records nothing, accepts bob's,
// lets alice remove it, and says in its task answer who removed it.
func TestTheBoardLetsOnlyAnAuthorEditTheirComment(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	gitOutput(t, repository, "config", "user.email", "server@example.test")
	ctx, cancel := context.WithCancel(withRequestActor(context.Background(), func(request *http.Request) (string, bool) {
		person := request.Header.Get(testActorHeader)
		return person, person != ""
	}))
	reserved := reserveListener(t)
	result := make(chan error, 1)
	var serveStdout, serveStderr bytes.Buffer
	go func() {
		result <- runServeWith(ctx, reserved.listen, []string{"--addr", reserved.addr}, repository, &serveStdout, &serveStderr)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-result; err != nil {
			t.Errorf("runServeWith() error = %v; stderr = %q", err, serveStderr.String())
		}
	})
	waitForHTTP(t, "http://"+reserved.addr+"/healthz")
	base := "http://" + reserved.addr

	request := func(method, path, body, person string) ([]byte, int) {
		t.Helper()
		contents, status, err := boardRequestAs(method, base+path, body, person)
		if err != nil {
			t.Fatal(err)
		}
		return contents, status
	}
	type commentAnswer struct {
		Task             core.Task `json:"task"`
		EditableComments []string  `json:"editableComments"`
	}
	decodeAnswer := func(body []byte, status int) commentAnswer {
		t.Helper()
		if status != http.StatusOK {
			t.Fatalf("comment route = %d; body = %s", status, body)
		}
		var answer commentAnswer
		if err := json.Unmarshal(body, &answer); err != nil {
			t.Fatal(err)
		}
		return answer
	}

	createdBody, createdStatus := request(http.MethodPost, "/api/tasks",
		`{"title":"Board thread","description":"","status":"ready","priority":"medium","labels":[]}`, commentBob)
	created := decodeServeMutation(t, createdBody, createdStatus)
	posted := decodeAnswer(request(http.MethodPost, "/api/tasks/"+created.ID+"/comments",
		`{"body":"bob's remark","expectedHead":"`+created.Head+`"}`, commentBob))
	commentID := posted.Task.Comments[0].ID
	if posted.Task.Comments[0].Author != commentBob || !reflect.DeepEqual(posted.EditableComments, []string{commentID}) {
		t.Fatalf("bob's comment answer = %#v, want it authored and editable by bob", posted)
	}

	// The poll answers "which are yours" for each request's person.
	for person, want := range map[string][]string{commentBob: {commentID}, commentAlice: nil} {
		body, status := request(http.MethodGet, "/api/tasks", "", person)
		if status != http.StatusOK {
			t.Fatalf("poll as %s = %d", person, status)
		}
		var document struct {
			Presentation []struct {
				TaskID           string   `json:"taskId"`
				EditableComments []string `json:"editableComments"`
			} `json:"presentation"`
		}
		if err := json.Unmarshal(body, &document); err != nil {
			t.Fatal(err)
		}
		if len(document.Presentation) != 1 || !reflect.DeepEqual(document.Presentation[0].EditableComments, want) {
			t.Fatalf("poll as %s presentation = %#v, want editable %v", person, document.Presentation, want)
		}
	}

	before := workbookRefs(t, repository)
	body, status := request(http.MethodPatch, "/api/tasks/"+created.ID+"/comments/"+commentID,
		`{"body":"alice's rewrite"}`, commentAlice)
	if status != http.StatusBadRequest {
		t.Fatalf("alice editing bob's comment = %d; body = %s; want 400", status, body)
	}
	var refusal struct {
		Error struct {
			Category core.Category `json:"category"`
			Message  string        `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Error.Category != core.CategoryValidation || !strings.Contains(refusal.Error.Message, "only its author may edit it") {
		t.Fatalf("refusal = %#v, want the authorship rule's validation error", refusal.Error)
	}
	if after := workbookRefs(t, repository); after != before {
		t.Fatalf("a refused board edit moved refs:\nbefore %s\nafter  %s", before, after)
	}

	edited := decodeAnswer(request(http.MethodPatch, "/api/tasks/"+created.ID+"/comments/"+commentID,
		`{"body":"bob's revision"}`, commentBob))
	if edited.Task.Comments[0].Body != "bob's revision" {
		t.Fatalf("bob's edit = %#v", edited.Task.Comments)
	}

	removed := decodeAnswer(request(http.MethodDelete, "/api/tasks/"+created.ID+"/comments/"+commentID, "", commentAlice))
	if len(removed.Task.Comments) != 0 || removed.EditableComments == nil || len(removed.EditableComments) != 0 {
		t.Fatalf("alice's removal answer = %#v, want the comment gone and editableComments []", removed)
	}

	body, status = request(http.MethodGet, "/api/tasks/"+created.ID, "", commentBob)
	if status != http.StatusOK {
		t.Fatalf("task answer = %d; body = %s", status, body)
	}
	var document struct {
		Format  string          `json:"format"`
		Version int             `json:"version"`
		Task    core.TaskDetail `json:"task"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if document.Format != "workbook.task" || document.Version != 1 || len(document.Task.RemovedComments) != 1 {
		t.Fatalf("task answer = %s, want one removed comment", body)
	}
	if got := document.Task.RemovedComments[0]; got.ID != commentID || got.Author != commentBob || got.RemovedBy != commentAlice {
		t.Fatalf("removed comment = %#v, want bob's, removed by alice", got)
	}
	if strings.Contains(string(body), "revision") {
		t.Fatalf("the task answer carries the removed text: %s", body)
	}
}

// commentLine is the line `show` prints for one comment of the thread, live or
// removed, and empty when the thread has none for it.
func commentLine(stdout, commentID string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "\t"+commentID+"\t") {
			return line
		}
	}
	return ""
}

// The reviewer's reproduction, end to end on two clones. Bob comments twice;
// alice, on a build without the rule, rewrites his first comment; she removes
// his second; the task is finished and compacted. Before compaction the thread
// names alice for both. After it, on both clones, the checkpoint keeps the edit
// time and not the editor, and the removal is gone with the operations that
// recorded it. The thread then says the edit's editor is not recorded, so
// alice's words never read as bob's own, and draws nothing for the removal.
func TestACompactedThreadSaysItNoLongerRecordsAnEarlierEditor(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	task, ids := commentedTask(t, first, "bob's words", "bob's second remark")
	craftForeignCommentEdit(t, first, task.ID, ids[0], commentAlice, "words alice put there")
	mustRunAs(t, first, commentAlice, "update", task.ID, "--remove-comment", ids[1])
	syncClone(t, first)
	syncClone(t, second)

	for _, clone := range []string{first, second} {
		code, stdout, stderr := runAs(t, clone, commentBob, "show", task.ID)
		if code != 0 {
			t.Fatalf("show before compaction code = %d; stderr = %q", code, stderr)
		}
		if edited := commentLine(stdout, ids[0]); !strings.HasSuffix(edited, " by "+commentAlice+")") || !strings.Contains(edited, "(edited ") {
			t.Fatalf("before compaction the edited comment's line = %q, want it to name alice:\n%s", edited, stdout)
		}
		if removed := commentLine(stdout, ids[1]); !strings.Contains(removed, "(removed ") || !strings.HasSuffix(removed, " by "+commentAlice+")") {
			t.Fatalf("before compaction the removed comment's line = %q, want it to name alice:\n%s", removed, stdout)
		}
	}

	mustRunAs(t, first, commentBob, "update", task.ID, "--status", "done")
	syncClone(t, first)
	compactRun(t, first, "compact", "--done-only", "--no-backup")
	syncClone(t, second)
	compacted := showDetailAs(t, first, commentBob, task.ID)
	if fetched := showDetailAs(t, second, commentBob, task.ID); fetched.Head != compacted.Head || fetched.HistoryGeneration != compacted.HistoryGeneration {
		t.Fatalf("second clone at %s (generation %s), want the compacted %s (generation %s)",
			fetched.Head, fetched.HistoryGeneration, compacted.Head, compacted.HistoryGeneration)
	}
	if compacted.HistoryGeneration == task.HistoryGeneration {
		t.Fatalf("history generation = %s, want compaction to have raised it", compacted.HistoryGeneration)
	}

	for name, clone := range map[string]string{"first": first, "second": second} {
		code, stdout, stderr := runAs(t, clone, commentBob, "show", task.ID)
		if code != 0 {
			t.Fatalf("%s clone: show code = %d; stderr = %q", name, code, stderr)
		}
		edited := commentLine(stdout, ids[0])
		if !strings.HasPrefix(edited, "\t"+ids[0]+"\t"+commentBob+"\t") || !strings.HasSuffix(edited, ", editor not recorded)") {
			t.Fatalf("%s clone: the edited comment's line = %q, want it marked as edited by someone the history no longer records:\n%s", name, edited, stdout)
		}
		if strings.Contains(stdout, commentAlice) {
			t.Fatalf("%s clone: show names alice, which the compacted history cannot:\n%s", name, stdout)
		}
		if removed := commentLine(stdout, ids[1]); removed != "" || strings.Contains(stdout, "(removed ") {
			t.Fatalf("%s clone: show still draws the earlier removal as %q, want its line gone:\n%s", name, removed, stdout)
		}
		if !strings.Contains(stdout, "words alice put there") {
			t.Fatalf("%s clone: show lost the edited text:\n%s", name, stdout)
		}

		_, stdout, _ = runAs(t, clone, commentBob, "show", task.ID, "--json")
		var raw struct {
			Data struct {
				Comments        []map[string]any `json:"comments"`
				RemovedComments []map[string]any `json:"removedComments"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
			t.Fatalf("%s clone: decode show --json: %v", name, err)
		}
		if raw.Data.RemovedComments != nil {
			t.Fatalf("%s clone: removedComments = %v, want the member absent", name, raw.Data.RemovedComments)
		}
		if len(raw.Data.Comments) != 1 {
			t.Fatalf("%s clone: comments = %v, want the edited one alone", name, raw.Data.Comments)
		}
		entry := raw.Data.Comments[0]
		if entry["editorNotRecorded"] != true {
			t.Fatalf("%s clone: comments[0] = %v, want editorNotRecorded: true", name, entry)
		}
		if _, has := entry["editedBy"]; has {
			t.Fatalf("%s clone: comments[0] = %v names an editor the history no longer holds", name, entry)
		}
	}

	// An edit made after the compaction is on the chain again, so the thread
	// names its maker and no longer says the editor is unrecorded.
	mustRunAs(t, second, commentBob, "update", task.ID, "--edit-comment", ids[0], "--comment", "bob's words again")
	if after := showDetailAs(t, second, commentBob, task.ID).Comments[0]; after.EditedBy != commentBob || after.EditorNotRecorded {
		t.Fatalf("after bob's own edit = %#v, want bob named and nothing unrecorded", after)
	}
}
