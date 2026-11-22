package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// These tests put many requests on one board at the same moment, which is what
// a board shared by a team, or hosted behind a sign-in proxy, sees as a matter
// of course. Every request refreshes the shared SQLite projection from the
// task refs before it answers, so the requests race each other — and any other
// process using the same repository — over the same cache rows. Losing that
// race used to answer 500 and tell the person to run `workbook rebuild`, which
// would have changed nothing: the cache was fine, and the request had simply
// found it updated by somebody else.
//
// What each test holds the board to is the same: every read is answered,
// every write is either done or refused with the stale-write answer a client
// may act on by reading again and resending, nothing is answered 500, nothing
// advises a rebuild, and nothing a refused request asked for was written.
//
// None of them depends on how fast anything runs. The races are made likely
// by sheer number — hundreds of requests sixteen at a time — and what is
// asserted holds for every interleaving, so a slow machine only lowers how
// many races happen, never whether the test passes.

// raceWorkers is how many requests are in flight at once.
const raceWorkers = 16

// raceGuard bounds each test. It is a hang guard, not a measurement: a run
// that takes longer than this has stopped making progress, and a slow machine
// only makes a healthy run take a larger fraction of it.
const raceGuard = 10 * time.Minute

// raceReply is one answer, kept with what the request was so a failure can say
// which one it was.
type raceReply struct {
	what   string
	body   []byte
	status int
	err    error
	// read marks a request that changes nothing. A read is never refused:
	// another request refreshing the cache at the same moment is no reason
	// not to answer it.
	read bool
	// sentHead is the expectedHead a write sent, so a test can check that
	// what it wrote was written on exactly that head.
	sentHead string
}

// raceClient waits as long as the test does. The package's board client
// gives up after thirty seconds, which is a hang guard for one request on an
// idle machine; a race loads the machine on purpose, and under -race with its
// siblings running beside it a healthy request can take longer than that. A
// request that never comes back still fails, at raceGuard.
var raceClient = &http.Client{Timeout: raceGuard}

// raceRequest sends one request the way the board's own page does: every
// mutation declares JSON, because the same-origin guard refuses one that does
// not.
func raceRequest(method, url, body string) raceReply {
	reply := raceReply{what: method + " " + url, read: method == http.MethodGet}
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		reply.err = err
		return reply
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := raceClient.Do(request)
	if err != nil {
		reply.err = err
		return reply
	}
	defer response.Body.Close()
	reply.status = response.StatusCode
	reply.body, reply.err = io.ReadAll(response.Body)
	return reply
}

// race runs jobs from raceWorkers goroutines that all start at once and
// returns every reply in job order. It fails the test if the jobs have not all
// finished within raceGuard.
func race(t *testing.T, jobs []func() []raceReply) []raceReply {
	t.Helper()
	replies := make([][]raceReply, len(jobs))
	next := make(chan int)
	start := make(chan struct{})
	var group sync.WaitGroup
	for range raceWorkers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for index := range next {
				replies[index] = jobs[index]()
			}
		}()
	}
	done := make(chan struct{})
	began := time.Now()
	go func() {
		close(start)
		for index := range jobs {
			next <- index
		}
		close(next)
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(raceGuard):
		t.Fatalf("%d racing jobs did not finish within %s", len(jobs), raceGuard)
	}
	elapsed := time.Since(began)
	var flat []raceReply
	for _, job := range replies {
		flat = append(flat, job...)
	}
	// Logged, never asserted: the rate is what a review compares across
	// builds, and a test that asserted it would fail on a busy machine.
	t.Logf("%d requests from %d workers in %s (%.0f requests/s)",
		len(flat), raceWorkers, elapsed.Round(time.Millisecond), float64(len(flat))/elapsed.Seconds())
	return flat
}

func one(job func() raceReply) func() []raceReply {
	return func() []raceReply { return []raceReply{job()} }
}

// raceVerdict is what one answer was: written, refused with an answer the
// client may resend, or a failure of the test.
type raceVerdict int

const (
	raceWritten raceVerdict = iota
	raceRetryable
	raceFailed
)

// judgeRace sorts one answer. The only refusal allowed is the stale-write
// answer, 409 with that category, and only for a write. Any answer that tells
// the person to run `workbook rebuild` fails, in its error or in a warning
// beside a success: nothing in these tests damages the cache, so a rebuild
// would change nothing.
func judgeRace(reply raceReply) (raceVerdict, string) {
	if reply.err != nil {
		return raceFailed, fmt.Sprintf("%s: %v", reply.what, reply.err)
	}
	if strings.Contains(string(reply.body), "workbook rebuild") {
		return raceFailed, fmt.Sprintf("%s = %d advises a rebuild for a passing race; body = %s", reply.what, reply.status, reply.body)
	}
	switch {
	case reply.status == http.StatusOK:
		return raceWritten, ""
	case reply.status == http.StatusConflict && !reply.read:
		var document struct {
			Error struct {
				Category core.Category `json:"category"`
			} `json:"error"`
		}
		if err := json.Unmarshal(reply.body, &document); err != nil {
			return raceFailed, fmt.Sprintf("%s = 409 with an undecodable body: %v; body = %s", reply.what, err, reply.body)
		}
		if document.Error.Category != core.CategoryStaleWrite {
			return raceFailed, fmt.Sprintf("%s = 409 %s, want a stale-write refusal; body = %s", reply.what, document.Error.Category, reply.body)
		}
		return raceRetryable, ""
	case reply.read:
		return raceFailed, fmt.Sprintf("%s = %d, want every read answered 200; body = %.400s", reply.what, reply.status, reply.body)
	default:
		return raceFailed, fmt.Sprintf("%s = %d, want 200 or a stale-write 409; body = %.400s", reply.what, reply.status, reply.body)
	}
}

// tallyRace judges every answer and fails the test, after counting them all,
// if any was neither answered nor retryably refused. It returns which replies
// succeeded, in order, and logs the counts a review compares across runs.
func tallyRace(t *testing.T, replies []raceReply) []bool {
	t.Helper()
	succeeded := make([]bool, len(replies))
	counts := map[raceVerdict]int{}
	var failures []string
	for index, reply := range replies {
		verdict, problem := judgeRace(reply)
		counts[verdict]++
		succeeded[index] = verdict == raceWritten
		if verdict == raceFailed {
			failures = append(failures, problem)
		}
	}
	t.Logf("%d answers: %d succeeded, %d refused retryably, %d failed",
		len(replies), counts[raceWritten], counts[raceRetryable], counts[raceFailed])
	if len(failures) > 0 {
		t.Fatalf("%d of %d racing requests failed; first: %s", len(failures), len(replies), failures[0])
	}
	return succeeded
}

// createJob files one task on the board at base.
func createJob(base, title string) func() raceReply {
	return func() raceReply {
		return raceRequest(http.MethodPost, base+"/api/tasks",
			fmt.Sprintf(`{"title":%q,"description":"","status":"ready","priority":"medium","labels":[]}`, title))
	}
}

// editJob retitles one task on the board at base, on the head the caller saw.
func editJob(base, taskID, head, title string) raceReply {
	reply := raceRequest(http.MethodPatch, base+"/api/tasks/"+taskID,
		fmt.Sprintf(`{"title":%q,"expectedHead":%q}`, title, head))
	reply.sentHead = head
	return reply
}

// moveJob moves one task on the board at base to the end of another column,
// on the head the caller saw.
func moveJob(base, taskID, head string, status core.Status) raceReply {
	reply := raceRequest(http.MethodPatch, base+"/api/tasks/"+taskID+"/status",
		fmt.Sprintf(`{"status":%q,"expectedHead":%q}`, status, head))
	reply.sentHead = head
	return reply
}

// readHead reads one task's detail on the board at base and returns its head,
// with the read's own answer for judging.
func readHead(base, taskID string) (raceReply, string) {
	reply := raceRequest(http.MethodGet, base+"/api/tasks/"+taskID, "")
	if reply.err != nil || reply.status != http.StatusOK {
		return reply, ""
	}
	var document struct {
		Task struct {
			Head string `json:"head"`
		} `json:"task"`
	}
	if err := json.Unmarshal(reply.body, &document); err != nil || document.Task.Head == "" {
		reply.status = http.StatusTeapot
		reply.body = append([]byte("undecodable task detail: "), reply.body...)
		return reply, ""
	}
	return reply, document.Task.Head
}

// writtenTasks decodes every succeeded write's task.
func writtenTasks(t *testing.T, replies []raceReply, succeeded []bool) []core.Task {
	t.Helper()
	var tasks []core.Task
	for index, ok := range succeeded {
		if ok && !replies[index].read {
			tasks = append(tasks, decodeServeMutation(t, replies[index].body, replies[index].status))
		}
	}
	return tasks
}

// distinctIDs fails the test if two tasks share an ID and returns the set.
func distinctIDs(t *testing.T, tasks []core.Task) map[string]bool {
	t.Helper()
	ids := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		if ids[task.ID] {
			t.Fatalf("two creates answered the same task %s", task.ID)
		}
		ids[task.ID] = true
	}
	return ids
}

// assertBoardHolds checks that the board lists exactly ids and that each has a
// ref of its own: a refused create wrote nothing, and a written one is there.
func assertBoardHolds(t *testing.T, repository, base string, ids map[string]bool) {
	t.Helper()
	listed := boardTaskIDs(t, base+"/api/tasks")
	if len(listed) != len(ids) {
		t.Fatalf("board lists %d tasks, want the %d whose creates succeeded", len(listed), len(ids))
	}
	for _, id := range listed {
		if !ids[id] {
			t.Fatalf("board lists task %s that no create answered", id)
		}
	}
	if got := taskRefCount(t, repository); got != len(ids) {
		t.Fatalf("%d task refs exist, want the %d whose creates succeeded", got, len(ids))
	}
}

// assertWrittenOnSentHeads checks each succeeded edit was written exactly once,
// directly on the head its request named, and that the task's history is the
// creation plus those edits and nothing else.
func assertWrittenOnSentHeads(t *testing.T, repository, taskID string, edits []raceReply) {
	t.Helper()
	tip := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+taskID)
	history := strings.Split(gitOutput(t, repository, "rev-list", tip), "\n")
	if want := len(edits) + 1; len(history) != want {
		t.Fatalf("%s has %d commits, want the creation and its %d succeeded edits", taskID, len(history), len(edits))
	}
	for _, edit := range edits {
		var document struct {
			Task core.Task `json:"task"`
		}
		if err := json.Unmarshal(edit.body, &document); err != nil {
			t.Fatalf("decode %s: %v", edit.what, err)
		}
		if parent := gitOutput(t, repository, "rev-parse", document.Task.Head+"^"); parent != edit.sentHead {
			t.Fatalf("%s wrote %s on %s, want it written on the head it sent, %s",
				edit.what, document.Task.Head, parent, edit.sentHead)
		}
	}
}

// createTasks files count tasks on the board one after another, before a race.
func createTasks(t *testing.T, base string, count int) []core.Task {
	t.Helper()
	tasks := make([]core.Task, count)
	for index := range tasks {
		reply := createJob(base, fmt.Sprintf("Edited by the race %02d", index))()
		if reply.err != nil {
			t.Fatal(reply.err)
		}
		tasks[index] = decodeServeMutation(t, reply.body, reply.status)
	}
	return tasks
}

// Two hundred and forty creates, sixteen at a time. Each create reads the
// board to rank the new card before it writes, and every one of those reads
// finds the cache moved by the others.
func TestServeSimultaneousCreatesAreWrittenOrRefusedRetryably(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	base := "http://" + startServeBoard(t, repository)

	const creates = 240
	jobs := make([]func() []raceReply, creates)
	for index := range jobs {
		jobs[index] = one(createJob(base, fmt.Sprintf("Raced create %03d", index)))
	}
	replies := race(t, jobs)
	ids := distinctIDs(t, writtenTasks(t, replies, tallyRace(t, replies)))
	if len(ids) == 0 {
		t.Fatal("no simultaneous create was written")
	}
	assertBoardHolds(t, repository, base, ids)
}

// Thirty-two tasks, each edited eight times in a row by its own request
// stream, all streams at once. The edits alternate a retitle with a move to
// the other column, and after each one the stream reads the board the way the
// page's poll does: a move ranks the card against its new column and the poll
// lists every task, so both read the whole board, and those reads race every
// other stream's writes. No two requests touch the same task, so no edit has a
// reason to be refused, every read is answered, and each task ends at its
// stream's last edit with exactly its stream's edits in its history.
func TestServeSimultaneousEditsToDifferentTasksAreWrittenOrRefusedRetryably(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	base := "http://" + startServeBoard(t, repository)
	tasks := createTasks(t, base, 32)

	const editsPerTask = 8
	jobs := make([]func() []raceReply, len(tasks))
	for index, task := range tasks {
		jobs[index] = func() []raceReply {
			head := task.Head
			replies := make([]raceReply, 0, 2*editsPerTask)
			for edit := range editsPerTask {
				var reply raceReply
				if edit%2 == 0 {
					reply = editJob(base, task.ID, head, fmt.Sprintf("%s, edit %d", task.Title, edit))
				} else {
					reply = moveJob(base, task.ID, head, []core.Status{core.StatusInProgress, core.StatusReady}[(edit/2)%2])
				}
				if reply.err == nil && reply.status == http.StatusOK {
					var document struct {
						Task core.Task `json:"task"`
					}
					if json.Unmarshal(reply.body, &document) == nil {
						head = document.Task.Head
					}
				}
				replies = append(replies, reply, raceRequest(http.MethodGet, base+"/api/tasks", ""))
			}
			return replies
		}
	}
	replies := race(t, jobs)
	succeeded := tallyRace(t, replies)

	for index, task := range tasks {
		var written []raceReply
		var last string
		for edit := range editsPerTask {
			position := (index*editsPerTask + edit) * 2
			if !succeeded[position] {
				continue
			}
			written = append(written, replies[position])
			if edit%2 == 0 {
				var document struct {
					Task core.Task `json:"task"`
				}
				if err := json.Unmarshal(replies[position].body, &document); err != nil {
					t.Fatal(err)
				}
				last = document.Task.Title
			}
		}
		assertWrittenOnSentHeads(t, repository, task.ID, written)
		if last == "" {
			continue
		}
		reply, _ := readHead(base, task.ID)
		if !strings.Contains(string(reply.body), fmt.Sprintf("%q", last)) {
			t.Fatalf("%s reads back as %.300s, want its last succeeded edit %q", task.ID, reply.body, last)
		}
	}
}

// Two hundred and forty edits to one task, each a read of the task, an edit
// on the head that read returned, and a read of the board the way the page's
// poll follows a write, sixteen at a time. Most edits lose the race to the
// ref, which is the board's stale-write answer today; none may be answered
// anything else, every read is answered, and the edits that won are the whole
// of the task's history after its creation, each written on the head it named.
func TestServeSimultaneousEditsToOneTaskAreWrittenOrRefusedAsStale(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	base := "http://" + startServeBoard(t, repository)
	task := createTasks(t, base, 1)[0]

	const edits = 240
	jobs := make([]func() []raceReply, edits)
	for index := range jobs {
		jobs[index] = func() []raceReply {
			read, head := readHead(base, task.ID)
			if head == "" {
				return []raceReply{read}
			}
			return []raceReply{
				read,
				editJob(base, task.ID, head, fmt.Sprintf("Contested edit %03d", index)),
				raceRequest(http.MethodGet, base+"/api/tasks", ""),
			}
		}
	}
	replies := race(t, jobs)
	succeeded := tallyRace(t, replies)

	var written []raceReply
	for index, reply := range replies {
		if succeeded[index] && !reply.read {
			written = append(written, reply)
		}
	}
	if len(written) == 0 {
		t.Fatal("no contested edit was written")
	}
	assertWrittenOnSentHeads(t, repository, task.ID, written)
}

// Reads of every kind the board serves — the page, the task list, a task's
// detail and its history — mixed with creates and edits, sixteen at a time.
// Every read is answered, whatever the writes beside it are doing.
func TestServeMixedSimultaneousReadsAndWritesAreAnswered(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	base := "http://" + startServeBoard(t, repository)
	tasks := createTasks(t, base, 32)

	const requests = 400
	jobs := make([]func() []raceReply, requests)
	for index := range jobs {
		task := tasks[index%len(tasks)]
		switch index % 5 {
		case 0:
			jobs[index] = one(func() raceReply { return raceRequest(http.MethodGet, base+"/", "") })
		case 1:
			jobs[index] = one(func() raceReply { return raceRequest(http.MethodGet, base+"/api/tasks", "") })
		case 2:
			jobs[index] = func() []raceReply {
				read, head := readHead(base, task.ID)
				if head == "" {
					return []raceReply{read}
				}
				return []raceReply{read, editJob(base, task.ID, head, fmt.Sprintf("Mixed edit %03d", index))}
			}
		case 3:
			jobs[index] = one(createJob(base, fmt.Sprintf("Mixed create %03d", index)))
		case 4:
			jobs[index] = one(func() raceReply {
				return raceRequest(http.MethodGet, base+"/api/tasks/"+task.ID+"/history", "")
			})
		}
	}
	replies := race(t, jobs)
	succeeded := tallyRace(t, replies)

	ids := make(map[string]bool)
	for _, task := range tasks {
		ids[task.ID] = true
	}
	for index, reply := range replies {
		if succeeded[index] && reply.what == http.MethodPost+" "+base+"/api/tasks" {
			ids[decodeServeMutation(t, reply.body, reply.status).ID] = true
		}
	}
	assertBoardHolds(t, repository, base, ids)
}

// processReply runs the real binary as one racing request: exit 0 is
// success, exit 6 is the stale-write refusal with its JSON envelope on
// stderr, and anything else fails the test.
func processReply(binary, repository string, read bool, args ...string) raceReply {
	what := "workbook " + strings.Join(args, " ")
	code, stdout, stderr, err := runBinaryOutcome(binary, repository, args...)
	reply := raceReply{what: what, err: err, read: read}
	switch code {
	case 0:
		reply.status = http.StatusOK
		reply.body = []byte(stdout)
	case 6:
		reply.status = http.StatusConflict
		reply.body = []byte(stderr)
	default:
		reply.status = 1000 + code
		reply.body = []byte(stderr)
	}
	return reply
}

// runBinaryOutcome is runBinary for a worker goroutine, which may not fail
// the test itself: a process that could not be started comes back as an
// error for the tally to report.
func runBinaryOutcome(binary, repository string, args ...string) (int, string, string, error) {
	command := exec.Command(binary, args...)
	command.Dir = repository
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, stdout.String(), stderr.String(), nil
	case asExitError(err, &exit):
		return exit.ExitCode(), stdout.String(), stderr.String(), nil
	default:
		return -1, stdout.String(), stderr.String(), err
	}
}

// The board and the command line in the same folder, as two real processes:
// each has its own SQLite connections to the one cache the repository's
// worktrees share, and only separate processes share nothing else. The
// command line's creates and lists race the board's creates and lists.
func TestServeAndCommandLineProcessesShareOneCache(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	repository := initializedRepository(t)
	base := "http://" + startServeBoard(t, repository)

	const requests = 160
	jobs := make([]func() []raceReply, requests)
	for index := range jobs {
		switch index % 4 {
		case 0:
			jobs[index] = one(createJob(base, fmt.Sprintf("Board create %03d", index)))
		case 1:
			jobs[index] = one(func() raceReply { return raceRequest(http.MethodGet, base+"/api/tasks", "") })
		case 2:
			jobs[index] = one(func() raceReply {
				return processReply(binary, repository, false, "create", fmt.Sprintf("Command-line create %03d", index), "--no-sync", "--json")
			})
		case 3:
			jobs[index] = one(func() raceReply { return processReply(binary, repository, true, "list", "--json") })
		}
	}
	replies := race(t, jobs)
	succeeded := tallyRace(t, replies)

	ids := make(map[string]bool)
	for index, reply := range replies {
		if !succeeded[index] || reply.read {
			continue
		}
		var task core.Task
		if strings.HasPrefix(reply.what, "workbook ") {
			task = decodeMutationTask(t, string(reply.body), "create")
		} else {
			task = decodeServeMutation(t, reply.body, reply.status)
		}
		if ids[task.ID] {
			t.Fatalf("two creates answered the same task %s", task.ID)
		}
		ids[task.ID] = true
	}
	assertBoardHolds(t, repository, base, ids)
}

// A board whose background synchronization keeps fetching tasks another clone
// publishes to a temporary bare origin, and keeps publishing the board's own,
// while the board is answering requests. Each synchronization refreshes the
// same cache the requests are refreshing; the board's synchronization state,
// which reports the last refresh's failure, is read throughout.
func TestServeRacesItsOwnBackgroundSynchronization(t *testing.T) {
	t.Parallel()
	board, other := cliSyncRepositories(t)
	base := "http://" + startServeBoard(t, board)

	const requests = 240
	var published sync.Map
	jobs := make([]func() []raceReply, requests)
	for index := range jobs {
		switch index % 6 {
		case 0, 1:
			jobs[index] = one(createJob(base, fmt.Sprintf("Board create %03d", index)))
		case 2:
			jobs[index] = one(func() raceReply { return raceRequest(http.MethodGet, base+"/api/tasks", "") })
		case 3:
			jobs[index] = one(func() raceReply { return raceRequest(http.MethodGet, base+"/api/sync", "") })
		case 4:
			// Another clone files a task and publishes it, so the board's
			// next synchronization fetches a ref it has to project.
			jobs[index] = one(func() raceReply {
				var stdout, stderr bytes.Buffer
				code := Run(context.Background(), []string{"create", fmt.Sprintf("Published elsewhere %03d", index), "--json"},
					other, strings.NewReader(""), &stdout, &stderr)
				reply := raceReply{what: "other clone: workbook create", status: http.StatusOK, body: stdout.Bytes()}
				if code != 0 {
					reply.status, reply.body = 1000+code, stderr.Bytes()
					return reply
				}
				var result struct {
					Data core.Task `json:"data"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &result); err == nil {
					published.Store(result.Data.ID, true)
				}
				return reply
			})
		case 5:
			jobs[index] = one(func() raceReply { return raceRequest(http.MethodGet, base+"/", "") })
		}
	}
	replies := race(t, jobs)
	succeeded := tallyRace(t, replies)

	ids := make(map[string]bool)
	for index, reply := range replies {
		if succeeded[index] && reply.what == http.MethodPost+" "+base+"/api/tasks" {
			ids[decodeServeMutation(t, reply.body, reply.status).ID] = true
		}
	}
	// The other clone publishes inline, so origin has its tasks now; the
	// board shows them once its own synchronization has fetched them.
	if code, _, stderr := run(t, other, "sync"); code != 0 {
		t.Fatalf("other clone's final sync code = %d; stderr = %q", code, stderr)
	}
	published.Range(func(key, _ any) bool {
		ids[key.(string)] = true
		return true
	})
	deadline := time.Now().Add(raceGuard)
	for {
		listed := boardTaskIDs(t, base+"/api/tasks")
		missing := 0
		for id := range ids {
			if !contains(listed, id) {
				missing++
			}
		}
		if missing == 0 && len(listed) == len(ids) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("board lists %d tasks with %d of the %d written still missing after %s", len(listed), missing, len(ids), raceGuard)
		}
		// Nudge the board's loop the way a write does, rather than waiting
		// out its schedule.
		reply := raceRequest(http.MethodGet, base+"/api/sync", "")
		if verdict, problem := judgeRace(reply); verdict == raceFailed {
			t.Fatal(problem)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Two worktrees of one repository share one cache under the common Git
// directory, and here each has a board of its own. Creates and reads go to
// both boards at once, and both end up listing every task either wrote.
func TestServeBoardsInTwoWorktreesShareOneCache(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	gitOutput(t, repository, "add", ".workbook/config.json")
	gitOutput(t, repository, "commit", "--quiet", "-m", "Initialize Workbook")
	second := filepath.Join(t.TempDir(), "second")
	gitOutput(t, repository, "worktree", "add", "--quiet", "-b", "second", second)
	bases := []string{"http://" + startServeBoard(t, repository), "http://" + startServeBoard(t, second)}

	const requests = 320
	jobs := make([]func() []raceReply, requests)
	for index := range jobs {
		base := bases[index%2]
		if index%4 < 2 {
			jobs[index] = one(createJob(base, fmt.Sprintf("Worktree create %03d", index)))
		} else {
			jobs[index] = one(func() raceReply { return raceRequest(http.MethodGet, base+"/api/tasks", "") })
		}
	}
	replies := race(t, jobs)
	succeeded := tallyRace(t, replies)

	ids := make(map[string]bool)
	for index, reply := range replies {
		if succeeded[index] && !reply.read {
			task := decodeServeMutation(t, reply.body, reply.status)
			if ids[task.ID] {
				t.Fatalf("two creates answered the same task %s", task.ID)
			}
			ids[task.ID] = true
		}
	}
	for _, base := range bases {
		assertBoardHolds(t, repository, base, ids)
	}
}
