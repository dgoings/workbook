package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

const (
	// configRef holds a project's canonical configuration ledger: the status
	// vocabulary, and whatever later sections the ledger grows.
	//
	// Like the identity ref it sits outside refs/workbook/tasks/*, so a v0.4.x
	// client is blind to it — that version's fetch refspec and ls-remote
	// patterns name only the task namespace, and a project that starts
	// configuring statuses stays readable by a teammate who has not upgraded.
	//
	// The name is a leaf and must stay one. Git's directory/file rule means no
	// ref may ever be created under refs/workbook/config/*, and this package
	// enforces it on the local side: a listing that returns anything but this
	// exact name is corruption rather than something to skip. A future family
	// of configuration documents needs a sibling namespace, not children of
	// this ref.
	configRef = "refs/workbook/config"

	// remoteConfigRef mirrors origin's configuration ledger. It parallels the
	// task and identity tracking namespaces so one fetch populates all three.
	//
	// It is permanently childless for the same directory/file reason, but the
	// verdict on a name found beneath it differs: origin's namespace is not
	// under this tool's control, and a stray ref there is skipped and reported
	// rather than treated as corruption. See listConfigRefs.
	remoteConfigRef = "refs/workbook/remotes/origin/config"

	// configFetchRefspec brings origin's configuration ref into the tracking
	// name above.
	//
	// The trailing glob is load-bearing, and is the lesson the identity ref
	// already paid for: Git fails a whole fetch when an explicitly named source
	// ref is missing, so a plain `+refs/workbook/config:...` refspec would
	// break every fetch against an origin that has no ledger — which is every
	// project until somebody changes a status. A pattern refspec matches
	// nothing silently instead.
	configFetchRefspec = "+" + configRef + "*:" + remoteConfigRef + "*"

	// parkedConfigRefPrefix retains the orphaned configuration tips that
	// reconciliation replaced.
	//
	// It is deliberately not under refs/workbook/reconciled/. That namespace's
	// lister splits every name into a task ID and an index and skips whatever
	// does not parse as one, so a configuration entry filed there would be
	// silently invisible to its own sweep and would accumulate forever. A
	// separate prefix with its own lister and its own retention bound is the
	// only shape in which the parked tips can be found again.
	parkedConfigRefPrefix = "refs/workbook/parked/config/"

	// maxParkedConfigRefs bounds retained orphaned configuration tips. It
	// mirrors maxParkedRefsPerTask, and for the same reason: a parked tip
	// exists so a person can recover an intent a conflict dropped, which is a
	// short-lived need, while the canonical ledger is the durable record.
	maxParkedConfigRefs = 3

	// The tree a configuration commit carries is the task tree's shape, an
	// operation beside the checkpoint it produced, so it is read by the same
	// parser and bounded by the same per-object ceiling.
	configOperationPath = "operation.json"
	configStatePath     = "state.json"

	configRefLogReason   = "workbook: project configuration"
	configGenesisSubject = "workbook: seed project configuration"
	configUpdateSubject  = "workbook: update project configuration"
	configReplaySubject  = "workbook: replay configuration operation"
	configPruneReason    = "workbook: prune parked configuration refs"
)

// configRecord is one validated configuration ledger tip: the commit it names,
// and the two documents that commit carries.
type configRecord struct {
	Head      string
	Operation core.ConfigOperationPack
	State     core.ConfigStateDocument
}

// ConfigWriteResult reports one authored write to the configuration ledger.
type ConfigWriteResult struct {
	// Head is the ledger's new tip.
	Head string
	// Seeded reports that this write also created the ledger's genesis root,
	// which happens once in a project's life and only for a project that
	// predates the ledger.
	Seeded bool
	// State is the checkpoint the write recorded.
	State core.ConfigStateDocument
}

// Vocabulary reads the written checkpoint's status vocabulary.
func (result ConfigWriteResult) Vocabulary() core.Vocabulary {
	return result.State.Vocabulary()
}

// LoadVocabulary returns the project's configured status vocabulary, resolving
// it once per opened repository exactly as LoadConfig and LoadIdentity resolve
// theirs.
//
// A project with no ledger is not an error and never will be. Most projects
// have none — the ledger is seeded lazily, by the first status change anybody
// makes — and a command that failed without one would fail for every user who
// never customized anything. Absence reads as core.LegacyVocabulary: what a
// project that predates the ledger was already using. That is a different
// accessor from DefaultVocabulary on purpose; see its comment.
//
// It reads no network and tolerates having none. The canonical ref is local,
// and whether it is current is synchronization's business, not this read's.
func (r *Repository) LoadVocabulary(ctx context.Context) (core.Vocabulary, error) {
	r.metadataMu.RLock()
	loaded, vocabulary := r.vocabularyLoaded, r.vocabulary
	r.metadataMu.RUnlock()
	if loaded {
		return vocabulary, nil
	}

	config, err := r.LoadConfig()
	if err != nil {
		return core.Vocabulary{}, err
	}
	record, found, err := r.readConfigRef(ctx, config, configRef)
	if err != nil {
		return core.Vocabulary{}, unreadableConfigLedger(err)
	}
	if !found {
		// A project with no ledger costs one ref enumeration and no object
		// read at all, which is the state every project is in until somebody
		// runs a status command.
		return r.rememberVocabulary(core.LegacyVocabulary()), nil
	}
	return r.rememberVocabulary(record.State.Vocabulary()), nil
}

// unreadableConfigLedger names the ref and the command that diagnoses it.
//
// Failing here rather than degrading to the legacy vocabulary is deliberate: a
// clone that silently fell back would draw every board in the wrong columns and
// accept status values the project does not have, which is worse than not
// running. But this failure reaches a person through `workbook list`, `show`,
// `next` and `create` alike, and what those used to say was whatever the
// decoder said — "cannot decode document", naming nothing. Every command that
// stops has to say which ref stopped it and what reads it in detail.
func unreadableConfigLedger(err error) error {
	category := core.CategoryOf(err)
	if category == "" {
		category = core.CategoryCorruptData
	}
	return core.Wrap(category,
		"cannot read this project's status configuration from "+configRef+
			"; run `workbook validate` to see which configuration commit is at fault",
		err)
}

func (r *Repository) rememberVocabulary(vocabulary core.Vocabulary) core.Vocabulary {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	if !r.vocabularyLoaded {
		r.vocabulary = vocabulary
		r.vocabularyLoaded = true
	}
	return r.vocabulary
}

// replaceVocabulary updates the memoized vocabulary after this process moved
// the ledger, so later work in the same command does not read the superseded
// configuration. It mirrors replaceConfig, and exists for the same reason: a
// command that changes a status and then projects a task must project it
// against what it just wrote.
func (r *Repository) replaceVocabulary(vocabulary core.Vocabulary, head string) {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	r.vocabulary = vocabulary
	r.vocabularyLoaded = true
	r.configLocalHead = head
}

// WriteConfigOperation records one batch of configuration changes as the
// ledger's next commit.
//
// The authoring gate runs first and against the current tip: ValidateConfigAuthoring
// is where arity and the size ceilings are refused, because this is the moment
// a person can still choose differently. The same operations arriving from a
// peer fold without that gate, which is the whole asymmetry the configuration
// fold is built around.
//
// A project with no ledger grows one here, lazily. The first write puts down a
// root commit whose pack is a config.genesis carrying core.LegacyVocabulary as
// data, and the author's operations as the commit after it. Writing the whole
// vocabulary into the root rather than saying "start from the defaults" is what
// makes the ledger version independent: the built-in defaults change between
// releases, and a v0.9 clone folding a v0.5 ledger has to reproduce the v0.5
// project.
//
// Two clones can seed concurrently. Fetching before mutating settles the common
// case, because the second clone sees the first's root and appends to it, and
// this function settles the local race by creating the ref rather than updating
// it: the loser re-reads and replays onto the winner. What neither settles is
// two clones that seeded while offline, which produces two unrelated histories
// rather than a conflict inside one; reconciliation resolves that by adopting
// origin's root.
func (r *Repository) WriteConfigOperation(
	ctx context.Context,
	config core.ProjectConfig,
	ids core.IDSource,
	operations []core.ConfigOperation,
	reason string,
) (ConfigWriteResult, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return ConfigWriteResult{}, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return ConfigWriteResult{}, err
	}
	if ids == nil {
		return ConfigWriteResult{}, core.Errorf(core.CategoryOperational, "configuration ID source is required")
	}
	if len(operations) == 0 {
		return ConfigWriteResult{}, core.Errorf(core.CategoryValidation,
			"a configuration write must carry at least one operation")
	}
	if len(operations) > core.MaxConfigOperationsPerPack {
		return ConfigWriteResult{}, core.Errorf(core.CategoryValidation,
			"a configuration write carries %d operations and must not exceed %d; split it into several commands",
			len(operations), core.MaxConfigOperationsPerPack)
	}
	authored, err := authoredConfigOperations(ids, operations)
	if err != nil {
		return ConfigWriteResult{}, err
	}
	if strings.TrimSpace(reason) == "" {
		reason = configUpdateSubject
	}
	actor, err := r.Actor(ctx)
	if err != nil {
		return ConfigWriteResult{}, err
	}

	tip, found, err := r.readConfigRef(ctx, config, configRef)
	if err != nil {
		return ConfigWriteResult{}, err
	}
	if !found {
		result, seeded, err := r.seedConfigLedger(ctx, config, ids, authored, actor, reason)
		if err != nil {
			return ConfigWriteResult{}, err
		}
		if seeded {
			return result, nil
		}
		// Another process in this repository seeded the ledger between the read
		// and the create. Its root is the project's root now, so this write
		// becomes an ordinary append onto it and the operations keep the IDs
		// they were already given.
		tip, found, err = r.readConfigRef(ctx, config, configRef)
		if err != nil {
			return ConfigWriteResult{}, err
		}
		if !found {
			return ConfigWriteResult{}, core.Errorf(core.CategoryStaleWrite,
				"%s was created and removed during this write; rerun the command", configRef)
		}
	}
	return r.appendConfigOperation(ctx, tip, authored, actor, reason)
}

// authoredConfigOperations copies the caller's operations and mints an ID for
// every one that has none.
//
// Supplied IDs are kept rather than replaced, because an operation ID is the
// identity of one recorded intent: a caller retrying the same authored change
// after a lost race must produce the same IDs, so the retry is one operation
// delivered twice rather than two operations.
//
// A config.genesis is refused here. The root is bookkeeping the ledger writes
// for itself, and a caller that could author one could give a project a second
// root.
func authoredConfigOperations(ids core.IDSource, operations []core.ConfigOperation) ([]core.ConfigOperation, error) {
	authored := make([]core.ConfigOperation, len(operations))
	for index, operation := range operations {
		if operation.Type == core.ConfigGenesis {
			return nil, core.Errorf(core.CategoryValidation,
				"config.genesis is written by the configuration ledger itself and cannot be authored")
		}
		if operation.ID == "" {
			id, err := ids.New()
			if err != nil {
				return nil, core.Wrap(core.CategoryOperational, "cannot generate configuration operation ID", err)
			}
			operation.ID = id
		}
		authored[index] = operation
	}
	return authored, nil
}

// seedConfigLedger writes the genesis root and the author's first pack as two
// commits, then claims the ref with one creation.
//
// Both commits are written before any ref moves, so an interrupted seed leaves
// unreferenced objects rather than a ledger holding a root nobody asked for.
// The creation is the compare-and-swap: Git's create verb fails if the ref
// exists at all, so a lost race is discovered rather than overwritten, and the
// second result reports it so the caller can replay onto the winner.
func (r *Repository) seedConfigLedger(
	ctx context.Context,
	config core.ProjectConfig,
	ids core.IDSource,
	operations []core.ConfigOperation,
	actor string,
	reason string,
) (ConfigWriteResult, bool, error) {
	generation, err := ids.New()
	if err != nil {
		return ConfigWriteResult{}, false, core.Wrap(core.CategoryOperational,
			"cannot generate configuration history generation", err)
	}
	genesisID, err := ids.New()
	if err != nil {
		return ConfigWriteResult{}, false, core.Wrap(core.CategoryOperational,
			"cannot generate configuration operation ID", err)
	}
	vocabulary := core.LegacyVocabulary().Document()
	genesisPack, err := core.NewConfigOperationPack(config.ProjectID, generation, actor, 1, configWallTime(),
		[]core.ConfigOperation{{
			ID:     genesisID,
			Type:   core.ConfigGenesis,
			Config: &core.ConfigData{Vocabulary: vocabulary},
		}})
	if err != nil {
		return ConfigWriteResult{}, false, err
	}
	genesisState, err := core.ApplyConfig(nil, genesisPack)
	if err != nil {
		return ConfigWriteResult{}, false, err
	}
	genesisHead, err := r.writeConfigObjects(ctx, "", genesisPack, genesisState, configGenesisSubject)
	if err != nil {
		return ConfigWriteResult{}, false, err
	}

	pack, err := core.NewConfigOperationPack(config.ProjectID, generation, actor, 2, configWallTime(), operations)
	if err != nil {
		return ConfigWriteResult{}, false, err
	}
	if err := core.ValidateConfigAuthoring(&genesisState, pack); err != nil {
		return ConfigWriteResult{}, false, err
	}
	state, err := core.ApplyConfig(&genesisState, pack)
	if err != nil {
		return ConfigWriteResult{}, false, err
	}
	head, err := r.writeConfigObjects(ctx, genesisHead, pack, state, reason)
	if err != nil {
		return ConfigWriteResult{}, false, err
	}
	if err := r.createRefWithReason(ctx, configRef, head, configRefLogReason); err != nil {
		if _, exists, readErr := r.readConfigRef(ctx, config, configRef); readErr != nil {
			return ConfigWriteResult{}, false, readErr
		} else if !exists {
			return ConfigWriteResult{}, false, core.Wrap(core.CategoryOperational,
				"cannot create the Workbook configuration ledger", err)
		}
		return ConfigWriteResult{}, false, nil
	}
	r.replaceVocabulary(state.Vocabulary(), head)
	return ConfigWriteResult{Head: head, Seeded: true, State: state}, true, nil
}

// appendConfigOperation records one pack on top of an observed tip.
func (r *Repository) appendConfigOperation(
	ctx context.Context,
	tip configRecord,
	operations []core.ConfigOperation,
	actor string,
	reason string,
) (ConfigWriteResult, error) {
	pack, err := core.NewConfigOperationPack(
		tip.State.ProjectID,
		tip.State.History.Generation,
		actor,
		tip.State.LogicalClock+1,
		configWallTime(),
		operations,
	)
	if err != nil {
		return ConfigWriteResult{}, err
	}
	if err := core.ValidateConfigAuthoring(&tip.State, pack); err != nil {
		return ConfigWriteResult{}, err
	}
	state, err := core.ApplyConfig(&tip.State, pack)
	if err != nil {
		return ConfigWriteResult{}, err
	}
	head, err := r.writeConfigObjects(ctx, tip.Head, pack, state, reason)
	if err != nil {
		return ConfigWriteResult{}, err
	}
	// A successful configuration write is the one moment this clone is
	// certainly acting on the ledger and already moving its ref, so superseded
	// parked tips are retired in the same transaction. Reads and fetches leave
	// them alone, exactly as they do for a task's parked tips.
	pruned, err := r.prunableParkedConfigRefs(ctx)
	if err != nil {
		return ConfigWriteResult{}, err
	}
	if err := r.commitConfigRefUpdate(ctx, head, tip.Head, pruned, configRefLogReason); err != nil {
		return ConfigWriteResult{}, err
	}
	r.replaceVocabulary(state.Vocabulary(), head)
	return ConfigWriteResult{Head: head, State: state}, nil
}

// configWallTime stamps a pack's display timestamp. Wall time is attribution
// only — the logical clock and the commit graph carry every causal claim — so
// this reads the process clock directly rather than threading one more
// injectable dependency through the ledger.
func configWallTime() time.Time {
	return time.Now().UTC()
}

// writeConfigObjects durably records one configuration pack and its checkpoint
// without touching any ref.
//
// The tree is the task tree's shape, operation.json beside state.json, and that
// is deliberate rather than incidental: a configuration commit is the same kind
// of object as a task commit — an immutable operation and the checkpoint it
// produced — so it is read by the same tree parser and bounded by the same
// per-object ceiling.
func (r *Repository) writeConfigObjects(
	ctx context.Context,
	parentHead string,
	pack core.ConfigOperationPack,
	state core.ConfigStateDocument,
	reason string,
) (string, error) {
	packBytes, err := core.EncodeDocument(pack)
	if err != nil {
		return "", err
	}
	stateBytes, err := core.EncodeDocument(state)
	if err != nil {
		return "", err
	}
	operationBlob, err := r.writeBlob(ctx, packBytes)
	if err != nil {
		return "", err
	}
	stateBlob, err := r.writeBlob(ctx, stateBytes)
	if err != nil {
		return "", err
	}
	tree, err := r.writeTaskTree(ctx, operationBlob, stateBlob)
	if err != nil {
		return "", err
	}
	return r.writeCommit(ctx, tree, parentHead, reason)
}

// commitConfigRefUpdate compare-and-swaps the configuration ref and retires
// superseded parked refs in one transaction.
func (r *Repository) commitConfigRefUpdate(ctx context.Context, head, expected string, pruned []string, reason string) error {
	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	fmt.Fprintf(&input, "update %s %s %s\n", configRef, head, expected)
	for _, name := range pruned {
		fmt.Fprintf(&input, "delete %s\n", name)
	}
	input.WriteString("prepare\ncommit\n")
	if _, err := r.Git(ctx, input.Bytes(),
		"update-ref", "--no-deref", "--create-reflog", "-m", reason, "--stdin"); err != nil {
		if r.refValueDiffers(ctx, configRef, expected) {
			return core.Wrap(core.CategoryStaleWrite, "the configuration ledger changed concurrently", err)
		}
		return err
	}
	return nil
}

// configRefListing is one enumeration of the configuration refs: the tips that
// exist, and the names under origin's mirror this version does not read.
type configRefListing struct {
	Heads map[string]string
	// Ignored names refs the fetch mirrored from origin's configuration
	// namespace that are not the configuration ref. They are stated under the
	// name origin holds them at, because that is the name a person would have
	// to act on, and reporting one is never an instruction to delete it.
	Ignored []string
}

// listConfigRefs enumerates the named configuration refs in one Git process.
//
// Each name is also a prefix pattern, so the listing reports any ref created
// under one of them, and the two namespaces earn different verdicts for exactly
// the reason the identity refs do. The canonical ref is under this tool's
// exclusive control: a name beneath it means the local namespace was
// rearranged, and that is corruption. Origin's mirror is not — anyone with push
// access can create refs/workbook/config/x on a remote that has no ledger yet,
// Git's directory/file rule only forbids the two coexisting, and the fetch
// refspec mirrors it faithfully. Refusing to read past that would let one stray
// ref deny synchronization to every clone, permanently, with no way for the
// affected user to clear it. Such a name is skipped and reported instead.
func (r *Repository) listConfigRefs(ctx context.Context, refs ...string) (configRefListing, error) {
	contents, err := r.Git(ctx, nil, append([]string{"for-each-ref", "--format=" + taskRefFormat}, refs...)...)
	if err != nil {
		return configRefListing{}, err
	}
	return parseConfigRefRecords(refs, contents)
}

func parseConfigRefRecords(refs []string, contents []byte) (configRefListing, error) {
	listing := configRefListing{Heads: make(map[string]string, len(refs))}
	if len(contents) == 0 {
		return listing, nil
	}
	if contents[len(contents)-1] != '\n' {
		return configRefListing{}, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated configuration ref record")
	}
	for _, line := range bytes.Split(contents[:len(contents)-1], []byte{'\n'}) {
		parts := bytes.Split(line, []byte{0})
		if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) == 0 {
			return configRefListing{}, core.Errorf(core.CategoryCorruptData, "Git returned an invalid configuration ref record")
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
			if strings.HasPrefix(refName, remoteConfigRef+"/") {
				listing.Ignored = append(listing.Ignored, originConfigRefName(refName))
				continue
			}
			return configRefListing{}, configRefChildError(configRefAncestorOf(refs, refName))
		}
		if symbolicTarget != "" {
			return configRefListing{}, core.Errorf(core.CategoryCorruptData, "configuration ref %q must not be symbolic", refName)
		}
		if _, duplicate := listing.Heads[requested]; duplicate {
			return configRefListing{}, core.Errorf(core.CategoryCorruptData, "configuration ref %q was returned more than once", refName)
		}
		listing.Heads[requested] = objectID
	}
	return listing, nil
}

// originConfigRefName restates a mirrored name under the ref origin holds it
// at. The local mirror is rebuilt by every fetch, so naming it would name a ref
// the user cannot usefully remove.
func originConfigRefName(refName string) string {
	return configRef + strings.TrimPrefix(refName, remoteConfigRef)
}

func configRefAncestorOf(refs []string, refName string) string {
	for _, ref := range refs {
		if strings.HasPrefix(refName, ref+"/") {
			return ref
		}
	}
	return refName
}

func configRefChildError(ref string) error {
	return core.Errorf(core.CategoryCorruptData,
		"%s must be one ref holding one ledger; Git's directory/file rule means nothing may be created under that name", ref)
}

// readConfigRef reads and structurally validates one configuration ref tip.
func (r *Repository) readConfigRef(ctx context.Context, config core.ProjectConfig, ref string) (configRecord, bool, error) {
	listing, err := r.listConfigRefs(ctx, ref)
	if err != nil {
		return configRecord{}, false, err
	}
	head, found := listing.Heads[ref]
	if !found {
		return configRecord{}, false, nil
	}
	record, err := r.readConfigRecordAt(ctx, config, ref, head)
	if err != nil {
		return configRecord{}, false, err
	}
	return record, true, nil
}

// readConfigRecordAt reads one already-observed configuration tip.
func (r *Repository) readConfigRecordAt(ctx context.Context, config core.ProjectConfig, ref, head string) (configRecord, error) {
	records, err := r.readConfigRecords(ctx, config, ref, []string{head})
	if err != nil {
		return configRecord{}, err
	}
	return records[0], nil
}

// readConfigRecords reads every named configuration commit through one batch
// process, so replaying a divergent ledger costs one Git process however many
// commits it has to fold.
//
// Reads go through the same batch reader every other object read uses, so
// MaxObjectBytes bounds them: a hand-built commit pushed by a collaborator
// cannot make a clone that fetches it allocate without limit.
func (r *Repository) readConfigRecords(
	ctx context.Context,
	config core.ProjectConfig,
	ref string,
	heads []string,
) ([]configRecord, error) {
	records := make([]configRecord, len(heads))
	if len(heads) == 0 {
		return records, nil
	}
	widths := make([]int, len(heads))
	for index, head := range heads {
		if err := r.rememberGitObjectID(head); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "Git returned an invalid configuration ref object ID", err)
		}
		decoded, err := decodeObjectID(head)
		if err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "Git returned an invalid configuration ref object ID", err)
		}
		widths[index] = len(decoded)
	}

	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		for _, head := range heads {
			if _, err := fmt.Fprintf(writer, "%s\n%s^{tree}\n%s:%s\n%s:%s\n",
				head, head, head, configOperationPath, head, configStatePath); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	defer batch.Close()

	for index, head := range heads {
		objects, err := readBatchObjects(batch.Reader())
		if err != nil {
			return nil, batch.ReadFailure("cannot read Workbook configuration objects from Git batch", err)
		}
		record, err := validateConfigObjects(objects, config, ref, head, widths[index])
		if err != nil {
			return nil, err
		}
		records[index] = record
	}
	if err := batch.Finish(); err != nil {
		return nil, err
	}
	return records, nil
}

// validateConfigObjects enforces the shape of one configuration commit: a tree
// holding exactly the operation and the checkpoint, canonical bytes for both, a
// topology that matches what the pack claims to be, and a root whose checkpoint
// recomputes.
//
// A non-root commit's checkpoint is not recomputed here, exactly as a task
// tip's is not: recomputing needs the parent, which needs the whole chain, and
// paying for that on every read would make opening a repository cost the
// project's history. `workbook validate` folds the chain and compares every
// checkpoint; this read checks what one object can answer for.
func validateConfigObjects(
	objects [4]batchObject,
	config core.ProjectConfig,
	ref, head string,
	objectIDBytes int,
) (configRecord, error) {
	commit, tree, operationBlob, stateBlob := objects[0], objects[1], objects[2], objects[3]
	for _, object := range objects {
		if object.refused != nil {
			return configRecord{}, object.refused
		}
		if object.missing {
			return configRecord{}, core.Errorf(core.CategoryCorruptData,
				"requested Workbook configuration object %q is missing", object.objectID)
		}
	}
	if commit.objectID != head || commit.kind != "commit" {
		return configRecord{}, core.Errorf(core.CategoryCorruptData, "%s does not point directly to a commit", ref)
	}
	if tree.kind != "tree" {
		return configRecord{}, core.Errorf(core.CategoryCorruptData, "the Workbook configuration commit does not point to a tree")
	}
	if operationBlob.kind != "blob" || stateBlob.kind != "blob" {
		return configRecord{}, core.Errorf(core.CategoryCorruptData, "the Workbook configuration documents are not blobs")
	}
	commitTree, err := commitTreeObjectID(commit.contents, "the Workbook configuration commit")
	if err != nil {
		return configRecord{}, err
	}
	if tree.objectID != commitTree {
		return configRecord{}, core.Errorf(core.CategoryCorruptData,
			"the Workbook configuration commit tree does not match its batch object")
	}
	entries, err := parseRawTaskTree(tree.contents, objectIDBytes)
	if err != nil {
		return configRecord{}, err
	}
	if entries[configOperationPath] != operationBlob.objectID || entries[configStatePath] != stateBlob.objectID {
		return configRecord{}, core.Errorf(core.CategoryCorruptData,
			"the Workbook configuration tree entries do not match their batch objects")
	}

	pack, err := decodeCanonicalConfigOperation(operationBlob.contents)
	if err != nil {
		return configRecord{}, err
	}
	if err := validateConfigPackBudget(pack); err != nil {
		return configRecord{}, err
	}
	state, err := decodeCanonicalConfigState(stateBlob.contents)
	if err != nil {
		return configRecord{}, err
	}
	if err := validateConfigTipIdentity(config, pack, state); err != nil {
		return configRecord{}, err
	}
	parents, err := commitParentCount(commit.contents, "the Workbook configuration commit")
	if err != nil {
		return configRecord{}, err
	}
	if isConfigGenesisPack(pack) {
		if parents != 0 {
			return configRecord{}, core.Errorf(core.CategoryCorruptData,
				"the Workbook configuration genesis commit must have no parents")
		}
		if err := core.ValidateConfigCheckpoint(nil, pack, state); err != nil {
			return configRecord{}, err
		}
	} else if parents != 1 {
		return configRecord{}, core.Errorf(core.CategoryCorruptData,
			"an ordinary Workbook configuration commit must have exactly one parent")
	}
	return configRecord{Head: head, Operation: pack, State: state}, nil
}

// isConfigGenesisPack reports the one pack shape that may root a ledger.
func isConfigGenesisPack(pack core.ConfigOperationPack) bool {
	return pack.LogicalClock == 1 && len(pack.Operations) == 1 && pack.Operations[0].Type == core.ConfigGenesis
}

// validateConfigPackBudget refuses to fold a pack larger than this process is
// willing to spend on somebody else's history.
//
// The category is deliberately operational and never CategoryCorruptData. The
// pack is well formed, it already folded on the clone that wrote it, and the
// same command with a raised bound folds it here — so this is a statement about
// this process, not a verdict on the data. Saying otherwise would tell a whole
// team their configuration history is broken because one of them scripted a
// large change, and would strand a project that append-only storage gives no
// way to repair.
func validateConfigPackBudget(pack core.ConfigOperationPack) error {
	if len(pack.Operations) > core.MaxConfigOperationsPerPack {
		return core.Errorf(core.CategoryOperational,
			"a configuration pack carries %d operations, over this clone's fold budget of %d "+
				"(MaxConfigOperationsPerPack); the ledger is unchanged, and raising the bound is the only thing that reads it",
			len(pack.Operations), core.MaxConfigOperationsPerPack)
	}
	return nil
}

func validateConfigTipIdentity(config core.ProjectConfig, pack core.ConfigOperationPack, state core.ConfigStateDocument) error {
	if pack.ProjectID != config.ProjectID || state.ProjectID != config.ProjectID {
		return core.Errorf(core.CategoryCorruptData, "the Workbook configuration documents do not match the configured project")
	}
	if pack.HistoryGeneration != state.History.Generation {
		return core.Errorf(core.CategoryCorruptData, "configuration operation and state history generations do not match")
	}
	if pack.LogicalClock != state.LogicalClock {
		return core.Errorf(core.CategoryCorruptData, "configuration operation and state logical clocks do not match")
	}
	return nil
}

func decodeCanonicalConfigOperation(contents []byte) (core.ConfigOperationPack, error) {
	pack, err := core.DecodeConfigOperationPack(contents)
	if err != nil {
		return core.ConfigOperationPack{}, err
	}
	canonical, err := core.EncodeDocument(pack)
	if err != nil {
		return core.ConfigOperationPack{}, core.Wrap(core.CategoryCorruptData, "cannot canonicalize the configuration operation", err)
	}
	if !bytes.Equal(contents, canonical) {
		return core.ConfigOperationPack{}, core.Errorf(core.CategoryCorruptData, "the configuration operation is not canonical")
	}
	return pack, nil
}

func decodeCanonicalConfigState(contents []byte) (core.ConfigStateDocument, error) {
	state, err := core.DecodeConfigStateDocument(contents)
	if err != nil {
		return core.ConfigStateDocument{}, err
	}
	canonical, err := core.EncodeDocument(state)
	if err != nil {
		return core.ConfigStateDocument{}, core.Wrap(core.CategoryCorruptData, "cannot canonicalize the configuration checkpoint", err)
	}
	if !bytes.Equal(contents, canonical) {
		return core.ConfigStateDocument{}, core.Errorf(core.CategoryCorruptData, "the configuration checkpoint is not canonical")
	}
	return state, nil
}

// parkedConfigRef is one retained orphaned configuration tip.
type parkedConfigRef struct {
	index    int
	name     string
	objectID string
}

// validParkedConfigRefName reports whether a name is one this clone could have
// constructed. The ref-update transaction checks it because a malformed name
// reaching Git would create the wrong ref.
func validParkedConfigRefName(name string) bool {
	suffix, found := strings.CutPrefix(name, parkedConfigRefPrefix)
	if !found {
		return false
	}
	index, err := strconv.Atoi(suffix)
	return err == nil && index >= 0 && strconv.Itoa(index) == suffix
}

// listParkedConfigRefs enumerates the configuration parking namespace.
//
// It is a separate lister from listParkedRefs rather than a parameterization of
// it, and that is the point: that one splits every name into a task ID and an
// index and skips whatever does not parse, so it would skip every configuration
// entry silently and forever. Entries here that do not name an index are
// ignored for the same reason task parks ignore theirs — these refs are
// disposable local bookkeeping, and a stray hand-written ref must not be able
// to stop synchronization.
func (r *Repository) listParkedConfigRefs(ctx context.Context) ([]parkedConfigRef, error) {
	contents, err := r.Git(ctx, nil, "for-each-ref", "--format=%(refname)%00%(objectname)", parkedConfigRefPrefix)
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, nil
	}
	if contents[len(contents)-1] != '\n' {
		return nil, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated ref record")
	}
	refs := make([]parkedConfigRef, 0, bytes.Count(contents, []byte{'\n'}))
	for _, line := range bytes.Split(contents[:len(contents)-1], []byte{'\n'}) {
		name, objectID, split := strings.Cut(string(line), "\x00")
		if !split || objectID == "" {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned an invalid ref record")
		}
		if !strings.HasPrefix(name, parkedConfigRefPrefix) {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned a ref outside %q", parkedConfigRefPrefix)
		}
		suffix := strings.TrimPrefix(name, parkedConfigRefPrefix)
		index, err := strconv.Atoi(suffix)
		if err != nil || index < 0 || strconv.Itoa(index) != suffix {
			continue
		}
		refs = append(refs, parkedConfigRef{index: index, name: name, objectID: objectID})
	}
	return refs, nil
}

// nextParkedConfigRefIndex returns the index the next parked configuration tip
// should use, one greater than the highest already present.
func (r *Repository) nextParkedConfigRefIndex(ctx context.Context) (int, error) {
	refs, err := r.listParkedConfigRefs(ctx)
	if err != nil {
		return 0, err
	}
	next := 0
	for _, ref := range refs {
		if ref.index >= next {
			next = ref.index + 1
		}
	}
	return next, nil
}

// prunableParkedConfigRefs returns the parked configuration refs past the
// retention bound, oldest first.
func (r *Repository) prunableParkedConfigRefs(ctx context.Context) ([]string, error) {
	refs, err := r.listParkedConfigRefs(ctx)
	if err != nil {
		return nil, err
	}
	if len(refs) <= maxParkedConfigRefs {
		return nil, nil
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].index < refs[j].index })
	names := make([]string, 0, len(refs)-maxParkedConfigRefs)
	for _, ref := range refs[:len(refs)-maxParkedConfigRefs] {
		names = append(names, ref.name)
	}
	return names, nil
}

// PruneParkedConfigRefs retires orphaned configuration tips past the retention
// bound and reports how many refs it deleted.
//
// It stands alone for the same reason the task sweep does: pruning inside a
// write bounds retention only for a clone that keeps writing, and a clone that
// fetches and reconciles but never changes a status again would otherwise keep
// every tip its ledger ever orphaned. The synchronization watcher calls it on
// every tick, beside the task sweep, which is what makes the bound a bound
// rather than a comment.
func (r *Repository) PruneParkedConfigRefs(ctx context.Context) (int, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return 0, err
	}
	names, err := r.prunableParkedConfigRefs(ctx)
	if err != nil {
		return 0, err
	}
	if len(names) == 0 {
		return 0, nil
	}
	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	for _, name := range names {
		fmt.Fprintf(&input, "delete %s\n", name)
	}
	input.WriteString("prepare\ncommit\n")
	if _, err := r.Git(ctx, input.Bytes(), "update-ref", "--no-deref", "-m", configPruneReason, "--stdin"); err != nil {
		return 0, err
	}
	return len(names), nil
}
