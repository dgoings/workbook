package core

import (
	"math/big"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	configOperationPackFormat = "workbook.config-operation-pack"
	configStateDocumentFormat = "workbook.config-state"
)

// ConfigOperationType names one durable change to a project's configuration.
//
// The set is closed and every member is a statement about a status, because
// status vocabulary is the only configuration the ledger carries today. A later
// section adds its own types beside these rather than overloading them: an
// operation whose meaning depends on which section it lands in cannot be
// replayed by a build that does not know that section.
type ConfigOperationType string

const (
	// ConfigGenesis carries a whole configuration as data and may appear only
	// as the sole operation of a root pack.
	//
	// It exists because the built-in defaults are a product decision that
	// changes between releases, and a ledger whose root said "start from the
	// defaults" would mean something different in every build that read it.
	// Writing the whole value makes the history self-contained and version
	// independent: a v0.9 clone folding a ledger seeded by v0.5 reproduces the
	// v0.5 vocabulary exactly, which is the same reason task.create carries a
	// whole TaskData instead of a diff against an implied blank task.
	ConfigGenesis ConfigOperationType = "config.genesis"
	// ConfigStatusAdd defines a new status.
	ConfigStatusAdd ConfigOperationType = "status.add"
	// ConfigStatusRename gives a status a new token, leaving an alias behind.
	ConfigStatusRename ConfigOperationType = "status.rename"
	// ConfigStatusRelabel changes a status's display label.
	ConfigStatusRelabel ConfigOperationType = "status.relabel"
	// ConfigStatusRemove retires a status and forwards its tasks elsewhere.
	ConfigStatusRemove ConfigOperationType = "status.remove"
	// ConfigStatusReorder moves a status to a literal rank.
	ConfigStatusReorder ConfigOperationType = "status.reorder"
	// ConfigStatusTag gives a status a role.
	ConfigStatusTag ConfigOperationType = "status.tag"
	// ConfigStatusUntag takes a role away.
	ConfigStatusUntag ConfigOperationType = "status.untag"
)

// ConfigOperation is one immutable configuration change.
//
// Like the task Operation it mirrors, it is a single struct with optional
// members rather than a union: the members that carry meaning are selected by
// Type, and validateConfigOperationDocument refuses an operation that carries a
// member its type does not read. The member names differ where the wire form
// reads better for it — an add names a status it is creating, a rename names
// the one it is replacing — because the ledger is a durable document a person
// may end up reading in a `git show`.
type ConfigOperation struct {
	ID   string              `json:"id"`
	Type ConfigOperationType `json:"type"`
	// Name is the status a status.add creates.
	Name Status `json:"name,omitempty"`
	// From and To are a status.rename's subject and its replacement.
	From Status `json:"from,omitempty"`
	To   Status `json:"to,omitempty"`
	// Status is the subject of every other status operation. It is resolved
	// through the rename chain before it is applied, so an operation authored
	// against a name a concurrent rename has since replaced still lands on the
	// status the author meant — and it stops at a retirement, so an operation
	// against a status a concurrent removal deleted is a no-op rather than an
	// edit to whichever status inherited its tasks.
	Status Status `json:"status,omitempty"`
	// Label carries a display label for status.add and status.relabel.
	Label string `json:"label,omitempty"`
	// Rank carries a reduced-rational position for status.add and
	// status.reorder.
	Rank string `json:"rank,omitempty"`
	// Tags carries the initial roles of a status.add.
	Tags []StatusTag `json:"tags,omitempty"`
	// Tag carries the single role of a status.tag or status.untag.
	Tag StatusTag `json:"tag,omitempty"`
	// Destination is where a status.remove forwards the retired status's
	// tasks.
	Destination Status `json:"destination,omitempty"`
	// Config is the whole configuration a config.genesis carries.
	Config *ConfigData `json:"config,omitempty"`
}

// ConfigOperationPack is one commit's worth of configuration changes.
//
// It has no task ID. The configuration is a singleton rooted at its own ref, so
// where a task pack has to say which of many histories it belongs to, this one
// only has to say which project.
type ConfigOperationPack struct {
	Format            string            `json:"format"`
	Version           int               `json:"version"`
	ProjectID         string            `json:"projectId"`
	HistoryGeneration string            `json:"historyGeneration"`
	Actor             Actor             `json:"actor"`
	LogicalClock      uint64            `json:"logicalClock"`
	WallTime          time.Time         `json:"wallTime"`
	Operations        []ConfigOperation `json:"operations"`
}

// VocabularyDocument is the stored form of a status vocabulary.
//
// Every member is a sorted array rather than a map, because the checkpoint's
// bytes are compared for equality by ValidateConfigCheckpoint and by the sync
// path. Go's encoder happens to sort map keys today; relying on that would make
// the durable format a property of the standard library. An array with a stated
// sort makes the canonical bytes something this package decides.
type VocabularyDocument struct {
	// Statuses are the live statuses, ordered by rank and then by name.
	Statuses []StatusDefinition `json:"statuses"`
	// Aliases are rename forwardings, ordered by source.
	Aliases []StatusAlias `json:"aliases"`
	// Retired are removal forwardings, ordered by source.
	Retired []RetiredStatus `json:"retired"`
}

// ConfigData is everything the ledger configures. It is a struct with one
// member rather than the vocabulary itself so that a later story adds a section
// beside this one without changing the genesis operation's shape.
type ConfigData struct {
	Vocabulary VocabularyDocument `json:"vocabulary"`
}

// ConfigStateDocument is a resolved configuration checkpoint, written beside
// every configuration operation pack.
//
// It is a checkpoint rather than a derived value for the same reason a task
// carries one: a cold read — rendering a board, resolving one task's status —
// must cost one object read, not a walk of the whole ledger. Storing it also
// makes the fold falsifiable, because ValidateConfigCheckpoint can recompute it
// and compare bytes.
type ConfigStateDocument struct {
	Format       string     `json:"format"`
	Version      int        `json:"version"`
	ProjectID    string     `json:"projectId"`
	History      History    `json:"history"`
	LogicalClock uint64     `json:"logicalClock"`
	Config       ConfigData `json:"config"`
}

// Vocabulary reads the checkpoint's vocabulary. A decoded checkpoint has
// already been normalized, so this cannot fail.
func (state ConfigStateDocument) Vocabulary() Vocabulary {
	return newVocabularyFromCanonical(state.Config.Vocabulary)
}

// NewConfigOperationPack stamps one authored batch of configuration operations
// with the durable format this version writes, and refuses a batch that is not
// a well formed pack.
//
// It is exported because the configuration ledger lives outside this package,
// unlike the task ledger: a task pack is built by Service, three functions
// away from the format constant, while a configuration pack is built by the
// Git-backed ledger that owns the singleton ref. Handing that caller the
// format and version constants instead would put two copies of the durable
// header in the tree, and a durable header with two authors eventually has two
// values.
//
// The clock and the history generation come from the caller because only the
// caller knows the parent: a root pack carries clock 1 and a fresh generation,
// and every later pack carries its parent's generation and one more than its
// clock. Both are checked again by applyConfigOperations, so a caller that gets
// them wrong is refused rather than recorded.
func NewConfigOperationPack(
	projectID string,
	historyGeneration string,
	actor string,
	logicalClock uint64,
	wallTime time.Time,
	operations []ConfigOperation,
) (ConfigOperationPack, error) {
	pack := ConfigOperationPack{
		Format:            configOperationPackFormat,
		Version:           documentVersion,
		ProjectID:         projectID,
		HistoryGeneration: historyGeneration,
		Actor:             Actor{ID: actor},
		LogicalClock:      logicalClock,
		WallTime:          wallTime,
		Operations:        append([]ConfigOperation(nil), operations...),
	}
	if err := validateConfigOperationPackDocument(pack); err != nil {
		return ConfigOperationPack{}, err
	}
	return pack, nil
}

// ApplyConfig applies one immutable configuration pack to a configuration
// state.
//
// It is deterministic, idempotent, and — this is the part that differs from the
// task fold — it never fails on arity. A pack that leaves the vocabulary with
// no default, or with no status tagged done, is normalized into a usable
// vocabulary rather than rejected, because by the time a pack reaches this
// function it has already happened somewhere: refusing it would strand the
// clone that fetched it, not the person who authored it. Arity is refused at
// the authoring boundary instead, by ValidateConfigAuthoring, where somebody
// can still choose differently.
//
// Structural failure is still failure. An unsupported operation type, a
// malformed token, a pack whose clock does not advance its parent — those are
// corrupt data, and folding past them would invent a state no author ever
// wrote.
func ApplyConfig(parent *ConfigStateDocument, pack ConfigOperationPack) (ConfigStateDocument, error) {
	vocabulary, generation, err := applyConfigOperations(parent, pack)
	if err != nil {
		return ConfigStateDocument{}, err
	}
	vocabulary.normalizeArity()
	document, err := vocabulary.document()
	if err != nil {
		return ConfigStateDocument{}, Wrap(CategoryCorruptData, "configuration pack produced an invalid vocabulary", err)
	}
	return ConfigStateDocument{
		Format:       configStateDocumentFormat,
		Version:      documentVersion,
		ProjectID:    pack.ProjectID,
		History:      History{Generation: generation},
		LogicalClock: pack.LogicalClock,
		Config:       ConfigData{Vocabulary: document},
	}, nil
}

// ValidateConfigAuthoring reports whether a pack is safe to write.
//
// It applies the pack without the arity normalization ApplyConfig performs and
// reports what the author actually asked for. Untagging the last status tagged
// done fails here with a message naming the command that fixes it; the same
// pack arriving from a peer folds cleanly, because the peer's clone already
// treats it as history.
//
// It is also the only place the size ceilings are enforced. A ceiling has to be
// asked before a pack exists rather than while folding one: a fold that can
// fail on a count can be made to fail forever by two clones doing something
// each was allowed to do.
func ValidateConfigAuthoring(parent *ConfigStateDocument, pack ConfigOperationPack) error {
	vocabulary, _, err := applyConfigOperations(parent, pack)
	if err != nil {
		return err
	}
	document, err := vocabulary.document()
	if err != nil {
		return err
	}
	var before VocabularyDocument
	if parent != nil {
		before = parent.Config.Vocabulary
	}
	if err := validateVocabularyGrowth(before, document); err != nil {
		return err
	}
	return newVocabularyFromCanonical(document).Validate()
}

// ValidateConfigCheckpoint verifies that a stored configuration state is the
// canonical result of applying a pack, by bytes rather than by structure.
func ValidateConfigCheckpoint(parent *ConfigStateDocument, pack ConfigOperationPack, stored ConfigStateDocument) error {
	computed, err := ApplyConfig(parent, pack)
	if err != nil {
		return err
	}
	computedBytes, err := EncodeDocument(computed)
	if err != nil {
		return Wrap(CategoryCorruptData, "cannot encode computed configuration checkpoint", err)
	}
	storedBytes, err := EncodeDocument(stored)
	if err != nil {
		return Wrap(CategoryCorruptData, "cannot encode stored configuration checkpoint", err)
	}
	if !reflect.DeepEqual(computedBytes, storedBytes) {
		return corrupt("stored configuration checkpoint differs from computed state")
	}
	return nil
}

// applyConfigOperations folds a pack over its parent and returns the raw
// vocabulary, before arity normalization. Both ApplyConfig and the authoring
// gate go through here; the split is what lets one normalize where the other
// refuses.
func applyConfigOperations(parent *ConfigStateDocument, pack ConfigOperationPack) (*configVocabulary, string, error) {
	if err := validateConfigOperationPackDocument(pack); err != nil {
		return nil, "", err
	}

	if parent == nil {
		// A genesis is the only valid root, and there is only ever meant to be
		// one of them per project.
		//
		// Two clones can nevertheless mint one concurrently, because the
		// configuration ledger is seeded lazily: a project that predates it
		// grows a genesis the first time anybody changes a status. Fetching
		// before mutating settles the common case — the second clone sees the
		// first's root and appends to it. What it cannot settle is two clones
		// that both seeded while offline, which produces two unrelated
		// histories rather than a conflict inside one. Resolving that means
		// adopting origin's root and replaying the local operations onto it,
		// which is a reconcile-time decision and belongs with the rest of the
		// sync work rather than here. This function's only obligation is to
		// make each root well defined.
		if pack.LogicalClock != 1 {
			return nil, "", corrupt("root configuration pack logical clock must be 1")
		}
		if len(pack.Operations) != 1 || pack.Operations[0].Type != ConfigGenesis {
			return nil, "", corrupt("root configuration pack must contain exactly one config.genesis operation")
		}
		vocabulary, err := newConfigVocabulary(pack.Operations[0].Config.Vocabulary)
		if err != nil {
			return nil, "", err
		}
		return vocabulary, pack.HistoryGeneration, nil
	}

	if err := validateConfigStateDocument(*parent); err != nil {
		return nil, "", err
	}
	if parent.ProjectID != pack.ProjectID {
		return nil, "", corrupt("configuration pack project ID does not match parent")
	}
	if parent.History.Generation != pack.HistoryGeneration {
		return nil, "", corrupt("configuration pack history generation does not match parent")
	}
	if pack.LogicalClock != parent.LogicalClock+1 {
		return nil, "", corrupt("configuration pack logical clock must advance parent by one")
	}

	vocabulary, err := newConfigVocabulary(parent.Config.Vocabulary)
	if err != nil {
		return nil, "", err
	}
	for _, operation := range pack.Operations {
		if operation.Type == ConfigGenesis {
			return nil, "", corrupt("config.genesis requires no parent")
		}
		if err := vocabulary.apply(operation); err != nil {
			return nil, "", err
		}
	}
	return vocabulary, parent.History.Generation, nil
}

// configStatus is a live status plus its parsed rank. The rank is parsed once,
// where it enters the fold, so that sorting and normalization are total.
type configStatus struct {
	definition StatusDefinition
	rank       *big.Rat
}

// configVocabulary is the mutable working form of a vocabulary during a fold.
type configVocabulary struct {
	statuses map[Status]*configStatus
	aliases  map[Status]Status
	retired  map[Status]Status
}

func newConfigVocabulary(document VocabularyDocument) (*configVocabulary, error) {
	normalized, err := normalizeVocabularyDocument(document)
	if err != nil {
		return nil, Wrap(CategoryCorruptData, "configuration contains an invalid vocabulary", err)
	}
	vocabulary := &configVocabulary{
		statuses: make(map[Status]*configStatus, len(normalized.Statuses)),
		aliases:  make(map[Status]Status, len(normalized.Aliases)),
		retired:  make(map[Status]Status, len(normalized.Retired)),
	}
	for _, definition := range normalized.Statuses {
		rank, err := parseRank(definition.Rank)
		if err != nil {
			return nil, Wrap(CategoryCorruptData, "status rank is invalid", err)
		}
		vocabulary.statuses[definition.Status] = &configStatus{definition: definition, rank: rank}
	}
	for _, alias := range normalized.Aliases {
		vocabulary.aliases[alias.From] = alias.To
	}
	for _, entry := range normalized.Retired {
		vocabulary.retired[entry.Status] = entry.Destination
	}
	return vocabulary, nil
}

func (vocabulary *configVocabulary) document() (VocabularyDocument, error) {
	document := VocabularyDocument{
		Statuses: make([]StatusDefinition, 0, len(vocabulary.statuses)),
		Aliases:  make([]StatusAlias, 0, len(vocabulary.aliases)),
		Retired:  make([]RetiredStatus, 0, len(vocabulary.retired)),
	}
	for _, status := range vocabulary.statuses {
		document.Statuses = append(document.Statuses, status.definition)
	}
	for from, to := range vocabulary.aliases {
		document.Aliases = append(document.Aliases, StatusAlias{From: from, To: to})
	}
	for status, destination := range vocabulary.retired {
		document.Retired = append(document.Retired, RetiredStatus{Status: status, Destination: destination})
	}
	// Map iteration delivered these in an arbitrary order; normalization is
	// what makes the result a function of the configuration rather than of this
	// process's hash seed.
	return normalizeVocabularyDocument(document)
}

// resolveSubject walks an operation's subject through rename aliases only, and
// stops at a retirement.
//
// The distinction between the two chains is the whole difference between "this
// status is now called something else" and "this status is gone". An operation
// authored against a name a concurrent rename replaced still means the status
// it named, so it follows the rename. An operation authored against a name a
// concurrent removal retired means a status that no longer exists, and
// following the retirement would apply it to the innocent status the tasks were
// forwarded into — renaming or relabelling a column nobody asked to touch.
// Those operations become no-ops instead, and PR-B reports them as
// status-retired conflicts.
func (vocabulary *configVocabulary) resolveSubject(status Status) (Status, bool) {
	if _, live := vocabulary.statuses[status]; live {
		return status, true
	}
	seen := make(map[Status]struct{}, len(vocabulary.aliases))
	current := status
	for range len(vocabulary.aliases) + 1 {
		next, aliased := vocabulary.aliases[current]
		if !aliased {
			return status, false
		}
		if _, repeated := seen[next]; repeated {
			return status, false
		}
		seen[next] = struct{}{}
		if _, live := vocabulary.statuses[next]; live {
			return next, true
		}
		current = next
	}
	return status, false
}

// resolve walks a stored status to the live status it now means, through both
// chains. It is the same walk Vocabulary.Resolve performs, over the mutable
// form, and it is what a removal's destination goes through: a destination that
// has itself since been retired should forward to wherever it went.
func (vocabulary *configVocabulary) resolve(status Status) (Status, bool) {
	if _, live := vocabulary.statuses[status]; live {
		return status, true
	}
	seen := make(map[Status]struct{}, len(vocabulary.aliases)+len(vocabulary.retired))
	current := status
	for range len(vocabulary.aliases) + len(vocabulary.retired) + 1 {
		next, forwarded := vocabulary.forwarded(current)
		if !forwarded {
			return status, false
		}
		if _, repeated := seen[next]; repeated {
			return status, false
		}
		seen[next] = struct{}{}
		if _, live := vocabulary.statuses[next]; live {
			return next, true
		}
		current = next
	}
	return status, false
}

func (vocabulary *configVocabulary) forwarded(status Status) (Status, bool) {
	if to, aliased := vocabulary.aliases[status]; aliased {
		return to, true
	}
	to, retired := vocabulary.retired[status]
	return to, retired
}

func (vocabulary *configVocabulary) apply(operation ConfigOperation) error {
	switch operation.Type {
	case ConfigStatusAdd:
		return vocabulary.applyAdd(operation)
	case ConfigStatusRename:
		vocabulary.applyRename(operation)
		return nil
	case ConfigStatusRelabel:
		vocabulary.applyRelabel(operation)
		return nil
	case ConfigStatusRemove:
		vocabulary.applyRemove(operation)
		return nil
	case ConfigStatusReorder:
		return vocabulary.applyReorder(operation)
	case ConfigStatusTag:
		vocabulary.applyTag(operation)
		return nil
	case ConfigStatusUntag:
		vocabulary.applyUntag(operation)
		return nil
	default:
		return corrupt("unsupported configuration operation type %q", operation.Type)
	}
}

// applyAdd defines a status, and does nothing at all when the name is already
// live.
//
// Doing nothing is what makes a duplicated pack a no-op, and it is also the
// concurrent rule: two clones that both add "shipped" converge on one status
// rather than on an error, and if they disagree about its label the one that
// applies first keeps it. Reconciliation always replays the fetched history
// before the local operations, so "first" means upstream, and a local add is
// the side that yields. PR-B's classify surfaces the discarded label; the fold
// only has to converge.
func (vocabulary *configVocabulary) applyAdd(operation ConfigOperation) error {
	if _, live := vocabulary.statuses[operation.Name]; live {
		return nil
	}
	rank, err := parseRank(operation.Rank)
	if err != nil {
		return Wrap(CategoryCorruptData, "status.add rank is invalid", err)
	}
	tags, err := normalizeStatusTags(operation.Tags)
	if err != nil {
		return Wrap(CategoryCorruptData, "status.add tags are invalid", err)
	}
	// The name is live again, so any forwarding pointer still aimed away from
	// it has to go: leaving one would make every stored task under this name
	// resolve past the status the author just created.
	delete(vocabulary.aliases, operation.Name)
	delete(vocabulary.retired, operation.Name)
	vocabulary.statuses[operation.Name] = &configStatus{
		definition: StatusDefinition{
			Status: operation.Name,
			Label:  operation.Label,
			Rank:   operation.Rank,
			Tags:   tags,
		},
		rank: rank,
	}
	for _, tag := range tags {
		if tag == StatusTagDefault {
			vocabulary.clearDefaultExcept(operation.Name)
		}
	}
	return nil
}

// applyRename resolves its subject through the forwarding chains first, which
// is what makes a rename authored against a stale name land on the right
// status.
//
// Renaming onto a name that is already live is a no-op rather than a merge.
// Two statuses cannot share a token, and picking a winner here would silently
// discard one status's tasks; PR-B classifies the collision as a
// status-rename conflict so a person decides.
func (vocabulary *configVocabulary) applyRename(operation ConfigOperation) {
	from, live := vocabulary.resolveSubject(operation.From)
	if !live || from == operation.To {
		return
	}
	if _, taken := vocabulary.statuses[operation.To]; taken {
		return
	}
	status := vocabulary.statuses[from]
	status.definition.Status = operation.To
	delete(vocabulary.statuses, from)
	vocabulary.statuses[operation.To] = status
	delete(vocabulary.aliases, operation.To)
	delete(vocabulary.retired, operation.To)
	vocabulary.aliases[from] = operation.To
}

func (vocabulary *configVocabulary) applyRelabel(operation ConfigOperation) {
	subject, live := vocabulary.resolveSubject(operation.Status)
	if !live {
		return
	}
	vocabulary.statuses[subject].definition.Label = operation.Label
}

func (vocabulary *configVocabulary) applyReorder(operation ConfigOperation) error {
	subject, live := vocabulary.resolveSubject(operation.Status)
	if !live {
		return nil
	}
	rank, err := parseRank(operation.Rank)
	if err != nil {
		return Wrap(CategoryCorruptData, "status.reorder rank is invalid", err)
	}
	// The recorded rank is literal, not relative. Two clones that reorder
	// different statuses concurrently therefore converge without either one
	// re-deriving a position from a list it never saw, and two clones that
	// reorder the same status to the same rank agree exactly. Equal ranks are a
	// reachable state, broken by status name.
	vocabulary.statuses[subject].definition.Rank = operation.Rank
	vocabulary.statuses[subject].rank = rank
	return nil
}

// applyRemove retires a status and leaves a forwarding pointer to where its
// tasks belong.
//
// Both the subject and the destination are resolved first, so an upstream
// rename of A to B followed by a local removal of A into D converges on
// retiring B into D — the author asked to remove a status, not a token.
//
// Removing the last live status is refused, silently: a project with no
// statuses has no column a task can be in and no default a create can use, and
// there is no later operation that could repair it from the outside.
func (vocabulary *configVocabulary) applyRemove(operation ConfigOperation) {
	subject, live := vocabulary.resolveSubject(operation.Status)
	if !live || len(vocabulary.statuses) == 1 {
		return
	}
	destination, resolved := vocabulary.resolve(operation.Destination)
	if !resolved || destination == subject {
		return
	}
	// A cycle cannot be built here, and that is a property of this ordering
	// rather than a coincidence: the destination is live at this moment, a live
	// status forwards nowhere, and the subject is not the destination, so the
	// chain that now starts at the subject terminates one hop later. Every
	// chain that used to end at the subject gains exactly that one hop.
	// ValidateConfigCheckpoint can therefore never see a cycle from a fold, and
	// normalizeVocabularyDocument's cycle check only ever fires on a document
	// that did not come from one. Detecting a cycle at reconcile time and
	// reporting it as a conflict is PR-B's job.
	delete(vocabulary.statuses, subject)
	vocabulary.retired[subject] = destination
}

// applyTag gives a status a role, and transfers the default tag atomically.
//
// Exclusivity by construction matters more than it looks: expressing "make
// triage the default" as an untag followed by a tag would make the intermediate
// state — no default at all — a thing a concurrent clone could fetch and
// normalize, and normalization would pick a status nobody chose. One operation
// has no intermediate state.
func (vocabulary *configVocabulary) applyTag(operation ConfigOperation) {
	subject, live := vocabulary.resolveSubject(operation.Status)
	if !live {
		return
	}
	if operation.Tag == StatusTagDefault {
		vocabulary.clearDefaultExcept(subject)
	}
	status := vocabulary.statuses[subject]
	if status.definition.HasTag(operation.Tag) {
		return
	}
	tags := append(append([]StatusTag(nil), status.definition.Tags...), operation.Tag)
	// The tags were validated by the operation document check, so this cannot
	// fail.
	status.definition.Tags, _ = normalizeStatusTags(tags)
}

func (vocabulary *configVocabulary) applyUntag(operation ConfigOperation) {
	subject, live := vocabulary.resolveSubject(operation.Status)
	if !live {
		return
	}
	status := vocabulary.statuses[subject]
	tags := make([]StatusTag, 0, len(status.definition.Tags))
	for _, tag := range status.definition.Tags {
		if tag != operation.Tag {
			tags = append(tags, tag)
		}
	}
	status.definition.Tags = tags
}

func (vocabulary *configVocabulary) clearDefaultExcept(keep Status) {
	for name, status := range vocabulary.statuses {
		if name == keep || !status.definition.HasTag(StatusTagDefault) {
			continue
		}
		tags := make([]StatusTag, 0, len(status.definition.Tags))
		for _, tag := range status.definition.Tags {
			if tag != StatusTagDefault {
				tags = append(tags, tag)
			}
		}
		status.definition.Tags = tags
	}
}

// normalizeArity repairs the three invariants a fold may break, in a fixed
// order so that two clones folding the same history reach the same answer.
//
// Every rule picks its subject by position rather than by name, because
// position is the one thing every clone agrees on without consulting anything
// outside the vocabulary. Repairing rather than failing is the point: a clone
// that fetched a pack leaving no status tagged done still has to render a board
// and still has to answer whether a dependency is satisfied.
func (vocabulary *configVocabulary) normalizeArity() {
	live := vocabulary.sortedStatuses()
	if len(live) == 0 {
		return
	}

	// 1. More than one default: keep the lowest-ranked and clear the rest. A
	// genesis document is the only way to reach this, since every tagging
	// operation transfers the tag rather than adding one.
	for _, status := range live {
		if status.definition.HasTag(StatusTagDefault) {
			vocabulary.clearDefaultExcept(status.definition.Status)
			break
		}
	}
	// 2. No default: the lowest-ranked status, which is where a board reads
	// left to right and where a new task most plausibly belongs.
	if vocabulary.taggedCount(StatusTagDefault) == 0 {
		vocabulary.addTag(live[0].definition.Status, StatusTagDefault)
	}
	// 3. No done: the highest-ranked status, by the same reading.
	if vocabulary.taggedCount(StatusTagDone) == 0 {
		vocabulary.addTag(live[len(live)-1].definition.Status, StatusTagDone)
	}
	// 4. No next: the default, which is the one status guaranteed to hold
	// tasks. Choosing it also keeps a single-status vocabulary coherent.
	if vocabulary.taggedCount(StatusTagNext) == 0 {
		for _, status := range vocabulary.sortedStatuses() {
			if status.definition.HasTag(StatusTagDefault) {
				vocabulary.addTag(status.definition.Status, StatusTagNext)
				break
			}
		}
	}
}

func (vocabulary *configVocabulary) addTag(status Status, tag StatusTag) {
	vocabulary.applyTag(ConfigOperation{Type: ConfigStatusTag, Status: status, Tag: tag})
}

func (vocabulary *configVocabulary) taggedCount(tag StatusTag) int {
	count := 0
	for _, status := range vocabulary.statuses {
		if status.definition.HasTag(tag) {
			count++
		}
	}
	return count
}

// sortedStatuses orders the live statuses by rank and then by name. The name
// tiebreak is what keeps two statuses that landed on the same rank — reachable
// whenever two clones insert concurrently — in the same order everywhere.
func (vocabulary *configVocabulary) sortedStatuses() []*configStatus {
	sorted := make([]*configStatus, 0, len(vocabulary.statuses))
	for _, status := range vocabulary.statuses {
		sorted = append(sorted, status)
	}
	sort.SliceStable(sorted, func(left, right int) bool {
		if compare := sorted[left].rank.Cmp(sorted[right].rank); compare != 0 {
			return compare < 0
		}
		return sorted[left].definition.Status < sorted[right].definition.Status
	})
	return sorted
}

func validateConfigOperationPackDocument(pack ConfigOperationPack) error {
	if pack.Format != configOperationPackFormat {
		return corrupt("unsupported configuration operation pack format %q", pack.Format)
	}
	if pack.Version != documentVersion {
		return corrupt("unsupported configuration operation pack version %d", pack.Version)
	}
	if err := validateCanonicalULID("configuration operation pack project ID", pack.ProjectID); err != nil {
		return err
	}
	if err := validateCanonicalULID("configuration operation pack history generation", pack.HistoryGeneration); err != nil {
		return err
	}
	if strings.TrimSpace(pack.Actor.ID) == "" {
		return corrupt("configuration operation pack actor ID must not be blank")
	}
	if pack.LogicalClock == 0 {
		return corrupt("configuration operation pack logical clock must be positive")
	}
	if pack.WallTime.IsZero() {
		return corrupt("configuration operation pack wall time must be present")
	}
	if len(pack.Operations) == 0 {
		return corrupt("configuration operation pack must contain at least one operation")
	}
	seen := make(map[string]struct{}, len(pack.Operations))
	for _, operation := range pack.Operations {
		if err := validateConfigOperationDocument(operation); err != nil {
			return err
		}
		if _, duplicate := seen[operation.ID]; duplicate {
			return corrupt("configuration operation pack contains duplicate operation ID %q", operation.ID)
		}
		seen[operation.ID] = struct{}{}
	}
	return nil
}

// configOperationMembers names the members each operation type reads. Anything
// outside its list must be absent, which is what stops a future build's
// operation from being folded as if it were an older one.
type configOperationMembers struct {
	name        bool
	from        bool
	to          bool
	status      bool
	label       bool
	rank        bool
	tags        bool
	tag         bool
	destination bool
	config      bool
}

var configOperationShapes = map[ConfigOperationType]configOperationMembers{
	ConfigGenesis:       {config: true},
	ConfigStatusAdd:     {name: true, label: true, rank: true, tags: true},
	ConfigStatusRename:  {from: true, to: true},
	ConfigStatusRelabel: {status: true, label: true},
	ConfigStatusRemove:  {status: true, destination: true},
	ConfigStatusReorder: {status: true, rank: true},
	ConfigStatusTag:     {status: true, tag: true},
	ConfigStatusUntag:   {status: true, tag: true},
}

func validateConfigOperationDocument(operation ConfigOperation) error {
	if err := validateOperationID(operation.ID); err != nil {
		return err
	}
	shape, known := configOperationShapes[operation.Type]
	if !known {
		return corrupt("unsupported configuration operation type %q", operation.Type)
	}
	present := configOperationMembers{
		name:        operation.Name != "",
		from:        operation.From != "",
		to:          operation.To != "",
		status:      operation.Status != "",
		label:       operation.Label != "",
		rank:        operation.Rank != "",
		tags:        operation.Tags != nil,
		tag:         operation.Tag != "",
		destination: operation.Destination != "",
		config:      operation.Config != nil,
	}
	// status.add is the one type with an optional member: a status may
	// legitimately carry no tags, and an absent list and an empty one mean the
	// same thing.
	if operation.Type == ConfigStatusAdd {
		present.tags = true
	}
	if present != shape {
		return corrupt("%s carries the wrong members", operation.Type)
	}

	for _, token := range []Status{operation.Name, operation.From, operation.To, operation.Status, operation.Destination} {
		if token == "" {
			continue
		}
		if err := validateStatusToken(token); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" names an invalid status", err)
		}
	}
	if operation.Label != "" {
		if err := validateStatusLabel(operation.Label); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid label", err)
		}
	}
	if operation.Rank != "" {
		if _, err := parseRank(operation.Rank); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid rank", err)
		}
	}
	if operation.Tag != "" {
		if err := validateStatusTag(operation.Tag); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid tag", err)
		}
	}
	for _, tag := range operation.Tags {
		if err := validateStatusTag(tag); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid tag", err)
		}
	}
	if operation.Type == ConfigStatusRename && operation.From == operation.To {
		return corrupt("status.rename must name a different status")
	}
	if operation.Type == ConfigStatusRemove && operation.Status == operation.Destination {
		return corrupt("status.remove must name a different destination")
	}
	if operation.Config != nil {
		normalized, err := normalizeVocabularyDocument(operation.Config.Vocabulary)
		if err != nil {
			return Wrap(CategoryCorruptData, "config.genesis carries an invalid vocabulary", err)
		}
		if !reflect.DeepEqual(operation.Config.Vocabulary, normalized) {
			return corrupt("config.genesis configuration is not canonical")
		}
	}
	return nil
}

func validateConfigStateDocument(state ConfigStateDocument) error {
	if state.Format != configStateDocumentFormat {
		return corrupt("unsupported configuration state format %q", state.Format)
	}
	if state.Version != documentVersion {
		return corrupt("unsupported configuration state version %d", state.Version)
	}
	if err := validateCanonicalULID("configuration state project ID", state.ProjectID); err != nil {
		return err
	}
	if err := validateCanonicalULID("configuration state history generation", state.History.Generation); err != nil {
		return err
	}
	if state.History.CompactedFrom != nil {
		return corrupt("configuration state compaction metadata is unsupported in the append-only POC")
	}
	if state.LogicalClock == 0 {
		return corrupt("configuration state logical clock must be positive")
	}
	normalized, err := normalizeVocabularyDocument(state.Config.Vocabulary)
	if err != nil {
		return Wrap(CategoryCorruptData, "configuration state contains an invalid vocabulary", err)
	}
	if !reflect.DeepEqual(state.Config.Vocabulary, normalized) {
		return corrupt("configuration state is not canonical")
	}
	return nil
}
