package core

import (
	"math/big"
	"sort"
	"strings"
	"sync"
)

// StatusTag names the role a status plays in the workflow.
//
// Workbook has exactly three behaviors that used to be hard-coded to a
// particular status name — where a new task lands, what `workbook next` picks
// up, and what counts as a finished dependency — so it has exactly three tags.
// A tag is a role a project assigns, not a property of the name: a project that
// calls its finished column "shipped" tags that one done, and everything that
// asked "is this task done?" keeps working without knowing the name.
type StatusTag string

const (
	// StatusTagDefault marks the status a new task is created in. Exactly one
	// status carries it, enforced by construction rather than by validation:
	// applying it to one status clears it from every other in the same
	// operation, so there is no window in which two carry it.
	StatusTagDefault StatusTag = "default"
	// StatusTagNext marks a status whose tasks are eligible for `workbook
	// next`. More than one may carry it.
	StatusTagNext StatusTag = "next"
	// StatusTagDone marks a status that satisfies a dependency. More than one
	// may carry it, which is what lets a project distinguish "shipped" from
	// "cancelled" and have both close out the work that waited on them.
	StatusTagDone StatusTag = "done"
)

// statusTags lists every tag in the canonical order a document stores them.
// The order is alphabetical, which is arbitrary but fixed: canonical bytes need
// one answer, not a good one.
var statusTags = [...]StatusTag{StatusTagDefault, StatusTagDone, StatusTagNext}

func validateStatusTag(tag StatusTag) error {
	for _, known := range statusTags {
		if tag == known {
			return nil
		}
	}
	return Errorf(CategoryValidation, "unsupported status tag %q", tag)
}

// StatusDefinition is one status: its token, its display label, its position,
// and its roles.
//
// It is both the in-memory shape the presentation layers read and the record
// the configuration ledger stores, deliberately. Two shapes would need a
// conversion, and a conversion is where a field quietly stops round-tripping.
type StatusDefinition struct {
	Status Status `json:"status"`
	Label  string `json:"label"`
	// Rank orders the status among its peers. It is the same reduced-rational
	// string a task rank uses, and for the same reason: two clones must be able
	// to insert a status between the same two neighbours without coordinating,
	// and a rational always has room between any two values where an integer
	// index does not.
	Rank string `json:"rank"`
	// Tags are sorted and deduplicated in a canonical document.
	Tags []StatusTag `json:"tags"`
}

// HasTag reports whether the definition carries a tag.
func (definition StatusDefinition) HasTag(tag StatusTag) bool {
	for _, candidate := range definition.Tags {
		if candidate == tag {
			return true
		}
	}
	return false
}

// StatusAlias forwards a status name that a rename retired to the name that
// replaced it. It is not bookkeeping: a clone that has not fetched the rename
// keeps writing the old name, and this is what lets the clone that has fetched
// it read those tasks into the right column instead of stranding them.
type StatusAlias struct {
	From Status `json:"from"`
	To   Status `json:"to"`
}

// RetiredStatus forwards a removed status to the status its tasks belong in.
// A removal never rewrites stored task documents — history is append-only — so
// the forwarding pointer is the whole mechanism.
type RetiredStatus struct {
	Status      Status `json:"status"`
	Destination Status `json:"destination"`
}

// Vocabulary is a project's resolved status configuration: the live statuses in
// their configured order, plus the forwarding chains that map a stored status
// no longer live onto one that is.
//
// Its fields are unexported so that a value in hand is always one that came
// through NewVocabulary or a decoded configuration checkpoint, which is what
// lets every accessor be total. The zero value is the empty vocabulary and is
// meaningful: Service reads it as "this caller did not configure one" and
// substitutes the built-in default, so every construction that predates
// per-project statuses keeps its behavior without being edited.
type Vocabulary struct {
	definitions []StatusDefinition
	aliases     []StatusAlias
	retired     []RetiredStatus

	byStatus map[Status]int
	forward  map[Status]Status
}

// NewVocabulary builds a vocabulary from a status set and its forwarding
// chains. It validates shape — tokens, labels, uniqueness, rank syntax, and the
// absence of a forwarding cycle — but neither arity nor the size ceilings: both
// are states the fold can reach from a peer's operations, and refusing to
// represent one here would only move the failure somewhere it cannot be
// reported. Validate covers arity, and ValidateConfigAuthoring covers the
// ceilings, both before a pack is written rather than while one is folded.
func NewVocabulary(definitions []StatusDefinition, aliases []StatusAlias, retired []RetiredStatus) (Vocabulary, error) {
	normalized, err := normalizeVocabularyDocument(VocabularyDocument{
		Statuses: definitions,
		Aliases:  aliases,
		Retired:  retired,
	})
	if err != nil {
		return Vocabulary{}, err
	}
	return newVocabularyFromCanonical(normalized), nil
}

// newVocabularyFromCanonical indexes an already-normalized document. It cannot
// fail, which is why decoding a checkpoint and reading its vocabulary are two
// steps rather than one fallible one.
func newVocabularyFromCanonical(document VocabularyDocument) Vocabulary {
	vocabulary := Vocabulary{
		definitions: document.Statuses,
		aliases:     document.Aliases,
		retired:     document.Retired,
		byStatus:    make(map[Status]int, len(document.Statuses)),
		forward:     make(map[Status]Status, len(document.Aliases)+len(document.Retired)),
	}
	for index, definition := range document.Statuses {
		vocabulary.byStatus[definition.Status] = index
	}
	for _, alias := range document.Aliases {
		vocabulary.forward[alias.From] = alias.To
	}
	for _, entry := range document.Retired {
		vocabulary.forward[entry.Status] = entry.Destination
	}
	return vocabulary
}

// IsZero reports whether this is the empty vocabulary, which is how a caller
// that never configured one is distinguished from one that configured a
// project down to a single status.
func (vocabulary Vocabulary) IsZero() bool {
	return len(vocabulary.definitions) == 0 && len(vocabulary.aliases) == 0 && len(vocabulary.retired) == 0
}

// Definitions returns the live statuses in configured order. The slice is a
// copy: callers hand it to templates and sort it.
func (vocabulary Vocabulary) Definitions() []StatusDefinition {
	definitions := make([]StatusDefinition, len(vocabulary.definitions))
	for index, definition := range vocabulary.definitions {
		definition.Tags = copyStatusTags(definition.Tags)
		definitions[index] = definition
	}
	return definitions
}

// copyStatusTags copies a tag list, keeping an empty list empty rather than
// letting it become nil. The distinction is durable: a canonical document
// encodes "tags":[], and an appended nil would encode "tags":null and fail the
// checkpoint's byte comparison.
func copyStatusTags(tags []StatusTag) []StatusTag {
	copied := make([]StatusTag, len(tags))
	copy(copied, tags)
	return copied
}

// Document returns the vocabulary in the canonical shape a configuration
// checkpoint stores.
// Every member comes back as a non-nil slice, empty where there is nothing to
// report. The distinction is durable — an empty list encodes as [] and a nil
// one as null — and a document that differs from its own normalization by a
// null fails the checkpoint comparison.
func (vocabulary Vocabulary) Document() VocabularyDocument {
	aliases := make([]StatusAlias, len(vocabulary.aliases))
	copy(aliases, vocabulary.aliases)
	retired := make([]RetiredStatus, len(vocabulary.retired))
	copy(retired, vocabulary.retired)
	return VocabularyDocument{
		Statuses: vocabulary.Definitions(),
		Aliases:  aliases,
		Retired:  retired,
	}
}

// Has reports whether a status is live in this vocabulary.
func (vocabulary Vocabulary) Has(status Status) bool {
	_, exists := vocabulary.byStatus[status]
	return exists
}

// Order returns a status's position for sorting. An unknown status sorts after
// every live one rather than failing, which is what keeps a board readable
// while a rename is still propagating.
func (vocabulary Vocabulary) Order(status Status) int {
	if index, exists := vocabulary.byStatus[status]; exists {
		return index
	}
	return len(vocabulary.definitions)
}

// Default returns the status a new task is created in. It returns the empty
// status only for an empty vocabulary; a vocabulary that reached the fold
// without a default has one supplied by normalization.
func (vocabulary Vocabulary) Default() Status {
	for _, definition := range vocabulary.definitions {
		if definition.HasTag(StatusTagDefault) {
			return definition.Status
		}
	}
	return ""
}

// IsNext reports whether tasks in a status are eligible for `workbook next`.
func (vocabulary Vocabulary) IsNext(status Status) bool {
	return vocabulary.hasTag(status, StatusTagNext)
}

// IsDone reports whether a status satisfies a dependency.
func (vocabulary Vocabulary) IsDone(status Status) bool {
	return vocabulary.hasTag(status, StatusTagDone)
}

func (vocabulary Vocabulary) hasTag(status Status, tag StatusTag) bool {
	index, exists := vocabulary.byStatus[status]
	if !exists {
		return false
	}
	return vocabulary.definitions[index].HasTag(tag)
}

// Label returns a status's display label, and the raw value for a status this
// vocabulary does not define, so a column never hides a status another clone
// recorded.
func (vocabulary Vocabulary) Label(status Status) string {
	if index, exists := vocabulary.byStatus[status]; exists {
		return vocabulary.definitions[index].Label
	}
	return string(status)
}

// Resolve follows a stored status through the rename and retirement chains to
// the live status it now means, reporting whether the walk terminated at one.
//
// It is transitive: A renamed to B and B later retired into C resolves A to C
// in one call, because a clone that was offline across both changes stored A
// and must still land in a real column. It is cycle-safe by bounding the walk
// and remembering where it has been, even though ApplyConfig refuses to build a
// cycle — a checkpoint is data read from a ref, and a total function on data
// from a ref is worth more than an invariant nobody can check at read time.
//
// A status that is already live resolves to itself. A status with no forwarding
// entry resolves to itself with ok false, which is the ordinary state of a
// status written by a newer build.
func (vocabulary Vocabulary) Resolve(status Status) (Status, bool) {
	if vocabulary.Has(status) {
		return status, true
	}
	seen := make(map[Status]struct{}, len(vocabulary.forward))
	current := status
	for range len(vocabulary.forward) + 1 {
		next, forwarded := vocabulary.forward[current]
		if !forwarded {
			return status, false
		}
		if _, repeated := seen[next]; repeated {
			return status, false
		}
		seen[next] = struct{}{}
		if vocabulary.Has(next) {
			return next, true
		}
		current = next
	}
	return status, false
}

// Validate reports the arity violations that make a vocabulary unusable.
//
// This is the authoring gate, not the read gate. ApplyConfig never calls it:
// a pack that arrives from a peer has already happened, and refusing to fold it
// would strand the clone rather than the mistake. A command that is about to
// write a pack calls it, so the person who can still choose differently is the
// one who hears about it — which is why every message names the command that
// fixes the state it describes.
func (vocabulary Vocabulary) Validate() error {
	if len(vocabulary.definitions) == 0 {
		return Errorf(
			CategoryValidation,
			"the project has no statuses; add one first: workbook status add <status> --label <label>",
		)
	}

	var defaults, next, done []string
	for _, definition := range vocabulary.definitions {
		if definition.HasTag(StatusTagDefault) {
			defaults = append(defaults, string(definition.Status))
		}
		if definition.HasTag(StatusTagNext) {
			next = append(next, string(definition.Status))
		}
		if definition.HasTag(StatusTagDone) {
			done = append(done, string(definition.Status))
		}
	}

	switch {
	case len(defaults) == 0:
		return Errorf(
			CategoryValidation,
			"no status is tagged default, so a new task would have nowhere to land; "+
				"tag another status first: workbook status tag <status> --tag default",
		)
	case len(defaults) > 1:
		return Errorf(
			CategoryValidation,
			"statuses %s are all tagged default, but exactly one may be; "+
				"move the tag instead: workbook status tag <status> --tag default",
			strings.Join(defaults, ", "),
		)
	}
	if len(next) == 0 {
		return Errorf(
			CategoryValidation,
			"no status is tagged next, so `workbook next` would never return a task; "+
				"tag another status first: workbook status tag <status> --tag next",
		)
	}
	if len(done) == 0 {
		return Errorf(
			CategoryValidation,
			"no status is tagged done, so no dependency could ever be satisfied; "+
				"tag another status first: workbook status tag <status> --tag done",
		)
	}
	return nil
}

// validateVocabularyGrowth refuses a pack that would push a project past a
// ceiling, and only when the pack is what pushes it there.
//
// The comparison is against the parent rather than against the ceiling alone,
// and that is the whole design. A folded state may already sit over a ceiling —
// two clones adding a status concurrently is enough — and a rule that refused
// every pack while over one would refuse the removals that bring the project
// back under, which is the only way out. So growth is refused and shrinkage is
// always allowed.
//
// Every message names what to do about it. The alias and retirement ceilings
// cannot name a command, because nothing drops a forwarding pointer yet: they
// stand in for a compaction pass, and the message says so rather than sending
// somebody looking for a flag that does not exist.
func validateVocabularyGrowth(before, after VocabularyDocument) error {
	if len(after.Statuses) > MaxStatusCount && len(after.Statuses) > len(before.Statuses) {
		return Errorf(
			CategoryValidation,
			"the project would define %d statuses and must not exceed %d; "+
				"remove one first: workbook status remove <status> --into <status>",
			len(after.Statuses), MaxStatusCount,
		)
	}
	if len(after.Aliases) > MaxStatusAliasCount && len(after.Aliases) > len(before.Aliases) {
		return Errorf(
			CategoryValidation,
			"the project has recorded %d status renames and must not exceed %d; "+
				"nothing can drop a rename yet, because a clone that has not fetched it "+
				"still needs it to read tasks stored under the old name",
			len(after.Aliases), MaxStatusAliasCount,
		)
	}
	if len(after.Retired) > MaxStatusRetiredCount && len(after.Retired) > len(before.Retired) {
		return Errorf(
			CategoryValidation,
			"the project has recorded %d status removals and must not exceed %d; "+
				"nothing can drop a removal yet, because a clone that has not fetched it "+
				"still needs it to read tasks stored under the removed name",
			len(after.Retired), MaxStatusRetiredCount,
		)
	}
	return nil
}

// builtInStatusDefinitions is the six-status workflow Workbook has shipped
// since its first release, in its stored form.
//
// The ranks are the integers 1 through 6 rather than an implied array index
// because rank is what a reorder edits, and a vocabulary whose ranks only
// existed once somebody customized it would make the first reorder a migration.
func builtInStatusDefinitions() []StatusDefinition {
	return []StatusDefinition{
		{Status: StatusBacklog, Label: "Backlog", Rank: "1/1", Tags: []StatusTag{StatusTagDefault}},
		{Status: StatusReady, Label: "Ready", Rank: "2/1", Tags: []StatusTag{StatusTagNext}},
		{Status: StatusBlocked, Label: "Blocked", Rank: "3/1", Tags: []StatusTag{}},
		{Status: StatusInProgress, Label: "In Progress", Rank: "4/1", Tags: []StatusTag{}},
		{Status: StatusInReview, Label: "In Review", Rank: "5/1", Tags: []StatusTag{}},
		{Status: StatusDone, Label: "Done", Rank: "6/1", Tags: []StatusTag{StatusTagDone}},
	}
}

// DefaultVocabulary is the vocabulary a project is minted with.
//
// It is the vocabulary a `workbook setup` writes into a new project's
// configuration genesis, and the fallback a Service uses when no vocabulary was
// configured.
//
// It is built once and shared. A Vocabulary is read-only after construction —
// Definitions and Document hand out copies, and nothing else writes — and
// Service reaches for this on every projected task, so rebuilding it per task
// would put six allocations and two maps on the hot path of every list.
var DefaultVocabulary = sync.OnceValue(func() Vocabulary {
	return mustVocabulary(builtInStatusDefinitions())
})

// LegacyVocabulary is the vocabulary a project that predates the configuration
// ledger is assumed to have been using.
//
// It is a separate accessor from DefaultVocabulary on purpose, and today they
// return the same six statuses — a test pins that, so the day they diverge is a
// deliberate edit rather than a surprise. They must diverge eventually: the
// built-in default is a product decision that will change between releases,
// while what an existing project was already using is a fact about that project
// and may not change under it. Seeding a pre-ledger project's genesis reads
// this one; minting a new project reads DefaultVocabulary.
var LegacyVocabulary = sync.OnceValue(func() Vocabulary {
	return mustVocabulary(builtInStatusDefinitions())
})

// mustVocabulary panics on a malformed built-in set, which is a programming
// error in this file and cannot be caused by any input.
func mustVocabulary(definitions []StatusDefinition) Vocabulary {
	vocabulary, err := NewVocabulary(definitions, nil, nil)
	if err != nil {
		panic("core: built-in vocabulary is invalid: " + err.Error())
	}
	return vocabulary
}

// normalizeVocabularyDocument validates a vocabulary document and returns it in
// canonical form: statuses ordered by rank then name, aliases by source,
// retirements by source, tags in their fixed order, and every empty collection
// an empty slice rather than a null.
//
// Sorting here rather than at encode time is what makes the checkpoint's bytes
// a property of the configuration instead of a property of whoever wrote it.
//
// It checks shape and never counts. MaxStatusCount used to be enforced here,
// and that was a latent way to brick a repository: this function runs inside
// ApplyConfig, so a project one status below the ceiling where two clones each
// add one concurrently would produce a pack that folds on neither clone —
// permanently, because history is append-only and the removal that would fix it
// can never be reached by a fold that fails first. Both authors would have been
// refused nothing; neither did anything wrong. The ceilings are therefore an
// authoring rule only, checked by validateVocabularyGrowth from
// ValidateConfigAuthoring, and a folded state is allowed to sit over one.
//
// Surfacing that condition is a later change: PR-B reports it through
// historyvalidation's advisories, and PR-C's status list says so where a person
// can act on it. Any read-time ceiling added afterwards must bound resource use
// — how much this process is willing to allocate for a document — and must
// never decide whether a checkpoint computes, which is the mistake this comment
// records.
func normalizeVocabularyDocument(document VocabularyDocument) (VocabularyDocument, error) {
	statuses := make([]StatusDefinition, 0, len(document.Statuses))
	ranks := make(map[Status]*big.Rat, len(document.Statuses))
	seen := make(map[Status]struct{}, len(document.Statuses))
	for _, definition := range document.Statuses {
		if err := validateStatusToken(definition.Status); err != nil {
			return VocabularyDocument{}, err
		}
		if err := validateStatusLabel(definition.Label); err != nil {
			return VocabularyDocument{}, err
		}
		rank, err := parseRank(definition.Rank)
		if err != nil {
			return VocabularyDocument{}, Wrap(CategoryValidation, "status rank is invalid", err)
		}
		if _, duplicate := seen[definition.Status]; duplicate {
			return VocabularyDocument{}, Errorf(CategoryValidation, "status %q is defined twice", definition.Status)
		}
		seen[definition.Status] = struct{}{}
		ranks[definition.Status] = rank

		tags, err := normalizeStatusTags(definition.Tags)
		if err != nil {
			return VocabularyDocument{}, err
		}
		definition.Tags = tags
		statuses = append(statuses, definition)
	}
	sort.SliceStable(statuses, func(left, right int) bool {
		if compare := ranks[statuses[left].Status].Cmp(ranks[statuses[right].Status]); compare != 0 {
			return compare < 0
		}
		return statuses[left].Status < statuses[right].Status
	})

	aliases, err := normalizeStatusAliases(document.Aliases)
	if err != nil {
		return VocabularyDocument{}, err
	}
	retired, err := normalizeRetiredStatuses(document.Retired)
	if err != nil {
		return VocabularyDocument{}, err
	}
	forward := make(map[Status]Status, len(aliases)+len(retired))
	for _, alias := range aliases {
		if _, duplicate := forward[alias.From]; duplicate {
			return VocabularyDocument{}, Errorf(CategoryValidation, "status %q is forwarded twice", alias.From)
		}
		forward[alias.From] = alias.To
	}
	for _, entry := range retired {
		if _, duplicate := forward[entry.Status]; duplicate {
			return VocabularyDocument{}, Errorf(CategoryValidation, "status %q is forwarded twice", entry.Status)
		}
		forward[entry.Status] = entry.Destination
	}
	for source := range forward {
		if _, live := seen[source]; live {
			return VocabularyDocument{}, Errorf(
				CategoryValidation,
				"status %q is both live and forwarded elsewhere",
				source,
			)
		}
		if err := forwardTerminates(forward, source); err != nil {
			return VocabularyDocument{}, err
		}
	}

	return VocabularyDocument{Statuses: statuses, Aliases: aliases, Retired: retired}, nil
}

// forwardTerminates rejects a forwarding cycle. ApplyConfig cannot build one —
// every chain it extends ends at a live status, and a live status forwards
// nowhere — so reaching this is a hand-edited or corrupted checkpoint, which is
// exactly what a decoder is for.
func forwardTerminates(forward map[Status]Status, source Status) error {
	seen := map[Status]struct{}{source: {}}
	current := source
	for range len(forward) + 1 {
		next, forwarded := forward[current]
		if !forwarded {
			return nil
		}
		if _, repeated := seen[next]; repeated {
			return Errorf(CategoryValidation, "status %q forwards to itself through a cycle", source)
		}
		seen[next] = struct{}{}
		current = next
	}
	return Errorf(CategoryValidation, "status %q forwards to itself through a cycle", source)
}

func normalizeStatusTags(tags []StatusTag) ([]StatusTag, error) {
	present := make(map[StatusTag]struct{}, len(tags))
	for _, tag := range tags {
		if err := validateStatusTag(tag); err != nil {
			return nil, err
		}
		present[tag] = struct{}{}
	}
	normalized := make([]StatusTag, 0, len(present))
	for _, tag := range statusTags {
		if _, tagged := present[tag]; tagged {
			normalized = append(normalized, tag)
		}
	}
	return normalized, nil
}

// MaxStatusAliasCount is not checked here, for the reason
// normalizeVocabularyDocument records: a count enforced inside the fold can
// make a legitimate concurrent pair unfoldable forever. It is an authoring
// ceiling, in validateVocabularyGrowth.
func normalizeStatusAliases(aliases []StatusAlias) ([]StatusAlias, error) {
	normalized := make([]StatusAlias, 0, len(aliases))
	for _, alias := range aliases {
		if err := validateStatusToken(alias.From); err != nil {
			return nil, err
		}
		if err := validateStatusToken(alias.To); err != nil {
			return nil, err
		}
		if alias.From == alias.To {
			return nil, Errorf(CategoryValidation, "status %q cannot alias itself", alias.From)
		}
		normalized = append(normalized, alias)
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		return normalized[left].From < normalized[right].From
	})
	return normalized, nil
}

// MaxStatusRetiredCount is not checked here either; see normalizeStatusAliases.
func normalizeRetiredStatuses(retired []RetiredStatus) ([]RetiredStatus, error) {
	normalized := make([]RetiredStatus, 0, len(retired))
	for _, entry := range retired {
		if err := validateStatusToken(entry.Status); err != nil {
			return nil, err
		}
		if err := validateStatusToken(entry.Destination); err != nil {
			return nil, err
		}
		if entry.Status == entry.Destination {
			return nil, Errorf(CategoryValidation, "status %q cannot retire into itself", entry.Status)
		}
		normalized = append(normalized, entry)
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		return normalized[left].Status < normalized[right].Status
	})
	return normalized, nil
}
