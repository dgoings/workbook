package core

import (
	"math/big"
	"sort"
	"strings"
	"sync"
)

// PriorityTag marks a priority's role. There is one today: where a task lands
// when nobody names a priority. It is a tag rather than a boolean so it shares
// the normalization statuses use, and so a second role can be added without
// changing the durable shape.
type PriorityTag string

const PriorityTagDefault PriorityTag = "default"

// priorityTags lists every tag in the canonical order a document stores them.
// The order is alphabetical, mirroring statusTags for the same reason:
// canonical bytes need one answer, not a good one. There is exactly one tag
// today, so the order only starts to matter once a second one exists.
var priorityTags = [...]PriorityTag{PriorityTagDefault}

// PriorityTags returns every tag a priority may carry, in the canonical order
// a document stores them. It exists so that a command refusing an unknown tag
// can list the ones that exist without keeping a second copy of the set.
func PriorityTags() []PriorityTag {
	tags := make([]PriorityTag, len(priorityTags))
	copy(tags, priorityTags[:])
	return tags
}

// ValidatePriorityTag reports whether a tag is one of the roles a project may
// assign to a priority. It is exported for the same reason ValidateStatusTag
// is: a word somebody typed has to be refused as a typo before it becomes a
// member of a durable operation, where the same check reports corrupt data.
func ValidatePriorityTag(tag PriorityTag) error {
	for _, known := range priorityTags {
		if tag == known {
			return nil
		}
	}
	return Errorf(CategoryValidation, "unsupported priority tag %q", tag)
}

// PriorityDefinition is one priority as the ledger stores it.
type PriorityDefinition struct {
	Priority Priority `json:"priority"`
	Label    string   `json:"label"`
	// Rank orders the priority among its peers, most urgent first. It is the
	// same reduced-rational string a status rank uses, and for the same reason:
	// two clones must be able to insert a priority between the same two
	// neighbours without coordinating.
	Rank string        `json:"rank"`
	Tags []PriorityTag `json:"tags"`
	// Color is the board's ink for this priority, `#rrggbb`. Unset means the
	// board derives one from the priority's position, which is why it is
	// omitempty rather than a stored default: nothing stores a default.
	Color string `json:"color,omitempty"`
}

// HasTag reports whether the definition carries a tag.
func (definition PriorityDefinition) HasTag(tag PriorityTag) bool {
	for _, candidate := range definition.Tags {
		if candidate == tag {
			return true
		}
	}
	return false
}

func (definition PriorityDefinition) key() Priority { return definition.Priority }
func (definition PriorityDefinition) rank() string  { return definition.Rank }

// PriorityAlias forwards a priority name a rename retired to the name that
// replaced it. A clone that has not fetched the rename keeps writing the old
// name; this is what lets the clone that has fetched it read those tasks into
// the right group instead of stranding them.
type PriorityAlias struct {
	From Priority `json:"from"`
	To   Priority `json:"to"`
}

// RetiredPriority forwards a removed priority to the one its tasks belong in.
// A removal never rewrites stored task documents.
type RetiredPriority struct {
	Priority    Priority `json:"priority"`
	Destination Priority `json:"destination"`
}

// PriorityDocument is the stored form of a priority vocabulary. Every member is
// a sorted array for the reason VocabularyDocument's are: the checkpoint's
// bytes are compared for equality, so the sort has to be something this package
// decides rather than a property of the encoder.
type PriorityDocument struct {
	// Priorities are the live priorities, ordered by rank and then by name.
	Priorities []PriorityDefinition `json:"priorities"`
	// Aliases are rename forwardings, ordered by source.
	Aliases []PriorityAlias `json:"aliases"`
	// Retired are removal forwardings, ordered by source.
	Retired []RetiredPriority `json:"retired"`
}

// PriorityVocabulary is a project's resolved priority configuration: the live
// priorities in their configured order, plus the forwarding chains that map a
// stored priority no longer live onto one that is.
//
// Its fields are unexported so that a value in hand is always one that came
// through NewPriorityVocabulary or a decoded configuration checkpoint, which is
// what lets every accessor be total. The zero value is the empty vocabulary,
// and unlike Vocabulary — whose zero value a caller such as Service reads as
// "unconfigured" and substitutes for — every accessor here does the
// substitution itself: there is no other layer yet that resolves a project's
// priority configuration the way Service resolves its status configuration.
type PriorityVocabulary struct {
	definitions []PriorityDefinition
	aliases     []PriorityAlias
	retired     []RetiredPriority

	byPriority map[Priority]int
	forward    map[Priority]Priority
}

// NewPriorityVocabulary builds a vocabulary from a priority set and its
// forwarding chains, mirroring NewVocabulary: it validates shape — token and
// label well-formedness, rank syntax, tag membership, uniqueness, and the
// absence of a forwarding cycle — but neither arity nor a size ceiling, both
// being states a fold can reach from a peer's operations that this must
// still be able to represent. Validate covers arity, and
// ValidateConfigAuthoring covers the ceiling via validatePriorityGrowth, the
// same split NewVocabulary's own comment describes for statuses.
func NewPriorityVocabulary(definitions []PriorityDefinition, aliases []PriorityAlias, retired []RetiredPriority) (PriorityVocabulary, error) {
	normalized, err := normalizePriorityDocument(PriorityDocument{
		Priorities: definitions,
		Aliases:    aliases,
		Retired:    retired,
	})
	if err != nil {
		return PriorityVocabulary{}, err
	}
	return newPriorityVocabularyFromCanonical(normalized), nil
}

// newPriorityVocabularyFromCanonical indexes an already-normalized document.
// It cannot fail, which is why decoding a checkpoint and reading its
// vocabulary are two steps rather than one fallible one.
func newPriorityVocabularyFromCanonical(document PriorityDocument) PriorityVocabulary {
	vocabulary := PriorityVocabulary{
		definitions: document.Priorities,
		aliases:     document.Aliases,
		retired:     document.Retired,
		byPriority:  make(map[Priority]int, len(document.Priorities)),
		forward:     make(map[Priority]Priority, len(document.Aliases)+len(document.Retired)),
	}
	for index, definition := range document.Priorities {
		vocabulary.byPriority[definition.Priority] = index
	}
	for _, alias := range document.Aliases {
		vocabulary.forward[alias.From] = alias.To
	}
	for _, entry := range document.Retired {
		vocabulary.forward[entry.Priority] = entry.Destination
	}
	return vocabulary
}

// IsZero reports whether this is the empty vocabulary, which is how a caller
// that never configured one is distinguished from one that configured a
// project down to a single priority.
func (vocabulary PriorityVocabulary) IsZero() bool {
	return len(vocabulary.definitions) == 0 && len(vocabulary.aliases) == 0 && len(vocabulary.retired) == 0
}

// BuiltInPriorityVocabulary is the priority vocabulary every project is using
// until something changes one: today's three, medium carrying the default
// tag the service used to hardcode.
//
// Unlike statuses, there is no DefaultVocabulary/LegacyVocabulary split here.
// A freshly minted project and one that predates the configuration ledger
// entirely are both, today, using the same built-in three — nothing has ever
// shipped a different starting set the way `blocked` once diverged from
// legacyStatusDefinitions. Should the built-ins ever need to diverge the way
// the status ones did, that is the day this splits into two, mirroring
// vocabulary.go's pair.
//
// It is exported so gitstore can record it durably in every genesis it
// writes — seedConfigLedger and MintConfigLedger both call this at project
// creation, the same moment DefaultVocabulary and LegacyVocabulary are
// recorded — so a genesis's priorities section states a fact about the
// project from the start rather than being synthesized later from an absence.
// That every genesis now carries the section, and so carries the
// compatibility marker ConfigPackMinReader stamps for it, is an accepted
// cost, not an oversight: see that function's comment for why firing on
// presence is the safe reading. It is cached behind sync.OnceValue the way
// DefaultVocabulary is, rather than rebuilt — with its two maps — on every
// accessor call a rendering path makes per task.
var BuiltInPriorityVocabulary = sync.OnceValue(func() PriorityVocabulary {
	return newPriorityVocabularyFromCanonical(PriorityDocument{Priorities: builtInPriorityDefinitions()})
})

// effective is the vocabulary every accessor but Validate actually reads: the
// receiver, unless it is the zero value, in which case the built-in set — the
// substitution the doc comment on PriorityVocabulary describes.
func (vocabulary PriorityVocabulary) effective() PriorityVocabulary {
	if vocabulary.IsZero() {
		return BuiltInPriorityVocabulary()
	}
	return vocabulary
}

// Definitions returns the live priorities in configured order. The slice is a
// copy: callers hand it to templates and sort it.
func (vocabulary PriorityVocabulary) Definitions() []PriorityDefinition {
	vocabulary = vocabulary.effective()
	definitions := make([]PriorityDefinition, len(vocabulary.definitions))
	for index, definition := range vocabulary.definitions {
		definition.Tags = copyPriorityTags(definition.Tags)
		definitions[index] = definition
	}
	return definitions
}

// copyPriorityTags copies a tag list, keeping an empty list empty rather than
// letting it become nil. The distinction is durable: a canonical document
// encodes "tags":[], and an appended nil would encode "tags":null and fail the
// checkpoint's byte comparison.
func copyPriorityTags(tags []PriorityTag) []PriorityTag {
	copied := make([]PriorityTag, len(tags))
	copy(copied, tags)
	return copied
}

// Document returns the vocabulary in the canonical shape a configuration
// checkpoint stores, exactly as this value holds it. Every member comes back
// as a non-nil slice, empty where there is nothing to report, for the reason
// Vocabulary.Document's does — and, like Vocabulary.Document, that is the
// whole of it: the zero value documents as the empty document, with no
// substitution.
//
// This is deliberately the one accessor here, besides Validate, that does not
// read the zero value as the built-in three: it is the one whose result is
// meant to be stored. ConfigData.Priorities stays nil for an unconfigured
// project, and populating it from a substituted document would write the
// built-in three into the ledger as if a project had chosen them — an
// irreversible change for every clone that later folds the pack, and the
// exact mistake this split exists to make unrepresentable rather than merely
// documented: a caller who wants "what does this project actually have on
// file" now gets it by construction, without first having to check IsZero and
// remember why. A caller who instead wants the substituted reading — what a
// board renders, what a command describes, anything that is not headed for a
// checkpoint — wants EffectiveDocument.
func (vocabulary PriorityVocabulary) Document() PriorityDocument {
	definitions := make([]PriorityDefinition, len(vocabulary.definitions))
	for index, definition := range vocabulary.definitions {
		definition.Tags = copyPriorityTags(definition.Tags)
		definitions[index] = definition
	}
	aliases := make([]PriorityAlias, len(vocabulary.aliases))
	copy(aliases, vocabulary.aliases)
	retired := make([]RetiredPriority, len(vocabulary.retired))
	copy(retired, vocabulary.retired)
	return PriorityDocument{
		Priorities: definitions,
		Aliases:    aliases,
		Retired:    retired,
	}
}

// EffectiveDocument returns the vocabulary in the same canonical shape
// Document does, but read the way every other accessor here reads: the zero
// value substituted for the built-in three. It is Document's reading
// counterpart, for a caller that wants "what priorities does this project
// effectively have" — rendering a board, describing a project, comparing
// against another project's effective set — and must never be used to
// populate ConfigData.Priorities; see Document's comment for why.
func (vocabulary PriorityVocabulary) EffectiveDocument() PriorityDocument {
	return vocabulary.effective().Document()
}

// Has reports whether a priority is live in this vocabulary.
func (vocabulary PriorityVocabulary) Has(priority Priority) bool {
	vocabulary = vocabulary.effective()
	_, exists := vocabulary.byPriority[priority]
	return exists
}

// Order returns a priority's position for sorting, lower meaning more urgent.
// An unknown priority sorts after every live one rather than failing, which is
// what keeps a board readable for a priority no chain reaches. Callers read a
// task's priority through Project first, which resolves a renamed or retired
// token to the live priority it now means, so this arm is reached only by a
// priority genuinely stranded — one this vocabulary neither defines nor
// forwards — and not, as it once was, by every task still stored under a name
// a rename replaced.
func (vocabulary PriorityVocabulary) Order(priority Priority) int {
	vocabulary = vocabulary.effective()
	if index, exists := vocabulary.byPriority[priority]; exists {
		return index
	}
	return len(vocabulary.definitions)
}

// Default returns the priority a task with none named is given.
func (vocabulary PriorityVocabulary) Default() Priority {
	vocabulary = vocabulary.effective()
	for _, definition := range vocabulary.definitions {
		if definition.HasTag(PriorityTagDefault) {
			return definition.Priority
		}
	}
	return ""
}

// Label returns a priority's display label, and the raw value for a priority
// this vocabulary does not define, so a board never hides a priority another
// clone recorded.
func (vocabulary PriorityVocabulary) Label(priority Priority) string {
	vocabulary = vocabulary.effective()
	if index, exists := vocabulary.byPriority[priority]; exists {
		return vocabulary.definitions[index].Label
	}
	return string(priority)
}

// Color returns a priority's stored ink, or the empty string when it is
// unknown or has none stored — the same "nothing stores a default" reading
// PriorityDefinition.Color's doc comment describes.
func (vocabulary PriorityVocabulary) Color(priority Priority) string {
	vocabulary = vocabulary.effective()
	if index, exists := vocabulary.byPriority[priority]; exists {
		return vocabulary.definitions[index].Color
	}
	return ""
}

// Resolve follows a stored priority through the rename and retirement chains
// to the live priority it now means, reporting whether the walk terminated at
// one. It is transitive and cycle-safe for the same reasons Vocabulary.Resolve
// is.
func (vocabulary PriorityVocabulary) Resolve(priority Priority) (Priority, bool) {
	vocabulary = vocabulary.effective()
	return resolveForward(vocabulary.forward, vocabulary.Has, priority)
}

// AppendRank returns the rank a priority added after every existing one
// takes. It is nextRank's rule for priorities, the same rule
// Vocabulary.AppendRank applies for statuses.
func (vocabulary PriorityVocabulary) AppendRank() string {
	vocabulary = vocabulary.effective()
	return appendRank(rankedPriorities(vocabulary.definitions))
}

// InsertRank returns the rank that places a priority immediately before or
// after an anchor, leaving every other priority where it is. It is
// movedRank's rule for priorities, the same rule Vocabulary.InsertRank applies
// for statuses.
func (vocabulary PriorityVocabulary) InsertRank(moved, anchor Priority, before bool) (string, error) {
	vocabulary = vocabulary.effective()
	return insertRank(rankedPriorities(vocabulary.definitions), moved, anchor, before, "priority", "priorities")
}

// rankedPriorities adapts a vocabulary's definitions to the shared ranked[T]
// interface, which is how AppendRank and InsertRank reach the rank arithmetic
// they share with Vocabulary without either depending on the other's item
// type.
func rankedPriorities(definitions []PriorityDefinition) []ranked[Priority] {
	items := make([]ranked[Priority], 0, len(definitions))
	for _, definition := range definitions {
		items = append(items, definition)
	}
	return items
}

// Validate reports the arity violations that make a priority vocabulary
// unusable: no priorities at all, or the default tag on anything but exactly
// one. It mirrors Vocabulary.Validate's arity half — a priority carries only
// the one tag, so there is no next/done equivalent to check.
//
// This is the authoring gate, not the read gate, the same distinction
// Vocabulary.Validate draws: it is called by a command that is about to write
// a pack, not while one is folded, so every message names the command that
// fixes the state it describes. It does not substitute the built-in set for
// the zero value the way every other accessor does — a project caught with no
// priorities configured is exactly the state this exists to refuse, and
// reading it as the built-in three would hide the very thing it is asked
// about.
func (vocabulary PriorityVocabulary) Validate() error {
	if len(vocabulary.definitions) == 0 {
		return Errorf(
			CategoryValidation,
			"the project has no priorities; add one first: workbook priority add <priority> --label <label>",
		)
	}

	var defaults []string
	for _, definition := range vocabulary.definitions {
		if definition.HasTag(PriorityTagDefault) {
			defaults = append(defaults, string(definition.Priority))
		}
	}

	switch {
	case len(defaults) == 0:
		return Errorf(
			CategoryValidation,
			"no priority is tagged default, so a new task would have no priority to land on; "+
				"tag one first: workbook priority tag <priority> --tag default",
		)
	case len(defaults) > 1:
		return Errorf(
			CategoryValidation,
			"priorities %s are all tagged default, but exactly one may be; "+
				"move the tag instead: workbook priority tag <priority> --tag default",
			strings.Join(defaults, ", "),
		)
	}
	return nil
}

// normalizePriorityDocument validates a priority document and returns it in
// canonical form: priorities ordered by rank then name, aliases by source,
// retirements by source, tags in their fixed order, and every empty
// collection an empty slice rather than a null — the same canonicalization
// normalizeVocabularyDocument performs for statuses, including the charset
// and label-length rules ValidatePriorityToken and ValidatePriorityLabel give
// a priority. A stored color, if present, must also be a valid theme color
// already in its canonical lowercase form — the same double check
// normalizeDisplayDocument performs on a stored color, and for the same
// reason: an unvalidated stored value in that field is not just a bad value,
// it is CSS a later stage composes verbatim into a template.CSS block.
//
// It checks shape and never counts, for the reason normalizeVocabularyDocument
// records at length: a size ceiling enforced inside the fold can brick a
// repository two clones pushed over it concurrently, permanently, because
// neither did anything a ceiling checked here would have refused alone. The
// ceiling is instead enforced at the authoring boundary by
// validatePriorityGrowth below, the priority equivalent of
// validateVocabularyGrowth, which ValidateConfigAuthoring calls the same way
// it calls the status one.
func normalizePriorityDocument(document PriorityDocument) (PriorityDocument, error) {
	priorities := make([]PriorityDefinition, 0, len(document.Priorities))
	ranks := make(map[Priority]*big.Rat, len(document.Priorities))
	seen := make(map[Priority]struct{}, len(document.Priorities))
	for _, definition := range document.Priorities {
		if err := ValidatePriorityToken(definition.Priority); err != nil {
			return PriorityDocument{}, err
		}
		if err := ValidatePriorityLabel(definition.Label); err != nil {
			return PriorityDocument{}, err
		}
		// Color is the one field a priority carries that ends up in a
		// template.CSS block once stage 3 composes the board's theme — the
		// safety argument for bypassing Go's contextual escaping there rests
		// entirely on every byte in that block being something this package
		// already validated, so an unvalidated stored color would be a CSS
		// injection path from a malicious or corrupted peer, not merely a bad
		// value a browser ignores. Mirrors normalizeDisplayDocument's own
		// validate-and-require-canonical check on a stored color, and
		// priority.recolor's identical check at the operation-document
		// boundary in configop.go.
		if definition.Color != "" {
			canonical, err := ValidateThemeColor(definition.Color)
			if err != nil {
				return PriorityDocument{}, err
			}
			if canonical != definition.Color {
				return PriorityDocument{}, Errorf(
					CategoryValidation, "priority %q color is not stored canonically", definition.Priority,
				)
			}
		}
		rank, err := parseRank(definition.Rank)
		if err != nil {
			return PriorityDocument{}, Wrap(CategoryValidation, "priority rank is invalid", err)
		}
		if _, duplicate := seen[definition.Priority]; duplicate {
			return PriorityDocument{}, Errorf(CategoryValidation, "priority %q is defined twice", definition.Priority)
		}
		seen[definition.Priority] = struct{}{}
		ranks[definition.Priority] = rank

		tags, err := normalizePriorityTags(definition.Tags)
		if err != nil {
			return PriorityDocument{}, err
		}
		definition.Tags = tags
		priorities = append(priorities, definition)
	}
	sort.SliceStable(priorities, func(left, right int) bool {
		if compare := ranks[priorities[left].Priority].Cmp(ranks[priorities[right].Priority]); compare != 0 {
			return compare < 0
		}
		return priorities[left].Priority < priorities[right].Priority
	})

	aliases, err := normalizePriorityAliases(document.Aliases)
	if err != nil {
		return PriorityDocument{}, err
	}
	retired, err := normalizeRetiredPriorities(document.Retired)
	if err != nil {
		return PriorityDocument{}, err
	}
	forward := make(map[Priority]Priority, len(aliases)+len(retired))
	for _, alias := range aliases {
		if _, duplicate := forward[alias.From]; duplicate {
			return PriorityDocument{}, Errorf(CategoryValidation, "priority %q is forwarded twice", alias.From)
		}
		forward[alias.From] = alias.To
	}
	for _, entry := range retired {
		if _, duplicate := forward[entry.Priority]; duplicate {
			return PriorityDocument{}, Errorf(CategoryValidation, "priority %q is forwarded twice", entry.Priority)
		}
		forward[entry.Priority] = entry.Destination
	}
	for source := range forward {
		if _, live := seen[source]; live {
			return PriorityDocument{}, Errorf(
				CategoryValidation,
				"priority %q is both live and forwarded elsewhere",
				source,
			)
		}
		if err := forwardTerminates(forward, source, "priority"); err != nil {
			return PriorityDocument{}, err
		}
	}

	return PriorityDocument{Priorities: priorities, Aliases: aliases, Retired: retired}, nil
}

// validatePriorityGrowth is validateVocabularyGrowth's counterpart for the
// priorities section, called from ValidateConfigAuthoring the same way and
// for the same reason: refuse a pack only when it is what pushes a
// collection past its ceiling, and never refuse shrinkage or a pack that
// merely leaves an already-over-ceiling count unchanged — a ceiling enforced
// inside the fold instead could brick a repository two clones pushed over it
// concurrently, permanently, since append-only means the very operation that
// would bring the count back down sits behind a fold that already refused to
// run. See validateVocabularyGrowth's own comment for the full argument;
// this is the identical rule over priorities instead of statuses.
func validatePriorityGrowth(before, after PriorityDocument) error {
	if len(after.Priorities) > MaxPriorityCount && len(after.Priorities) > len(before.Priorities) {
		return Errorf(
			CategoryValidation,
			"the project would define %d priorities and must not exceed %d; "+
				"remove one first: workbook priority delete <priority> --into <priority>",
			len(after.Priorities), MaxPriorityCount,
		)
	}
	if err := forwardingsGrew(
		priorityAliasForwardings(before.Aliases), priorityAliasForwardings(after.Aliases),
		MaxPriorityAliasCount, "priority", "rename", "old",
	); err != nil {
		return err
	}
	if err := forwardingsGrew(
		retiredPriorityForwardings(before.Retired), retiredPriorityForwardings(after.Retired),
		MaxPriorityRetiredCount, "priority", "removal", "removed",
	); err != nil {
		return err
	}
	return nil
}

// normalizeStoredPriorityDocument is the pointer-aware form of
// normalizePriorityDocument used at a configuration checkpoint's boundary,
// mirroring normalizeDisplayDocument: nil in, nil out, and a section that
// normalizes to nothing — no priorities, aliases, or retirements —
// canonicalizes to nil rather than an empty-but-present document, so
// "configured nothing" has exactly one representation.
//
// Without this, a stored empty document (`{"priorities":[],"aliases":[],
// "retired":[]}`) would pass validation, change the checkpoint's bytes
// relative to an unconfigured project, and read back through IsZero as the
// built-in three — indistinguishable from a project that configured nothing,
// which is exactly the invariant this section exists to protect.
func normalizeStoredPriorityDocument(document *PriorityDocument) (*PriorityDocument, error) {
	if document == nil {
		return nil, nil
	}
	normalized, err := normalizePriorityDocument(*document)
	if err != nil {
		return nil, err
	}
	if len(normalized.Priorities) == 0 && len(normalized.Aliases) == 0 && len(normalized.Retired) == 0 {
		return nil, nil
	}
	return &normalized, nil
}

func normalizePriorityTags(tags []PriorityTag) ([]PriorityTag, error) {
	present := make(map[PriorityTag]struct{}, len(tags))
	for _, tag := range tags {
		if err := ValidatePriorityTag(tag); err != nil {
			return nil, err
		}
		present[tag] = struct{}{}
	}
	normalized := make([]PriorityTag, 0, len(present))
	for _, tag := range priorityTags {
		if _, tagged := present[tag]; tagged {
			normalized = append(normalized, tag)
		}
	}
	return normalized, nil
}

// priorityAliasForwardings and retiredPriorityForwardings convert a
// document's own field names to forwarding[Priority] at the boundary into the
// shared normalization, the same conversion statusAliasForwardings and
// retiredStatusForwardings do for statuses.
func priorityAliasForwardings(aliases []PriorityAlias) []forwarding[Priority] {
	pairs := make([]forwarding[Priority], len(aliases))
	for index, alias := range aliases {
		pairs[index] = forwarding[Priority]{From: alias.From, To: alias.To}
	}
	return pairs
}

func retiredPriorityForwardings(retired []RetiredPriority) []forwarding[Priority] {
	pairs := make([]forwarding[Priority], len(retired))
	for index, entry := range retired {
		pairs[index] = forwarding[Priority]{From: entry.Priority, To: entry.Destination}
	}
	return pairs
}

func normalizePriorityAliases(aliases []PriorityAlias) ([]PriorityAlias, error) {
	normalized, err := normalizeForwardings(priorityAliasForwardings(aliases), ValidatePriorityToken, "priority", "alias")
	if err != nil {
		return nil, err
	}
	result := make([]PriorityAlias, len(normalized))
	for index, pair := range normalized {
		result[index] = PriorityAlias{From: pair.From, To: pair.To}
	}
	return result, nil
}

func normalizeRetiredPriorities(retired []RetiredPriority) ([]RetiredPriority, error) {
	normalized, err := normalizeForwardings(retiredPriorityForwardings(retired), ValidatePriorityToken, "priority", "retire into")
	if err != nil {
		return nil, err
	}
	result := make([]RetiredPriority, len(normalized))
	for index, pair := range normalized {
		result[index] = RetiredPriority{Priority: pair.From, Destination: pair.To}
	}
	return result, nil
}

// builtInPriorityDefinitions is the set a project that configured none is read
// as having: today's three, most urgent first, with medium carrying the default
// the service used to hardcode.
func builtInPriorityDefinitions() []PriorityDefinition {
	return []PriorityDefinition{
		{Priority: PriorityHigh, Label: "High", Rank: "1/1", Tags: []PriorityTag{}},
		{Priority: PriorityMedium, Label: "Medium", Rank: "2/1", Tags: []PriorityTag{PriorityTagDefault}},
		{Priority: PriorityLow, Label: "Low", Rank: "3/1", Tags: []PriorityTag{}},
	}
}
