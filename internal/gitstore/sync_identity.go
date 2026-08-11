package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// SyncIdentityStatus reports what one run did about the canonical project
// identity.
type SyncIdentityStatus string

const (
	// SyncIdentityMatched reports that this clone and origin already agree.
	SyncIdentityMatched SyncIdentityStatus = "matched"
	// SyncIdentityCreated reports that this run published the local identity
	// ref, which is the one-time migration for a project that predates it.
	SyncIdentityCreated SyncIdentityStatus = "created"
	// SyncIdentityPublished reports that origin gained the identity ref.
	SyncIdentityPublished SyncIdentityStatus = "published"
	// SyncIdentityAdopted reports that origin's identity commit replaced an
	// equivalent local one.
	SyncIdentityAdopted SyncIdentityStatus = "adopted"
	// SyncIdentityMismatched reports two different projects. It always travels
	// with an error.
	SyncIdentityMismatched SyncIdentityStatus = "mismatched"
)

// SyncIdentityResult is the identity stage's account of one run.
type SyncIdentityResult struct {
	Status SyncIdentityStatus `json:"status"`
	Detail string             `json:"detail,omitempty"`
	Head   string             `json:"head,omitempty"`
	// Ignored names refs origin holds under the identity ref's name that this
	// version does not read. Like the task namespace's ignored refs, this is a
	// report and never an instruction: the names belong to origin, and a clone
	// states what it skipped rather than refusing to run.
	Ignored []string `json:"ignoredRefs,omitempty"`
}

// originIdentityState records what this opened repository has established about
// origin's copy of the canonical identity.
//
// It exists so that no path publishes a task ref to a remote whose project has
// not been checked. Synchronization establishes it for free — it already
// fetched and compared origin's ref — and the publication paths that run
// without a fetch establish it themselves.
type originIdentityState uint8

const (
	originIdentityUnknown originIdentityState = iota
	// originIdentityAgreed means origin's identity ref was observed in this
	// command and names this project.
	originIdentityAgreed
	// originIdentityAbsent means origin was observed in this command and has no
	// identity ref, so publishing one is this clone's job.
	originIdentityAbsent
	// originIdentityUnavailable means origin has no identity ref and this clone
	// could not publish one — a read-only remote, or a name blocked by a ref
	// somebody else pushed under it. Nothing on origin claims a project, so
	// publication proceeds exactly as it did before identity refs existed
	// rather than leaving the user with a repository they cannot push from.
	originIdentityUnavailable
)

func (r *Repository) rememberOriginIdentity(state originIdentityState) {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	r.originIdentity = state
}

func (r *Repository) observedOriginIdentity() originIdentityState {
	r.metadataMu.RLock()
	defer r.metadataMu.RUnlock()
	return r.originIdentity
}

// ensureOriginIdentityAgreement establishes that origin is this project before
// any task ref is published to it.
//
// Task refs are the project's history, and pushing one to another project's
// repository writes into a history it does not belong to. Synchronization
// already refuses that, but publication has its own paths — `workbook push`,
// and the targeted push every automatically synchronizing mutation makes — and
// an invariant that only one path enforces is not an invariant.
//
// It costs nothing on the mutation path, where a fetch has already compared the
// two refs in the same command. Where nothing is known yet it asks origin once,
// which is also what makes the bare-branch bootstrap structurally sound: task
// refs on origin now imply an identity ref beside them, so a fresh clone always
// has something authoritative to adopt.
func (r *Repository) ensureOriginIdentityAgreement(ctx context.Context, observed map[string]string) error {
	switch r.observedOriginIdentity() {
	case originIdentityAgreed, originIdentityUnavailable:
		return nil
	case originIdentityAbsent:
		return r.publishIdentityBeforeTasks(ctx, "")
	}

	remoteHead, probed := "", false
	if observed != nil {
		remoteHead, probed = observed[identityRef]
		probed = true
	}
	if !probed {
		listing, err := r.Git(ctx, nil, "ls-remote", "--refs", "origin", identityRef)
		if err != nil {
			return core.Wrap(core.CategoryOperational,
				"cannot ask origin which Workbook project it holds before publishing", err)
		}
		remoteHead, err = parseRemoteIdentityHead(listing)
		if err != nil {
			return err
		}
	}
	return r.publishIdentityBeforeTasks(ctx, remoteHead)
}

// publishIdentityBeforeTasks reconciles this clone's identity with the head
// origin reported, publishing it when origin has none.
func (r *Repository) publishIdentityBeforeTasks(ctx context.Context, remoteHead string) error {
	// Publication runs on a validated configuration, so this process has already
	// read and validated its own identity. Reusing it keeps the check free of
	// local Git work on the path every mutation takes.
	local, found, err := r.memoizedIdentityRecord()
	if err != nil {
		return err
	}
	if !found {
		local, found, err = r.readIdentityRef(ctx, identityRef)
		if err != nil {
			return err
		}
	}
	if !found {
		return core.Errorf(core.CategoryCorruptData,
			"%s is missing, so this clone cannot say which project it is publishing to origin; rerun workbook setup",
			identityRef)
	}
	if remoteHead == local.Head {
		r.rememberOriginIdentity(originIdentityAgreed)
		return nil
	}

	if remoteHead == "" {
		// The deterministic identity commit makes this idempotent: a clone that
		// raced here published the same object, and Git reports it up to date.
		// A publication that origin refuses for its own reasons — read-only
		// access, or a name blocked by a ref pushed under it — leaves nothing on
		// origin claiming any project, which is the state every pre-v0.5.0
		// remote is in. Task publication continues from there; refusing would
		// take a working repository away from a user who cannot fix the remote.
		result := &SyncIdentityResult{Status: SyncIdentityMatched, Head: local.Head}
		if err := r.publishIdentityToOrigin(ctx, local, result); err != nil {
			return err
		}
		r.rememberOriginIdentity(originIdentityAgreed)
		if result.Status != SyncIdentityPublished && result.Status != SyncIdentityAdopted {
			r.rememberOriginIdentity(originIdentityUnavailable)
		}
		return nil
	}

	// Origin holds a different commit. Only its document decides whether that is
	// this project written differently or another project entirely.
	if _, err := r.Git(ctx, nil,
		"fetch", "--no-tags", "--no-auto-maintenance", "origin", identityFetchRefspec); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot fetch origin's Workbook project identity", err)
	}
	remote, found, err := r.readIdentityRef(ctx, remoteIdentityRef)
	if err != nil {
		return err
	}
	if !found {
		return core.Errorf(core.CategoryOperational,
			"origin listed %s but did not deliver it; rerun the command", identityRef)
	}
	if !bytes.Equal(remote.Document, local.Document) {
		return r.originIdentityMismatch(local, remote)
	}
	result := &SyncIdentityResult{Status: SyncIdentityMatched, Head: local.Head}
	if err := r.adoptOriginIdentityCommit(ctx, local, remote, result); err != nil {
		return err
	}
	r.rememberOriginIdentity(originIdentityAgreed)
	return nil
}

// parseRemoteIdentityHead reads the single identity record an ls-remote of the
// identity ref may return. Origin's namespace can hold names under it, which
// this query cannot match and which are none of this check's business.
func parseRemoteIdentityHead(output []byte) (string, error) {
	head := ""
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if line == "" {
			continue
		}
		objectID, refName, found := strings.Cut(line, "\t")
		if !found || objectID == "" {
			return "", core.Errorf(core.CategoryOperational, "Git returned an invalid remote identity record")
		}
		if refName != identityRef {
			continue
		}
		if head != "" {
			return "", core.Errorf(core.CategoryOperational, "Git returned duplicate remote identity records")
		}
		head = objectID
	}
	return head, nil
}

// reconcileIdentity converges this clone's canonical identity with origin's.
//
// It runs before any task work, because every task document names the project
// these two refs are being compared for: replaying one project's operations
// into another's history is the failure this stage exists to make impossible.
// A mismatch therefore stops the run instead of degrading it.
//
// It costs no network round trip. Origin's identity rides the same fetch that
// brings the task refs, and everything else here reads local refs — except the
// one publication that a project performs once in its life.
// announce distinguishes the caller that reports this stage's outcome from the
// one that only needs its effect: Fetch runs the stage but returns a
// task-shaped result with nowhere to put it, so it must not consume the
// one-time publication announcement that Sync would otherwise print.
func (r *Repository) reconcileIdentity(ctx context.Context, publish, announce bool) (*SyncIdentityResult, error) {
	// Both refs are enumerated together. Synchronization is on the hot path of
	// every automatically synchronizing mutation, so the stage costs one ref
	// listing plus, at most, one object read of origin's document.
	listing, err := r.listIdentityRefs(ctx, identityRef, remoteIdentityRef)
	if err != nil {
		return nil, err
	}
	localHead, found := listing.Heads[identityRef]
	if !found {
		// Synchronization runs on a validated configuration, so the identity
		// was resolved and published before this stage. Its absence now means
		// something deleted the ref mid-command.
		return nil, core.Errorf(core.CategoryCorruptData,
			"%s disappeared during synchronization; rerun the command", identityRef)
	}
	remoteHead, remoteFound := listing.Heads[remoteIdentityRef]
	// The local publication is claimed before anything else can report, so that
	// a run which goes on to publish to origin says so once rather than leaving
	// the migration to be announced again by the next run.
	var localSource identitySource
	publishedLocally := false
	if announce {
		localSource, publishedLocally = r.consumeIdentityPublication()
	}

	result := &SyncIdentityResult{Status: SyncIdentityMatched, Head: localHead}
	// Equal object IDs are equal documents, so the steady state — the case that
	// runs on every synchronization forever — reads no objects at all.
	switch {
	case remoteFound && remoteHead == localHead:
		r.rememberOriginIdentity(originIdentityAgreed)
	case remoteFound:
		local, err := r.identityRecordAt(ctx, identityRef, localHead)
		if err != nil {
			return nil, err
		}
		remote, err := r.identityRecordAt(ctx, remoteIdentityRef, remoteHead)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(remote.Document, local.Document) {
			result.Status = SyncIdentityMismatched
			result.Detail = fmt.Sprintf("this clone is project %s (key %s); origin is project %s (key %s)",
				local.Identity.ProjectID, local.Identity.Key, remote.Identity.ProjectID, remote.Identity.Key)
			return result, r.originIdentityMismatch(local, remote)
		}
		if err := r.adoptOriginIdentityCommit(ctx, local, remote, result); err != nil {
			return result, err
		}
		r.rememberOriginIdentity(originIdentityAgreed)
	case publish:
		local, err := r.identityRecordAt(ctx, identityRef, localHead)
		if err != nil {
			return nil, err
		}
		if err := r.publishIdentityToOrigin(ctx, local, result); err != nil {
			return result, err
		}
	default:
		r.rememberOriginIdentity(originIdentityAbsent)
	}

	if result.Status == SyncIdentityMatched && publishedLocally {
		result.Status = SyncIdentityCreated
		result.Detail = fmt.Sprintf("published %s from the %s", identityRef, identityOriginNoun(localSource))
	}
	if len(listing.Ignored) > 0 {
		result.Ignored = listing.Ignored
		if result.Detail == "" {
			result.Detail = fmt.Sprintf("origin holds %d ref(s) under %s that this version does not read",
				len(listing.Ignored), identityRef)
		}
	}
	if result.Status == SyncIdentityMatched && len(result.Ignored) == 0 {
		// Nothing happened, so nothing is reported. Callers omit the member
		// entirely, which keeps a steady-state run's output exactly what it was
		// before this stage existed.
		return nil, nil
	}
	return result, nil
}

func identityOriginNoun(source identitySource) string {
	switch source {
	case identitySourceFile:
		return "tracked " + configPath
	case identitySourceGuard:
		return "private project guard"
	case identitySourceMint:
		return "newly minted project identity"
	default:
		return "local identity ref"
	}
}

// publishIdentityToOrigin pushes the identity ref that origin does not have.
//
// A rejection is not automatically a failure. Because the identity commit is
// deterministic, the ordinary race — two clones adopting the same identity at
// once — produces the same object, and Git reports it as up to date. What a
// rejection means is that origin holds something else, so this re-reads origin
// and decides: an identical document is adopted, and a different one is the
// mismatch that stops the run.
//
// A push that fails without origin gaining the ref at all is a transport or
// permission problem, not a disagreement. It is reported and the run continues:
// the identity is already durable locally, and refusing to synchronize tasks
// over it would take a working repository away from a user who cannot fix it.
func (r *Repository) publishIdentityToOrigin(ctx context.Context, local identityRecord, result *SyncIdentityResult) error {
	push := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil,
		"push", "--porcelain", "origin", local.Head+":"+identityRef)
	if push.err == nil {
		result.Status = SyncIdentityPublished
		result.Detail = fmt.Sprintf("published %s to origin", identityRef)
		r.rememberOriginIdentity(originIdentityAgreed)
		return nil
	}
	pushErr := gitCommandResultError(push)

	if _, err := r.Git(ctx, nil,
		"fetch", "--no-tags", "--no-auto-maintenance", "origin", identityFetchRefspec); err != nil {
		result.Detail = fmt.Sprintf("could not publish %s to origin: %v", identityRef, pushErr)
		r.rememberOriginIdentity(originIdentityAbsent)
		return nil
	}
	remote, found, err := r.readIdentityRef(ctx, remoteIdentityRef)
	if err != nil {
		return err
	}
	if !found {
		result.Detail = fmt.Sprintf("could not publish %s to origin: %v", identityRef, pushErr)
		r.rememberOriginIdentity(originIdentityAbsent)
		return nil
	}
	if !bytes.Equal(remote.Document, local.Document) {
		result.Status = SyncIdentityMismatched
		result.Detail = fmt.Sprintf("this clone is project %s (key %s); origin is project %s (key %s)",
			local.Identity.ProjectID, local.Identity.Key, remote.Identity.ProjectID, remote.Identity.Key)
		return r.originIdentityMismatch(local, remote)
	}
	if remote.Head == local.Head {
		result.Status = SyncIdentityPublished
		result.Detail = fmt.Sprintf("%s was already published to origin", identityRef)
		r.rememberOriginIdentity(originIdentityAgreed)
		return nil
	}
	if err := r.adoptOriginIdentityCommit(ctx, local, remote, result); err != nil {
		return err
	}
	r.rememberOriginIdentity(originIdentityAgreed)
	return nil
}

// adoptOriginIdentityCommit takes origin's commit for an identity this clone
// already holds under a different object.
//
// This is the one place a Workbook ref moves off a commit that is not its
// ancestor, and it is safe for a reason that holds nowhere else: the ref
// carries exactly one immutable document, the two commits carry the same
// document byte for byte, and the commit has no history to lose. Converging on
// origin's object is what lets a clone that published an older-shaped commit —
// or one written before the commit was made deterministic — stop being
// permanently rejected by origin.
func (r *Repository) adoptOriginIdentityCommit(
	ctx context.Context,
	local, remote identityRecord,
	result *SyncIdentityResult,
) error {
	if err := r.replaceRef(ctx, identityRef, remote.Head, local.Head); err != nil {
		return core.Wrap(core.CategoryStaleWrite,
			"cannot adopt origin's Workbook project identity commit", err)
	}
	r.rememberIdentityHead(remote.Head)
	result.Status = SyncIdentityAdopted
	result.Head = remote.Head
	result.Detail = fmt.Sprintf(
		"adopted origin's identity commit for project %s; both commits carry the same document",
		remote.Identity.ProjectID)
	return nil
}

func (r *Repository) originIdentityMismatch(local, remote identityRecord) error {
	return core.Errorf(core.CategoryCorruptData,
		"this repository and origin are different Workbook projects: %s names project %s (key %s) but origin's %s names project %s (key %s); "+
			"a project identity is immutable, so one of these clones is pointed at the wrong remote — check `git remote -v` before synchronizing again",
		identityRef, local.Identity.ProjectID, local.Identity.Key,
		identityRef, remote.Identity.ProjectID, remote.Identity.Key)
}

// memoizedIdentityRecord returns the identity this process already resolved and
// validated, without touching Git.
func (r *Repository) memoizedIdentityRecord() (identityRecord, bool, error) {
	r.metadataMu.RLock()
	loaded, identity, head := r.identityLoaded, r.identity, r.identityHead
	r.metadataMu.RUnlock()
	if !loaded || head == "" {
		return identityRecord{}, false, nil
	}
	document, err := core.EncodeDocument(identity)
	if err != nil {
		return identityRecord{}, false, err
	}
	return identityRecord{Head: head, Identity: identity, Document: document}, true, nil
}

// identityRecordAt reads the document at one already-observed identity ref tip,
// reusing the document this repository validated when the ref has not moved.
// Every command that synchronizes has loaded its configuration first, so the
// local side of the comparison usually costs nothing at all.
func (r *Repository) identityRecordAt(ctx context.Context, ref, head string) (identityRecord, error) {
	r.metadataMu.RLock()
	loaded, identity, memoized := r.identityLoaded, r.identity, r.identityHead
	r.metadataMu.RUnlock()
	if loaded && memoized == head {
		document, err := core.EncodeDocument(identity)
		if err != nil {
			return identityRecord{}, err
		}
		return identityRecord{Head: head, Identity: identity, Document: document}, nil
	}

	if err := r.rememberGitObjectID(head); err != nil {
		return identityRecord{}, core.Wrap(core.CategoryCorruptData, "Git returned an invalid project identity ref object ID", err)
	}
	decoded, err := decodeObjectID(head)
	if err != nil {
		return identityRecord{}, core.Wrap(core.CategoryCorruptData, "Git returned an invalid project identity ref object ID", err)
	}
	return r.readIdentityObjects(ctx, ref, head, len(decoded))
}

func (r *Repository) rememberIdentityHead(head string) {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	r.identityHead = head
}
