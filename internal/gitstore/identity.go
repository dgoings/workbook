package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

const (
	// identityRef holds a project's canonical identity document.
	//
	// It sits outside refs/workbook/tasks/*, which is what keeps a v0.4.x
	// client blind to it: that version's fetch refspec and ls-remote patterns
	// name only the task namespace, so publishing this ref is invisible to a
	// teammate who has not upgraded.
	//
	// The name is a leaf and must stay one. Git's directory/file rule means no
	// ref may ever be created under refs/workbook/project/*; a future family of
	// project documents needs a sibling namespace, not children of this ref.
	// Readers here enforce that: a listing that returns anything but this exact
	// name is corruption rather than something to skip.
	identityRef = "refs/workbook/project"

	// remoteIdentityRef mirrors origin's identity ref. It parallels the task
	// tracking namespace so one fetch can populate both.
	remoteIdentityRef = "refs/workbook/remotes/origin/project"

	// identityFetchRefspec brings origin's identity ref into the tracking name
	// above.
	//
	// The trailing glob is load-bearing. Git fails a whole fetch when an
	// explicitly named source ref is missing, so a plain
	// `+refs/workbook/project:...` refspec would break every fetch against an
	// origin that has not published the ref yet — which is every project until
	// the first v0.5.0 clone publishes one. A pattern refspec matches nothing
	// silently instead, so one fetch serves both worlds.
	identityFetchRefspec = "+" + identityRef + "*:" + remoteIdentityRef + "*"

	// identityDocumentPath is the only entry the identity tree may contain.
	identityDocumentPath = "project.json"

	// The identity commit is built with a fixed author, committer, date and
	// message so that two clones adopting the same identity independently
	// produce the same commit object. Convergence is what makes publication
	// safe: the second publisher is "up to date" rather than rejected, and no
	// project ends up with two competing identity histories.
	identityCommitMessage = "workbook: project identity"
	identityCommitName    = "Workbook"
	identityCommitEmail   = "workbook@invalid"
	identityCommitDate    = "@0 +0000"

	identityRefLogReason = "workbook: publish project identity"
)

// identitySource names where a resolved identity came from. It is reported so a
// bootstrap can say which path ran rather than leaving a user to guess whether
// their project was joined or invented.
type identitySource string

const (
	identitySourceRef   identitySource = "ref"
	identitySourceFile  identitySource = "file"
	identitySourceGuard identitySource = "guard"
	identitySourceMint  identitySource = "mint"
)

// IdentityDrift reports a disagreement observed while resolving a project's
// identity that is not worth refusing to run over.
//
// Identity itself is never drift: a ref and a tracked file naming different
// projects is fatal, because continuing would write one project's operations
// into another's history. What lands here is everything around the identity —
// an advisory copy that is missing, a private guard that had to be repaired —
// where the canonical answer is known and the command can proceed.
type IdentityDrift struct {
	Detail string
}

// identityRecord is one validated identity ref tip: the commit it names, the
// document that commit carries, and that document's exact bytes.
type identityRecord struct {
	Head     string
	Identity core.ProjectIdentity
	Document []byte
}

// identityResolution is the outcome of walking the precedence chain once.
type identityResolution struct {
	Identity core.ProjectIdentity
	Head     string
	Source   identitySource
	// Minted reports that this call invented the identity and won the race to
	// publish it. Adoption of an identity that already existed anywhere — the
	// ref, the tracked file, the guard, or a concurrent minter — is not a mint,
	// and bootstrap reports it as joining rather than creating a project.
	Minted bool
	// Published reports that this call created the local identity ref, whether
	// by minting or by migrating an older record into it.
	Published bool
	Drift     string
}

// IdentityOrigin reports how an opened repository came by its identity.
type IdentityOrigin struct {
	// Source is one of "ref", "file", "guard" or "mint".
	Source    string
	Minted    bool
	Published bool
}

// LoadIdentity returns the repository's canonical project identity, resolving
// it once per opened repository.
//
// The precedence chain is deliberate:
//
//  1. the local identity ref, which is canonical;
//  2. the tracked .workbook/config.json, whose identity fields are adopted and
//     published as the ref (the self-healing migration for every project that
//     predates v0.5.0);
//  3. the private guard, which is the last local record of a project this
//     clone already belonged to;
//  4. nothing, which is an uninitialized repository.
//
// Minting a new identity is not in this chain. It belongs to Init alone, so no
// ordinary command can invent a project by being run in the wrong directory.
func (r *Repository) LoadIdentity(ctx context.Context) (core.ProjectIdentity, error) {
	r.metadataMu.RLock()
	loaded, identity := r.identityLoaded, r.identity
	r.metadataMu.RUnlock()
	if loaded {
		return identity, nil
	}

	resolution, err := r.resolveIdentity(ctx, nil)
	if err != nil {
		return core.ProjectIdentity{}, err
	}
	return r.rememberIdentity(resolution), nil
}

// IdentityDrift reports what resolving this repository's identity had to work
// around, if anything. It is read once per opened repository so a command emits
// at most one line about it.
func (r *Repository) IdentityDrift() (IdentityDrift, bool) {
	r.metadataMu.RLock()
	defer r.metadataMu.RUnlock()
	if r.identityDrift == "" {
		return IdentityDrift{}, false
	}
	return IdentityDrift{Detail: r.identityDrift}, true
}

// IdentityOrigin reports which link of the precedence chain answered and what
// this process had to write. Bootstrap surfaces it so a user can see whether
// their project was joined or minted; its second result is false until an
// identity has been resolved.
func (r *Repository) IdentityOrigin() (IdentityOrigin, bool) {
	r.metadataMu.RLock()
	defer r.metadataMu.RUnlock()
	if !r.identityLoaded {
		return IdentityOrigin{}, false
	}
	return IdentityOrigin{
		Source:    string(r.identitySource),
		Minted:    r.identityMinted,
		Published: r.identityRefCreated,
	}, true
}

// consumeIdentityPublication reports whether this opened repository created the
// local identity ref and has not yet said so.
//
// It is one-shot because the migration it announces is one event, not a
// property of the repository: a command that synchronizes twice would otherwise
// report the same publication in both runs.
func (r *Repository) consumeIdentityPublication() (identitySource, bool) {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	if !r.identityRefCreated || r.identityPublicationReported {
		return "", false
	}
	r.identityPublicationReported = true
	return r.identitySource, true
}

func (r *Repository) rememberIdentity(resolution identityResolution) core.ProjectIdentity {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	if !r.identityLoaded {
		r.identity = resolution.Identity
		r.identityHead = resolution.Head
		r.identitySource = resolution.Source
		r.identityMinted = resolution.Minted
		r.identityRefCreated = resolution.Published
		r.identityDrift = resolution.Drift
		r.identityLoaded = true
	}
	return r.identity
}

// resolveIdentity walks the precedence chain once, publishing the ref when an
// older record has to be adopted into it.
//
// mint is supplied only by Init. A nil mint makes the empty repository report
// that Workbook is not initialized instead of quietly inventing a project.
//
// It deliberately holds no lock: every step runs Git, and the metadata mutex
// guards fields that Git plumbing helpers take themselves.
func (r *Repository) resolveIdentity(
	ctx context.Context,
	mint func() (core.ProjectIdentity, error),
) (identityResolution, error) {
	record, found, err := r.readIdentityRef(ctx, identityRef)
	if err != nil {
		return identityResolution{}, err
	}
	tracked, trackedExists, err := r.readConfig()
	if err != nil {
		return identityResolution{}, err
	}
	guard, guardExists, err := r.readProjectGuard()
	if err != nil {
		return identityResolution{}, err
	}

	if found {
		return r.settleIdentity(record, identitySourceRef, false, false, trackedExists, tracked, guardExists, guard)
	}

	var candidate core.ProjectIdentity
	var source identitySource
	switch {
	case trackedExists:
		// No ref exists to arbitrate, so the pre-v0.5.0 rule still governs:
		// a guard naming another project is the only evidence available that
		// this working tree was swapped under a repository, and it stays
		// fatal. Once a ref exists, the same disagreement is repairable.
		if guardExists && !tracked.SameIdentity(guard) {
			return identityResolution{}, r.guardMismatch(tracked, guard)
		}
		candidate, source = identityFromConfig(tracked), identitySourceFile
	case guardExists:
		candidate, source = identityFromConfig(guard), identitySourceGuard
	case mint != nil:
		candidate, err = mint()
		if err != nil {
			return identityResolution{}, err
		}
		source = identitySourceMint
	default:
		return identityResolution{}, core.Errorf(core.CategoryNotInitialized, "Workbook is not initialized")
	}

	// A minted identity that loses the race was never a project: nothing refers
	// to it, so the winner is simply adopted. An identity read from an existing
	// record is different — losing to a ref naming another project means two
	// records disagree about which project this repository is, and that is not
	// something to resolve by picking one.
	published, created, err := r.publishIdentity(ctx, candidate, source == identitySourceMint)
	if err != nil {
		return identityResolution{}, err
	}
	// Only a mint that won created a project. Publishing an identity that a
	// tracked file or a guard already held is the migration doing its work, and
	// a caller that treated it as creation would tell a user their existing
	// project had just been made.
	minted := created && source == identitySourceMint
	if !published.Identity.SameIdentity(candidate) {
		source = identitySourceRef
	}
	return r.settleIdentity(published, source, minted, created, trackedExists, tracked, guardExists, guard)
}

// settleIdentity validates a resolved identity against the advisory records
// beside it and brings the private guard into line.
func (r *Repository) settleIdentity(
	record identityRecord,
	source identitySource,
	minted bool,
	published bool,
	trackedExists bool,
	tracked core.ProjectConfig,
	guardExists bool,
	guard core.ProjectConfig,
) (identityResolution, error) {
	if trackedExists && !tracked.SameIdentity(configFromIdentity(record.Identity)) {
		return identityResolution{}, r.identityMismatch(record.Identity, tracked, guardExists, guard)
	}
	repaired, err := r.alignProjectGuard(record.Identity, guardExists, guard)
	if err != nil {
		return identityResolution{}, err
	}

	resolution := identityResolution{
		Identity:  record.Identity,
		Head:      record.Head,
		Source:    source,
		Minted:    minted,
		Published: published,
	}
	switch {
	case repaired:
		resolution.Drift = fmt.Sprintf(
			"repaired the private project guard %s from %s, which is canonical",
			filepath.Join(r.CommonGitDir, "workbook", projectGuard), identityRef,
		)
	case !trackedExists:
		resolution.Drift = fmt.Sprintf(
			"this checkout has no %s; the project identity comes from %s",
			configPath, identityRef,
		)
	}
	return resolution, nil
}

// alignProjectGuard keeps the private guard usable by a v0.4.x binary sharing
// this repository, and repairs it when it disagrees with the canonical ref.
//
// v0.4.x reads the guard and refuses to run when it names another project, so
// v0.5.0 keeps writing it. What changes is the verdict on disagreement: the ref
// now says which project this is, so a stale guard is repaired instead of
// wedging every command. Deleting the guard outright waits until no supported
// version reads it.
func (r *Repository) alignProjectGuard(
	identity core.ProjectIdentity,
	guardExists bool,
	guard core.ProjectConfig,
) (bool, error) {
	want := configFromIdentity(identity)
	if guardExists && guard.SameIdentity(want) {
		return false, nil
	}
	if err := r.ensurePrivateCache(); err != nil {
		return false, err
	}
	if !guardExists {
		persisted, _, err := r.publishProjectGuard(want)
		if err != nil {
			return false, err
		}
		if persisted.SameIdentity(want) {
			return false, nil
		}
		// Another process published a guard naming a different project while
		// this one was resolving. The ref is canonical, so the guard is what
		// gives way.
	}
	if err := r.repairProjectGuard(want); err != nil {
		return false, err
	}
	return true, nil
}

// identityMismatch reports a canonical identity and a tracked configuration
// that name different projects.
//
// This is the one identity disagreement Workbook refuses to work around, so the
// message has to carry the whole picture: both identities, every file and ref
// that states one, and the way out.
func (r *Repository) identityMismatch(
	identity core.ProjectIdentity,
	tracked core.ProjectConfig,
	guardExists bool,
	guard core.ProjectConfig,
) error {
	guardPath := filepath.Join(r.CommonGitDir, "workbook", projectGuard)
	guardDetail := fmt.Sprintf("the common project guard %s is absent", guardPath)
	if guardExists {
		guardDetail = fmt.Sprintf("the common project guard %s names project %s (key %s)",
			guardPath, guard.ProjectID, guard.Key)
	}
	return core.Errorf(core.CategoryCorruptData,
		"tracked Workbook configuration does not match this repository's canonical project identity: %s names project %s (key %s) but %s names project %s (key %s); %s; "+
			"the ref is canonical, so check out the branch whose %s belongs to project %s, or copy that identity into the tracked file — never edit the ref to match the file",
		identityRef, identity.ProjectID, identity.Key,
		filepath.Join(r.Root, configPath), tracked.ProjectID, tracked.Key,
		guardDetail,
		configPath, identity.ProjectID)
}

func identityFromConfig(config core.ProjectConfig) core.ProjectIdentity {
	return core.ProjectIdentity{
		Format:    core.ProjectIdentityFormat,
		Version:   core.ProjectIdentityVersion,
		ProjectID: config.ProjectID,
		Key:       config.Key,
	}
}

func configFromIdentity(identity core.ProjectIdentity) core.ProjectConfig {
	return core.ProjectConfig{
		Format:    projectFormat,
		Version:   projectVersion,
		ProjectID: identity.ProjectID,
		Key:       identity.Key,
	}
}

// publishIdentity records one identity as the repository's canonical ref.
//
// Creation is exactly-once: the update-ref transaction's create verb fails if
// the ref exists at all, so a lost race is discovered rather than overwritten.
// The loser re-reads and adopts whatever won, which for two clones adopting the
// same identity is byte-identical to what it tried to write.
//
// adoptWinner decides what a race with a different project means. It is set
// only for a freshly minted identity, where nothing yet refers to the loser's
// ID and the winner is the project.
func (r *Repository) publishIdentity(
	ctx context.Context,
	identity core.ProjectIdentity,
	adoptWinner bool,
) (identityRecord, bool, error) {
	head, err := r.writeIdentityObjects(ctx, identity)
	if err != nil {
		return identityRecord{}, false, err
	}
	document, err := core.EncodeDocument(identity)
	if err != nil {
		return identityRecord{}, false, err
	}
	createErr := r.createRef(ctx, identityRef, head)
	if createErr == nil {
		return identityRecord{Head: head, Identity: identity, Document: document}, true, nil
	}

	record, found, err := r.readIdentityRef(ctx, identityRef)
	if err != nil {
		return identityRecord{}, false, err
	}
	if !found {
		return identityRecord{}, false, core.Wrap(core.CategoryOperational,
			"cannot publish the Workbook project identity ref", createErr)
	}
	if !record.Identity.SameIdentity(identity) && !adoptWinner {
		return identityRecord{}, false, core.Errorf(core.CategoryCorruptData,
			"%s already names project %s (key %s), but this command tried to publish project %s (key %s); "+
				"a project identity is immutable, so the published one wins",
			identityRef, record.Identity.ProjectID, record.Identity.Key, identity.ProjectID, identity.Key)
	}
	return record, false, nil
}

// writeIdentityObjects durably records one identity's object graph without
// touching any ref. Unreferenced objects are harmless, so an interrupted
// publication leaves nothing to clean up and the retry writes the same objects
// again.
func (r *Repository) writeIdentityObjects(ctx context.Context, identity core.ProjectIdentity) (string, error) {
	document, err := core.EncodeDocument(identity)
	if err != nil {
		return "", err
	}
	blob, err := r.writeBlob(ctx, document)
	if err != nil {
		return "", err
	}
	var entries bytes.Buffer
	fmt.Fprintf(&entries, "100644 blob %s\t%s\n", blob, identityDocumentPath)
	output, err := r.Git(ctx, entries.Bytes(), "mktree")
	if err != nil {
		return "", err
	}
	tree, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid tree object ID", err)
	}
	return r.writeIdentityCommit(ctx, tree)
}

// writeIdentityCommit builds the root commit that carries an identity tree.
//
// Every input is fixed — no parent, a constant author, committer, date and
// message, and signing explicitly disabled — so the commit object is a pure
// function of the document. Two clones that adopt the same identity therefore
// publish the same object ID, and the second publication is a no-op instead of
// a conflict.
func (r *Repository) writeIdentityCommit(ctx context.Context, tree string) (string, error) {
	environment := []string{
		"GIT_AUTHOR_NAME=" + identityCommitName,
		"GIT_AUTHOR_EMAIL=" + identityCommitEmail,
		"GIT_AUTHOR_DATE=" + identityCommitDate,
		"GIT_COMMITTER_NAME=" + identityCommitName,
		"GIT_COMMITTER_EMAIL=" + identityCommitEmail,
		"GIT_COMMITTER_DATE=" + identityCommitDate,
	}
	output, err := r.gitWithEnv(ctx, environment, nil,
		"-c", "commit.gpgsign=false", "commit-tree", tree, "-m", identityCommitMessage)
	if err != nil {
		return "", err
	}
	commit, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid commit object ID", err)
	}
	return commit, nil
}

// createRef creates one ref, failing when it already exists.
func (r *Repository) createRef(ctx context.Context, ref, head string) error {
	return r.createRefWithReason(ctx, ref, head, identityRefLogReason)
}

// createRefWithReason creates one ref under a caller-chosen reflog reason. The
// singleton refs share this transaction shape — creation is the compare-and-swap
// when the ref does not exist yet — and differ only in what the reflog should
// say a person was doing.
func (r *Repository) createRefWithReason(ctx context.Context, ref, head, reason string) error {
	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	fmt.Fprintf(&input, "create %s %s\n", ref, head)
	input.WriteString("prepare\ncommit\n")
	_, err := r.Git(ctx, input.Bytes(),
		"update-ref", "--no-deref", "--create-reflog", "-m", reason, "--stdin")
	return err
}

// replaceRef compare-and-swaps one ref from an expected value.
func (r *Repository) replaceRef(ctx context.Context, ref, head, expected string) error {
	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	fmt.Fprintf(&input, "update %s %s %s\n", ref, head, expected)
	input.WriteString("prepare\ncommit\n")
	_, err := r.Git(ctx, input.Bytes(),
		"update-ref", "--no-deref", "--create-reflog", "-m", identityRefLogReason, "--stdin")
	return err
}

// readIdentityRef reads and structurally validates one identity ref.
//
// Reads go through the same batch reader every other object read uses, so
// MaxObjectBytes bounds them too: a hand-built commit pushed by a collaborator
// cannot make a clone that fetches it allocate without limit.
func (r *Repository) readIdentityRef(ctx context.Context, ref string) (identityRecord, bool, error) {
	listing, err := r.listIdentityRefs(ctx, ref)
	if err != nil {
		return identityRecord{}, false, err
	}
	head, found := listing.Heads[ref]
	if !found {
		return identityRecord{}, false, nil
	}
	if err := r.rememberGitObjectID(head); err != nil {
		return identityRecord{}, false, core.Wrap(core.CategoryCorruptData, "Git returned an invalid project identity ref object ID", err)
	}
	decoded, err := decodeObjectID(head)
	if err != nil {
		return identityRecord{}, false, core.Wrap(core.CategoryCorruptData, "Git returned an invalid project identity ref object ID", err)
	}
	record, err := r.readIdentityObjects(ctx, ref, head, len(decoded))
	if err != nil {
		return identityRecord{}, false, err
	}
	return record, true, nil
}

// identityRefListing is one enumeration of the identity refs: the tips that
// exist, and the names under origin's mirror that this version does not read.
type identityRefListing struct {
	Heads map[string]string
	// Ignored names refs the fetch mirrored from origin's identity namespace
	// that are not the identity ref. They are stated under the name origin
	// holds them at, because that is the name a person would have to act on,
	// and reporting one is never an instruction to delete it.
	Ignored []string
}

// listIdentityRefs enumerates the named identity refs in one Git process.
//
// Each name is also a prefix pattern, so the listing reports any ref created
// under one of them, and the two namespaces earn different verdicts.
//
// The canonical ref is under this tool's exclusive control: a name beneath it
// means the local namespace was rearranged, and that is corruption. Origin's
// mirror is not. Anyone with push access can create refs/workbook/project/x on
// a remote that has no identity ref yet — Git's directory/file rule only
// forbids the two coexisting — and the fetch refspec mirrors it faithfully.
// Refusing to read past that would let one stray ref deny bootstrap and
// synchronization to every clone, permanently, with no way for the affected
// user to clear it. Such a name is skipped and reported instead.
func (r *Repository) listIdentityRefs(ctx context.Context, refs ...string) (identityRefListing, error) {
	contents, err := r.Git(ctx, nil, append([]string{"for-each-ref", "--format=" + taskRefFormat}, refs...)...)
	if err != nil {
		return identityRefListing{}, err
	}
	return parseIdentityRefRecords(refs, contents)
}

func parseIdentityRefRecords(refs []string, contents []byte) (identityRefListing, error) {
	listing := identityRefListing{Heads: make(map[string]string, len(refs))}
	if len(contents) == 0 {
		return listing, nil
	}
	if contents[len(contents)-1] != '\n' {
		return identityRefListing{}, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated project identity ref record")
	}
	for _, line := range bytes.Split(contents[:len(contents)-1], []byte{'\n'}) {
		parts := bytes.Split(line, []byte{0})
		if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) == 0 {
			return identityRefListing{}, core.Errorf(core.CategoryCorruptData, "Git returned an invalid project identity ref record")
		}
		refName, objectID, symbolicTarget := string(parts[0]), string(parts[1]), string(parts[2])
		requested := ""
		for _, ref := range refs {
			if refName == ref {
				requested = ref
				break
			}
		}
		if requested == "" {
			if strings.HasPrefix(refName, remoteIdentityRef+"/") {
				listing.Ignored = append(listing.Ignored, originIdentityRefName(refName))
				continue
			}
			return identityRefListing{}, identityRefChildError(identityRefAncestorOf(refs, refName))
		}
		if symbolicTarget != "" {
			return identityRefListing{}, core.Errorf(core.CategoryCorruptData, "project identity ref %q must not be symbolic", refName)
		}
		if _, duplicate := listing.Heads[requested]; duplicate {
			return identityRefListing{}, core.Errorf(core.CategoryCorruptData, "project identity ref %q was returned more than once", refName)
		}
		listing.Heads[requested] = objectID
	}
	return listing, nil
}

// originIdentityRefName restates a mirrored name under the ref origin holds it
// at. The local mirror is rebuilt by every fetch, so naming it would name a ref
// the user cannot usefully remove.
func originIdentityRefName(refName string) string {
	return identityRef + strings.TrimPrefix(refName, remoteIdentityRef)
}

// identityRefAncestorOf names the requested ref a listed child hangs from, so
// the failure blames the name a user would have to act on.
func identityRefAncestorOf(refs []string, refName string) string {
	for _, ref := range refs {
		if strings.HasPrefix(refName, ref+"/") {
			return ref
		}
	}
	return refName
}

func identityRefChildError(ref string) error {
	return core.Errorf(core.CategoryCorruptData,
		"%s must be one ref holding one document; Git's directory/file rule means nothing may be created under that name", ref)
}

func (r *Repository) readIdentityObjects(ctx context.Context, ref, head string, objectIDBytes int) (identityRecord, error) {
	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		_, err := fmt.Fprintf(writer, "%s\n%s^{tree}\n%s:%s\n", head, head, head, identityDocumentPath)
		return err
	})
	if err != nil {
		return identityRecord{}, err
	}
	defer batch.Close()

	var objects [3]batchObject
	for i := range objects {
		object, err := readBatchObject(batch.Reader())
		if err != nil {
			return identityRecord{}, batch.ReadFailure("cannot read Workbook project identity objects from Git batch", err)
		}
		objects[i] = object
	}
	record, err := validateIdentityObjects(objects, ref, head, objectIDBytes)
	if err != nil {
		return identityRecord{}, err
	}
	if err := batch.Finish(); err != nil {
		return identityRecord{}, err
	}
	return record, nil
}

// validateIdentityObjects enforces the shape of the identity ref: a root commit
// with no parents whose tree holds exactly one document.
//
// The structure is the invariant, not a convention. A tip with parents would
// mean the identity had a history to disagree about, and a second tree entry
// would mean the ref had grown a second authority.
func validateIdentityObjects(objects [3]batchObject, ref, head string, objectIDBytes int) (identityRecord, error) {
	commit, tree, document := objects[0], objects[1], objects[2]
	for _, object := range objects {
		if object.refused != nil {
			return identityRecord{}, object.refused
		}
		if object.missing {
			return identityRecord{}, core.Errorf(core.CategoryCorruptData,
				"requested Workbook project identity object %q is missing", object.objectID)
		}
	}
	if commit.objectID != head || commit.kind != "commit" {
		return identityRecord{}, core.Errorf(core.CategoryCorruptData, "%s does not point directly to a commit", ref)
	}
	if tree.kind != "tree" {
		return identityRecord{}, core.Errorf(core.CategoryCorruptData, "the Workbook project identity commit does not point to a tree")
	}
	if document.kind != "blob" {
		return identityRecord{}, core.Errorf(core.CategoryCorruptData, "the Workbook project identity document is not a blob")
	}

	commitTree, err := commitTreeObjectID(commit.contents, "the Workbook project identity commit")
	if err != nil {
		return identityRecord{}, err
	}
	if tree.objectID != commitTree {
		return identityRecord{}, core.Errorf(core.CategoryCorruptData,
			"the Workbook project identity commit tree does not match its batch object")
	}
	parents, err := commitParentCount(commit.contents, "the Workbook project identity commit")
	if err != nil {
		return identityRecord{}, err
	}
	if parents != 0 {
		return identityRecord{}, core.Errorf(core.CategoryCorruptData,
			"the Workbook project identity commit must have no parents; identity is immutable, so the ref carries one root commit")
	}
	entries, err := parseRawIdentityTree(tree.contents, objectIDBytes)
	if err != nil {
		return identityRecord{}, err
	}
	if entries[identityDocumentPath] != document.objectID {
		return identityRecord{}, core.Errorf(core.CategoryCorruptData,
			"the Workbook project identity tree entry does not match its batch object")
	}
	identity, err := decodeCanonicalIdentity(document.contents)
	if err != nil {
		return identityRecord{}, err
	}
	return identityRecord{Head: head, Identity: identity, Document: document.contents}, nil
}

func parseRawIdentityTree(contents []byte, objectIDBytes int) (map[string]string, error) {
	entries := make(map[string]string, 1)
	err := scanRawTreeEntries(contents, objectIDBytes, "project identity tree",
		func(mode, name, objectID string) error {
			if mode != "100644" {
				return core.Errorf(core.CategoryCorruptData, "project identity tree entry %q is not a regular blob", name)
			}
			if name != identityDocumentPath {
				return core.Errorf(core.CategoryCorruptData, "project identity tree has an unexpected entry %q", name)
			}
			if _, duplicate := entries[name]; duplicate {
				return core.Errorf(core.CategoryCorruptData, "project identity tree contains duplicate entry %q", name)
			}
			entries[name] = objectID
			return nil
		})
	if err != nil {
		return nil, err
	}
	if len(entries) != 1 {
		return nil, core.Errorf(core.CategoryCorruptData, "project identity tree must contain exactly %s", identityDocumentPath)
	}
	return entries, nil
}

// decodeCanonicalIdentity decodes a stored identity document and insists it is
// byte-for-byte what this version would write. Determinism is only worth
// anything if the bytes are pinned: a document that decodes the same but
// encodes differently would hash to a different commit in the next clone.
func decodeCanonicalIdentity(contents []byte) (core.ProjectIdentity, error) {
	identity, err := core.DecodeProjectIdentity(contents)
	if err != nil {
		return core.ProjectIdentity{}, err
	}
	canonical, err := core.EncodeDocument(identity)
	if err != nil {
		return core.ProjectIdentity{}, core.Wrap(core.CategoryCorruptData, "cannot canonicalize the Workbook project identity", err)
	}
	if !bytes.Equal(contents, canonical) {
		return core.ProjectIdentity{}, core.Errorf(core.CategoryCorruptData, "the Workbook project identity document is not canonical")
	}
	return identity, nil
}
