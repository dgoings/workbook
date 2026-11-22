package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// What a sign-in proxy in front of the board looks like from the board's side:
// requests addressed to the public name, an Origin the browser sets for that
// name, and the signed-in person's email in a header the proxy sets. The
// header is the one Cloudflare Access sets, which is the guide's example.
const (
	proxyHost   = "tasks.example.com"
	proxyOrigin = "https://tasks.example.com"
	proxyHeader = "Cf-Access-Authenticated-User-Email"
)

// hostedServe is one `workbook serve` started the way the guide starts it.
type hostedServe struct {
	addr   string
	stderr *lockedWriter
}

// startHostedServe runs serve in folder with the environment env and the extra
// arguments given, on a reserved loopback port, until the test ends.
func startHostedServe(t *testing.T, folder string, env map[string]string, args ...string) *hostedServe {
	t.Helper()
	reserved := reserveListener(t)
	return startHostedServeOn(t, folder, reserved, env, args...)
}

func startHostedServeOn(t *testing.T, folder string, reserved *reservedListener, env map[string]string, args ...string) *hostedServe {
	t.Helper()
	lookup := func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
	board := &hostedServe{addr: reserved.addr, stderr: &lockedWriter{}}
	// WORKBOOK_ACTOR goes on the context the way Run puts it there, so a test
	// that sets it is about the variable and not about a context without it.
	actorCtx, err := core.WithEnvironmentActor(context.Background(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(actorCtx)
	result := make(chan error, 1)
	stdout := &lockedWriter{}
	go func() {
		result <- runServeWithEnvironment(ctx, lookup, reserved.listen, append([]string{"--addr", reserved.addr}, args...), folder, stdout, board.stderr)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-result:
			if err != nil {
				t.Errorf("serve error = %v; stderr = %q", err, board.stderr.String())
			}
		case <-time.After(30 * time.Second):
			t.Error("serve did not stop")
		}
		if stdout.String() != "" {
			t.Errorf("serve stdout = %q, want empty", stdout.String())
		}
	})
	waitForHTTP(t, "http://"+reserved.addr+"/healthz")
	return board
}

// proxied is one request as the proxy forwards it to the board.
type proxied struct {
	method string
	path   string
	body   string
	// person is the header's value; empty sends no header at all.
	person string
	// host is the Host header, the public name unless a test says otherwise.
	host string
	// origin is the Origin header; a mutation gets the public origin, as a
	// browser at the public address sends it, unless a test says otherwise.
	origin string
	// header is a further header to set, for a test about some other header.
	header func(http.Header)
}

func (board *hostedServe) do(t *testing.T, request proxied) ([]byte, int, http.Header) {
	t.Helper()
	body, status, header, err := board.send(request)
	if err != nil {
		t.Fatal(err)
	}
	return body, status, header
}

func (board *hostedServe) send(request proxied) ([]byte, int, http.Header, error) {
	var reader io.Reader
	if request.body != "" {
		reader = strings.NewReader(request.body)
	}
	outgoing, err := http.NewRequest(request.method, "http://"+board.addr+request.path, reader)
	if err != nil {
		return nil, 0, nil, err
	}
	outgoing.Host = proxyHost
	if request.host != "" {
		outgoing.Host = request.host
	}
	origin := request.origin
	if origin == "" && request.method != http.MethodGet {
		origin = proxyOrigin
	}
	if origin != "-" && origin != "" {
		outgoing.Header.Set("Origin", origin)
	}
	if request.method != http.MethodGet {
		outgoing.Header.Set("Content-Type", "application/json")
	}
	if request.person != "" {
		outgoing.Header.Set(proxyHeader, request.person)
	}
	if request.header != nil {
		request.header(outgoing.Header)
	}
	response, err := boardTestClient.Do(outgoing)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("%s %s: %w", request.method, request.path, err)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	return contents, response.StatusCode, response.Header, err
}

// hostedFolder is the guide's server: a tasks-only folder pointed at a
// project's origin, with an identity of its own, and the contributor's
// ordinary code checkout of the same project beside it.
func hostedFolder(t *testing.T) (folder, origin, contributor string) {
	t.Helper()
	origin, contributor, _ = tasksOnlyOriginWithCode(t)
	folder = t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin); code != 0 {
		t.Fatalf("setup --tasks-only = (%d, %q, %q)", code, stdout, stderr)
	}
	cliGit(t, folder, "config", "user.name", "Board Server")
	cliGit(t, folder, "config", "user.email", "board@example.test")
	return folder, origin, contributor
}

// waitForOrigin waits until origin's ref for taskID is head.
func waitForOrigin(t *testing.T, origin, taskID, head string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, _ := exec.Command("git", "-C", origin, "rev-parse", "--verify", "--quiet", "refs/workbook/tasks/"+taskID).Output()
		got := strings.TrimSpace(string(out))
		if got == head {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("origin's %s = %q, never reached %s", taskID, got, head)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func historyActors(t *testing.T, checkout, taskID string) []string {
	t.Helper()
	code, stdout, stderr := run(t, checkout, "show", taskID, "--history", "--json")
	if code != 0 {
		t.Fatalf("show --history code = %d; stderr = %q", code, stderr)
	}
	var detail core.TaskDetail
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &detail); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	actors := make([]string, 0, len(detail.History.Changes))
	for _, change := range detail.History.Changes {
		actors = append(actors, change.Actor)
	}
	return actors
}

// The heart of the story. Two people signed in through the proxy each file a
// task and edit the other's, and a contributor's ordinary code checkout of
// the same project shows every edit under the person who made it — not under
// the server's identity, and not under each other's.
func TestServeBehindAProxyRecordsEachPersonsEditUnderTheirName(t *testing.T) {
	t.Parallel()
	folder, origin, contributor := hostedFolder(t)
	board := startHostedServe(t, folder, nil, "--public-url", proxyOrigin, "--actor-header", proxyHeader)
	const alice, bob = "alice@client.example", "bob@client.example"

	created := map[string]core.Task{}
	for _, person := range []string{alice, bob} {
		body, status, _ := board.do(t, proxied{method: http.MethodPost, path: "/api/tasks",
			body: `{"title":"Filed by ` + person + `"}`, person: person})
		created[person] = decodeServeMutation(t, body, status)
	}
	// Each edits the other's task.
	edited := map[string]core.Task{}
	for person, other := range map[string]string{alice: bob, bob: alice} {
		task := created[other]
		body, status, _ := board.do(t, proxied{method: http.MethodPatch, path: "/api/tasks/" + task.ID,
			body: `{"description":"Edited by ` + person + `","expectedHead":"` + task.Head + `"}`, person: person})
		edited[other] = decodeServeMutation(t, body, status)
	}
	for _, person := range []string{alice, bob} {
		waitForOrigin(t, origin, edited[person].ID, edited[person].Head)
	}

	if code, _, stderr := run(t, contributor, "sync"); code != 0 {
		t.Fatalf("contributor sync code = %d; stderr = %q", code, stderr)
	}
	for owner, other := range map[string]string{alice: bob, bob: alice} {
		got := historyActors(t, contributor, created[owner].ID)
		// Oldest first: the owner's create, then the other's edit.
		if want := []string{owner, other}; !reflect.DeepEqual(got, want) {
			t.Errorf("contributor's history of %s's task = %v, want %v", owner, got, want)
		}
	}
}

// Who is looking matters for reads: each person sees their own assignment as
// theirs to remove and the other's as not, the service refuses each the
// other's, each may edit only their own comment, and a comment may be removed
// by anyone, as Workbook's comment rule says.
func TestServeBehindAProxyAnswersAssignmentsAndCommentsPerPerson(t *testing.T) {
	t.Parallel()
	folder, _, _ := hostedFolder(t)
	board := startHostedServe(t, folder, nil, "--public-url", proxyOrigin, "--actor-header", proxyHeader)
	const alice, bob = "alice@client.example", "bob@client.example"
	people := []string{alice, bob}
	other := map[string]string{alice: bob, bob: alice}

	assigned := map[string]core.Task{}
	for _, person := range people {
		body, status, _ := board.do(t, proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Held by ` + person + `"}`, person: person})
		task := decodeServeMutation(t, body, status)
		body, status, _ = board.do(t, proxied{method: http.MethodPost, path: "/api/tasks/" + task.ID + "/assignments",
			body: `{"to":"","expectedHead":"` + task.Head + `"}`, person: person})
		task, views := decodeServeAssignment(t, body, status)
		if len(task.Assignments) != 1 || task.Assignments[0].Principal != person || len(views) != 1 || !views[0].Removable {
			t.Fatalf("%s assigning self = %#v %#v, want %s's own, removable", person, task.Assignments, views, person)
		}
		assigned[person] = task
	}
	for _, person := range people {
		body, status, _ := board.do(t, proxied{method: http.MethodGet, path: "/api/tasks", person: person})
		if status != http.StatusOK {
			t.Fatalf("poll as %s = %d; body = %s", person, status, body)
		}
		var document struct {
			Presentation []struct {
				TaskID      string             `json:"taskId"`
				Assignments []servedAssignment `json:"assignments"`
			} `json:"presentation"`
		}
		if err := json.Unmarshal(body, &document); err != nil {
			t.Fatal(err)
		}
		for _, row := range document.Presentation {
			for _, owner := range people {
				if row.TaskID == assigned[owner].ID && (len(row.Assignments) != 1 || row.Assignments[0].Removable != (owner == person)) {
					t.Errorf("as %s, %s's assignment = %#v, want removable only by %s", person, owner, row.Assignments, owner)
				}
			}
		}
		// The page itself names the person whose assignments are theirs.
		page, status, _ := board.do(t, proxied{method: http.MethodGet, path: "/", person: person})
		if status != http.StatusOK || !strings.Contains(string(page), `"`+person+`"`) || strings.Contains(string(page), other[person]+`"`) && !strings.Contains(string(page), "Held by "+other[person]) {
			t.Fatalf("page as %s = %d, want it to name %s", person, status, person)
		}
	}
	for _, person := range people {
		owner := other[person]
		body, status, _ := board.do(t, proxied{method: http.MethodDelete, path: "/api/tasks/" + assigned[owner].ID + "/assignments",
			body: `{"from":"` + owner + `","expectedHead":"` + assigned[owner].Head + `"}`, person: person})
		if status != http.StatusBadRequest || !strings.Contains(string(body), "may be removed only by") {
			t.Fatalf("%s removing %s's assignment = %d; body = %s", person, owner, status, body)
		}
	}
	for _, person := range people {
		body, status, _ := board.do(t, proxied{method: http.MethodDelete, path: "/api/tasks/" + assigned[person].ID + "/assignments",
			body: `{"from":"` + person + `","expectedHead":"` + assigned[person].Head + `"}`, person: person})
		if withdrawn, _ := decodeServeAssignment(t, body, status); len(withdrawn.Assignments) != 0 {
			t.Fatalf("%s withdrawing their own left %#v", person, withdrawn.Assignments)
		}
	}

	// Comments: bob writes one; only bob may edit it; alice may remove it.
	task := assigned[bob]
	body, status, _ := board.do(t, proxied{method: http.MethodGet, path: "/api/tasks/" + task.ID, person: bob})
	if status != http.StatusOK {
		t.Fatalf("read bob's task = %d; body = %s", status, body)
	}
	var detail struct {
		Task core.TaskDetail `json:"task"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	body, status, _ = board.do(t, proxied{method: http.MethodPost, path: "/api/tasks/" + task.ID + "/comments",
		body: `{"body":"bob's remark","expectedHead":"` + detail.Task.Head + `"}`, person: bob})
	var posted struct {
		Task             core.Task `json:"task"`
		EditableComments []string  `json:"editableComments"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &posted) != nil || len(posted.Task.Comments) != 1 {
		t.Fatalf("bob's comment = %d; body = %s", status, body)
	}
	comment := posted.Task.Comments[0]
	if comment.Author != bob || !reflect.DeepEqual(posted.EditableComments, []string{comment.ID}) {
		t.Fatalf("bob's comment = %#v editable %v, want bob's and editable by bob", comment, posted.EditableComments)
	}
	body, status, _ = board.do(t, proxied{method: http.MethodPatch, path: "/api/tasks/" + task.ID + "/comments/" + comment.ID,
		body: `{"body":"alice's rewrite"}`, person: alice})
	if status != http.StatusBadRequest || !strings.Contains(string(body), "only its author may edit it") {
		t.Fatalf("alice editing bob's comment = %d; body = %s", status, body)
	}
	body, status, _ = board.do(t, proxied{method: http.MethodPatch, path: "/api/tasks/" + task.ID + "/comments/" + comment.ID,
		body: `{"body":"bob's revision"}`, person: bob})
	if status != http.StatusOK || !strings.Contains(string(body), "bob's revision") {
		t.Fatalf("bob editing his comment = %d; body = %s", status, body)
	}
	body, status, _ = board.do(t, proxied{method: http.MethodDelete, path: "/api/tasks/" + task.ID + "/comments/" + comment.ID,
		body: `{}`, person: alice})
	if status != http.StatusOK {
		t.Fatalf("alice removing bob's comment = %d; body = %s", status, body)
	}
	body, status, _ = board.do(t, proxied{method: http.MethodGet, path: "/api/tasks/" + task.ID, person: bob})
	if status != http.StatusOK || !strings.Contains(string(body), `"removedBy":"`+alice+`"`) {
		t.Fatalf("bob's task after alice's removal = %d; body = %s; want it removed by alice", status, body)
	}
}

// With the header on, a request with no name is refused, reads and the page
// included, and nothing is recorded; the health check is the one exception.
// A header sent twice, a list in one header and a name that is not a usable
// actor are each refused, and none is recorded or replaced by the server's
// name.
func TestServeBehindAProxyRefusesARequestWithoutOneUsableName(t *testing.T) {
	t.Parallel()
	folder, _, _ := hostedFolder(t)
	board := startHostedServe(t, folder, nil, "--public-url", proxyOrigin, "--actor-header", proxyHeader)
	before := workbookRefs(t, folder)

	for _, path := range []string{"/", "/tasks/new", "/config", "/api/tasks", "/api/vocabulary", "/api/sync"} {
		body, status, header := board.do(t, proxied{method: http.MethodGet, path: path})
		if status != http.StatusUnauthorized {
			t.Fatalf("GET %s with no name = %d; body = %s; want 401", path, status, body)
		}
		assertBoardRefusal(t, body, header, core.CategoryInvocation, "came without one")
	}
	// A person's browser opening the page with no name sees one sentence.
	body, status, header := board.do(t, proxied{method: http.MethodGet, path: "/", header: func(h http.Header) {
		h.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	}})
	if status != http.StatusUnauthorized || !strings.HasPrefix(header.Get("Content-Type"), "text/plain") ||
		strings.Count(strings.TrimSpace(string(body)), "\n") != 0 || !strings.Contains(string(body), "sign-in proxy") {
		t.Fatalf("browser with no name = %d %q; body = %q, want one plain sentence", status, header.Get("Content-Type"), body)
	}
	body, status, _ = board.do(t, proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Nobody's"}`})
	if status != http.StatusUnauthorized {
		t.Fatalf("create with no name = %d; body = %s", status, body)
	}
	body, status, _ = board.do(t, proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Blank"}`,
		header: func(h http.Header) { h.Set(proxyHeader, "") }})
	if status != http.StatusUnauthorized {
		t.Fatalf("create with a blank name = %d; body = %s", status, body)
	}
	// The health check answers whoever asks, because hosting platforms call it
	// directly rather than through the proxy.
	if _, status, _ := board.do(t, proxied{method: http.MethodGet, path: "/healthz", host: board.addr}); status != http.StatusOK {
		t.Fatalf("health check with no name = %d", status)
	}

	// Each refusal names its own reason, so a list is refused as a list
	// rather than only because a list happens not to be an email address.
	for name, refused := range map[string]struct {
		header func(http.Header)
		says   string
	}{
		"sent twice": {func(h http.Header) {
			h.Add(proxyHeader, "alice@client.example")
			h.Add(proxyHeader, "mallory@client.example")
		}, "was sent more than once"},
		"a list":              {func(h http.Header) { h.Set(proxyHeader, "alice@client.example,mallory@client.example") }, "carries several values"},
		"a list with a space": {func(h http.Header) { h.Set(proxyHeader, "alice@client.example, mallory@client.example") }, "carries several values"},
		"not an address":      {func(h http.Header) { h.Set(proxyHeader, "not an address") }, "must be an email address"},
		"with a label":        {func(h http.Header) { h.Set(proxyHeader, "alice@client.example/agent") }, "with no /label"},
		"angle brackets":      {func(h http.Header) { h.Set(proxyHeader, "<alice@client.example>") }, "acting person"},
	} {
		for _, request := range []proxied{
			{method: http.MethodGet, path: "/api/tasks", header: refused.header},
			{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Chosen name"}`, header: refused.header},
		} {
			body, status, response := board.do(t, request)
			if status != http.StatusBadRequest {
				t.Fatalf("%s %s with a name %s = %d; body = %s; want 400", request.method, request.path, name, status, body)
			}
			assertBoardRefusal(t, body, response, core.CategoryValidation, refused.says)
		}
	}
	if after := workbookRefs(t, folder); after != before {
		t.Fatalf("a refused request moved refs:\nbefore %s\nafter  %s", before, after)
	}
}

// assertBoardRefusal requires the board's own error document, with its
// security headers, carrying one sentence.
func assertBoardRefusal(t *testing.T, body []byte, header http.Header, category core.Category, contains string) {
	t.Helper()
	if header.Get("Content-Security-Policy") == "" || header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("refusal headers = %v, want the board's security headers", header)
	}
	var document struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
		Error   struct {
			Category core.Category `json:"category"`
			Message  string        `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("refusal is not JSON: %v; body = %s", err, body)
	}
	if document.Format != "workbook.error" || document.Version != 1 || document.Error.Category != category ||
		document.Error.Message == "" || !strings.Contains(document.Error.Message, contains) {
		t.Fatalf("refusal = %#v, want workbook.error v1, %s, containing %q", document, category, contains)
	}
}

// With the header left off, a request that carries it is treated like any
// other: the name in it is ignored and the edit is the server's own.
func TestServeWithoutTheActorHeaderIgnoresTheHeader(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"no options":          nil,
		"public address only": {"--public-url", proxyOrigin},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			folder, _, _ := hostedFolder(t)
			board := startHostedServe(t, folder, nil, args...)
			host := ""
			if args == nil {
				host = board.addr
			}
			origin := "-"
			if args != nil {
				origin = proxyOrigin
			}
			body, status, _ := board.do(t, proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Claimed by a header"}`,
				person: "mallory@client.example", host: host, origin: origin})
			task := decodeServeMutation(t, body, status)
			if got := historyActors(t, folder, task.ID); !reflect.DeepEqual(got, []string{"board@example.test"}) {
				t.Fatalf("history = %v, want the server's own name and not the header's", got)
			}
			body, status, _ = board.do(t, proxied{method: http.MethodGet, path: "/", host: host})
			if status != http.StatusOK || strings.Contains(string(body), "mallory") {
				t.Fatalf("page = %d, want it to answer with no name and never the header's", status)
			}
		})
	}
}

// A request from another website is refused at the public address and at the
// local one, whether it reads or writes, and it records nothing.
func TestServeBehindAProxyRefusesOtherWebsites(t *testing.T) {
	t.Parallel()
	folder, _, _ := hostedFolder(t)
	board := startHostedServe(t, folder, nil, "--public-url", proxyOrigin, "--actor-header", proxyHeader)
	before := workbookRefs(t, folder)
	for _, request := range []proxied{
		{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Drive-by"}`, origin: "https://evil.example"},
		{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Drive-by"}`, origin: "https://evil.example", host: board.addr},
		{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Drive-by"}`, origin: "http://" + proxyHost},
		{method: http.MethodGet, path: "/api/tasks", origin: "https://evil.example"},
		{method: http.MethodGet, path: "/api/tasks", origin: "https://evil.example", host: board.addr},
		{method: http.MethodGet, path: "/api/tasks", host: "evil.example"},
		{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Rebound"}`, host: "evil.example", origin: "http://evil.example"},
	} {
		request.person = "alice@client.example"
		body, status, header := board.do(t, request)
		if status != http.StatusForbidden {
			t.Fatalf("%s %s from Host %q Origin %q = %d; body = %s; want 403", request.method, request.path, request.host, request.origin, status, body)
		}
		assertBoardRefusal(t, body, header, core.CategoryValidation, proxyOrigin)
	}
	// The plain-form request a cross-site page can send without asking first.
	body, status, _ := board.do(t, proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Form"}`,
		person: "alice@client.example", header: func(h http.Header) { h.Set("Content-Type", "text/plain") }})
	if status != http.StatusUnsupportedMediaType {
		t.Fatalf("form post = %d; body = %s; want 415", status, body)
	}
	if after := workbookRefs(t, folder); after != before {
		t.Fatalf("a refused request moved refs:\nbefore %s\nafter  %s", before, after)
	}
	// And the board's own page at the public address is served.
	if _, status, _ := board.do(t, proxied{method: http.MethodGet, path: "/", person: "alice@client.example"}); status != http.StatusOK {
		t.Fatalf("own page = %d", status)
	}
}

// With the header on and WORKBOOK_ACTOR also set, the header names the person
// for each request.
func TestServeActorHeaderNamesThePersonOverWorkbookActor(t *testing.T) {
	t.Parallel()
	folder, _, _ := hostedFolder(t)
	board := startHostedServe(t, folder, map[string]string{core.ActorVariable: "service@client.example"},
		"--public-url", proxyOrigin, "--actor-header", proxyHeader)
	body, status, _ := board.do(t, proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Alice's"}`, person: "alice@client.example"})
	task := decodeServeMutation(t, body, status)
	if got := historyActors(t, folder, task.ID); !reflect.DeepEqual(got, []string{"alice@client.example"}) {
		t.Fatalf("history = %v, want the header's person", got)
	}
	if _, status, _ := board.do(t, proxied{method: http.MethodGet, path: "/api/tasks"}); status != http.StatusUnauthorized {
		t.Fatalf("no name with WORKBOOK_ACTOR set = %d, want 401 rather than the variable's name", status)
	}
}

// The two variables set the options, and a flag wins over its variable.
func TestServeReadsItsHostingOptionsFromTheEnvironment(t *testing.T) {
	t.Parallel()
	folder, _, _ := hostedFolder(t)
	board := startHostedServe(t, folder, map[string]string{
		servePublicURLVariable:   proxyOrigin,
		serveActorHeaderVariable: strings.ToLower(proxyHeader),
	})
	body, status, _ := board.do(t, proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"From the environment"}`, person: "Alice@Client.example"})
	task := decodeServeMutation(t, body, status)
	// The name is recorded exactly as sent, case included.
	if got := historyActors(t, folder, task.ID); !reflect.DeepEqual(got, []string{"Alice@Client.example"}) {
		t.Fatalf("history = %v, want the name exactly as sent", got)
	}

	other, _, _ := hostedFolder(t)
	flagged := startHostedServe(t, other, map[string]string{
		servePublicURLVariable:   "https://elsewhere.example",
		serveActorHeaderVariable: "X-Elsewhere",
	}, "--public-url", proxyOrigin, "--actor-header", proxyHeader)
	body, status, _ = flagged.do(t, proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Flags win"}`, person: "alice@client.example"})
	decodeServeMutation(t, body, status)
	if _, status, _ := flagged.do(t, proxied{method: http.MethodGet, path: "/api/tasks", host: "elsewhere.example",
		person: "alice@client.example"}); status != http.StatusForbidden {
		t.Fatalf("the variable's address = %d, want refused because the flag won", status)
	}
}

// Every unusable option stops serve before it binds anything, with a sentence
// saying what is accepted.
func TestServeRefusesUnusableHostingOptions(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	for _, test := range []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"plain http public address", nil, []string{"--public-url", "http://tasks.example.com"}, "https://"},
		{"public address with a path", nil, []string{"--public-url", "https://tasks.example.com/board"}, "no path"},
		{"public address with a query", nil, []string{"--public-url", "https://tasks.example.com?x=1"}, "no query"},
		{"public address with a user", nil, []string{"--public-url", "https://sam@tasks.example.com"}, "no user name"},
		{"blank public address", nil, []string{"--public-url", ""}, "--public-url"},
		{"blank public variable", map[string]string{servePublicURLVariable: ""}, nil, servePublicURLVariable},
		{"bad public variable", map[string]string{servePublicURLVariable: "tasks.example.com"}, nil, servePublicURLVariable},
		{"blank header", nil, []string{"--actor-header", ""}, "must not be blank"},
		{"blank header variable", map[string]string{serveActorHeaderVariable: ""}, nil, serveActorHeaderVariable},
		{"header with a space", nil, []string{"--actor-header", "X Email"}, "not a header name"},
		{"header with a colon", nil, []string{"--actor-header", "X-Email:"}, "not a header name"},
		{"the Host header", nil, []string{"--actor-header", "host"}, "already means something"},
		{"the Origin header", nil, []string{"--actor-header", "Origin"}, "already means something"},
		{"bad flag beats a good variable", map[string]string{servePublicURLVariable: proxyOrigin}, []string{"--public-url", "ftp://x"}, "--public-url"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookup := func(name string) (string, bool) {
				value, ok := test.env[name]
				return value, ok
			}
			bound := false
			listen := func(string, string) (net.Listener, error) {
				bound = true
				return nil, fmt.Errorf("serve bound a listener")
			}
			var stdout, stderr lockedWriter
			err := runServeWithEnvironment(context.Background(), lookup, listen, test.args, repository, &stdout, &stderr)
			if core.CategoryOf(err) != core.CategoryInvocation || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("serve error = %v (%s), want an invocation error containing %q", err, core.CategoryOf(err), test.want)
			}
			if bound || stdout.String() != "" || stderr.String() != "" {
				t.Fatalf("serve bound %v, wrote %q and %q before refusing", bound, stdout.String(), stderr.String())
			}
		})
	}
}

var boardWarningPattern = regexp.MustCompile(`takes each person's name from the ` + proxyHeader + ` header and is listening beyond this machine`)

// The startup warning appears whenever the header is on and the board listens
// beyond the local machine, names the address, and does not stop the board.
func TestServeWarnsWhenTheActorHeaderIsOnBeyondThisMachine(t *testing.T) {
	t.Parallel()
	folder, _, _ := hostedFolder(t)
	wide := reserveListenerOn(t, "0.0.0.0")
	board := startHostedServeOn(t, folder, wide, nil, "--actor-header", proxyHeader)
	if !boardWarningPattern.MatchString(board.stderr.String()) || !strings.Contains(board.stderr.String(), wide.addr) {
		t.Fatalf("stderr = %q, want the header warning naming %s", board.stderr.String(), wide.addr)
	}
	local := startHostedServe(t, folder, nil, "--actor-header", proxyHeader)
	if boardWarningPattern.MatchString(local.stderr.String()) {
		t.Fatalf("loopback stderr = %q, want no header warning", local.stderr.String())
	}
	other, _, _ := hostedFolder(t)
	plain := startHostedServeOn(t, other, reserveListenerOn(t, "0.0.0.0"), nil)
	if boardWarningPattern.MatchString(plain.stderr.String()) {
		t.Fatalf("no header stderr = %q, want no header warning", plain.stderr.String())
	}
}

// A hosted board's answer never carries the server's folder, the remote's
// address or Git's output: a change whose publication fails answers with a
// short sentence and a reference, and serve's own log has the detail under
// the same reference. The same failure on a board with no public address
// answers with every word, as it always has.
func TestHostedServeKeepsGitOutputInTheServerLog(t *testing.T) {
	t.Parallel()
	for _, hosted := range []bool{true, false} {
		t.Run(fmt.Sprintf("hosted=%v", hosted), func(t *testing.T) {
			t.Parallel()
			folder, origin, _ := hostedFolder(t)
			var args []string
			request := proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Unpublishable"}`}
			if hosted {
				args = []string{"--public-url", proxyOrigin, "--actor-header", proxyHeader}
				request.person = "alice@client.example"
			}
			// Origin points at an address nothing answers, from the start, so
			// every publication asks it which project it holds and fails with
			// Git's own words naming the address.
			_ = origin
			cliGit(t, folder, "remote", "set-url", "origin", "http://127.0.0.1:1/team/secret-path/repo.git")
			board := startHostedServe(t, folder, nil, args...)
			if !hosted {
				request.host, request.origin = board.addr, "-"
			}
			// Publish inline so the failure is this request's.
			setBody, setStatus, _ := board.do(t, proxied{method: http.MethodPut, path: "/api/sync", body: `{"mode":"inline"}`,
				person: request.person, host: request.host, origin: request.origin})
			if setStatus != http.StatusOK {
				t.Fatalf("inline mode = %d; body = %s", setStatus, setBody)
			}
			body, status, header := board.do(t, request)
			task := decodeServeMutation(t, body, status)
			if task.ID == "" {
				t.Fatal("no task")
			}
			if !hosted {
				if !strings.Contains(string(body), "secret-path") || header.Get("Cache-Control") != "" {
					t.Fatalf("local answer = %s with Cache-Control %q, want the whole detail and no caching header", body, header.Get("Cache-Control"))
				}
				return
			}
			if strings.Contains(string(body), "secret-path") || strings.Contains(string(body), "127.0.0.1:1") ||
				strings.Contains(string(body), "fatal") || strings.Contains(string(body), folder) {
				t.Fatalf("hosted answer carries server detail: %s", body)
			}
			match := regexp.MustCompile(`reference ([0-9a-f]{12})`).FindStringSubmatch(string(body))
			if match == nil {
				t.Fatalf("hosted answer names no reference: %s", body)
			}
			logged := board.stderr.String()
			if !strings.Contains(logged, "reference "+match[1]+": ") || !strings.Contains(logged, "secret-path") {
				t.Fatalf("server log = %q, want reference %s with the detail", logged, match[1])
			}
			if header.Get("Cache-Control") != "private, no-store" {
				t.Fatalf("hosted Cache-Control = %q", header.Get("Cache-Control"))
			}
		})
	}
}

// An edit on the hosted board and a different edit to the same field from a
// contributor's CLI, made at the same moment, both survive as a visible
// conflict, whichever reaches origin first. The description is the field: it
// is the one Workbook reconciles as a conflict rather than by its documented
// last-syncer-wins rule for the other fields.
//
// When the board's edit lands first, the contributor's push is rejected and
// their next sync stops with exit 8 and the conflict, both texts in it, while
// the board's text is on origin. When the contributor's lands first, the
// board's sync loop reports the conflict on serve's standard error and keeps
// the board's edit in the server's folder, parked where reconciliation parks a
// local tip.
func TestHostedAndCLIEditsToOneDescriptionSurviveAsAConflict(t *testing.T) {
	t.Parallel()
	const alice = "alice@client.example"
	const boardText, cliText = "Alice's text from the board", "The contributor's text"
	setup := func(t *testing.T) (*hostedServe, string, string, string, core.Task) {
		folder, origin, contributor := hostedFolder(t)
		board := startHostedServe(t, folder, nil, "--public-url", proxyOrigin, "--actor-header", proxyHeader)
		body, status, _ := board.do(t, proxied{method: http.MethodPost, path: "/api/tasks",
			body: `{"title":"Contested","description":"Base text"}`, person: alice})
		task := decodeServeMutation(t, body, status)
		waitForOrigin(t, origin, task.ID, task.Head)
		if code, _, stderr := run(t, contributor, "sync"); code != 0 {
			t.Fatalf("contributor sync code = %d; stderr = %q", code, stderr)
		}
		return board, folder, origin, contributor, task
	}
	boardEdit := func(t *testing.T, board *hostedServe, task core.Task) ([]byte, int) {
		body, status, _ := board.do(t, proxied{method: http.MethodPatch, path: "/api/tasks/" + task.ID,
			body: `{"description":"` + boardText + `","expectedHead":"` + task.Head + `"}`, person: alice})
		return body, status
	}
	// contributorTold is the order in which the board's edit reached origin
	// first: the contributor's next sync stops on the conflict.
	contributorTold := func(t *testing.T, contributor string) {
		code, stdout, stderr := run(t, contributor, "sync", "--json")
		if code != 8 {
			t.Fatalf("contributor sync = (%d, %q, %q), want exit 8", code, stdout, stderr)
		}
		document := assertJSONResult(t, stdout, "sync")
		want := core.DescriptionConflict{Base: "Base text", Ours: cliText, Theirs: boardText}
		if len(document.Conflict) != 1 || document.Conflict[0].Description == nil || *document.Conflict[0].Description != want {
			t.Fatalf("contributor's conflict = %#v, want %#v", document.Conflict, want)
		}
	}
	// boardReported is the other order: the board's sync loop reports the
	// conflict and parks the board's edit, and origin holds the contributor's.
	boardReported := func(t *testing.T, board *hostedServe, folder, contributor string, task core.Task) {
		deadline := time.Now().Add(30 * time.Second)
		for !strings.Contains(board.stderr.String(), "conflict on "+task.ID) {
			if time.Now().After(deadline) {
				t.Fatalf("serve never reported the conflict; stderr = %s", board.stderr.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
		parked := strings.Fields(cliGitOutput(t, folder, "for-each-ref", "--format=%(objectname)", "refs/workbook/reconciled/"+task.ID+"/"))
		if len(parked) == 0 {
			t.Fatalf("the board's edit was not parked; refs = %s", workbookRefs(t, folder))
		}
		if got := cliGitOutput(t, folder, "cat-file", "-p", parked[0]+":operation.json"); !strings.Contains(got, boardText) || !strings.Contains(got, alice) {
			t.Fatalf("the parked tip holds %s, want alice's text", got)
		}
		code, stdout, stderr := run(t, contributor, "show", task.ID, "--json")
		if code != 0 || !strings.Contains(stdout, cliText) {
			t.Fatalf("contributor's show = (%d, %q, %q), want its own text on origin", code, stdout, stderr)
		}
	}

	t.Run("at the same moment", func(t *testing.T) {
		t.Parallel()
		board, folder, origin, contributor, task := setup(t)
		cliDone := make(chan [3]string, 1)
		start := make(chan struct{})
		go func() {
			<-start
			code, stdout, stderr := run(t, contributor, "update", task.ID, "--description", cliText, "--json")
			cliDone <- [3]string{fmt.Sprint(code), stdout, stderr}
		}()
		close(start)
		body, status := boardEdit(t, board, task)
		cli := <-cliDone
		if cli[0] != "0" {
			t.Fatalf("contributor update = %v", cli)
		}
		if status == http.StatusConflict {
			t.Fatalf("the board refused its edit as stale: %s; the board had not fetched the contributor's yet", body)
		}
		edited := decodeServeMutation(t, body, status)
		if strings.Contains(cli[1], `"status":"rejected"`) {
			waitForOrigin(t, origin, task.ID, edited.Head)
			contributorTold(t, contributor)
			return
		}
		boardReported(t, board, folder, contributor, task)
	})

	t.Run("the contributor first", func(t *testing.T) {
		t.Parallel()
		// The board's own sync loop fetches every few seconds, and a fetch
		// between the contributor's push and the board's edit makes the edit
		// a refused stale write rather than a concurrent one. That is a
		// different, already visible outcome, so the attempt is made again.
		for attempt := 1; ; attempt++ {
			board, folder, _, contributor, task := setup(t)
			if code, _, stderr := run(t, contributor, "update", task.ID, "--description", cliText); code != 0 {
				t.Fatalf("contributor update code = %d; stderr = %q", code, stderr)
			}
			body, status := boardEdit(t, board, task)
			if status == http.StatusConflict && attempt < 4 {
				t.Logf("attempt %d: the board had fetched first and refused a stale write; trying again", attempt)
				continue
			}
			decodeServeMutation(t, body, status)
			boardReported(t, board, folder, contributor, task)
			return
		}
	})
}

// `workbook serve` with neither option, and neither variable, behaves exactly
// as it did before they existed: the same startup lines, the public name
// refused, any name header ignored, answers with no caching header beyond the
// poll's own, and a failure's whole detail in the answer.
func TestServeWithNoHostingOptionsBehavesAsBefore(t *testing.T) {
	t.Parallel()
	folder, _, _ := hostedFolder(t)
	board := startHostedServe(t, folder, map[string]string{"WORKBOOK_SERVE_UNRELATED": "x"})
	local := func(request proxied) proxied {
		request.host = board.addr
		if request.origin == "" {
			request.origin = "-"
		}
		return request
	}

	lines := strings.Split(strings.TrimSpace(board.stderr.String()), "\n")
	if lines[0] != "Workbook board: http://"+board.addr {
		t.Fatalf("first line = %q, want the banner alone", lines[0])
	}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, "Workbook sync watcher: ") {
			t.Fatalf("stderr has %q beyond the banner and the watcher line: %q", line, board.stderr.String())
		}
	}

	// Without a name, every read and write is served as it always was.
	for _, path := range []string{"/", "/tasks/new", "/api/tasks", "/api/vocabulary", "/api/sync"} {
		body, status, header := board.do(t, local(proxied{method: http.MethodGet, path: path}))
		if status != http.StatusOK {
			t.Fatalf("GET %s = %d; body = %s", path, status, body)
		}
		want := ""
		if path == "/api/tasks" {
			want = "no-cache"
		}
		if got := header.Get("Cache-Control"); got != want {
			t.Fatalf("GET %s Cache-Control = %q, want %q as before", path, got, want)
		}
	}
	body, status, _ := board.do(t, local(proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Local"}`}))
	if task := decodeServeMutation(t, body, status); task.ID == "" {
		t.Fatal("no task")
	}

	// The public name, and its origin, are not this board's.
	for _, request := range []proxied{
		{method: http.MethodGet, path: "/api/tasks"},
		{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Public"}`},
		local(proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Public origin"}`, origin: proxyOrigin}),
	} {
		body, status, header := board.do(t, request)
		if status != http.StatusForbidden {
			t.Fatalf("%s at %q from %q = %d; body = %s; want 403", request.method, request.host, request.origin, status, body)
		}
		assertBoardRefusal(t, body, header, core.CategoryValidation, board.addr)
	}
}
