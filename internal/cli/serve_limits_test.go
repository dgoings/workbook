package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
	"github.com/dgoings/workbook/internal/webui"
)

func TestBoardWorkLimitsReadTheOptionThenTheEnvironment(t *testing.T) {
	t.Parallel()
	environment := func(value string) func(string) string {
		return func(name string) string {
			if name == serveMaxRequestsVariable {
				return value
			}
			return ""
		}
	}
	for _, test := range []struct {
		name      string
		flag      string
		flagGiven bool
		env       string
		want      int
	}{
		{name: "neither", want: webui.DefaultMaxRequests},
		{name: "environment", env: "6", want: 6},
		{name: "option", flag: "3", flagGiven: true, want: 3},
		{name: "option over environment", flag: "3", flagGiven: true, env: "6", want: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits, err := boardWorkLimits(test.flag, test.flagGiven, environment(test.env))
			if err != nil {
				t.Fatalf("boardWorkLimits() error = %v", err)
			}
			if limits.MaxRequests != test.want {
				t.Fatalf("MaxRequests = %d, want %d", limits.MaxRequests, test.want)
			}
		})
	}
}

func TestBoardWorkLimitsRefuseAValueThatIsNotACap(t *testing.T) {
	t.Parallel()
	// Production mutation: reading 0 or garbage as "no cap", or quietly as the
	// default, turns a typo in a service definition into an uncapped shared
	// board nobody is told about.
	for _, test := range []struct {
		flag      string
		flagGiven bool
		env       string
		source    string
	}{
		{flag: "0", flagGiven: true, source: "--max-requests"},
		{flag: "", flagGiven: true, source: "--max-requests"},
		{flag: "-4", flagGiven: true, source: "--max-requests"},
		{flag: "many", flagGiven: true, source: "--max-requests"},
		{env: "0", source: serveMaxRequestsVariable},
		{env: "unlimited", source: serveMaxRequestsVariable},
	} {
		_, err := boardWorkLimits(test.flag, test.flagGiven, func(string) string { return test.env })
		if err == nil {
			t.Fatalf("boardWorkLimits(%q, %v, env %q) = nil error, want a refusal", test.flag, test.flagGiven, test.env)
		}
		if core.CategoryOf(err) != core.CategoryInvocation || !strings.Contains(err.Error(), test.source) {
			t.Fatalf("error = %v (%s), want an invocation error naming %s", err, core.CategoryOf(err), test.source)
		}
	}
}

// The cap through the real serve command, holding a real Git process.
//
// A pre-receive hook on origin holds the first create's inline push until the
// test lets it go, so that create sits in the board's only write slot for as
// long as the test needs and no longer. Nothing here waits on a clock to
// decide anything: the hook announces itself with a file, and the second
// create's refusal is an answer the test reads, whenever it comes.
func TestRunServeCapsWritesWhileAPushHoldsTheWriteSlot(t *testing.T) {
	t.Parallel()
	repository, _ := cliSyncRepositories(t)
	bare := strings.TrimSpace(gitOutput(t, repository, "remote", "get-url", "origin"))
	gate := t.TempDir()
	release := filepath.Join(gate, "release")
	hook := "#!/bin/sh\n" +
		"touch '" + filepath.Join(gate, "held") + "'.$$\n" +
		"while [ ! -e '" + release + "' ]; do sleep 0.05; done\n" +
		"cat >/dev/null\nexit 0\n"
	testenv.WriteExecutable(t, filepath.Join(bare, "hooks", "pre-receive"), []byte(hook))

	// Two requests at once, so writes have one slot and reads keep the other.
	addr := startServeBoardWith(t, repository, "--max-requests", "2")
	// Registered after the board, so it runs first: the board cannot stop while
	// its request is still held inside the hook.
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	base := "http://" + addr
	if body, status := boardRequest(t, http.MethodPut, base+"/api/sync", `{"mode":"inline"}`); status != http.StatusOK {
		t.Fatalf("PUT /api/sync = %d, want %d; body = %s", status, http.StatusOK, body)
	}
	before := len(taskRefs(t, repository))

	held := make(chan int, 1)
	go func() {
		// Not boardRequest: a test may only be failed from its own goroutine.
		request, err := http.NewRequest(http.MethodPost, base+"/api/tasks", strings.NewReader(`{"title":"Held in the push"}`))
		if err != nil {
			held <- -1
			return
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			held <- -1
			return
		}
		response.Body.Close()
		held <- response.StatusCode
	}()
	// Hang guard, not a measurement: the hook runs as soon as the create's
	// push reaches origin.
	deadline := time.Now().Add(60 * time.Second)
	for {
		matches, _ := filepath.Glob(filepath.Join(gate, "held.*"))
		if len(matches) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the create's push never reached origin's hook")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The board's own refresh still answers while the write slot is held.
	if body, status := boardRequest(t, http.MethodGet, base+"/api/tasks", ""); status != http.StatusOK {
		t.Fatalf("GET /api/tasks while writes are saturated = %d, want %d; body = %s", status, http.StatusOK, body)
	}
	// A second create waits its turn, gets none, and is refused in words a
	// client can retry on, without having written anything.
	request, err := http.NewRequest(http.MethodPost, base+"/api/tasks", strings.NewReader(`{"title":"Refused at the cap"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	// Hang guard, not a measurement: the board refuses this create once its
	// five-second wait is up. A board that admitted it instead would leave it
	// held in the hook beside the first, and this says so rather than hanging.
	response, err := (&http.Client{Timeout: 60 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("second create was not refused while the write slot was held: %v", err)
	}
	var document webui.ErrorDocument
	decodeErr := json.NewDecoder(response.Body).Decode(&document)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") == "" {
		t.Fatalf("second create = %d with Retry-After %q, want %d with one", response.StatusCode, response.Header.Get("Retry-After"), http.StatusServiceUnavailable)
	}
	if decodeErr != nil || document.Format != "workbook.error" || !strings.Contains(document.Error.Message, "busy") {
		t.Fatalf("second create body = %+v (%v), want a workbook.error saying the board is busy", document, decodeErr)
	}
	if body, status := boardRequest(t, http.MethodGet, base+"/healthz", ""); status != http.StatusOK {
		t.Fatalf("GET /healthz while writes are saturated = %d, want %d; body = %s", status, http.StatusOK, body)
	}
	if after := len(taskRefs(t, repository)); after != before+1 {
		t.Fatalf("task refs = %d, want %d: only the held create may have written", after, before+1)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if status := <-held; status != http.StatusOK {
		t.Fatalf("held create = %d, want %d once its push was let go", status, http.StatusOK)
	}
	if body, status := boardRequest(t, http.MethodPost, base+"/api/tasks", `{"title":"After the cap cleared"}`); status != http.StatusOK {
		t.Fatalf("create after the slot freed = %d, want %d; body = %s", status, http.StatusOK, body)
	}
}
