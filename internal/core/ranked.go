package core

import (
	"math/big"
	"sort"
	"strings"
)

// ranked is the shape any project-configured vocabulary item must present to
// share the rank arithmetic below: a stable key to compare against an anchor
// or an item being moved, and the rank string that orders it among its peers.
//
// It lets appendRank and insertRank work over a vocabulary's own item type —
// StatusDefinition today, a priority definition later — without either one
// depending on the other's package-level type.
type ranked[T ~string] interface {
	key() T
	rank() string
}

// appendRank returns the rank an item added after every existing one takes.
// It is nextRank's rule for a vocabulary's items: one past the highest, so
// appending never has to renumber anything.
//
// Ranks in a constructed vocabulary have already been parsed by
// normalizeVocabularyDocument, so an unparseable one cannot reach here; one
// that somehow did is skipped rather than allowed to fail a total function.
func appendRank[T ~string](items []ranked[T]) string {
	maximum := new(big.Rat)
	for _, item := range items {
		rank, err := parseRank(item.rank())
		if err != nil {
			continue
		}
		if rank.Cmp(maximum) > 0 {
			maximum = rank
		}
	}
	return formatRank(new(big.Rat).Add(maximum, big.NewRat(1, 1)))
}

// insertRank returns the rank that places an item immediately before or after
// an anchor, leaving every other item where it is.
//
// It is movedRank's rule for a vocabulary's items, and it is the same rule
// for the same reason: a rational always has room between two neighbours, so
// two clones can insert into the same gap without coordinating and without
// renumbering a column somebody else is looking at. moved names the item
// being placed and is empty when a new one is being added; it is excluded
// from the neighbour search so that moving an item one place does not measure
// the gap against itself.
//
// Items sort by rank and then by key, so an exhausted gap is decided the
// same way: two items may share a rank, and the insertion is representable
// only when the keys already fall in the order the caller asked for.
//
// noun names the kind of item being placed — "status", "priority" — for both
// the messages below and their categories: an undefined anchor is
// CategoryValidation (the caller typed something that was never there), while
// an unparseable rank is CategoryCorruptData (a value that was there is
// broken), and those categories map to different CLI exit codes. Collapsing
// them into one would report the wrong exit code for a corrupt rank, not just
// the wrong string, so insertRank returns errors already carrying the
// category a caller must pass through unwrapped.
//
// plural is noun's plural, spelled out by the caller rather than derived:
// "status" becomes "statuses" and "priority" becomes "priorities", two
// irregular forms with nothing in common for a rule to generalize from, so
// asking each caller for its own plural is the straightforward choice next
// to inferring English morphology inside a data-model package.
func insertRank[T ~string](items []ranked[T], moved, anchor T, before bool, noun, plural string) (string, error) {
	var anchorRank *big.Rat
	var anchorFound bool
	for _, item := range items {
		if item.key() != anchor {
			continue
		}
		rank, err := parseRank(item.rank())
		if err != nil {
			return "", Wrap(CategoryCorruptData, noun+" rank is invalid", err)
		}
		anchorRank = rank
		anchorFound = true
		break
	}
	if !anchorFound {
		return "", Errorf(CategoryValidation, "%s %q is not defined by this project", noun, anchor)
	}

	var neighbor *big.Rat
	var neighborKey T
	for _, item := range items {
		if item.key() == moved || item.key() == anchor {
			continue
		}
		rank, err := parseRank(item.rank())
		if err != nil {
			return "", Wrap(CategoryCorruptData, noun+" rank is invalid", err)
		}
		anchorComparison := rank.Cmp(anchorRank)
		if anchorComparison == 0 {
			anchorComparison = strings.Compare(string(item.key()), string(anchor))
		}
		neighborComparison := 0
		if neighbor != nil {
			neighborComparison = rank.Cmp(neighbor)
			if neighborComparison == 0 {
				neighborComparison = strings.Compare(string(item.key()), string(neighborKey))
			}
		}
		if before {
			if anchorComparison < 0 && (neighbor == nil || neighborComparison > 0) {
				neighbor, neighborKey = rank, item.key()
			}
			continue
		}
		if anchorComparison > 0 && (neighbor == nil || neighborComparison < 0) {
			neighbor, neighborKey = rank, item.key()
		}
	}

	if neighbor == nil {
		if before {
			return formatRank(new(big.Rat).Quo(anchorRank, big.NewRat(2, 1))), nil
		}
		next := new(big.Int).Quo(anchorRank.Num(), anchorRank.Denom())
		next.Add(next, big.NewInt(1))
		return formatRank(new(big.Rat).SetInt(next)), nil
	}
	if neighbor.Cmp(anchorRank) == 0 {
		representable := strings.Compare(string(neighborKey), string(moved)) < 0 &&
			strings.Compare(string(moved), string(anchor)) < 0
		if !before {
			representable = strings.Compare(string(anchor), string(moved)) < 0 &&
				strings.Compare(string(moved), string(neighborKey)) < 0
		}
		if !representable {
			return "", Errorf(
				CategoryValidation,
				"%s %q and %q share a rank, so %q cannot be placed between them; move one of them first",
				plural, neighborKey, anchor, moved,
			)
		}
		return formatRank(anchorRank), nil
	}
	return formatRank(new(big.Rat).Quo(new(big.Rat).Add(anchorRank, neighbor), big.NewRat(2, 1))), nil
}

// resolveForward follows a stored value through a forwarding chain to the
// live value it now means, reporting whether the walk terminated at one.
//
// It is transitive: a rename to an intermediate name followed by a later
// rename or retirement resolves the original in one call, because a clone
// that was offline across both changes stored the original and must still
// land in a real column. It is cycle-safe by bounding the walk and
// remembering where it has been, even though a document that built the
// forward map already refused to create a cycle — a checkpoint is data read
// from a ref, and a total function on data from a ref is worth more than an
// invariant nobody can check at read time.
//
// A value that is already live resolves to itself. A value with no
// forwarding entry resolves to itself with ok false, which is the ordinary
// state of a value written by a newer build.
func resolveForward[T ~string](forward map[T]T, live func(T) bool, from T) (T, bool) {
	if live(from) {
		return from, true
	}
	seen := make(map[T]struct{}, len(forward))
	current := from
	for range len(forward) + 1 {
		next, forwarded := forward[current]
		if !forwarded {
			return from, false
		}
		if _, repeated := seen[next]; repeated {
			return from, false
		}
		seen[next] = struct{}{}
		if live(next) {
			return next, true
		}
		current = next
	}
	return from, false
}

// forwarding is one source-to-destination pointer in a vocabulary's
// forwarding chain: a rename's old value to its new one, or a retirement's
// removed value to the value its tasks belong in now. StatusAlias and
// RetiredStatus convert to and from it at the package boundary — their own
// field names and JSON tags are part of the durable checkpoint shape and stay
// exactly as they are — so the normalization and growth rules below serve
// every project-configured vocabulary that has a forwarding chain, without
// either type depending on the other's field names.
type forwarding[T ~string] struct {
	From T
	To   T
}

// normalizeForwardings puts a forwarding list into canonical form: every
// pair's own fields validated, no source forwarding to itself, sorted by
// source. It is normalizeStatusAliases and normalizeRetiredStatuses's shared
// body — they differ only in which of StatusAlias's or RetiredStatus's two
// fields is the source and which is the destination, which is exactly what
// converting to forwarding[Status] before calling this erases.
//
// validate checks one field's own well-formedness — ValidateStatusToken for
// a status vocabulary — and is a parameter rather than something this file
// owns because that rule is not generic: a later priority vocabulary
// validates its own tokens by its own rule, unrelated to a status's charset
// and length. Running it on both of a pair's fields and then the
// self-forward check, per pair, in list order, is what reproduces the
// original per-entry ordering: normalizeStatusAliases and
// normalizeRetiredStatuses validated token, token, self-check for one entry
// before ever looking at the next, so a document with a malformed token in a
// later entry and a self-forward in an earlier one reports the earlier
// entry's problem — and this has to walk the list the same interleaved way
// to keep reporting the same one.
//
// Sorting happens once at the end, after every pair has passed, which is
// what makes the canonical document's bytes a property of the configuration
// instead of a property of whoever wrote it — the same reason
// normalizeVocabularyDocument sorts statuses by rank.
//
// A source recorded twice is not caught here. Neither normalizeStatusAliases
// nor normalizeRetiredStatuses ever checked it before either was shared;
// normalizeVocabularyDocument already does, while it builds the combined
// forward map — across the alias list and the retirement list at once,
// which normalizeForwardings could not do anyway, since it only ever sees
// one list at a time.
//
// noun and verb build the self-forward message out of the two words the
// alias and retirement callers disagree on ("status"/"alias" and
// "status"/"retire into"), the same shape forwardingsGrew's noun, kind and
// location build its own message.
func normalizeForwardings[T ~string](pairs []forwarding[T], validate func(T) error, noun, verb string) ([]forwarding[T], error) {
	normalized := make([]forwarding[T], 0, len(pairs))
	for _, pair := range pairs {
		if err := validate(pair.From); err != nil {
			return nil, err
		}
		if err := validate(pair.To); err != nil {
			return nil, err
		}
		if pair.From == pair.To {
			return nil, Errorf(CategoryValidation, "%s %q cannot %s itself", noun, pair.From, verb)
		}
		normalized = append(normalized, pair)
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		return normalized[left].From < normalized[right].From
	})
	return normalized, nil
}

// forwardingsGrew is validateVocabularyGrowth's ceiling check, shared between
// the alias half and the retirement half the same way normalizeForwardings is
// shared between normalizeStatusAliases and normalizeRetiredStatuses: refuse
// a pack only when it is what pushes the list past ceiling, exactly the shape
// the status-definition ceiling above it in validateVocabularyGrowth uses for
// statuses.
//
// Comparing against before rather than against ceiling alone is the whole
// design, carried over unchanged from before this was shared: a folded state
// may already sit over a ceiling — two clones each renaming a different
// status concurrently is enough — and a rule that refused every pack while
// over one would refuse the very shrinkage that could bring it back under.
// So growth past the ceiling is refused and everything else — including a
// same-size or smaller pack left over the ceiling — is allowed. This does
// not inspect content: a pack that reused or discarded a specific forwarding
// pointer without changing the list's length is not caught here. It is a
// real gap — recorded, not fixed, because fixing it is a behavior change
// this task does not make.
//
// noun, kind and location build the message out of the three words the alias
// and retirement callers disagree on: noun is what is forwarded ("status"),
// kind is the singular action, pluralized here for the count
// ("rename"/"removal"), and location is the adjective before "name"
// ("old"/"removed"). Together they reproduce validateVocabularyGrowth's two
// original messages byte-for-byte.
func forwardingsGrew[T ~string](before, after []forwarding[T], ceiling int, noun, kind, location string) error {
	if len(after) > ceiling && len(after) > len(before) {
		return Errorf(
			CategoryValidation,
			"the project has recorded %d %s %ss and must not exceed %d; "+
				"nothing can drop a %s yet, because a clone that has not fetched it "+
				"still needs it to read tasks stored under the %s name",
			len(after), noun, kind, ceiling, kind, location,
		)
	}
	return nil
}

// forwardTerminates rejects a forwarding cycle. ApplyConfig cannot build one —
// every chain it extends ends at a live value, and a live value forwards
// nowhere — so reaching this is a hand-edited or corrupted checkpoint, which is
// exactly what a decoder is for.
//
// noun names the kind of value cycling, "status" or "priority", the same
// treatment insertRank's noun parameter already gives its own messages.
func forwardTerminates[T ~string](forward map[T]T, source T, noun string) error {
	seen := map[T]struct{}{source: {}}
	current := source
	for range len(forward) + 1 {
		next, forwarded := forward[current]
		if !forwarded {
			return nil
		}
		if _, repeated := seen[next]; repeated {
			return Errorf(CategoryValidation, "%s %q forwards to itself through a cycle", noun, source)
		}
		seen[next] = struct{}{}
		current = next
	}
	return Errorf(CategoryValidation, "%s %q forwards to itself through a cycle", noun, source)
}
