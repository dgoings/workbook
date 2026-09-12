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
// The set is closed, and it now spans two sections: the status vocabulary the
// ledger was built for, and the display settings a project presents itself
// with. The second section got its own types beside the first rather than
// overloading them, which is the rule this comment stated before there was a
// second section to apply it to: an operation whose meaning depends on which
// section it lands in cannot be replayed by a build that does not know that
// section, and a build that does not know a type refuses it by name.
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
	// ConfigDisplaySet records what one display setting is, and
	// ConfigDisplayUnset records that it is nothing again.
	//
	// They are the second section the type comment above anticipated, and they
	// are two symmetric types rather than one carrying an empty value on the way
	// out. An operation whose meaning turned on whether a member was blank would
	// make "cleared it" and "set it to nothing" the same recorded intent, and
	// only one of those is a thing somebody can mean. Two types also give the
	// classifier and the inverse renderer a shape to key on rather than a value
	// to interpret.
	//
	// One pair covers all three settings, keyed on Setting, because they differ
	// only in which member of the section they land on: a build that can fold one
	// can fold all three, so a type per setting would declare a reader that never
	// existed — the same arithmetic the writer-format generation makes.
	ConfigDisplaySet   ConfigOperationType = "display.set"
	ConfigDisplayUnset ConfigOperationType = "display.unset"
	// The eight priority operations are the third section's mutations, the
	// same eight shapes the status vocabulary has plus one: statuses have no
	// equivalent of ConfigPriorityRecolor, because a priority carries a field —
	// color — that a status does not, and that field is the one thing on a
	// priority that can be explicitly cleared back to a derived default rather
	// than merely reassigned. See ConfigPriorityRecolor's own comment.
	//
	// ConfigPriorityAdd defines a new priority.
	ConfigPriorityAdd ConfigOperationType = "priority.add"
	// ConfigPriorityRename gives a priority a new token, leaving an alias
	// behind, mirroring ConfigStatusRename.
	ConfigPriorityRename ConfigOperationType = "priority.rename"
	// ConfigPriorityRelabel changes a priority's display label.
	ConfigPriorityRelabel ConfigOperationType = "priority.relabel"
	// ConfigPriorityRemove retires a priority and forwards its tasks
	// elsewhere.
	ConfigPriorityRemove ConfigOperationType = "priority.remove"
	// ConfigPriorityReorder moves a priority to a literal rank.
	ConfigPriorityReorder ConfigOperationType = "priority.reorder"
	// ConfigPriorityTag gives a priority a role.
	ConfigPriorityTag ConfigOperationType = "priority.tag"
	// ConfigPriorityUntag takes a role away.
	ConfigPriorityUntag ConfigOperationType = "priority.untag"
	// ConfigPriorityRecolor sets or clears a priority's stored color.
	//
	// It is its own operation rather than a member of priority.relabel because
	// color is the one field on a priority that can be cleared back to the
	// board's derived ramp — the same set/unset shape display.set and
	// display.unset give the display section, collapsed into one type here
	// because there is only one field to set or clear rather than three: an
	// empty Value means clear, the same convention ConfigOperation.Value
	// already carries for display.set.
	ConfigPriorityRecolor ConfigOperationType = "priority.recolor"
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
	// Setting names the display setting a display.set or display.unset acts on:
	// project-name, primary-color, or text-color.
	Setting string `json:"setting,omitempty"`
	// Value is what a display.set records, or the color a priority.recolor
	// sets — empty clears it back to the derived ramp. It is stored in the
	// canonical form the boundary produced — a trimmed name, a lowercase color
	// — because the checkpoint these fold into is compared by bytes, so a
	// value with two spellings would be two configurations.
	Value string `json:"value,omitempty"`
	// Config is the whole configuration a config.genesis carries.
	Config *ConfigData `json:"config,omitempty"`

	// The seven priority members below are the priority section's counterparts
	// to Name/From/To/Status/Destination/Tag/Tags above. They are separate
	// fields rather than a shared one, because Priority is a distinct type
	// from Status — the same distinction that keeps a task's priority and its
	// status from being typo'd into each other anywhere else in this package —
	// and a field can only ever hold one of the two.
	//
	// PriorityName is the priority a priority.add creates.
	PriorityName Priority `json:"priorityName,omitempty"`
	// PriorityFrom and PriorityTo are a priority.rename's subject and its
	// replacement.
	PriorityFrom Priority `json:"priorityFrom,omitempty"`
	PriorityTo   Priority `json:"priorityTo,omitempty"`
	// Priority is the subject of every other priority operation. Like Status
	// above, it is resolved through the rename chain before it is applied, so
	// an operation authored against a name a concurrent rename has since
	// replaced still lands on the priority the author meant, and it stops at a
	// retirement for the same reason Status's does.
	Priority Priority `json:"priority,omitempty"`
	// PriorityDestination is where a priority.remove forwards the retired
	// priority's tasks.
	PriorityDestination Priority `json:"priorityDestination,omitempty"`
	// PriorityTag carries the single role of a priority.tag or
	// priority.untag.
	PriorityTag PriorityTag `json:"priorityTag,omitempty"`
	// PriorityTags carries the initial roles of a priority.add.
	PriorityTags []PriorityTag `json:"priorityTags,omitempty"`
}

// ConfigOperationPack is one commit's worth of configuration changes.
//
// It has no task ID. The configuration is a singleton rooted at its own ref, so
// where a task pack has to say which of many histories it belongs to, this one
// only has to say which project.
type ConfigOperationPack struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	// MinReader is the lowest writer-format generation that can fold this pack,
	// and means exactly what the task pack's member means. The ledger is a
	// shared ref like any other, and a configuration section a future build
	// adds is precisely the kind of change an older reader cannot replay, so it
	// gets the same signal rather than a second mechanism.
	MinReader         int               `json:"minReader,omitempty"`
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

// ConfigData is everything the ledger configures. It is a struct rather than
// the vocabulary itself so that a section can be added beside that one without
// changing the genesis operation's shape — which is what the display section
// below did.
type ConfigData struct {
	Vocabulary VocabularyDocument `json:"vocabulary"`
	// Display is how this project presents itself: what its board is called and
	// what colors it draws in. It is a pointer with omitempty, and the canonical
	// value for a project that has configured nothing is nil, so every
	// checkpoint written before this section existed still encodes to exactly
	// the bytes it was stored as. See DisplayDocument.
	Display *DisplayDocument `json:"display,omitempty"`
	// Priorities is this project's configured priority vocabulary, the sibling
	// of Display on the same terms: a pointer with omitempty, and nil — not an
	// empty document — is the canonical value for a project that has
	// configured none, so every checkpoint written before this section existed
	// still encodes to exactly the bytes it was stored as. Populate this from
	// PriorityVocabulary.Document, never from EffectiveDocument — see their
	// comments for why the two must not be confused here.
	Priorities *PriorityDocument `json:"priorities,omitempty"`
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
	Format  string `json:"format"`
	Version int    `json:"version"`
	// MinReader is the highest generation any pack in the ledger's history has
	// required, carried forward by ApplyConfig. It is the ledger's counterpart
	// to the task checkpoint's watermark, and it is what lets a clone answer
	// "can I still change the configuration?" from the tip alone.
	MinReader    int        `json:"minReader,omitempty"`
	ProjectID    string     `json:"projectId"`
	History      History    `json:"history"`
	LogicalClock uint64     `json:"logicalClock"`
	Config       ConfigData `json:"config"`
}

// configOperationMinReader declares, per configuration operation type, the
// writer-format generation a reader needs to fold a pack containing it.
//
// The status entries are zero: they are what the ledger was built to carry, and
// every build that has a ledger at all folds them. The display entries are two,
// the generation the display section introduced, and they are the reason this
// table is per operation type rather than per document. A build without the
// section cannot decode a checkpoint carrying `display` strictly, and a build
// that folded a display operation by ignoring it would compute a different
// configuration from the same bytes — so it is told to upgrade instead, and
// only about a project that has configured something. A project that has not
// keeps a ledger those builds fold exactly as they always did.
//
// The priority entries are three, the generation the priority vocabulary
// introduced, for the identical reason the display entries are two: a build
// that predates them would compute a different — or no — configuration from a
// checkpoint carrying a `priorities` section, so it is told to upgrade rather
// than left to misfold silently.
var configOperationMinReader = map[ConfigOperationType]int{
	ConfigGenesis:         0,
	ConfigStatusAdd:       0,
	ConfigStatusRename:    0,
	ConfigStatusRelabel:   0,
	ConfigStatusRemove:    0,
	ConfigStatusReorder:   0,
	ConfigStatusTag:       0,
	ConfigStatusUntag:     0,
	ConfigDisplaySet:      2,
	ConfigDisplayUnset:    2,
	ConfigPriorityAdd:     3,
	ConfigPriorityRename:  3,
	ConfigPriorityRelabel: 3,
	ConfigPriorityRemove:  3,
	ConfigPriorityReorder: 3,
	ConfigPriorityTag:     3,
	ConfigPriorityUntag:   3,
	ConfigPriorityRecolor: 3,
}

// ConfigPackMinReader returns the generation a reader needs to fold these
// configuration operations.
//
// A config.genesis is judged by what it carries rather than by its type alone,
// which is the one place the table above is not the whole answer. A genesis
// carries a whole ConfigData as data, so one carrying a display section is a
// document an older reader cannot read even though no display operation
// appears in the pack.
//
// Priorities are judged differently, because — unlike display — every genesis
// this build writes carries a priorities section: seedConfigLedger and
// MintConfigLedger both record core.BuiltInPriorityVocabulary() at creation,
// so "the section is present" is no longer a fact about whether the project
// configured anything. What still is a fact is whether the recorded set
// differs from the built-ins: when it holds exactly the built-in three, an
// older build that ignores the section and falls back to its own built-in
// three reaches the identical answer, so nothing is misread and the marker
// would be a lie. The guard therefore compares the genesis-carried document
// against BuiltInPriorityVocabulary().Document() and fires only on a
// difference — a different label, a different rank, an extra or missing
// entry, a different default, any alias or retirement — not merely on the
// section's presence. A pack keeps whatever its writing build decided even if
// a later release changes the built-in set, because ConfigPackMinReader is
// only ever called once, at write time (NewConfigOperationPack), and the
// verdict is then carried forward from the stored MinReader rather than
// recomputed from content.
func ConfigPackMinReader(operations []ConfigOperation) int {
	generation := 0
	for _, operation := range operations {
		if required := configOperationMinReader[operation.Type]; required > generation {
			generation = required
		}
		if operation.Config != nil && operation.Config.Display != nil {
			if required := configOperationMinReader[ConfigDisplaySet]; required > generation {
				generation = required
			}
		}
		if operation.Config != nil && operation.Config.Priorities != nil &&
			!reflect.DeepEqual(*operation.Config.Priorities, BuiltInPriorityVocabulary().Document()) {
			if required := configOperationMinReader[ConfigPriorityAdd]; required > generation {
				generation = required
			}
		}
	}
	return generation
}

// RequiresNewerReader reports a configuration pack this build must not fold.
func (pack ConfigOperationPack) RequiresNewerReader() bool {
	return pack.MinReader > SupportedFormatGeneration
}

// RequiresNewerReader reports a configuration checkpoint whose history this
// build must not fold past.
func (state ConfigStateDocument) RequiresNewerReader() bool {
	return state.MinReader > SupportedFormatGeneration
}

// newerWriterConfig is the refusal every configuration surface repeats. It
// names the ledger rather than a task, because the configuration is a singleton
// and there is nothing narrower to name.
func newerWriterConfig() error {
	return newerWriter(
		"this project's configuration was written by a newer workbook; upgrade workbook to change it")
}

// Vocabulary reads the checkpoint's vocabulary. A decoded checkpoint has
// already been normalized, so this cannot fail.
func (state ConfigStateDocument) Vocabulary() Vocabulary {
	return newVocabularyFromCanonical(state.Config.Vocabulary)
}

// Display reads the checkpoint's display settings, the sibling of Vocabulary
// and normalized on the same terms. The zero value is a project that has
// configured none of them, which is every project until somebody does.
func (state ConfigStateDocument) Display() DisplaySettings {
	return ResolveDisplaySettings(state.Config.Display)
}

// PriorityVocabulary reads the checkpoint's priority vocabulary, the sibling
// of Vocabulary and Display and normalized on the same terms. A checkpoint
// carrying no priorities section decodes to the zero PriorityVocabulary,
// whose own accessors substitute the built-in three — the same way an
// unconfigured status Vocabulary reads as DefaultVocabulary.
func (state ConfigStateDocument) PriorityVocabulary() PriorityVocabulary {
	if state.Config.Priorities == nil {
		return PriorityVocabulary{}
	}
	return newPriorityVocabularyFromCanonical(*state.Config.Priorities)
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
		MinReader:         ConfigPackMinReader(operations),
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
// The priorities section is held to the same rule, for the same reason: a
// replay can leave it with no default tagged, or — a genesis is the only way
// to reach this, since every tagging operation transfers the tag rather than
// adding one — with two. Left alone, a configured-but-default-less vocabulary
// would answer Default() with "", which is not a priority any task can be
// created with; normalizeArity repairs it the same deterministic way the
// vocabulary's does, by position, so replay never produces that state.
//
// Structural failure is still failure. An unsupported operation type, a
// malformed token, a pack whose clock does not advance its parent — those are
// corrupt data, and folding past them would invent a state no author ever
// wrote.
func ApplyConfig(parent *ConfigStateDocument, pack ConfigOperationPack) (ConfigStateDocument, error) {
	folded, generation, err := applyConfigOperations(parent, pack)
	if err != nil {
		return ConfigStateDocument{}, err
	}
	vocabulary := folded.vocabulary
	vocabulary.normalizeArity()
	document, err := vocabulary.document()
	if err != nil {
		return ConfigStateDocument{}, Wrap(CategoryCorruptData, "configuration pack produced an invalid vocabulary", err)
	}
	folded.priorities.normalizeArity()
	priorities, err := folded.priorities.document()
	if err != nil {
		return ConfigStateDocument{}, Wrap(CategoryCorruptData, "configuration pack produced an invalid priority vocabulary", err)
	}
	minReader := pack.MinReader
	if parent != nil && parent.MinReader > minReader {
		minReader = parent.MinReader
	}
	return ConfigStateDocument{
		Format:       configStateDocumentFormat,
		Version:      documentVersion,
		MinReader:    minReader,
		ProjectID:    pack.ProjectID,
		History:      History{Generation: generation},
		LogicalClock: pack.LogicalClock,
		Config:       ConfigData{Vocabulary: document, Display: folded.display.canonical(), Priorities: priorities},
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
	folded, _, err := applyConfigOperations(parent, pack)
	if err != nil {
		return err
	}
	document, err := folded.vocabulary.document()
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
	if err := newVocabularyFromCanonical(document).Validate(); err != nil {
		return err
	}

	// The display section, by contrast, has no arity to violate and no
	// collection to grow: three optional values, each bounded where it is
	// authored and each already refused by the operation document check if it
	// is not. There is therefore nothing for this gate to ask about it that
	// the fold has not already settled, and inventing a question would be a
	// second rule to keep in step with the first — which is exactly why
	// priorities, unlike display, get the same two questions statuses just
	// answered above: priorities have both an arity rule (exactly one
	// default) and three collections that grow (the live set, aliases,
	// retirements), the identical shape the status vocabulary has.
	priorityDocument, err := folded.priorities.document()
	if err != nil {
		return err
	}
	var beforePriorities PriorityDocument
	if parent != nil && parent.Config.Priorities != nil {
		beforePriorities = *parent.Config.Priorities
	}
	var afterPriorities PriorityDocument
	if priorityDocument != nil {
		afterPriorities = *priorityDocument
	}
	if err := validatePriorityGrowth(beforePriorities, afterPriorities); err != nil {
		return err
	}
	// A nil priorityDocument means the project has configured no priorities
	// at all — the canonical "use the built-in three" state
	// normalizeStoredPriorityDocument enforces — and that is a perfectly
	// valid configuration, not an arity violation. Validate() on the zero
	// PriorityVocabulary would say "the project has no priorities", which is
	// the wrong answer for a project that simply never touched this section;
	// asking it is what distinguishes "configured nothing" from "configured
	// down to nothing", the same distinction PriorityVocabulary.Validate's own
	// doc comment draws. So this only asks once the section is actually
	// configured.
	if priorityDocument != nil {
		if err := newPriorityVocabularyFromCanonical(*priorityDocument).Validate(); err != nil {
			return err
		}
	}
	return nil
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

// configFold is every section's working state during one fold, threaded
// together so that adding a section is one member here rather than one more
// return value at four call sites.
type configFold struct {
	vocabulary *configVocabulary
	display    *configDisplay
	// priorities is the mutable working form of the priorities section, the
	// sibling of vocabulary rather than of display: like the status
	// vocabulary it has an apply method routing eight operation types and an
	// arity invariant ApplyConfig repairs after the fold, which display's
	// three independent, arity-free settings have no equivalent of.
	priorities *configPriorities
}

// applyConfigOperations folds a pack over its parent and returns the raw
// sections, before arity normalization. Both ApplyConfig and the authoring
// gate go through here; the split is what lets one normalize where the other
// refuses.
func applyConfigOperations(parent *ConfigStateDocument, pack ConfigOperationPack) (configFold, string, error) {
	// Same gate, same reason, same ordering as the task fold: a document that
	// declared a generation this build does not have is refused as
	// newer-writer before any rule of this build's is applied to it.
	if parent != nil && parent.RequiresNewerReader() {
		return configFold{}, "", newerWriterConfig()
	}
	if pack.RequiresNewerReader() {
		return configFold{}, "", newerWriterConfig()
	}
	if err := validateConfigOperationPackDocument(pack); err != nil {
		return configFold{}, "", err
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
			return configFold{}, "", corrupt("root configuration pack logical clock must be 1")
		}
		if len(pack.Operations) != 1 || pack.Operations[0].Type != ConfigGenesis {
			return configFold{}, "", corrupt("root configuration pack must contain exactly one config.genesis operation")
		}
		folded, err := newConfigFold(*pack.Operations[0].Config)
		if err != nil {
			return configFold{}, "", err
		}
		return folded, pack.HistoryGeneration, nil
	}

	if err := validateConfigStateDocument(*parent); err != nil {
		return configFold{}, "", err
	}
	if parent.ProjectID != pack.ProjectID {
		return configFold{}, "", corrupt("configuration pack project ID does not match parent")
	}
	if parent.History.Generation != pack.HistoryGeneration {
		return configFold{}, "", corrupt("configuration pack history generation does not match parent")
	}
	if pack.LogicalClock != parent.LogicalClock+1 {
		return configFold{}, "", corrupt("configuration pack logical clock must advance parent by one")
	}

	folded, err := newConfigFold(parent.Config)
	if err != nil {
		return configFold{}, "", err
	}
	for _, operation := range pack.Operations {
		if operation.Type == ConfigGenesis {
			return configFold{}, "", corrupt("config.genesis requires no parent")
		}
		if err := folded.apply(operation); err != nil {
			return configFold{}, "", err
		}
	}
	return folded, parent.History.Generation, nil
}

// newConfigFold builds every section's working state from a stored
// configuration.
func newConfigFold(config ConfigData) (configFold, error) {
	vocabulary, err := newConfigVocabulary(config.Vocabulary)
	if err != nil {
		return configFold{}, err
	}
	display, err := newConfigDisplay(config.Display)
	if err != nil {
		return configFold{}, err
	}
	// Normalized on the way in, the same as Display and Vocabulary, so a fold
	// never carries a non-canonical or aliasing priorities document forward —
	// see normalizeStoredPriorityDocument.
	priorities, err := newConfigPriorities(config.Priorities)
	if err != nil {
		return configFold{}, err
	}
	return configFold{vocabulary: vocabulary, display: display, priorities: priorities}, nil
}

// apply routes one operation to the section that owns it.
//
// The routing is by type rather than by trial, and the default arm belongs to
// the vocabulary because that is where every unsupported type is already
// refused. A section added later gets an arm here; an operation belonging to no
// section is corrupt data, which is what stops a build from folding a future
// generation's operation as if it were an older one.
func (folded configFold) apply(operation ConfigOperation) error {
	switch operation.Type {
	case ConfigDisplaySet, ConfigDisplayUnset:
		return folded.display.apply(operation)
	case ConfigPriorityAdd, ConfigPriorityRename, ConfigPriorityRelabel, ConfigPriorityRemove,
		ConfigPriorityReorder, ConfigPriorityTag, ConfigPriorityUntag, ConfigPriorityRecolor:
		return folded.priorities.apply(operation)
	default:
		return folded.vocabulary.apply(operation)
	}
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

// configPriorityEntry is a live priority plus its parsed rank, mirroring
// configStatus: the rank is parsed once, where it enters the fold, so that
// sorting and normalization are total.
type configPriorityEntry struct {
	definition PriorityDefinition
	rank       *big.Rat
}

// configPriorities is the mutable working form of a priority vocabulary
// during a fold, the sibling of configVocabulary.
//
// Unlike configVocabulary it can genuinely hold zero live priorities: a
// project that has never run a priority operation folds every pack with an
// empty configPriorities, and document() reports that state as nil, the same
// "configured nothing" normalizeStoredPriorityDocument enforces at rest —
// mirroring the same substitution PriorityVocabulary.effective() performs for
// a caller reading it. Once the first priority.add lands, applyRemove refuses
// to take the count back to zero the same way the vocabulary's applyRemove
// refuses to remove the last live status, so a non-empty section never folds
// back to empty; normalizeArity's default repair can therefore assume at
// least one candidate whenever there is anything to repair at all.
//
// That empty start is a hazard the status side does not have, and it is worth
// naming precisely because nothing in this stage can reach it yet. A
// project's config.genesis always carries a whole VocabularyDocument — every
// build that creates a project writes DefaultVocabulary or LegacyVocabulary
// into it explicitly — so newConfigVocabulary's fold never starts from
// nothing a live status could be missing from. Priorities have no genesis
// step: newConfigPriorities, just below, folds a nil section into an empty
// configPriorities, while PriorityVocabulary.effective() (priority.go) reads
// that same nil section as the built-in three. Those two readings of "nothing
// configured" agree only by accident, for exactly as long as nothing writes to
// the section: the moment a fold has folded even one priority operation, the
// checkpoint's priorities section is no longer nil, so every accessor stops
// substituting the built-in three and starts reading this fold's actual
// contents instead — which is whatever operations have been folded, not the
// built-in three plus those operations.
//
// Concretely: a project that has configured nothing is, today, every task
// filed under high, medium, or low. The first priority.add a future stage
// authors against that project — say, priority.add critical --tag default —
// folds against an empty configPriorities and produces a vocabulary of
// exactly {critical}. Every task still stored as high, medium, or low is now
// unresolvable: not live, not forwarded, sorted last by Order's stranded-token
// fallback, and invisible to any priority filter. The vocabulary was never
// wrong by the fold's own rules — normalizeArity has nothing to repair,
// because a single live default-tagged priority is a perfectly valid
// vocabulary — but it is wrong for the project, because the fold was never
// told about the three priorities every existing task actually depends on.
//
// This is unreachable in this stage: nothing here authors a priority
// operation, so no fold anywhere is asked to take this step. It is stage 2's
// problem, and it has to be solved before stage 2 ships any authoring path
// (a CLI command, an agent tool, anything that can produce a first
// ConfigPriorityAdd against a project that has never had one) — not discovered
// after. The fix is not obviously "seed the built-ins into the ledger"; that
// begs the question of when, since a genesis-time seed would give every prior
// stage-1 project a retroactive priorities section it never asked for, and a
// lazy seed on first-write has to decide atomically with that same write or
// reintroduce the identical race between two clones. Whatever the mechanism,
// it has to guarantee that the fold a first priority.add runs against already
// contains the three priorities every task in the project is depending on —
// not trust that the built-in substitution a *reader* performs will somehow
// also cover a *fold in progress*, which is precisely the confusion this
// comment exists to head off.
type configPriorities struct {
	priorities map[Priority]*configPriorityEntry
	aliases    map[Priority]Priority
	retired    map[Priority]Priority
}

// newConfigPriorities builds the working form from a stored priorities
// section. A nil document — the canonical "configured nothing" — builds an
// empty working form rather than failing, because an unconfigured project is
// exactly the state a fold must be able to start from.
func newConfigPriorities(document *PriorityDocument) (*configPriorities, error) {
	normalized, err := normalizeStoredPriorityDocument(document)
	if err != nil {
		return nil, Wrap(CategoryCorruptData, "configuration contains an invalid priority vocabulary", err)
	}
	priorities := &configPriorities{
		priorities: make(map[Priority]*configPriorityEntry),
		aliases:    make(map[Priority]Priority),
		retired:    make(map[Priority]Priority),
	}
	if normalized == nil {
		return priorities, nil
	}
	for _, definition := range normalized.Priorities {
		rank, err := parseRank(definition.Rank)
		if err != nil {
			return nil, Wrap(CategoryCorruptData, "priority rank is invalid", err)
		}
		priorities.priorities[definition.Priority] = &configPriorityEntry{definition: definition, rank: rank}
	}
	for _, alias := range normalized.Aliases {
		priorities.aliases[alias.From] = alias.To
	}
	for _, entry := range normalized.Retired {
		priorities.retired[entry.Priority] = entry.Destination
	}
	return priorities, nil
}

// document returns the section in the canonical stored form: nil when there
// is nothing configured, matching normalizeStoredPriorityDocument's rule that
// an all-empty section canonicalizes to the absent member rather than an
// empty-but-present one.
func (priorities *configPriorities) document() (*PriorityDocument, error) {
	document := PriorityDocument{
		Priorities: make([]PriorityDefinition, 0, len(priorities.priorities)),
		Aliases:    make([]PriorityAlias, 0, len(priorities.aliases)),
		Retired:    make([]RetiredPriority, 0, len(priorities.retired)),
	}
	for _, entry := range priorities.priorities {
		document.Priorities = append(document.Priorities, entry.definition)
	}
	for from, to := range priorities.aliases {
		document.Aliases = append(document.Aliases, PriorityAlias{From: from, To: to})
	}
	for priority, destination := range priorities.retired {
		document.Retired = append(document.Retired, RetiredPriority{Priority: priority, Destination: destination})
	}
	// Map iteration delivered these in an arbitrary order; normalization is
	// what makes the result a function of the configuration rather than of
	// this process's hash seed, and what collapses an empty result to nil.
	return normalizeStoredPriorityDocument(&document)
}

// resolveSubject walks an operation's subject through rename aliases only,
// and stops at a retirement, mirroring configVocabulary.resolveSubject for
// the same reason: an operation authored against a name a concurrent rename
// replaced still means the priority it named, while one against a name a
// concurrent removal retired means a priority that no longer exists.
func (priorities *configPriorities) resolveSubject(priority Priority) (Priority, bool) {
	if _, live := priorities.priorities[priority]; live {
		return priority, true
	}
	seen := make(map[Priority]struct{}, len(priorities.aliases))
	current := priority
	for range len(priorities.aliases) + 1 {
		next, aliased := priorities.aliases[current]
		if !aliased {
			return priority, false
		}
		if _, repeated := seen[next]; repeated {
			return priority, false
		}
		seen[next] = struct{}{}
		if _, live := priorities.priorities[next]; live {
			return next, true
		}
		current = next
	}
	return priority, false
}

// resolve walks a stored priority to the live priority it now means, through
// both chains, mirroring configVocabulary.resolve. It is what a removal's
// destination goes through: a destination that has itself since been retired
// should forward to wherever it went.
func (priorities *configPriorities) resolve(priority Priority) (Priority, bool) {
	if _, live := priorities.priorities[priority]; live {
		return priority, true
	}
	seen := make(map[Priority]struct{}, len(priorities.aliases)+len(priorities.retired))
	current := priority
	for range len(priorities.aliases) + len(priorities.retired) + 1 {
		next, forwarded := priorities.forwarded(current)
		if !forwarded {
			return priority, false
		}
		if _, repeated := seen[next]; repeated {
			return priority, false
		}
		seen[next] = struct{}{}
		if _, live := priorities.priorities[next]; live {
			return next, true
		}
		current = next
	}
	return priority, false
}

func (priorities *configPriorities) forwarded(priority Priority) (Priority, bool) {
	if to, aliased := priorities.aliases[priority]; aliased {
		return to, true
	}
	to, retired := priorities.retired[priority]
	return to, retired
}

func (priorities *configPriorities) apply(operation ConfigOperation) error {
	switch operation.Type {
	case ConfigPriorityAdd:
		return priorities.applyAdd(operation)
	case ConfigPriorityRename:
		priorities.applyRename(operation)
		return nil
	case ConfigPriorityRelabel:
		priorities.applyRelabel(operation)
		return nil
	case ConfigPriorityRemove:
		priorities.applyRemove(operation)
		return nil
	case ConfigPriorityReorder:
		return priorities.applyReorder(operation)
	case ConfigPriorityTag:
		priorities.applyTag(operation)
		return nil
	case ConfigPriorityUntag:
		priorities.applyUntag(operation)
		return nil
	case ConfigPriorityRecolor:
		priorities.applyRecolor(operation)
		return nil
	default:
		return corrupt("unsupported configuration operation type %q", operation.Type)
	}
}

// applyAdd defines a priority, and does nothing at all when the name is
// already live — the same no-op rule configVocabulary.applyAdd documents at
// length: it is what makes a duplicated pack a no-op, and it is the
// concurrent rule that two clones adding the same priority converge on one
// definition, the first upstream one, rather than on an error.
func (priorities *configPriorities) applyAdd(operation ConfigOperation) error {
	if _, live := priorities.priorities[operation.PriorityName]; live {
		return nil
	}
	rank, err := parseRank(operation.Rank)
	if err != nil {
		return Wrap(CategoryCorruptData, "priority.add rank is invalid", err)
	}
	tags, err := normalizePriorityTags(operation.PriorityTags)
	if err != nil {
		return Wrap(CategoryCorruptData, "priority.add tags are invalid", err)
	}
	// The name is live again, so any forwarding pointer still aimed away from
	// it has to go: leaving one would make every stored task under this name
	// resolve past the priority the author just created.
	delete(priorities.aliases, operation.PriorityName)
	delete(priorities.retired, operation.PriorityName)
	priorities.priorities[operation.PriorityName] = &configPriorityEntry{
		definition: PriorityDefinition{
			Priority: operation.PriorityName,
			Label:    operation.Label,
			Rank:     operation.Rank,
			Tags:     tags,
		},
		rank: rank,
	}
	for _, tag := range tags {
		if tag == PriorityTagDefault {
			priorities.clearDefaultExcept(operation.PriorityName)
		}
	}
	return nil
}

// applyRename mirrors configVocabulary.applyRename: renaming onto a name that
// is already live is a no-op rather than a merge, because two priorities
// cannot share a token and picking a winner here would silently discard one
// priority's tasks.
func (priorities *configPriorities) applyRename(operation ConfigOperation) {
	from, live := priorities.resolveSubject(operation.PriorityFrom)
	if !live || from == operation.PriorityTo {
		return
	}
	if _, taken := priorities.priorities[operation.PriorityTo]; taken {
		return
	}
	entry := priorities.priorities[from]
	entry.definition.Priority = operation.PriorityTo
	delete(priorities.priorities, from)
	priorities.priorities[operation.PriorityTo] = entry
	delete(priorities.aliases, operation.PriorityTo)
	delete(priorities.retired, operation.PriorityTo)
	priorities.aliases[from] = operation.PriorityTo
}

func (priorities *configPriorities) applyRelabel(operation ConfigOperation) {
	subject, live := priorities.resolveSubject(operation.Priority)
	if !live {
		return
	}
	priorities.priorities[subject].definition.Label = operation.Label
}

func (priorities *configPriorities) applyReorder(operation ConfigOperation) error {
	subject, live := priorities.resolveSubject(operation.Priority)
	if !live {
		return nil
	}
	rank, err := parseRank(operation.Rank)
	if err != nil {
		return Wrap(CategoryCorruptData, "priority.reorder rank is invalid", err)
	}
	// The recorded rank is literal, not relative, for the same convergence
	// reason configVocabulary.applyReorder's comment gives.
	priorities.priorities[subject].definition.Rank = operation.Rank
	priorities.priorities[subject].rank = rank
	return nil
}

// applyRemove retires a priority and leaves a forwarding pointer to where its
// tasks belong, mirroring configVocabulary.applyRemove including its refusal
// to remove the last live priority: a project with no priorities has no
// value a task's priority field could hold, and no default a create could
// fall back to, and there is no later operation that could repair it from the
// outside.
func (priorities *configPriorities) applyRemove(operation ConfigOperation) {
	subject, live := priorities.resolveSubject(operation.Priority)
	if !live || len(priorities.priorities) == 1 {
		return
	}
	destination, resolved := priorities.resolve(operation.PriorityDestination)
	if !resolved || destination == subject {
		return
	}
	// No cycle can be built here, for exactly the reason
	// configVocabulary.applyRemove's comment gives: the destination is live at
	// this moment, a live priority forwards nowhere, and the subject is not
	// the destination, so the chain that now starts at the subject terminates
	// one hop later.
	delete(priorities.priorities, subject)
	priorities.retired[subject] = destination
}

// applyTag gives a priority a role, and transfers the default tag atomically
// the same way configVocabulary.applyTag does, and for the same reason: an
// intermediate state with no default at all is not one a concurrent clone
// should ever be able to fetch and normalize into a choice nobody made.
func (priorities *configPriorities) applyTag(operation ConfigOperation) {
	subject, live := priorities.resolveSubject(operation.Priority)
	if !live {
		return
	}
	if operation.PriorityTag == PriorityTagDefault {
		priorities.clearDefaultExcept(subject)
	}
	entry := priorities.priorities[subject]
	if entry.definition.HasTag(operation.PriorityTag) {
		return
	}
	tags := append(append([]PriorityTag(nil), entry.definition.Tags...), operation.PriorityTag)
	// The tag was validated by the operation document check, so this cannot
	// fail.
	entry.definition.Tags, _ = normalizePriorityTags(tags)
}

func (priorities *configPriorities) applyUntag(operation ConfigOperation) {
	subject, live := priorities.resolveSubject(operation.Priority)
	if !live {
		return
	}
	entry := priorities.priorities[subject]
	tags := make([]PriorityTag, 0, len(entry.definition.Tags))
	for _, existing := range entry.definition.Tags {
		if existing != operation.PriorityTag {
			tags = append(tags, existing)
		}
	}
	entry.definition.Tags = tags
}

// applyRecolor sets or clears a priority's stored color, reusing
// ConfigOperation.Value the same way display.set and display.unset share it:
// an empty value means clear, back to the board's derived ramp. It is folded
// as a plain assignment rather than judged, for the same reason
// configDisplay.apply never judges a color a future build would refuse to
// author — by the time it reaches here somebody has already recorded it, and
// refusing would strand the clone that fetched it rather than the person who
// wrote it.
func (priorities *configPriorities) applyRecolor(operation ConfigOperation) {
	subject, live := priorities.resolveSubject(operation.Priority)
	if !live {
		return
	}
	priorities.priorities[subject].definition.Color = operation.Value
}

func (priorities *configPriorities) clearDefaultExcept(keep Priority) {
	for name, entry := range priorities.priorities {
		if name == keep || !entry.definition.HasTag(PriorityTagDefault) {
			continue
		}
		tags := make([]PriorityTag, 0, len(entry.definition.Tags))
		for _, existing := range entry.definition.Tags {
			if existing != PriorityTagDefault {
				tags = append(tags, existing)
			}
		}
		entry.definition.Tags = tags
	}
}

// normalizeArity repairs the one invariant a fold may break: more than one
// priority tagged default, or none. It is configVocabulary.normalizeArity's
// rule stripped to its "default" case, because a priority carries only that
// one tag — there is no next/done equivalent to keep in step.
//
// Like the vocabulary's repair it picks its subject by position, the
// lowest-ranked priority, so that two clones folding the same history reach
// the same answer without consulting anything outside the section. It does
// not itself report what it changed — a configPriorities has no channel to
// report through during a fold — which is why the repair has to be this
// deterministic: it is what lets a reconcile-time classifier, comparing a
// replay's result against what the local author would have gotten from
// ValidateConfigAuthoring, name exactly what was normalized after the fact.
//
// A configPriorities holding no live priorities has nothing to repair: there
// is no candidate to promote, and document() reports that state as nil, the
// same "configured nothing" a project that has never run a priority
// operation always was.
func (priorities *configPriorities) normalizeArity() {
	live := priorities.sortedPriorities()
	if len(live) == 0 {
		return
	}

	// 1. More than one default: keep the lowest-ranked and clear the rest.
	for _, entry := range live {
		if entry.definition.HasTag(PriorityTagDefault) {
			priorities.clearDefaultExcept(entry.definition.Priority)
			break
		}
	}
	// 2. No default: the lowest-ranked priority, the most urgent, which is
	// where a task with none named most plausibly belongs.
	if priorities.taggedCount(PriorityTagDefault) == 0 {
		priorities.addTag(live[0].definition.Priority, PriorityTagDefault)
	}
}

func (priorities *configPriorities) addTag(priority Priority, tag PriorityTag) {
	priorities.applyTag(ConfigOperation{Type: ConfigPriorityTag, Priority: priority, PriorityTag: tag})
}

func (priorities *configPriorities) taggedCount(tag PriorityTag) int {
	count := 0
	for _, entry := range priorities.priorities {
		if entry.definition.HasTag(tag) {
			count++
		}
	}
	return count
}

// sortedPriorities orders the live priorities by rank and then by name,
// mirroring configVocabulary.sortedStatuses for the same tiebreak reason: two
// priorities that land on the same rank, reachable whenever two clones insert
// concurrently, sort in the same order everywhere.
func (priorities *configPriorities) sortedPriorities() []*configPriorityEntry {
	sorted := make([]*configPriorityEntry, 0, len(priorities.priorities))
	for _, entry := range priorities.priorities {
		sorted = append(sorted, entry)
	}
	sort.SliceStable(sorted, func(left, right int) bool {
		if compare := sorted[left].rank.Cmp(sorted[right].rank); compare != 0 {
			return compare < 0
		}
		return sorted[left].definition.Priority < sorted[right].definition.Priority
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
	if pack.MinReader < 0 {
		return corrupt("configuration operation pack minimum reader generation %d is invalid", pack.MinReader)
	}
	// This is also what stops EncodeDocument from writing a newer pack back out
	// with the members it could not decode silently dropped.
	if pack.RequiresNewerReader() {
		return newerWriterConfig()
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
	setting     bool
	value       bool
	config      bool
	// The seven priority members below are the priority section's
	// counterparts to name/from/to/status/destination/tag/tags above, one per
	// distinctly-typed ConfigOperation field. Label, rank and value are
	// reused as-is: they are already plain strings, and the priority
	// operations that carry a label, a rank or a color mean exactly what the
	// status and display operations above mean by them.
	priorityName        bool
	priorityFrom        bool
	priorityTo          bool
	priority            bool
	priorityDestination bool
	priorityTag         bool
	priorityTags        bool
}

var configOperationShapes = map[ConfigOperationType]configOperationMembers{
	ConfigGenesis:         {config: true},
	ConfigStatusAdd:       {name: true, label: true, rank: true, tags: true},
	ConfigStatusRename:    {from: true, to: true},
	ConfigStatusRelabel:   {status: true, label: true},
	ConfigStatusRemove:    {status: true, destination: true},
	ConfigStatusReorder:   {status: true, rank: true},
	ConfigStatusTag:       {status: true, tag: true},
	ConfigStatusUntag:     {status: true, tag: true},
	ConfigDisplaySet:      {setting: true, value: true},
	ConfigDisplayUnset:    {setting: true},
	ConfigPriorityAdd:     {priorityName: true, label: true, rank: true, priorityTags: true},
	ConfigPriorityRename:  {priorityFrom: true, priorityTo: true},
	ConfigPriorityRelabel: {priority: true, label: true},
	ConfigPriorityRemove:  {priority: true, priorityDestination: true},
	ConfigPriorityReorder: {priority: true, rank: true},
	ConfigPriorityTag:     {priority: true, priorityTag: true},
	ConfigPriorityUntag:   {priority: true, priorityTag: true},
	ConfigPriorityRecolor: {priority: true, value: true},
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
		name:                operation.Name != "",
		from:                operation.From != "",
		to:                  operation.To != "",
		status:              operation.Status != "",
		label:               operation.Label != "",
		rank:                operation.Rank != "",
		tags:                operation.Tags != nil,
		tag:                 operation.Tag != "",
		destination:         operation.Destination != "",
		setting:             operation.Setting != "",
		value:               operation.Value != "",
		config:              operation.Config != nil,
		priorityName:        operation.PriorityName != "",
		priorityFrom:        operation.PriorityFrom != "",
		priorityTo:          operation.PriorityTo != "",
		priority:            operation.Priority != "",
		priorityDestination: operation.PriorityDestination != "",
		priorityTag:         operation.PriorityTag != "",
		priorityTags:        operation.PriorityTags != nil,
	}
	// status.add and priority.add are the two types with an optional member:
	// a status or priority may legitimately carry no tags, and an absent list
	// and an empty one mean the same thing.
	if operation.Type == ConfigStatusAdd {
		present.tags = true
	}
	if operation.Type == ConfigPriorityAdd {
		present.priorityTags = true
	}
	// priority.recolor is the one type whose value is optional in the other
	// direction: an empty Value is not absent, it is the clear instruction,
	// the same set/unset meaning display.unset gives an absent member. See
	// configPriorities.applyRecolor.
	if operation.Type == ConfigPriorityRecolor {
		present.value = true
	}
	if present != shape {
		return corrupt("%s carries the wrong members", operation.Type)
	}

	for _, token := range []Status{operation.Name, operation.From, operation.To, operation.Status, operation.Destination} {
		if token == "" {
			continue
		}
		if err := ValidateStatusToken(token); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" names an invalid status", err)
		}
	}
	for _, token := range []Priority{
		operation.PriorityName, operation.PriorityFrom, operation.PriorityTo,
		operation.Priority, operation.PriorityDestination,
	} {
		if token == "" {
			continue
		}
		if err := ValidatePriorityToken(token); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" names an invalid priority", err)
		}
	}
	if operation.Label != "" {
		// priority.add and priority.relabel are the only priority types that
		// carry a label, so a Priority-domain type is otherwise
		// indistinguishable from a Status-domain one here — the switch is what
		// keeps the two domains' label ceilings independent of each other.
		// Both are 60 bytes today, set separately and by coincidence equal, so
		// this has no observable effect yet beyond the noun in the error
		// message; it matters the day one of the two ceilings moves and the
		// other must not follow it.
		validateLabel := ValidateStatusLabel
		if operation.Type == ConfigPriorityAdd || operation.Type == ConfigPriorityRelabel {
			validateLabel = ValidatePriorityLabel
		}
		if err := validateLabel(operation.Label); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid label", err)
		}
	}
	if operation.Rank != "" {
		if _, err := parseRank(operation.Rank); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid rank", err)
		}
	}
	if operation.Tag != "" {
		if err := ValidateStatusTag(operation.Tag); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid tag", err)
		}
	}
	for _, tag := range operation.Tags {
		if err := ValidateStatusTag(tag); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid tag", err)
		}
	}
	if operation.PriorityTag != "" {
		if err := ValidatePriorityTag(operation.PriorityTag); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid tag", err)
		}
	}
	for _, tag := range operation.PriorityTags {
		if err := ValidatePriorityTag(tag); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" carries an invalid tag", err)
		}
	}
	if operation.Setting != "" {
		if err := ValidateDisplaySetting(operation.Setting); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" names an unknown display setting", err)
		}
	}
	// The stored value has to be the canonical one, not merely an acceptable
	// one. This is the deliberate second half of the double validation the
	// boundary performs: the boundary folds `#ABC123` and trims a name so a
	// person is not refused for typing either, and this refuses a document that
	// recorded the unfolded form, because a checkpoint whose bytes are compared
	// cannot afford two spellings of one configuration.
	if operation.Type == ConfigDisplaySet {
		canonical, err := CanonicalDisplayValue(operation.Setting, operation.Value)
		if err != nil {
			return Wrap(CategoryCorruptData, "display.set carries an invalid value", err)
		}
		if canonical != operation.Value {
			return corrupt("display.set value for %s is not canonical", operation.Setting)
		}
	}
	// priority.recolor's value is a color like display.set's, canonicalized
	// the same way, except that an empty value is the valid "clear" case
	// rather than a member that must be present — see the shape override
	// above.
	if operation.Type == ConfigPriorityRecolor && operation.Value != "" {
		canonical, err := ValidateThemeColor(operation.Value)
		if err != nil {
			return Wrap(CategoryCorruptData, "priority.recolor carries an invalid color", err)
		}
		if canonical != operation.Value {
			return corrupt("priority.recolor color is not stored canonically")
		}
	}
	if operation.Type == ConfigStatusRename && operation.From == operation.To {
		return corrupt("status.rename must name a different status")
	}
	if operation.Type == ConfigStatusRemove && operation.Status == operation.Destination {
		return corrupt("status.remove must name a different destination")
	}
	if operation.Type == ConfigPriorityRename && operation.PriorityFrom == operation.PriorityTo {
		return corrupt("priority.rename must name a different priority")
	}
	if operation.Type == ConfigPriorityRemove && operation.Priority == operation.PriorityDestination {
		return corrupt("priority.remove must name a different destination")
	}
	if operation.Config != nil {
		normalized, err := normalizeVocabularyDocument(operation.Config.Vocabulary)
		if err != nil {
			return Wrap(CategoryCorruptData, "config.genesis carries an invalid vocabulary", err)
		}
		if !reflect.DeepEqual(operation.Config.Vocabulary, normalized) {
			return corrupt("config.genesis configuration is not canonical")
		}
		display, err := normalizeDisplayDocument(operation.Config.Display)
		if err != nil {
			return Wrap(CategoryCorruptData, "config.genesis carries invalid display settings", err)
		}
		if !reflect.DeepEqual(operation.Config.Display, display) {
			return corrupt("config.genesis configuration is not canonical")
		}
		priorities, err := normalizeStoredPriorityDocument(operation.Config.Priorities)
		if err != nil {
			return Wrap(CategoryCorruptData, "config.genesis carries an invalid priority vocabulary", err)
		}
		if !reflect.DeepEqual(operation.Config.Priorities, priorities) {
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
	if state.MinReader < 0 {
		return corrupt("configuration state minimum reader generation %d is invalid", state.MinReader)
	}
	if state.RequiresNewerReader() {
		return newerWriterConfig()
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
	display, err := normalizeDisplayDocument(state.Config.Display)
	if err != nil {
		return Wrap(CategoryCorruptData, "configuration state contains invalid display settings", err)
	}
	if !reflect.DeepEqual(state.Config.Display, display) {
		return corrupt("configuration state is not canonical")
	}
	priorities, err := normalizeStoredPriorityDocument(state.Config.Priorities)
	if err != nil {
		return Wrap(CategoryCorruptData, "configuration state contains an invalid priority vocabulary", err)
	}
	if !reflect.DeepEqual(state.Config.Priorities, priorities) {
		return corrupt("configuration state is not canonical")
	}
	return nil
}
