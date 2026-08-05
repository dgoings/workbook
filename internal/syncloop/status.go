package syncloop

import (
	"sort"
	"sync"
	"time"

	"github.com/dgoings/workbook/internal/core"
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

// ConflictFor returns the recorded conflict for a task, if any.
func (s Status) ConflictFor(taskID string) (ConflictEntry, bool) {
	for _, entry := range s.Conflicts {
		if entry.TaskID == taskID {
			return entry, true
		}
	}
	return ConflictEntry{}, false
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
	Head   string `json:"head,omitempty"`
}

type response struct {
	OK     bool    `json:"ok"`
	Status *Status `json:"status,omitempty"`
	Error  string  `json:"error,omitempty"`
}
