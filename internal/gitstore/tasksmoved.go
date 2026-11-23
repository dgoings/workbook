package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// When a project moves its tasks to another repository, the repository they
// left is marked, so a checkout that has not picked up the new setting is
// refused with a message saying where the tasks went instead of saving edits
// to the old place.
//
// The mark rides the project's identity ref, refs/workbook/project, as one
// commit on top of the identity commit:
//
//	refs/workbook/project ──> M  "workbook: tasks moved"
//	                          ├── parent: I  (the identity commit, unchanged)
//	                          └── tree
//	                               ├── project.json  (I's document, byte for byte)
//	                               └── moved.json    (workbook.tasks-moved, version 1)
//
// Building on I makes publishing the mark an ordinary fast-forward: no
// commit is changed and nothing is force-pushed, and the identity itself is
// untouched, because project.json is the same blob. Every input is fixed, as
// it is for I, so two runs marking the same move write the same commit and
// the second is up to date.
//
// A Workbook build from before the mark existed refuses the commit outright —
// its identity reader requires a root commit with one entry — so its fetch
// stops before any task is synchronized and its publication refuses before any
// task ref is pushed. It says the identity is invalid rather than where the
// tasks went, but it saves nothing to the old place.
//
// The tasks can come back to a place they left. The return is one more
// fast-forward commit, R, on top of that place's marker, whose tree is I's tree
// and nothing else, so it needs no document of its own; leaving again builds a
// new marker on R. A place's identity ref is therefore one line of commits
// that alternates from I:
//
//	refs/workbook/project ──> M2 ── R1 ── M1 ── I
//	                          moved  returned  moved  identity
//
// A place is read by the shape of its tip, never by a commit message: I or R
// means the tasks are there, M means they went where it says
// (readIdentityChain). A build from before returns existed refuses R, and a
// marker on top of R, as an invalid identity, so it too stops before writing.
const (
	tasksMovedDocumentPath  = "moved.json"
	tasksMovedFormat        = "workbook.tasks-moved"
	tasksMovedVersion       = 1
	tasksMovedCommitMessage = "workbook: tasks moved"
	// tasksReturnedCommitMessage names a return commit for a person reading
	// the ref's history. Workbook reads the commit's shape, never its message.
	tasksReturnedCommitMessage = "workbook: tasks returned"
)

// TasksMoved is the marker document: which project, and where its tasks went.
type TasksMoved struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	ProjectID string `json:"projectId"`
	// Remote is the remote name the project's settings give the new place.
	Remote string `json:"remote"`
	// URL is the new place's address, as the project's settings record it.
	URL string `json:"url"`
}

func encodeTasksMoved(moved TasksMoved) ([]byte, error) {
	if err := validateTasksMoved(moved); err != nil {
		return nil, err
	}
	contents, err := json.Marshal(moved)
	if err != nil {
		return nil, core.Wrap(core.CategoryValidation, "cannot encode the tasks-moved marker", err)
	}
	return append(contents, '\n'), nil
}

func decodeTasksMoved(contents []byte) (TasksMoved, error) {
	var moved TasksMoved
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&moved); err != nil {
		return TasksMoved{}, core.Wrap(core.CategoryCorruptData, "cannot decode the tasks-moved marker", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return TasksMoved{}, core.Errorf(core.CategoryCorruptData, "the tasks-moved marker holds more than one JSON value")
	}
	if moved.Format == tasksMovedFormat && moved.Version > tasksMovedVersion {
		return TasksMoved{}, core.Errorf(core.CategoryNewerWriter,
			"the tasks-moved marker is version %d, written by a newer Workbook; upgrade Workbook", moved.Version)
	}
	if err := validateTasksMoved(moved); err != nil {
		return TasksMoved{}, core.Wrap(core.CategoryCorruptData, "the tasks-moved marker is invalid", err)
	}
	canonical, err := encodeTasksMoved(moved)
	if err != nil || !bytes.Equal(canonical, contents) {
		return TasksMoved{}, core.Errorf(core.CategoryCorruptData, "the tasks-moved marker is not canonical")
	}
	return moved, nil
}

func validateTasksMoved(moved TasksMoved) error {
	if moved.Format != tasksMovedFormat || moved.Version != tasksMovedVersion {
		return core.Errorf(core.CategoryValidation, "unsupported tasks-moved marker %q version %d", moved.Format, moved.Version)
	}
	if err := core.ValidateProjectID(moved.ProjectID); err != nil {
		return err
	}
	if err := core.ValidateTasksRemoteName(moved.Remote); err != nil {
		return err
	}
	return core.ValidateTasksRemoteURL(moved.URL)
}

// writeTasksMovedCommit builds the marker commit on top of base, the identity
// commit or a return above it, whose document it keeps.
func (r *Repository) writeTasksMovedCommit(ctx context.Context, base identityRecord, moved TasksMoved) (string, error) {
	root := base
	marker, err := encodeTasksMoved(moved)
	if err != nil {
		return "", err
	}
	document, err := r.writeBlob(ctx, root.Document)
	if err != nil {
		return "", err
	}
	markerBlob, err := r.writeBlob(ctx, marker)
	if err != nil {
		return "", err
	}
	var entries bytes.Buffer
	fmt.Fprintf(&entries, "100644 blob %s\t%s\n", markerBlob, tasksMovedDocumentPath)
	fmt.Fprintf(&entries, "100644 blob %s\t%s\n", document, identityDocumentPath)
	output, err := r.Git(ctx, entries.Bytes(), "mktree")
	if err != nil {
		return "", err
	}
	tree, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid tree object ID", err)
	}
	return r.writeIdentityChainCommit(ctx, tree, root.Head, tasksMovedCommitMessage)
}

// writeTasksReturnedCommit builds the return commit on top of a place's
// marker: its tree is the identity commit's tree, unchanged, which is what
// says the tasks are back without naming any place.
func (r *Repository) writeTasksReturnedCommit(ctx context.Context, marked identityRecord) (string, error) {
	output, err := r.Git(ctx, nil, "rev-parse", "--verify", "--quiet", marked.Root+"^{tree}")
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "cannot read the project identity commit's tree", err)
	}
	tree, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid tree object ID", err)
	}
	return r.writeIdentityChainCommit(ctx, tree, marked.Head, tasksReturnedCommitMessage)
}

// writeIdentityChainCommit writes one commit above the identity commit with the
// identity commit's fixed author, committer and date, so the same move or
// return written twice is the same commit.
func (r *Repository) writeIdentityChainCommit(ctx context.Context, tree, parent, message string) (string, error) {
	environment := []string{
		"GIT_AUTHOR_NAME=" + identityCommitName,
		"GIT_AUTHOR_EMAIL=" + identityCommitEmail,
		"GIT_AUTHOR_DATE=" + identityCommitDate,
		"GIT_COMMITTER_NAME=" + identityCommitName,
		"GIT_COMMITTER_EMAIL=" + identityCommitEmail,
		"GIT_COMMITTER_DATE=" + identityCommitDate,
	}
	output, err := r.gitWithEnv(ctx, environment, nil,
		"-c", "commit.gpgsign=false", "commit-tree", tree, "-p", parent, "-m", message)
	if err != nil {
		return "", err
	}
	commit, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid commit object ID", err)
	}
	return commit, nil
}

// identityChainLimit bounds how many commits a remote's identity ref may
// carry above the identity commit: one per move and one per return, so a
// project can move its tasks 256 times. A longer chain is refused as corrupt
// rather than read, because the ref is fetched from whoever can push to it.
const identityChainLimit = 512

// identityChain is what the commits below an identity ref tip with a parent
// say: the identity commit at the bottom, and the moves and returns above it.
type identityChain struct {
	root string
	// moved is the tip's marker, when the tip is one: the place is left.
	moved *TasksMoved
	// departed is the newest marker in the chain: the tip's own, or, for a
	// tip that records a return, the marker it returned over.
	departed *TasksMoved
	// departures is every marker in the chain, newest first.
	departures []TasksMoved
}

// readIdentityChain validates the history below an identity ref tip that has
// a parent, which a place the tasks moved away from, or came back to, carries.
// The tip has already been read: document is its project.json blob and
// tipMarker its moved.json blob, empty for a return.
//
// The history has one shape. At the bottom is the identity commit, a root
// whose tree holds project.json alone. Above it, each commit has exactly one
// parent and alternates: a marker on a live commit (the identity commit or a
// return), and a return on a marker. A marker's tree holds the same
// project.json blob and moved.json; a return's tree is the identity commit's
// own tree, which is how it records "tasks returned" without naming any place.
// Anything else is refused as corrupt, so the tip alone answers whether the
// place is live or left, and where to look next.
//
// It costs two Git processes, a rev-list of the chain and one batch read of
// its trees and markers, and runs at most once per tip and handle: commits do
// not change, so the answer is memoized by the tip's object ID.
func (r *Repository) readIdentityChain(
	ctx context.Context,
	ref, head string,
	objectIDBytes int,
	document, tipMarker string,
) (identityChain, error) {
	r.metadataMu.RLock()
	memo, found := r.identityChains[head]
	r.metadataMu.RUnlock()
	if found {
		return memo, nil
	}
	malformed := func() error {
		return core.Errorf(core.CategoryCorruptData,
			"%s carries an identity history that is not a chain of moves and returns built on the project's identity commit", ref)
	}
	output, err := r.Git(ctx, nil, "rev-list", "--topo-order", "--parents",
		fmt.Sprintf("--max-count=%d", identityChainLimit+2), head)
	if err != nil {
		return identityChain{}, err
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) > identityChainLimit+1 {
		return identityChain{}, core.Errorf(core.CategoryCorruptData,
			"%s carries more than %d moves and returns above the project's identity commit", ref, identityChainLimit)
	}
	commits := make([]string, len(lines))
	for index, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return identityChain{}, malformed()
		}
		commits[index] = fields[0]
		last := index == len(lines)-1
		switch {
		case last && len(fields) != 1:
			return identityChain{}, malformed()
		case !last && (len(fields) != 2 || index+1 >= len(lines)):
			return identityChain{}, malformed()
		}
		if index > 0 && strings.Fields(lines[index-1])[1] != fields[0] {
			return identityChain{}, malformed()
		}
	}
	if len(commits) < 2 || commits[0] != head {
		return identityChain{}, malformed()
	}
	// Markers and live commits alternate, the bottom one is live, and the tip
	// is a marker exactly when it carries moved.json.
	tipIsMarker := tipMarker != ""
	bottom := len(commits) - 1
	isMarker := func(index int) bool { return (index%2 == 0) == tipIsMarker }
	if isMarker(bottom) {
		return identityChain{}, malformed()
	}
	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		for index, commit := range commits {
			if _, err := fmt.Fprintf(writer, "%s^{tree}\n", commit); err != nil {
				return err
			}
			if isMarker(index) {
				if _, err := fmt.Fprintf(writer, "%s:%s\n", commit, tasksMovedDocumentPath); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return identityChain{}, err
	}
	defer batch.Close()
	type link struct {
		tree   batchObject
		marker batchObject
	}
	links := make([]link, len(commits))
	for index := range commits {
		for _, target := range []*batchObject{&links[index].tree, &links[index].marker} {
			if target == &links[index].marker && !isMarker(index) {
				continue
			}
			object, err := readBatchObject(batch.Reader())
			if err != nil {
				return identityChain{}, batch.ReadFailure("cannot read the project identity history from Git batch", err)
			}
			if object.refused != nil {
				return identityChain{}, object.refused
			}
			if object.missing {
				return identityChain{}, core.Errorf(core.CategoryCorruptData,
					"%s carries an identity history whose objects are missing", ref)
			}
			*target = object
		}
	}
	if err := batch.Finish(); err != nil {
		return identityChain{}, err
	}
	rootTree := links[bottom].tree
	if rootTree.kind != "tree" {
		return identityChain{}, malformed()
	}
	entries, err := parseRawIdentityTree(rootTree.contents, objectIDBytes, false)
	if err != nil {
		return identityChain{}, err
	}
	if entries[identityDocumentPath] != document {
		return identityChain{}, malformed()
	}
	chain := identityChain{root: commits[bottom]}
	for index := bottom - 1; index >= 0; index-- {
		tree := links[index].tree
		if !isMarker(index) {
			if tree.objectID != rootTree.objectID {
				return identityChain{}, malformed()
			}
			continue
		}
		if tree.kind != "tree" || links[index].marker.kind != "blob" {
			return identityChain{}, malformed()
		}
		entries, err := parseRawIdentityTree(tree.contents, objectIDBytes, true)
		if err != nil {
			return identityChain{}, err
		}
		if len(entries) != 2 || entries[identityDocumentPath] != document ||
			entries[tasksMovedDocumentPath] != links[index].marker.objectID {
			return identityChain{}, malformed()
		}
		moved, err := decodeTasksMoved(links[index].marker.contents)
		if err != nil {
			return identityChain{}, err
		}
		chain.departed = &moved
		chain.departures = append([]TasksMoved{moved}, chain.departures...)
		if index == 0 {
			chain.moved = &moved
		}
	}
	if tipIsMarker && links[0].marker.objectID != tipMarker {
		return identityChain{}, malformed()
	}
	r.metadataMu.Lock()
	if r.identityChains == nil {
		r.identityChains = map[string]identityChain{}
	}
	r.identityChains[head] = chain
	r.metadataMu.Unlock()
	return chain, nil
}

// trackedMoveMarker reports the marker this checkout last saw on remote's
// identity ref, whose tracking ref names head, if any. It runs no Git process
// in the ordinary case, where the tracking ref and the local identity ref name
// the same commit.
func (r *Repository) trackedMoveMarker(ctx context.Context, remote, head string) (TasksMoved, bool, error) {
	r.metadataMu.RLock()
	loaded, localHead := r.identityLoaded, r.identityHead
	r.metadataMu.RUnlock()
	if loaded && head == localHead {
		return TasksMoved{}, false, nil
	}
	record, err := r.identityRecordAt(ctx, remoteIdentityRefFor(remote), head)
	if err != nil {
		return TasksMoved{}, false, err
	}
	if record.Moved == nil {
		return TasksMoved{}, false, nil
	}
	return *record.Moved, true, nil
}

// returningRemote is the remote a move in progress on this handle is
// bringing the tasks back to, or "".
func (r *Repository) returningRemote() string {
	r.metadataMu.RLock()
	defer r.metadataMu.RUnlock()
	return r.returningTo
}

// movedFollowLimit bounds how many places followMoved visits.
const movedFollowLimit = 32

// movedPath is where following markers from one place led.
type movedPath struct {
	// Current is the last place reached: where the tasks are now, unless
	// Unverified or Loop says the walk stopped short of knowing that.
	Current TasksMoved
	// Places names every place visited after the first, by address.
	Places []string
	// Unverified says a place on the way could not be read, so Current is
	// the last place a marker named rather than one shown to be live.
	Unverified bool
	// Loop says the markers lead back to a place already visited: two moves
	// ran at once, or one was stopped in a way that left every place marked.
	Loop bool
	// at is when the walk was made, for the memo.
	at time.Time
}

// movedFollowLifetime is how long a long-lived handle, such as a board's,
// trusts a walk it made before walking again.
const movedFollowLifetime = 30 * time.Second

// followMoved follows markers from the place moved names to the place that
// holds the tasks now, so a checkout on settings any number of moves old is
// told the current place rather than the next one. Each step lists one place's
// identity ref and fetches it when this checkout lacks it: through this
// checkout's own remote of the name the marker gives, when it has one, and
// otherwise at the address the marker records. A walk that meets a place it
// already visited stops and says so, and none goes further than
// movedFollowLimit places. A place that cannot be read ends the walk there.
//
// It runs only on the way to a refusal, and at most once per marker in
// movedFollowLifetime on one handle.
func (r *Repository) followMoved(ctx context.Context, moved TasksMoved) movedPath {
	r.metadataMu.RLock()
	memo, found := r.followed[moved]
	r.metadataMu.RUnlock()
	if found && time.Since(memo.at) < movedFollowLifetime {
		return memo
	}
	path := movedPath{Current: moved, at: time.Now()}
	local, haveLocal, err := r.memoizedIdentityRecord()
	if err != nil || !haveLocal {
		local, haveLocal, _ = r.readIdentityRef(ctx, identityRef)
	}
	seen := map[TasksMoved]bool{}
	next := moved
	for hops := 1; ; hops++ {
		key := TasksMoved{Remote: next.Remote, URL: next.URL}
		if seen[key] {
			path.Loop = true
			break
		}
		seen[key] = true
		path.Current = next
		path.Places = append(path.Places, next.URL)
		if hops > movedFollowLimit {
			path.Unverified = true
			break
		}
		record, found, err := r.readPlaceIdentity(ctx, next)
		if err != nil {
			path.Unverified = true
			break
		}
		if !found {
			break
		}
		if !haveLocal || !bytes.Equal(record.Document, local.Document) {
			path.Unverified = true
			break
		}
		if record.Moved == nil {
			break
		}
		next = *record.Moved
	}
	r.metadataMu.Lock()
	if r.followed == nil {
		r.followed = map[TasksMoved]movedPath{}
	}
	r.followed[moved] = path
	r.metadataMu.Unlock()
	return path
}

// readPlaceIdentity reads the identity ref of the place a marker names,
// through this checkout's remote of that name when it has one, which is the
// address its owner gave it. A tasks-only folder's one remote is called origin
// whatever place it points at, so there the marker's address is used always.
func (r *Repository) readPlaceIdentity(ctx context.Context, place TasksMoved) (identityRecord, bool, error) {
	tasksOnly, err := r.TasksOnly(ctx)
	if err != nil {
		return identityRecord{}, false, err
	}
	if !tasksOnly && r.hasGitRemote(ctx, place.Remote) {
		record, found, err := r.remoteIdentityRecord(ctx, place.Remote)
		if err != nil || !found {
			return identityRecord{}, found, err
		}
		return record, true, nil
	}
	if err := core.ValidateTasksRemoteURL(place.URL); err != nil {
		return identityRecord{}, false, err
	}
	// An address read from a marker is somebody else's text. It passed the
	// same check an address given to config set does, and Git's own transport
	// policy, which refuses ext:: unless configured otherwise, applies to it.
	listing, err := r.Git(ctx, nil, "ls-remote", "--refs", place.URL, identityRef)
	if err != nil {
		return identityRecord{}, false, err
	}
	head, err := parseRemoteIdentityHead(listing)
	if err != nil || head == "" {
		return identityRecord{}, false, err
	}
	if _, err := r.Git(ctx, nil, "cat-file", "-e", head+"^{commit}"); err != nil {
		if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--no-write-fetch-head",
			"--no-auto-maintenance", place.URL, identityRef); err != nil {
			return identityRecord{}, false, err
		}
	}
	if err := r.rememberGitObjectID(head); err != nil {
		return identityRecord{}, false, err
	}
	decoded, err := decodeObjectID(head)
	if err != nil {
		return identityRecord{}, false, core.Wrap(core.CategoryCorruptData, "Git returned an invalid project identity ref object ID", err)
	}
	record, err := r.readIdentityObjects(ctx, "the identity ref at "+place.URL, head, len(decoded))
	return record, err == nil, err
}

// remoteIdentityRecord reads remote's identity ref as it is now, asking remote
// and fetching it into its tracking ref when that is behind.
func (r *Repository) remoteIdentityRecord(ctx context.Context, remote string) (identityRecord, bool, error) {
	tracking := remoteIdentityRefFor(remote)
	listing, err := r.Git(ctx, nil, "ls-remote", "--refs", remote, identityRef)
	if err != nil {
		return identityRecord{}, false, err
	}
	head, err := parseRemoteIdentityHead(listing)
	if err != nil || head == "" {
		return identityRecord{}, false, err
	}
	current, _ := r.Git(ctx, nil, "rev-parse", "--verify", "--quiet", tracking)
	if strings.TrimSpace(string(current)) != head {
		if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--no-auto-maintenance", remote, identityFetchRefspecFor(remote)); err != nil {
			return identityRecord{}, false, err
		}
	}
	return r.readIdentityRef(ctx, tracking)
}

// tasksMovedRefusalHere is tasksMovedRefusal for this handle's remote, with the
// advice a tasks-only folder needs in place of a pull. It names the place the
// tasks are now, following the markers there.
func (r *Repository) tasksMovedRefusalHere(ctx context.Context, moved TasksMoved) error {
	path := r.followMoved(ctx, moved)
	if path.Loop {
		return movedLoopRefusal(r.remote(), moved, path)
	}
	moved = path.Current
	tasksOnly, err := r.TasksOnly(ctx)
	if err == nil && tasksOnly {
		return core.TasksRemoteError(core.CategoryNotInitialized,
			core.TasksRemoteRefusal{
				Reason:  core.TasksRemoteMoved,
				Remote:  r.remote(),
				MovedTo: &core.TasksRemoteTarget{Remote: moved.Remote, URL: moved.URL},
				Command: "workbook setup --tasks-only " + moved.URL,
			},
			"this project's tasks moved from this folder's origin to %s, so nothing is read from or saved to origin any more; "+
				"run workbook setup --tasks-only %s in this folder, which points it at the new place and publishes there "+
				"every edit saved here that has not been published, then serve the board from here again",
			moved.URL, moved.URL)
	}
	if !path.Unverified && r.settingsCommittedAwayFrom(ctx, moved.Remote) && r.neverLeftFor(ctx, moved, r.remote()) {
		return unfinishedMoveRefusal(r.remote(), moved)
	}
	return tasksMovedRefusal(r.remote(), moved)
}

// neverLeftFor reports whether the tasks have certainly never been moved to
// remote, so settings that once named place and now name remote are a change
// of tasks-remote whose move-tasks has not run, and not a checkout that missed
// a later change. It holds only when place, where the tasks are, is live, and
// no marker recorded at remote, at place, or at any place those markers lead
// to names remote as where the tasks went. A place that cannot be read says
// nothing, and the refusal stays the ordinary one.
func (r *Repository) neverLeftFor(ctx context.Context, place TasksMoved, remote string) bool {
	record, found, err := r.readPlaceIdentity(ctx, place)
	if err != nil || !found || record.Moved != nil {
		return false
	}
	left, found, err := r.readPlaceIdentity(ctx, TasksMoved{Remote: remote})
	if err != nil || !found {
		return false
	}
	queue := append(append([]TasksMoved(nil), left.Departures...), record.Departures...)
	seen := map[TasksMoved]bool{{Remote: remote}: true, {Remote: place.Remote, URL: place.URL}: true}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if next.Remote == remote {
			return false
		}
		key := TasksMoved{Remote: next.Remote, URL: next.URL}
		if seen[key] {
			continue
		}
		if len(seen) > movedFollowLimit {
			return false
		}
		seen[key] = true
		visited, found, err := r.readPlaceIdentity(ctx, next)
		if err != nil {
			return false
		}
		if found {
			queue = append(queue, visited.Departures...)
		}
	}
	return true
}

// settingsCommittedAwayFrom reports whether the settings committed at HEAD
// name this handle's remote while an earlier committed version of them named
// place, where the markers say the tasks are. That is a change of
// tasks-remote, committed and pulled, whose move-tasks has not run yet:
// pulling and running setup cannot help, because the settings are already
// the new ones. It reads the file's last 32 committed versions, and only on
// the way to a refusal; a clone without that history, or a working tree
// whose settings differ from HEAD's, is told to pull and run setup as before.
func (r *Repository) settingsCommittedAwayFrom(ctx context.Context, place string) bool {
	if place == "" || place == r.remote() {
		return false
	}
	listing, err := r.Git(ctx, nil, "log", "--format=%H", "-n", "32", "HEAD", "--", configPath)
	if err != nil {
		return false
	}
	commits := strings.Fields(string(listing))
	if len(commits) < 2 {
		return false
	}
	var request strings.Builder
	for _, commit := range commits {
		request.WriteString(commit + ":" + configPath + "\n")
	}
	output, err := r.Git(ctx, []byte(request.String()), "cat-file", "--batch")
	if err != nil {
		return false
	}
	names := committedTasksRemoteNames(output)
	if len(names) != len(commits) || names[0] != r.remote() {
		return false
	}
	for _, name := range names[1:] {
		if name == place {
			return true
		}
	}
	return false
}

// committedTasksRemoteNames is the remote each version of the settings in a
// cat-file --batch answer names for the tasks, newest first: origin when it
// names none, and "" for a version that is missing or cannot be read.
func committedTasksRemoteNames(output []byte) []string {
	var names []string
	for len(output) > 0 {
		newline := bytes.IndexByte(output, '\n')
		if newline < 0 {
			break
		}
		header := strings.Fields(string(output[:newline]))
		output = output[newline+1:]
		if len(header) != 3 || header[1] != "blob" {
			names = append(names, "")
			continue
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size+1 > len(output) {
			break
		}
		var settings struct {
			TasksRemote struct {
				Name string `json:"name"`
			} `json:"tasksRemote"`
		}
		name := ""
		if json.Unmarshal(output[:size], &settings) == nil {
			name = settings.TasksRemote.Name
			if name == "" {
				name = "origin"
			}
		}
		names = append(names, name)
		output = output[size+1:]
	}
	return names
}

// unfinishedMoveRefusal is the moved refusal for a checkout whose committed
// settings already name the place being moved to, before move-tasks ran.
func unfinishedMoveRefusal(remote string, moved TasksMoved) error {
	return core.TasksRemoteError(core.CategoryNotInitialized,
		core.TasksRemoteRefusal{
			Reason:  core.TasksRemoteMoved,
			Remote:  remote,
			MovedTo: &core.TasksRemoteTarget{Remote: moved.Remote, URL: moved.URL},
			Command: "workbook move-tasks",
		},
		"this checkout's %s names %s for the tasks, and an earlier commit of it named %s, but the tasks are still at %s "+
			"(remote %q) and %s is marked as the place they left: the change of tasks-remote is committed, and the move "+
			"it is for has not been run. Pulling and running setup cannot finish it. Whoever changed tasks-remote runs "+
			"workbook move-tasks, from any clone with these settings, which moves the tasks to %s; "+
			"edits this checkout has not published yet are kept, and go there at its next publication after that",
		configPath, remote, moved.Remote, moved.URL, moved.Remote, remote, remote)
}

// tasksMovedRefusal is what every command says in a checkout whose tasks
// remote is marked as moved: where the tasks are, and what to do.
func tasksMovedRefusal(remote string, moved TasksMoved) error {
	return core.TasksRemoteError(core.CategoryNotInitialized,
		core.TasksRemoteRefusal{
			Reason:  core.TasksRemoteMoved,
			Remote:  remote,
			MovedTo: &core.TasksRemoteTarget{Remote: moved.Remote, URL: moved.URL},
			Command: "workbook setup",
		},
		"this project's tasks moved from %s to %s (remote %q), so nothing is read from or saved to %s any more; "+
			"pull the commit that sets tasks-remote in %s, then run workbook setup; "+
			"edits this checkout has not published yet are kept and go to the new place then. "+
			"If %s already names %s because the tasks are being moved back here, whoever changed it runs workbook move-tasks first",
		remote, moved.URL, moved.Remote, remote, configPath, configPath, remote)
}

// movedLoopRefusal is the refusal for markers that lead in a circle, so no
// place says it holds the tasks.
func movedLoopRefusal(remote string, moved TasksMoved, path movedPath) error {
	return core.TasksRemoteError(core.CategoryNotInitialized,
		core.TasksRemoteRefusal{
			Reason:  core.TasksRemoteMoved,
			Remote:  remote,
			MovedTo: &core.TasksRemoteTarget{Remote: moved.Remote, URL: moved.URL},
			Command: "workbook move-tasks",
		},
		"%s is marked as the place this project's tasks moved away from, and following the markers from there "+
			"(%s) leads back to a place already passed, so no place says it holds the tasks: a move was stopped partway, "+
			"or two ran at once, and nothing is published anywhere until it is finished. "+
			"Run workbook move-tasks to finish the move, from a clone whose %s names where the tasks are to be, "+
			"which is usually whoever started it; edits this checkout has not published yet are kept",
		remote, strings.Join(path.Places, " -> "), configPath)
}

// movedRemotePublication is the refusal for publishing to a remote marked as
// moved, which a command reaches only by naming that remote with --remote.
func movedRemotePublication(remote string, moved TasksMoved) error {
	return core.TasksRemoteError(core.CategoryValidation,
		core.TasksRemoteRefusal{
			Reason:  core.TasksRemoteMoved,
			Remote:  remote,
			MovedTo: &core.TasksRemoteTarget{Remote: moved.Remote, URL: moved.URL},
		},
		"%s is marked as the place this project's tasks moved away from, to %s (remote %q); "+
			"it can still be read, but nothing is published to it", remote, moved.URL, moved.Remote)
}
