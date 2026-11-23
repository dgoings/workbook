package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/testrepo"
)

// gitTraceCursor reads the top-level Git processes a traced Workbook process
// started since the last read, from its trace2 event file.
type gitTraceCursor struct {
	path   string
	offset int64
}

// gitCost is how many Git processes, and how many of them connected to a
// remote, a stretch of a trace holds, with each connection's command.
type gitCost struct {
	processes   int
	connections []string
	// commands are the processes' Git subcommands, for saying where two
	// costs differ.
	commands []string
}

func (cost gitCost) String() string {
	return fmt.Sprintf("%d processes, %d connections %v", cost.processes, len(cost.connections), cost.connections)
}

func (cursor *gitTraceCursor) next(t *testing.T) gitCost {
	t.Helper()
	file, err := os.Open(cursor.path)
	if os.IsNotExist(err) {
		return gitCost{}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Seek(cursor.offset, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(file)
	var cost gitCost
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			// A partial last line is read again next time.
			break
		}
		cursor.offset += int64(len(line))
		var event struct {
			Event string   `json:"event"`
			SID   string   `json:"sid"`
			Argv  []string `json:"argv"`
		}
		if json.Unmarshal(line, &event) != nil || event.Event != "start" || strings.Contains(event.SID, "/") {
			continue
		}
		cost.processes++
		argv := event.Argv
		if len(argv) > 2 && argv[1] == "-C" {
			argv = append([]string{argv[0]}, argv[3:]...)
		}
		if len(argv) > 1 {
			command := argv[1]
			if len(argv) > 2 && !strings.HasPrefix(argv[2], "-") && (command == "cat-file" || command == "for-each-ref" || command == "rev-parse") {
				command += " " + argv[2]
			} else if len(argv) > 2 {
				command += " " + argv[2]
			}
			cost.commands = append(cost.commands, normalizeSessionText(command, nil))
		}
		if len(argv) > 1 {
			switch argv[1] {
			case "ls-remote", "fetch", "push":
				cost.connections = append(cost.connections, argv[1])
			}
		}
	}
	return cost
}

// costProject is a project set up the way a teammate's clone of it is, with
// the tasks on origin: one that never moved them, or one whose tasks went to
// their own repository and came back.
func costProject(t *testing.T, movedAndBack bool) (tasksRemoteProject, string) {
	t.Helper()
	project := codeProject(t, testrepo.FormatSHA1)
	if movedAndBack {
		project.moveTasks(t)
		if code, stdout, stderr := run(t, project.seed, "config", "unset", "tasks-remote"); code != 0 {
			t.Fatalf("config unset = (%d, %q, %q)", code, stdout, stderr)
		}
		if code, stdout, stderr := run(t, project.seed, "move-tasks"); code != 0 {
			t.Fatalf("move-tasks back = (%d, %q, %q)", code, stdout, stderr)
		}
		cliGit(t, project.seed, "add", ".workbook/config.json")
		cliGit(t, project.seed, "commit", "--quiet", "-m", "Move the tasks back")
		cliGit(t, project.seed, "push", "--quiet", "origin", "main")
	}
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, clone, "sync"); code != 0 {
		t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
	}
	return project, clone
}

// tracedCommand runs one Workbook command with trace2 on and returns its cost.
func tracedCommand(t *testing.T, binary, clone string, env []string, args ...string) (gitCost, []byte) {
	t.Helper()
	cursor := &gitTraceCursor{path: filepath.Join(t.TempDir(), "trace2.json")}
	command := exec.Command(binary, args...)
	command.Dir = clone
	command.Env = append(append(os.Environ(), env...), "GIT_TRACE2_EVENT="+cursor.path)
	var stderr strings.Builder
	command.Stderr = &stderr
	stdout, err := command.Output()
	if err != nil {
		t.Fatalf("%v: %v\n%s%s", args, err, stdout, stderr.String())
	}
	return cursor.next(t), stdout
}

// startTracedProcess starts a long-running Workbook process with trace2 on.
func startTracedProcess(t *testing.T, binary, clone string, args ...string) (*gitTraceCursor, *lockedWriter, func()) {
	t.Helper()
	cursor := &gitTraceCursor{path: filepath.Join(t.TempDir(), "trace2.json")}
	command := exec.Command(binary, args...)
	command.Dir = clone
	command.Env = append(os.Environ(), "GIT_TRACE2_EVENT="+cursor.path)
	output := &lockedWriter{}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}
	t.Cleanup(stop)
	return cursor, output, stop
}

// settle waits until a traced process has started no Git process for a
// while, and returns what it ran until then.
func (cursor *gitTraceCursor) settle(t *testing.T) gitCost {
	t.Helper()
	var total gitCost
	quiet := 0
	deadline := time.Now().Add(30 * time.Second)
	for quiet < 10 {
		if time.Now().After(deadline) {
			t.Fatal("the traced process never went quiet")
		}
		time.Sleep(100 * time.Millisecond)
		cost := cursor.next(t)
		if cost.processes == 0 {
			quiet++
			continue
		}
		quiet = 0
		total.processes += cost.processes
		total.connections = append(total.connections, cost.connections...)
		total.commands = append(total.commands, cost.commands...)
	}
	return total
}

// costsOf measures, in one clone, an inline update, an update handed to a
// watcher and the watcher's round that publishes it, and a board save
// published inline and one handed to the watcher with the watcher's round.
//
// The watcher waits an hour between scheduled rounds, so the only round it
// runs while measured is the one a handed change asks for. The board runs no
// loop of its own while the watcher owns the repository; its own loop is the
// same code on a five-second timer, whose rounds would land in a measurement
// or not depending on timing.
func costsOf(t *testing.T, binary string, project tasksRemoteProject, clone string) map[string]gitCost {
	t.Helper()
	costs := map[string]gitCost{}
	task := project.ids[0]
	costs["update inline"], _ = tracedCommand(t, binary, clone, nil, "update", task, "--title", "Retitled inline", "--json")

	watcher, watcherOutput, stopWatcher := startTracedProcess(t, binary, clone, "sync", "--watch", "--interval", "1h")
	deadline := time.Now().Add(30 * time.Second)
	for {
		status := exec.Command(binary, "sync", "--status", "--json")
		status.Dir = clone
		output, _ := status.Output()
		if strings.Contains(string(output), `"lastSyncOk":true`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the watcher never synchronized; it wrote %q", watcherOutput.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	watcher.settle(t)
	handed, output := tracedCommand(t, binary, clone, nil, "update", task, "--title", "Retitled through the watcher", "--json")
	if !strings.Contains(string(output), `"status":"deferred"`) {
		t.Fatalf("the update was not handed to the watcher: %s", output)
	}
	costs["update handed to a watcher"] = handed
	costs["watcher round"] = watcher.settle(t)

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	probe.Close()
	board, boardOutput, stopBoard := startTracedProcess(t, binary, clone, "serve", "--addr", address)
	waitForHTTP(t, "http://"+address+"/healthz")
	base := "http://" + address
	board.settle(t)
	if body, status := boardRequest(t, http.MethodGet, base+"/api/sync", ""); status != http.StatusOK ||
		!strings.Contains(string(body), `"watcher":true`) {
		t.Fatalf("GET /api/sync = %d; body = %s, want the watcher answering", status, body)
	}
	for _, mode := range []string{"inline", "deferred"} {
		if body, status := boardRequest(t, http.MethodPut, base+"/api/sync", `{"mode":"`+mode+`"}`); status != http.StatusOK {
			t.Fatalf("PUT /api/sync %s = %d; body = %s", mode, status, body)
		}
		board.settle(t)
		watcher.settle(t)
		saved := boardSave(t, http.MethodPatch, base+"/api/tasks/"+task, `{"title":"Saved on the board, `+mode+`"}`)
		if len(saved.Warnings) != 0 {
			t.Fatalf("%s save warnings = %+v; the board wrote %q", mode, saved.Warnings, boardOutput.String())
		}
		costs["board save "+mode] = board.settle(t)
		if mode == "deferred" {
			costs["board save's watcher round"] = watcher.settle(t)
		}
	}
	stopBoard()
	stopWatcher()
	return costs
}

// A project whose tasks went to their own repository and came back costs
// what one that never moved them costs: the same connections to origin for an
// update published inline, one handed to a watcher and the watcher's round,
// and a board save published inline and one handed to the watcher with the
// watcher's round. Git processes are logged, not compared.
func TestTasksRemoteMovedAndBackCostsWhatANeverMovedProjectCosts(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	never, neverClone := costProject(t, false)
	back, backClone := costProject(t, true)
	neverCosts := costsOf(t, binary, never, neverClone)
	backCosts := costsOf(t, binary, back, backClone)
	for _, name := range []string{
		"update inline", "update handed to a watcher", "watcher round",
		"board save inline", "board save deferred", "board save's watcher round",
	} {
		t.Logf("%s: never moved %s; moved and back %s", name, neverCosts[name], backCosts[name])
		if extra, missing := commandDifference(backCosts[name].commands, neverCosts[name].commands); len(extra)+len(missing) > 0 {
			t.Logf("%s: the moved-and-back project runs %v more and %v fewer", name, extra, missing)
		}
		if fmt.Sprint(neverCosts[name].connections) != fmt.Sprint(backCosts[name].connections) {
			t.Errorf("%s: a moved-and-back project costs %s, want what a never-moved one costs, %s",
				name, backCosts[name], neverCosts[name])
		}
	}
}

// commandDifference is the commands in a but not b, and in b but not a,
// counting repeats.
func commandDifference(a, b []string) (extra, missing []string) {
	counts := map[string]int{}
	for _, command := range a {
		counts[command]++
	}
	for _, command := range b {
		counts[command]--
	}
	for command, count := range counts {
		for ; count > 0; count-- {
			extra = append(extra, command)
		}
		for ; count < 0; count++ {
			missing = append(missing, command)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	return extra, missing
}
