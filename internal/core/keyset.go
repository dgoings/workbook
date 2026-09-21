package core

import (
	"fmt"
	"strings"
)

// KeyState is what a project key is doing: minting new tasks, or holding only
// the tasks already created under it.
type KeyState string

const (
	KeyStateActive  KeyState = "active"
	KeyStateRetired KeyState = "retired"
)

// KeyDefinition is one project key as the configuration ledger stores it.
//
// Retirement is a bool with omitempty rather than a KeyState member because the
// checkpoint's bytes are compared for equality and an active key is the
// ordinary case: a project's own key encodes as {"key":"WB"}, and a state
// member would put a word in every one of them forever.
type KeyDefinition struct {
	Key     string `json:"key"`
	Retired bool   `json:"retired,omitempty"`
}

// KeyDocument is the stored form of a project's keys: the keys in the order
// they were added, and which of the active ones is current.
//
// The order is add order rather than a sort, which is the one place this
// section differs from the status and priority documents. There is no `key
// move`, the fold only ever appends, and two clones folding the same history
// append in the same order — so add order is a function of the history exactly
// as a rank is, and normalization checks the order rather than imposing one.
type KeyDocument struct {
	Keys    []KeyDefinition `json:"keys"`
	Current string          `json:"current"`
}

// KeySet is a project's resolved keys, and the one authority for whether a task
// ID belongs to this project.
//
// Its fields are unexported so that a value in hand came through NewKeySet,
// FoundingKeySet or a decoded configuration checkpoint, which is what lets
// every accessor be total. The zero value is "not configured" and every caller
// that can hold one — core.Service, gitstore's boundaries — substitutes the
// founding key for it, the way Service substitutes LegacyVocabulary for a zero
// Vocabulary.
type KeySet struct {
	definitions []KeyDefinition
	current     string
	index       map[string]int
}

// FoundingKeySet is the key set of a project whose ledger records nothing about
// keys: the key in the project identity alone, active and current.
//
// It takes no error because the founding key has already been validated
// wherever a ProjectConfig or a ProjectIdentity was read, and an accessor that
// could fail here would push a second error path into every read of every ref.
func FoundingKeySet(founding string) KeySet {
	return newKeySetFromCanonical(KeyDocument{
		Keys:    []KeyDefinition{{Key: founding}},
		Current: founding,
	})
}

// NewKeySet builds a key set from a stored document, normalizing it first, so a
// document that came from anywhere but a fold is refused here rather than
// producing an accessor that lies.
func NewKeySet(document KeyDocument) (KeySet, error) {
	normalized, err := normalizeKeyDocument(&document)
	if err != nil {
		return KeySet{}, err
	}
	if normalized == nil {
		return KeySet{}, Errorf(CategoryValidation, "a project key set must name at least one key")
	}
	return newKeySetFromCanonical(*normalized), nil
}

// newKeySetFromCanonical indexes an already-normalized document. It cannot
// fail, which is why decoding a checkpoint and reading its keys are two steps
// rather than one fallible one.
func newKeySetFromCanonical(document KeyDocument) KeySet {
	set := KeySet{
		definitions: document.Keys,
		current:     document.Current,
		index:       make(map[string]int, len(document.Keys)),
	}
	for position, definition := range document.Keys {
		set.index[definition.Key] = position
	}
	return set
}

// normalizeKeyDocument is the canonical form of a stored key section: nil for a
// project that has recorded nothing, and otherwise a document whose keys are
// well formed and unique, with exactly one current key that is active.
//
// It preserves add order and refuses rather than repairs, because it runs over
// a document at rest. The fold repairs instead — see configKeys.normalizeArity
// — which is the same division ApplyConfig and validateConfigStateDocument
// already draw for the status vocabulary.
func normalizeKeyDocument(document *KeyDocument) (*KeyDocument, error) {
	if document == nil {
		return nil, nil
	}
	if len(document.Keys) == 0 && document.Current == "" {
		return nil, nil
	}
	if len(document.Keys) == 0 {
		return nil, Errorf(CategoryValidation, "a project key set must name at least one key")
	}
	keys := make([]KeyDefinition, 0, len(document.Keys))
	seen := make(map[string]struct{}, len(document.Keys))
	active := 0
	for _, definition := range document.Keys {
		if err := ValidateProjectKey(definition.Key); err != nil {
			return nil, err
		}
		if _, duplicate := seen[definition.Key]; duplicate {
			return nil, Errorf(CategoryValidation, "project key %q appears twice", definition.Key)
		}
		seen[definition.Key] = struct{}{}
		if !definition.Retired {
			active++
		}
		keys = append(keys, definition)
	}
	if active == 0 {
		return nil, Errorf(CategoryValidation, "a project must keep at least one active key")
	}
	position, known := indexOfKey(keys, document.Current)
	if !known {
		return nil, Errorf(CategoryValidation, "the current project key %q is not one of this project's keys", document.Current)
	}
	if keys[position].Retired {
		return nil, Errorf(CategoryValidation, "the current project key %q is retired", document.Current)
	}
	return &KeyDocument{Keys: keys, Current: document.Current}, nil
}

func indexOfKey(keys []KeyDefinition, key string) (int, bool) {
	for position, definition := range keys {
		if definition.Key == key {
			return position, true
		}
	}
	return 0, false
}

// IsZero reports the unconfigured key set, which is how a caller that never
// read the ledger is distinguished from a project that recorded its keys.
func (set KeySet) IsZero() bool { return len(set.definitions) == 0 }

// Current is the key a new task is minted under when nobody names one.
func (set KeySet) Current() string { return set.current }

// Active names the keys a new task may be minted under, in add order.
func (set KeySet) Active() []string {
	active := make([]string, 0, len(set.definitions))
	for _, definition := range set.definitions {
		if !definition.Retired {
			active = append(active, definition.Key)
		}
	}
	return active
}

// Keys is every key in add order, retired ones included. The slice is a copy:
// callers hand it to templates and to JSON.
func (set KeySet) Keys() []KeyDefinition {
	keys := make([]KeyDefinition, len(set.definitions))
	copy(keys, set.definitions)
	return keys
}

// Contains reports a key this project has ever minted under, active or retired,
// which is the question ownership is decided by.
func (set KeySet) Contains(key string) bool {
	_, known := set.index[key]
	return known
}

// IsActive reports a key a new task may be minted under.
func (set KeySet) IsActive(key string) bool {
	position, known := set.index[key]
	return known && !set.definitions[position].Retired
}

// State reports what one key is, and whether this project has it at all.
func (set KeySet) State(key string) (KeyState, bool) {
	position, known := set.index[key]
	if !known {
		return "", false
	}
	if set.definitions[position].Retired {
		return KeyStateRetired, true
	}
	return KeyStateActive, true
}

// Parse splits a task ID into its key and its ULID body, and reports whether it
// is shaped like a task ID.
//
// It is a method rather than only a package function because this type is the
// one authority a boundary holds: a caller that has a key set in hand should
// never have to reach past it for the split and then decide membership itself.
// It deliberately answers about shape alone, so Owns is the one question that
// consults the set.
func (set KeySet) Parse(taskID string) (string, string, bool) {
	return ParseTaskID(taskID)
}

// Owns reports a task ID that belongs to this project: a task-ID shape whose
// key this project has minted under, active or retired.
func (set KeySet) Owns(taskID string) bool {
	key, _, ok := set.Parse(taskID)
	return ok && set.Contains(key)
}

// RequireOwned is Owns with a message, for a name somebody typed or a ref
// Workbook found.
func (set KeySet) RequireOwned(taskID string) error {
	key, _, ok := set.Parse(taskID)
	if !ok {
		return ValidateTaskIDShape(taskID)
	}
	if !set.Contains(key) {
		return Errorf(CategoryValidation,
			"task ID %q carries project key %q, which this project does not have; its keys are: %s",
			taskID, key, KeyNameList(set))
	}
	return nil
}

// RequireActive refuses a key a new task may not be minted under, and says
// which keys it may.
func (set KeySet) RequireActive(key string) error {
	if err := ValidateProjectKey(key); err != nil {
		return err
	}
	switch state, known := set.State(key); {
	case !known:
		return Errorf(CategoryValidation,
			"no project key %q in this project; its active keys are: %s", key, ActiveKeyList(set))
	case state == KeyStateRetired:
		return Errorf(CategoryValidation,
			"project key %q is retired, so no new task is minted under it; the active keys are: %s",
			key, ActiveKeyList(set))
	default:
		return nil
	}
}

// Validate reports a key set that is not usable: no keys, or a current key that
// is not active. It is the arity question ValidateConfigAuthoring asks, and it
// is deliberately separate from normalizeKeyDocument's shape checks.
func (set KeySet) Validate() error {
	if set.IsZero() {
		return Errorf(CategoryValidation, "this project has no keys")
	}
	if !set.IsActive(set.current) {
		return Errorf(CategoryValidation, "this project's current key %q is not active", set.current)
	}
	return nil
}

// Document returns the set in the canonical shape a configuration checkpoint
// stores it in.
func (set KeySet) Document() KeyDocument {
	return KeyDocument{Keys: set.Keys(), Current: set.current}
}

// PlausibleTaskID reports whether a ref name could be some Workbook's task,
// which is the gate in front of destructive advice.
//
// Two names qualify: one under any key this project has, which a version
// writing an ID format this build predates would produce, and one shaped like
// <KEY>-<ULID> under any valid key, which a second project sharing origin's
// namespace produces. A name nested under either is judged by the segment it
// hangs from, so a child ref is as protected as its parent, and Git's
// peeled-tag suffix is dropped before either rule runs, so a peeled name is
// judged as the task it points at under any key rather than only under this
// project's.
//
// It exists to gate destructive advice, never to widen what Workbook reads as a
// task: a true answer means only "do not offer to delete this". Every name that
// fails both rules belongs to no project's ID format and can be named as
// removable; Owns remains the authority on what this project's tasks are.
func (set KeySet) PlausibleTaskID(name string) bool {
	name = strings.TrimSuffix(name, peeledRefSuffix)
	for _, definition := range set.definitions {
		if strings.HasPrefix(name, definition.Key+"-") {
			return true
		}
	}
	head, _, _ := strings.Cut(name, "/")
	foreignKey, body, separated := strings.Cut(head, "-")
	if !separated {
		return false
	}
	return ValidateProjectKey(foreignKey) == nil && ulidShapePattern.MatchString(body)
}

// AdoptableKey names the key `workbook key add` would adopt an ignored ref
// under, or nothing when adopting it is not the answer.
//
// It is narrower than PlausibleTaskID on purpose. Advice to adopt is only
// honest for a name that is exactly a task ID under a key this project does not
// have: a name under a key it already has needs no adoption, and a name this
// build merely cannot parse would not become readable by adding a key.
func (set KeySet) AdoptableKey(name string) string {
	key, _, ok := ParseTaskID(strings.TrimSuffix(name, peeledRefSuffix))
	if !ok || set.Contains(key) {
		return ""
	}
	return key
}

// KeyNameList names every key this project has, in add order, marking the ones
// that are retired, for a message that has to tell somebody what exists.
func KeyNameList(set KeySet) string {
	names := make([]string, 0, len(set.definitions))
	for _, definition := range set.definitions {
		if definition.Retired {
			names = append(names, fmt.Sprintf("%s (retired)", definition.Key))
			continue
		}
		names = append(names, definition.Key)
	}
	return strings.Join(names, ", ")
}

// ActiveKeyList names the keys a new task may be minted under.
func ActiveKeyList(set KeySet) string {
	return strings.Join(set.Active(), ", ")
}
