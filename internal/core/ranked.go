package core

import (
	"math/big"
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
