package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The cap on how much work the board does at once.
//
// None of these tests measures time. A request held inside the handler is held
// on a channel the test closes, the wait for a turn ends on a timer the test
// fires, and every "by now" is a wait for the limiter to report the state the
// test is about to rely on. So each bound below holds on any machine at any
// speed, and the only clock in sight is the hang guard.

// heldWork is a handler that records how many requests it is working on at
// once and holds every one of them until release is closed.
type heldWork struct {
	release chan struct{}
	entered chan string

	mutex      sync.Mutex
	active     int
	peak       int
	peakWrites int
	writes     int
	started    int
}

func newHeldWork() *heldWork {
	return &heldWork{release: make(chan struct{}), entered: make(chan string, 1024)}
}

func (work *heldWork) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	write := mutatingMethod(request.Method)
	work.mutex.Lock()
	work.active++
	work.started++
	if write {
		work.writes++
	}
	work.peak = max(work.peak, work.active)
	work.peakWrites = max(work.peakWrites, work.writes)
	work.mutex.Unlock()
	work.entered <- request.Method + " " + request.URL.Path
	<-work.release
	work.mutex.Lock()
	work.active--
	if write {
		work.writes--
	}
	work.mutex.Unlock()
	writer.WriteHeader(http.StatusOK)
}

func (work *heldWork) counts() (started, peak, peakWrites int) {
	work.mutex.Lock()
	defer work.mutex.Unlock()
	return work.started, work.peak, work.peakWrites
}

// manualTimers replaces the limiter's wait with timers the test fires.
type manualTimers struct {
	mutex   sync.Mutex
	pending []chan time.Time
	started chan struct{}
}

func installManualTimers(limiter *workLimiter) *manualTimers {
	timers := &manualTimers{started: make(chan struct{}, 1024)}
	limiter.timer = func(time.Duration) (<-chan time.Time, func()) {
		fired := make(chan time.Time, 1)
		timers.mutex.Lock()
		timers.pending = append(timers.pending, fired)
		timers.mutex.Unlock()
		timers.started <- struct{}{}
		return fired, func() {}
	}
	return timers
}

// fireAll ends every wait that has started.
func (timers *manualTimers) fireAll() {
	timers.mutex.Lock()
	defer timers.mutex.Unlock()
	for _, fired := range timers.pending {
		select {
		case fired <- time.Time{}:
		default:
		}
	}
}

// awaitWorkState waits until the limiter reports a state the test names.
func awaitWorkState(t *testing.T, limiter *workLimiter, describe string, ready func(workState) bool) {
	t.Helper()
	// Hang guard, not a measurement: each state arrives as soon as the
	// goroutines that produce it are scheduled.
	deadline := time.After(30 * time.Second)
	for {
		state := limiter.state()
		if ready(state) {
			return
		}
		select {
		case <-limiter.changed:
		case <-deadline:
			t.Fatalf("limiter never reached %s; it is at %+v", describe, state)
		}
	}
}

func awaitEntered(t *testing.T, work *heldWork, count int) []string {
	t.Helper()
	var entered []string
	deadline := time.After(30 * time.Second)
	for len(entered) < count {
		select {
		case route := <-work.entered:
			entered = append(entered, route)
		case <-deadline:
			t.Fatalf("only %d of %d requests reached the handler: %v", len(entered), count, entered)
		}
	}
	return entered
}

// sendAsync serves one request on its own goroutine and hands back its answer.
func sendAsync(handler http.Handler, method, target string, body io.Reader) <-chan *httptest.ResponseRecorder {
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(method, target, body)
		if mutatingMethod(method) {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		answered <- response
	}()
	return answered
}

func awaitAnswer(t *testing.T, answered <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-answered:
		return response
	case <-time.After(30 * time.Second):
		t.Fatal("a request was never answered")
		return nil
	}
}

// requireBusyRefusal checks a refusal says what it is in every way a client
// might read it: the status, the header that says when to ask again, and the
// board's own error document.
func requireBusyRefusal(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	if got := response.Header().Get("Retry-After"); got != fmt.Sprint(workRetryAfterSeconds) {
		t.Fatalf("Retry-After = %q, want %q", got, fmt.Sprint(workRetryAfterSeconds))
	}
	if response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("a refusal carries the page's security headers like every other answer; headers = %v", response.Header())
	}
	var document ErrorDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode refusal: %v; body = %s", err, response.Body.String())
	}
	if document.Format != "workbook.error" || document.Version != 1 || document.Error.Category != core.CategoryOperational {
		t.Fatalf("refusal document = %+v, want a version 1 workbook.error in the operational category", document)
	}
	if !strings.Contains(document.Error.Message, "busy") {
		t.Fatalf("refusal message = %q, want it to say the board is busy", document.Error.Message)
	}
}

func TestWorkLimitNeverWorksOnMoreRequestsThanItsSlots(t *testing.T) {
	t.Parallel()
	// Production mutation: a handler served with no cap starts every request
	// it is sent, which is how 300 simultaneous creates came to run 385 Git
	// processes at once.
	limiter := newWorkLimiter(WorkLimits{MaxRequests: 4})
	installManualTimers(limiter)
	work := newHeldWork()
	handler := limiter.wrap(work)

	var answers []<-chan *httptest.ResponseRecorder
	for index := 0; index < 4; index++ {
		answers = append(answers, sendAsync(handler, http.MethodGet, "/api/tasks", nil))
	}
	awaitEntered(t, work, 4)
	// The next sixteen fill the line: four slots, four waiting for each.
	for index := 0; index < 4*workQueuePerSlot; index++ {
		answers = append(answers, sendAsync(handler, http.MethodGet, "/api/tasks", nil))
	}
	awaitWorkState(t, limiter, "a full line", func(state workState) bool {
		return state.Busy == 4 && state.Waiting == 4*workQueuePerSlot
	})
	// Everything past a full line is refused at once, without being worked on
	// and without waiting: the wait is never fired here, so an answer at all is
	// the proof.
	for index := 0; index < 10; index++ {
		requireBusyRefusal(t, awaitAnswer(t, sendAsync(handler, http.MethodPost, "/api/tasks", strings.NewReader(`{"title":"flood"}`))))
	}
	if started, _, _ := work.counts(); started != 4 {
		t.Fatalf("the handler started %d requests while four slots were full, want 4", started)
	}

	// Freed slots go to the line, and the line drains completely.
	close(work.release)
	for _, answered := range answers {
		if response := awaitAnswer(t, answered); response.Code != http.StatusOK {
			t.Fatalf("a request that waited its turn answered %d, want %d", response.Code, http.StatusOK)
		}
	}
	started, peak, _ := work.counts()
	if started != 4+4*workQueuePerSlot {
		t.Fatalf("the handler started %d requests, want every admitted one (%d)", started, 4+4*workQueuePerSlot)
	}
	if peak > 4 {
		t.Fatalf("the handler worked on %d requests at once, want at most 4", peak)
	}
	if state := limiter.state(); state != (workState{}) {
		t.Fatalf("limiter after the line drained = %+v, want every slot back", state)
	}
}

func TestWorkLimitKeepsRoomForReadsWhileWritesAreSaturated(t *testing.T) {
	t.Parallel()
	// Production mutation: one shared pool lets a flood of writes take every
	// slot, and the refresh every open page runs once a second then waits
	// behind them — the review measured a reader's refresh going from 40 ms to
	// 11 seconds.
	limiter := newWorkLimiter(WorkLimits{MaxRequests: 4})
	installManualTimers(limiter)
	work := newHeldWork()
	handler := limiter.wrap(work)

	var writes []<-chan *httptest.ResponseRecorder
	for index := 0; index < 6; index++ {
		writes = append(writes, sendAsync(handler, http.MethodPost, "/api/tasks", strings.NewReader(`{}`)))
	}
	awaitEntered(t, work, 2)
	awaitWorkState(t, limiter, "two writes working and four waiting", func(state workState) bool {
		return state.Busy == 2 && state.BusyWrites == 2 && state.Waiting == 4
	})
	// Two reads arrive behind four waiting writes and start at once: the
	// writes are waiting for the writes' share, not for a slot.
	reads := []<-chan *httptest.ResponseRecorder{
		sendAsync(handler, http.MethodGet, "/api/tasks", nil),
		sendAsync(handler, http.MethodGet, "/api/tasks/WB-1/attachments/a", nil),
	}
	entered := awaitEntered(t, work, 2)
	for _, route := range entered {
		if !strings.HasPrefix(route, "GET ") {
			t.Fatalf("%q started while the writes' share was full and reads were waiting", route)
		}
	}
	if _, _, peakWrites := work.counts(); peakWrites != 2 {
		t.Fatalf("the handler worked on %d writes at once, want the writes' share of 2", peakWrites)
	}

	close(work.release)
	for _, answered := range append(writes, reads...) {
		if response := awaitAnswer(t, answered); response.Code != http.StatusOK {
			t.Fatalf("answer = %d, want %d", response.Code, http.StatusOK)
		}
	}
	if _, peak, peakWrites := work.counts(); peak > 4 || peakWrites > 2 {
		t.Fatalf("peak = %d requests and %d writes, want at most 4 and 2", peak, peakWrites)
	}
}

func TestWorkLimitRefusesARequestWhoseWaitRunsOut(t *testing.T) {
	t.Parallel()
	// Production mutation: a wait with no end is an unbounded queue by another
	// name — the person whose request is in it is never told anything.
	limiter := newWorkLimiter(WorkLimits{MaxRequests: 1})
	timers := installManualTimers(limiter)
	work := newHeldWork()
	handler := limiter.wrap(work)

	first := sendAsync(handler, http.MethodGet, "/api/tasks", nil)
	awaitEntered(t, work, 1)
	waiting := sendAsync(handler, http.MethodGet, "/api/tasks", nil)
	<-timers.started
	awaitWorkState(t, limiter, "one waiting", func(state workState) bool { return state.Waiting == 1 })

	timers.fireAll()
	requireBusyRefusal(t, awaitAnswer(t, waiting))
	if state := limiter.state(); state.Waiting != 0 || state.Busy != 1 {
		t.Fatalf("limiter after a wait ran out = %+v, want the refused request out of the line and no slot taken", state)
	}
	close(work.release)
	if response := awaitAnswer(t, first); response.Code != http.StatusOK {
		t.Fatalf("first request = %d, want %d", response.Code, http.StatusOK)
	}
	if started, _, _ := work.counts(); started != 1 {
		t.Fatalf("the handler started %d requests, want only the one that had a turn", started)
	}
}

func TestWorkLimitDropsAWaitingRequestWhoseClientLeft(t *testing.T) {
	t.Parallel()
	// Production mutation: a request whose client has gone keeps its place in
	// line and is worked on for nobody.
	limiter := newWorkLimiter(WorkLimits{MaxRequests: 1})
	installManualTimers(limiter)
	work := newHeldWork()
	handler := limiter.wrap(work)

	first := sendAsync(handler, http.MethodGet, "/api/tasks", nil)
	awaitEntered(t, work, 1)
	ctx, cancel := context.WithCancel(context.Background())
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/tasks", nil).WithContext(ctx))
		answered <- response
	}()
	awaitWorkState(t, limiter, "one waiting", func(state workState) bool { return state.Waiting == 1 })
	cancel()
	awaitAnswer(t, answered)
	if state := limiter.state(); state.Waiting != 0 {
		t.Fatalf("limiter after the client left = %+v, want the line empty", state)
	}
	close(work.release)
	awaitAnswer(t, first)
	if started, _, _ := work.counts(); started != 1 {
		t.Fatalf("the handler started %d requests, want 1", started)
	}
}

func TestWorkLimitAnswersTheHealthCheckWhenEverySlotIsTaken(t *testing.T) {
	t.Parallel()
	// Production mutation: a health check that waits in line behind a flood
	// reports a busy board as a dead one, which is what gets a board
	// restarted by its supervisor in the middle of its work.
	limiter := newWorkLimiter(WorkLimits{MaxRequests: 1})
	installManualTimers(limiter)
	listed := make(chan struct{}, 16)
	release := make(chan struct{})
	handler := limiter.wrap(NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) {
			listed <- struct{}{}
			<-release
			return nil, nil
		},
	}))
	held := sendAsync(handler, http.MethodGet, "/api/tasks", nil)
	<-listed
	var queued []<-chan *httptest.ResponseRecorder
	for index := 0; index < workQueuePerSlot; index++ {
		queued = append(queued, sendAsync(handler, http.MethodGet, "/api/tasks", nil))
	}
	awaitWorkState(t, limiter, "a full line", func(state workState) bool { return state.Busy == 1 && state.Waiting == workQueuePerSlot })
	requireBusyRefusal(t, request(t, handler, http.MethodGet, "/api/tasks"))

	health := request(t, handler, http.MethodGet, "/healthz")
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"workbook.health"`) {
		t.Fatalf("GET /healthz on a full board = %d %s, want the health document", health.Code, health.Body.String())
	}
	close(release)
	awaitAnswer(t, held)
	for _, answered := range queued {
		awaitAnswer(t, answered)
	}
}

func TestWorkLimitBelowOneMeansTheDefault(t *testing.T) {
	t.Parallel()
	// Production mutation: a zero value read literally is a board that refuses
	// everything, and a negative one a board with no cap.
	for _, requested := range []int{0, -3} {
		limiter := newWorkLimiter(WorkLimits{MaxRequests: requested})
		if limiter.slots != DefaultMaxRequests || limiter.writeSlots != DefaultMaxRequests/2 {
			t.Fatalf("MaxRequests %d gives %d slots and %d for writes, want %d and %d", requested, limiter.slots, limiter.writeSlots, DefaultMaxRequests, DefaultMaxRequests/2)
		}
	}
	if limiter := newWorkLimiter(WorkLimits{MaxRequests: 1}); limiter.writeSlots != 1 {
		t.Fatalf("one slot gives writes %d, want 1 so a board capped at one request can still write", limiter.writeSlots)
	}
}

func TestServeCapsTheBoardsWorkByDefault(t *testing.T) {
	t.Parallel()
	// Production mutation: Serve without LimitWork is a board with no cap,
	// whatever the limiter does when it is used.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// Room for every request the board will ever admit, so a release never
	// blocks on the count.
	listed := make(chan struct{}, 8*DefaultMaxRequests)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, listener, NewHandler(Options{
		List: func(context.Context) ([]core.Task, error) {
			listed <- struct{}{}
			<-release
			return nil, nil
		},
	}))
	base := "http://" + listener.Addr().String()
	// Connections are opened a few at a time so the test measures the board
	// rather than the listen backlog of the machine it runs on.
	dials := make(chan struct{}, 4)
	dialer := &net.Dialer{}
	client := &http.Client{Transport: &http.Transport{
		MaxIdleConnsPerHost: 4 * DefaultMaxRequests,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dials <- struct{}{}
			defer func() { <-dials }()
			return dialer.DialContext(ctx, network, address)
		},
	}}
	defer client.CloseIdleConnections()

	// Every slot and every place in line, then one more than that.
	held := DefaultMaxRequests + DefaultMaxRequests*workQueuePerSlot
	answers := make(chan int, held+1)
	for index := 0; index < held+1; index++ {
		go func() {
			response, err := client.Get(base + "/api/tasks")
			if err != nil {
				answers <- -1
				return
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			answers <- response.StatusCode
		}()
	}
	// At least one answer is a refusal before anything is released: with every
	// slot held and the line full, the request past it cannot be anything else.
	// Which request that is depends on arrival order, which is why the test
	// reads the first answer rather than naming a request.
	select {
	case status := <-answers:
		if status != http.StatusServiceUnavailable {
			t.Fatalf("the first answer from a saturated board = %d, want %d", status, http.StatusServiceUnavailable)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no request was refused by a board holding every slot and a full line")
	}
	response, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz on a saturated board = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if len(listed) > DefaultMaxRequests {
		t.Fatalf("the board listed tasks for %d requests at once, want at most %d", len(listed), DefaultMaxRequests)
	}
	unblock()
	for index := 0; index < held; index++ {
		<-answers
	}
}
