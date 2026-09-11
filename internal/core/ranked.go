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
// noun names the kind of item being placed — "status", eventually "priority"
// — for both the messages below and their categories: an undefined anchor is
// CategoryValidation (the caller typed something that was never there), while
// an unparseable rank is CategoryCorruptData (a value that was there is
// broken), and those categories map to different CLI exit codes. Collapsing
// them into one would report the wrong exit code for a corrupt rank, not just
// the wrong string, so insertRank returns errors already carrying the
// category a caller must pass through unwrapped.
func insertRank[T ~string](items []ranked[T], moved, anchor T, before bool, noun string) (string, error) {
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
				"statuses %q and %q share a rank, so %q cannot be placed between them; move one of them first",
				neighborKey, anchor, moved,
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

// normalizeForwardings puts a forwarding list into canonical form: sorted by
// source, with every source appearing at most once and no source forwarding
// to itself. It is normalizeStatusAliases and normalizeRetiredStatuses's
// shared body — they differ only in which of StatusAlias's or RetiredStatus's
// two fields is the source and which is the destination, which is exactly
// what converting to forwarding[Status] before calling this erases.
//
// Sorting by source is what makes the canonical document's bytes a property
// of the configuration instead of a property of whoever wrote it, the same
// reason normalizeVocabularyDocument sorts statuses by rank. A source
// forwarding to itself or recorded twice are both shapes no forwarding chain
// can represent — the first is a no-op that would strand nothing but is never
// a value an author meant, and the second would leave resolveForward's walk
// no way to choose between two destinations — so both are refused here rather
// than deferred to whatever reads the chain later.
//
// A source repeated across two different lists — a rename and a retirement
// both naming the same value — is not caught here, because normalizeForwardings
// only ever sees one list at a time; normalizeVocabularyDocument catches that
// case itself while it builds the combined forward map.
//
// noun names what is being forwarded ("status", eventually "priority") for
// the messages below, so the status caller's text stays byte-identical to
// what normalizeStatusAliases and normalizeRetiredStatuses produced before
// this was shared.
func normalizeForwardings[T ~string](pairs []forwarding[T], noun string) ([]forwarding[T], error) {
	normalized := make([]forwarding[T], 0, len(pairs))
	seen := make(map[T]struct{}, len(pairs))
	for _, pair := range pairs {
		if pair.From == pair.To {
			return nil, Errorf(CategoryValidation, "%s %q cannot forward to itself", noun, pair.From)
		}
		if _, duplicate := seen[pair.From]; duplicate {
			return nil, Errorf(CategoryValidation, "%s %q is forwarded twice", noun, pair.From)
		}
		seen[pair.From] = struct{}{}
		normalized = append(normalized, pair)
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		return normalized[left].From < normalized[right].From
	})
	return normalized, nil
}

// forwardingsGrew reports whether after still carries every pointer before
// did, unchanged. It is the rule behind the comment on
// MaxStatusAliasCount and MaxStatusRetiredCount that "nothing drops a
// forwarding pointer yet": a rename or a retirement is what lets a clone that
// has not fetched the latest name still land a stored task in the right
// column, so a pack that reused or discarded a source would strand exactly
// the tasks that were counting on it still being there — a correctness
// failure no ceiling would catch, because it can drop a pointer while
// shrinking a list that was always under its limit.
//
// It is checked at authoring time only, the same boundary the size ceilings
// are checked at and for the same reason: a fold cannot be allowed to fail on
// it without risking a history no clone can ever read, so ApplyConfig itself
// stays permissive and this runs from ValidateConfigAuthoring instead.
//
// noun names what is being forwarded ("status", eventually "priority") for
// the message below.
func forwardingsGrew[T ~string](before, after []forwarding[T], noun string) error {
	kept := make(map[forwarding[T]]struct{}, len(after))
	for _, pair := range after {
		kept[pair] = struct{}{}
	}
	for _, pair := range before {
		if _, still := kept[pair]; !still {
			return Errorf(
				CategoryValidation,
				"%s %q must still forward to %q; a forwarding pointer cannot be dropped or repointed",
				noun, pair.From, pair.To,
			)
		}
	}
	return nil
}

// forwardTerminates rejects a forwarding cycle. ApplyConfig cannot build one —
// every chain it extends ends at a live value, and a live value forwards
// nowhere — so reaching this is a hand-edited or corrupted checkpoint, which is
// exactly what a decoder is for.
func forwardTerminates[T ~string](forward map[T]T, source T) error {
	seen := map[T]struct{}{source: {}}
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
