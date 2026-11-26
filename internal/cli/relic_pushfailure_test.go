package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testenv"
)

// relicPushFailingGit writes a git that fails every push naming a relic ref
// the way a dropped connection does, and runs every other command with the
// real git. It returns the directory to put first on PATH.
func relicPushFailingGit(t *testing.T) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	testenv.WriteExecutable(t, filepath.Join(directory, "git"), []byte("#!/bin/sh\npush=no\nrelic=no\n"+
		"for arg in \"$@\"; do\n  case \"$arg\" in push) push=yes ;; *refs/workbook/relics/*) relic=yes ;; esac\ndone\n"+
		"if [ $push = yes ] && [ $relic = yes ]; then\n"+
		"  echo 'fatal: the remote end hung up unexpectedly' >&2\n  exit 128\nfi\n"+
		"exec '"+real+"' \"$@\"\n"))
	return directory
}

// A push whose task refs went out, one of them rejected, and whose relic push
// then failed on the wire reports both, in text and in JSON, and keeps the
// push phase completed with every task item.
func TestAFailedRelicPushDoesNotHideATaskRejection(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	first, second := cliSyncRepositories(t)
	task := cliCreateTask(t, second, "Shared")
	if code, stdout, stderr := run(t, second, "sync"); code != 0 {
		t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, first, "sync"); code != 0 {
		t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, first, "update", task.ID, "--title", "Renamed there"); code != 0 {
		t.Fatalf("update = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, second, "update", task.ID, "--title", "Renamed here", "--no-sync"); code != 0 {
		t.Fatalf("update = (%d, %q, %q)", code, stdout, stderr)
	}
	mustCreateRelic(t, second, "Waiting here", "--type", "spec", "--no-sync")
	path := "PATH=" + relicPushFailingGit(t) + string(os.PathListSeparator) + os.Getenv("PATH")

	push := func(args ...string) (int, string, string) {
		command := exec.Command(binary, append([]string{"push"}, args...)...)
		command.Dir = second
		command.Env = append(os.Environ(), path)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("push %v = %v, want it to fail", args, err)
		}
		return exit.ExitCode(), stdout.String(), stderr.String()
	}

	code, stdout, stderr := push("--json")
	if code != 1 {
		t.Fatalf("push --json exit = %d, want 1; stdout %s stderr %s", code, stdout, stderr)
	}
	var envelope struct {
		Data struct {
			Status string `json:"status"`
			Detail string `json:"detail"`
			Tasks  []struct {
				TaskID string `json:"taskId"`
				Status string `json:"status"`
			} `json:"tasks"`
		} `json:"data"`
		Error struct {
			Category string `json:"category"`
			Message  string `json:"message"`
		} `json:"error"`
	}
	// The result document goes to standard output, the error document to
	// standard error.
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode %s: %v", stdout, err)
	}
	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil {
		t.Fatalf("decode %s: %v", stderr, err)
	}
	if envelope.Data.Status != "completed" {
		t.Fatalf("push status = %q, want completed: %s", envelope.Data.Status, stdout)
	}
	if !strings.Contains(envelope.Data.Detail, "push failed before publishing relics") {
		t.Fatalf("push detail = %q, want the relic failure", envelope.Data.Detail)
	}
	if len(envelope.Data.Tasks) != 1 || envelope.Data.Tasks[0].TaskID != task.ID || envelope.Data.Tasks[0].Status != "rejected" {
		t.Fatalf("push tasks = %+v, want %s rejected", envelope.Data.Tasks, task.ID)
	}
	for _, want := range []string{"rejected by origin", "push failed before publishing relics", "hung up"} {
		if !strings.Contains(envelope.Error.Message, want) {
			t.Fatalf("error message %q, want it to say %q", envelope.Error.Message, want)
		}
	}
	if strings.Index(envelope.Error.Message, "rejected by origin") > strings.Index(envelope.Error.Message, "publishing relics") {
		t.Fatalf("error message %q names the relic failure before the task error", envelope.Error.Message)
	}

	code, stdout, stderr = push()
	if code != 1 {
		t.Fatalf("push exit = %d, want 1; stdout %s stderr %s", code, stdout, stderr)
	}
	for _, want := range []string{"rejected by origin", "push failed before publishing relics"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("push stderr %q, want it to say %q", stderr, want)
		}
	}
	if !strings.Contains(stdout, task.ID) || !strings.Contains(stdout, "rejected") {
		t.Fatalf("push stdout %q, want the rejected task", stdout)
	}
}
