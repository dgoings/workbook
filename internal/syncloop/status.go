package syncloop

import (
	"bufio"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

const (
	// StatusFormat and StatusVersion name the status document a watcher serves.
	// Both durable wire formats in this package are versioned from their first
	// release so a newer CLI can recognize an older watcher rather than guess.
	StatusFormat  = "workbook.watcher-status"
	StatusVersion = 1

	// minimumStaleAfter floors the staleness threshold. A very short interval
	// must not make a healthy watcher look dead to a command that arrives
	// during one slow fetch.
	minimumStaleAfter = 30 * time.Second
)

// Status is everything a watcher reports about itself. A caller uses it to
// decide whether to trust the watcher with publication, so it carries the
// outcome of the last synchronization as well as the timing.
type Status struct {
	Format     string          `json:"format"`
	Version    int             `json:"version"`
	PID        int             `json:"pid"`
	IntervalMS int64           `json:"intervalMs"`
	StartedAt  time.Time       `json:"startedAt"`
	LastSyncAt time.Time       `json:"lastSyncAt"`
	LastSyncOK bool            `json:"lastSyncOk"`
	LastError  string          `json:"lastSyncError,omitempty"`
	Conflicts  []ConflictEntry `json:"conflicts"`
	// IgnoredRefs names the refs under origin's task namespace the last
	// synchronization skipped. It is the set that synchronization observed
	// rather than an accumulating memory: the tracking mirror is pruned against
	// origin on every fetch, so a name that is gone from origin is gone here on
	// the next tick. A report is not an instruction to remove anything.
	IgnoredRefs []gitstore.IgnoredRef `json:"ignoredRefs,omitempty"`
}

// ConflictEntry is one conflict the watcher observed and no command has
// reported yet. Head is the task's canonical tip at the moment the conflict was
// recorded, which is what lets the entry expire when the task moves on without
// anyone acknowledging it.
type ConflictEntry struct {
	core.Conflict
	Head string `json:"head"`
}

// StaleAfter is how long a watcher may go without synchronizing before a
// command stops trusting it. Three intervals tolerates one slow fetch and one
// missed tick without tolerating a wedged process.
func (s Status) StaleAfter() time.Duration {
	threshold := 3 * time.Duration(s.IntervalMS) * time.Millisecond
	if threshold < minimumStaleAfter {
		return minimumStaleAfter
	}
	return threshold
}

// Trustworthy reports whether a command may hand publication to this watcher.
//
// A failed last synchronization is deliberately disqualifying. If origin is
// unreachable the watcher knows, and the command has to take the inline path so
// the local-only warning still reaches the caller.
func (s Status) Trustworthy(now time.Time) bool {
	if s.Format != StatusFormat || s.Version != StatusVersion {
		return false
	}
	if !s.LastSyncOK || s.LastSyncAt.IsZero() {
		return false
	}
	return now.Sub(s.LastSyncAt) <= s.StaleAfter()
}

// conflictSet is the watcher's memory of conflicts no command has reported.
//
// The generated guidelines used to promise that no conflict state is kept
// between invocations, which held while the fetch that dropped operations and
// the caller who caused it were the same process. A watcher fetches with nobody
// present, and a stopped replay leaves the ref truncated, so the next fetch
// finds nothing divergent and the conflict would otherwise vanish unreported.
//
// Its mutex is held only for map operations and never across Git or network
// work, so the request handler can read it while a synchronization is blocked.
type conflictSet struct {
	mu      sync.Mutex
	entries map[string]ConflictEntry
}

func newConflictSet() *conflictSet {
	return &conflictSet{entries: make(map[string]ConflictEntry)}
}

// add records a conflict and reports whether it was new, so the watcher
// announces each one to its terminal exactly once.
func (c *conflictSet) add(entry ConflictEntry) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	existing, found := c.entries[entry.TaskID]
	if found && existing.Head == entry.Head && existing.Type == entry.Type {
		return false
	}
	c.entries[entry.TaskID] = entry
	return true
}

// acknowledge drops the entry a command just reported, reproducing the one-shot
// semantics of the inline gate: the identical retry then proceeds.
func (c *conflictSet) acknowledge(taskID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, found := c.entries[taskID]; !found {
		return false
	}
	delete(c.entries, taskID)
	return true
}

// heads returns the task IDs and recorded tips, for expiry.
func (c *conflictSet) heads() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	heads := make(map[string]string, len(c.entries))
	for taskID, entry := range c.entries {
		heads[taskID] = entry.Head
	}
	return heads
}

// expire drops entries whose task has moved past the recorded tip. It retires
// conflicts on tasks nobody returns to, and costs nothing beyond the ref reads
// the watcher already performs.
func (c *conflictSet) expire(moved []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, taskID := range moved {
		delete(c.entries, taskID)
	}
}

func (c *conflictSet) list() []ConflictEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries := make([]ConflictEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].TaskID < entries[j].TaskID })
	return entries
}

// boundConflicts drops the entries past maxConflictBytes, so a status response
// always fits the bound the client enforces.
//
// Without it the bound is reachable from the inside and the failure is
// self-sustaining: a client that refuses the answer never learns the watcher is
// trustworthy, so it never acknowledges anything, so the set that made the
// answer too large is never drained and every mutation takes the inline path
// with `sync --status` reporting a live watcher as not running. Serving a
// prefix instead keeps the watcher usable and lets the set drain the ordinary
// way. The prefix is the same one every time, because list sorts by task ID, so
// the conflicts that are visible stay visible until they are acknowledged.
func boundConflicts(entries []ConflictEntry) []ConflictEntry {
	total := 0
	for index, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return entries[:index]
		}
		// The comma that joins this entry to the previous one.
		total += len(encoded) + 1
		if total > maxConflictBytes {
			return entries[:index]
		}
	}
	return entries
}

// boundIgnoredRefs drops the entries past maxIgnoredBytes, for the reason
// boundConflicts bounds the conflict set: this list grows with what origin
// holds rather than with what this clone did, and anyone with push access can
// grow it. An answer past the client's bound is refused, which would leave a
// healthy watcher untrusted and every mutation on the inline path.
//
// The prefix is stable because the loop publishes the refs sorted by name, so
// the ones a reader can see stay visible until origin loses them.
func boundIgnoredRefs(entries []gitstore.IgnoredRef) []gitstore.IgnoredRef {
	total := 0
	for index, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return entries[:index]
		}
		// The comma that joins this entry to the previous one.
		total += len(encoded) + 1
		if total > maxIgnoredBytes {
			return entries[:index]
		}
	}
	return entries
}

// request is the newline-delimited command a client sends. Exactly one member
// is populated.
type request struct {
	Status *statusRequest `json:"status,omitempty"`
	Nudge  *nudgeRequest  `json:"nudge,omitempty"`
	Ack    *ackRequest    `json:"ack,omitempty"`
}

type statusRequest struct{}

type nudgeRequest struct {
	TaskID string `json:"taskId,omitempty"`
}

type ackRequest struct {
	TaskID string `json:"taskId"`
}

type response struct {
	OK     bool    `json:"ok"`
	Status *Status `json:"status,omitempty"`
	Error  string  `json:"error,omitempty"`
}

const (
	// maxRequestBytes, maxConflictBytes, and maxResponseBytes bound one
	// protocol line. The handler deadline bounds how long a peer may take, not
	// how much it may send, so without these a peer that never writes a newline
	// makes the other end grow a buffer until the process dies.
	//
	// A request is a command and a task ID, so it is bounded tightly. A status
	// response carries the conflicts the watcher is still holding, and a
	// description conflict reports three descriptions of up to
	// core.MaxDescriptionBytes each, so the budget has to clear about 192 KiB
	// per conflict. Twenty maximum-sized ones is already far past what a
	// repository can accumulate before somebody notices.
	//
	// A status response also names the refs origin holds that this build does
	// not read, which grows with what a poisoned namespace contains rather than
	// with anything this clone did, so maxIgnoredBytes gives that report its own
	// budget. Ref names and their reasons are short, so 64 KiB is hundreds of
	// them: far past the point where the first few already made the case.
	//
	// The bounds are layered so a healthy watcher can never reach the client's
	// bound: maxConflictBytes and maxIgnoredBytes budget what the server
	// serializes, and each layer above adds room for the JSON framing the layer
	// below does not count — the per-entry framing of twenty conflicts, then the
	// status document around them. Sizing the response at exactly twenty
	// conflicts' descriptions would have refused twenty of them, since it left
	// nothing for the framing.
	maxRequestBytes  = 64 << 10
	maxConflictBytes = 20*3*core.MaxDescriptionBytes + 64<<10
	maxIgnoredBytes  = 64 << 10
	maxResponseBytes = maxConflictBytes + maxIgnoredBytes + 64<<10
)

// errLineTooLong reports a protocol line that outgrew its bound.
var errLineTooLong = errors.New("watcher protocol line is too long")

// readLine reads one newline-terminated message, refusing one longer than
// limit. bufio.Reader.ReadBytes accumulates without bound, so the limit has to
// be enforced while reading rather than checked afterwards.
func readLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) > limit {
			return nil, errLineTooLong
		}
		// ReadSlice returns the reader's own buffer, valid only until the next
		// read, so the copy is not optional.
		line = append(line, chunk...)
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}
