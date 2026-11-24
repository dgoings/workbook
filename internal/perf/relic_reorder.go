package perf

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// RelicReorderPattern names a sequence of part moves MeasureRelicReorder makes.
type RelicReorderPattern string

const (
	// RelicReorderRandom moves a uniform seeded part before or after a uniform
	// other part.
	RelicReorderRandom RelicReorderPattern = "random"
	// RelicReorderFront moves the last part to the front, every move.
	RelicReorderFront RelicReorderPattern = "front"
	// RelicReorderPingPong is the adversarial pattern: each move places a part
	// into the gap between the two parts placed last, so the gap narrows on
	// every move from both ends. Three parts take the moves in turn; a fixed
	// neighbour stands in for the part placed before the first move. Once a
	// move is refused, the pattern tries it again, as nothing else changed.
	//
	// Two parts moving alternately, as the plan words it, always place a part
	// beside the one fixed neighbour, so one end of the gap never moves and
	// positions grow by one denominator step a move: about 11 bytes after
	// 10,000 moves, never the limit. The third part is what makes both ends
	// move.
	RelicReorderPingPong RelicReorderPattern = "ping-pong"
)

// relicReorderPatterns is every pattern, in report order.
var relicReorderPatterns = []RelicReorderPattern{RelicReorderRandom, RelicReorderFront, RelicReorderPingPong}

// RelicReorderResult is what one pattern's moves did to part positions.
// FirstRefusedMove counts from 1 and is 0 when no move was refused. The
// longest position is the longest any move produced; the median is over the
// positions the parts hold at the end.
type RelicReorderResult struct {
	Pattern              RelicReorderPattern `json:"pattern"`
	Parts                int                 `json:"parts"`
	Moves                int                 `json:"moves"`
	Refused              int                 `json:"refused"`
	FirstRefusedMove     int                 `json:"firstRefusedMove"`
	LongestPositionBytes int                 `json:"longestPositionBytes"`
	MedianPositionBytes  int                 `json:"medianPositionBytes"`
}

// MeasureRelicReorder makes moves moves of the given pattern on a relic of
// parts parts, placed first at 1/1, 2/1 and on as relic create places them.
// It is core.PlacePart alone, with no Git. A move PlacePart refuses because the
// position would pass core.MaxRelicPartPositionBytes counts in Refused and
// leaves the parts as they were.
func MeasureRelicReorder(pattern RelicReorderPattern, parts, moves int, seed int64) (RelicReorderResult, error) {
	if parts < 4 {
		return RelicReorderResult{}, fmt.Errorf("reordering needs at least 4 parts, got %d", parts)
	}
	if moves < 0 {
		return RelicReorderResult{}, fmt.Errorf("moves must not be negative")
	}
	chooser, err := newRelicReorderMoves(pattern, parts, seed)
	if err != nil {
		return RelicReorderResult{}, err
	}
	placements := make([]core.PartPlacement, parts)
	index := make(map[string]int, parts)
	for i := range placements {
		placements[i] = core.PartPlacement{ID: relicReorderPartID(i), Position: fmt.Sprintf("%d/1", i+1)}
		index[placements[i].ID] = i
	}
	result := RelicReorderResult{Pattern: pattern, Parts: parts, Moves: moves}
	// refused is the last move PlacePart refused while nothing has moved
	// since. PlacePart reads only the parts and the move, so the same move on
	// the same parts is refused again, and is counted without asking: asking
	// costs a parse of every long position, which at the limit is most of the
	// run.
	type relicMove struct {
		moved, anchor string
		before        bool
	}
	var refused *relicMove
	for move := 1; move <= moves; move++ {
		moved, anchor, before, err := chooser.next(placements)
		if err != nil {
			return RelicReorderResult{}, err
		}
		attempt := relicMove{moved, anchor, before}
		if refused != nil && *refused == attempt {
			result.Refused++
			continue
		}
		position, err := core.PlacePart(placements, moved, anchor, before)
		if err != nil {
			if !strings.Contains(err.Error(), "a position must not exceed") {
				return RelicReorderResult{}, fmt.Errorf("%s move %d: %w", pattern, move, err)
			}
			result.Refused++
			if result.FirstRefusedMove == 0 {
				result.FirstRefusedMove = move
			}
			refused = &attempt
			continue
		}
		refused = nil
		placements[index[moved]].Position = position
		chooser.placed()
		result.LongestPositionBytes = max(result.LongestPositionBytes, len(position))
	}
	lengths := make([]int, len(placements))
	for i, placement := range placements {
		lengths[i] = len(placement.Position)
		result.LongestPositionBytes = max(result.LongestPositionBytes, lengths[i])
	}
	sort.Ints(lengths)
	result.MedianPositionBytes = lengths[len(lengths)/2]
	return result, nil
}

// relicReorderPartID names part i so IDs sort in creation order.
func relicReorderPartID(i int) string { return fmt.Sprintf("P%06d", i) }

// relicReorderMoves chooses each move from the parts as they stand. placed is
// called after a move PlacePart accepted, so a pattern that follows its own
// moves tries a refused move again rather than moving on as if it were made.
type relicReorderMoves interface {
	next(parts []core.PartPlacement) (moved, anchor string, before bool, err error)
	placed()
}

type relicReorderFunc func(parts []core.PartPlacement) (string, string, bool, error)

func (f relicReorderFunc) next(parts []core.PartPlacement) (string, string, bool, error) {
	return f(parts)
}
func (relicReorderFunc) placed() {}

func newRelicReorderMoves(pattern RelicReorderPattern, parts int, seed int64) (relicReorderMoves, error) {
	switch pattern {
	case RelicReorderRandom:
		random := rand.New(rand.NewSource(seed))
		return relicReorderFunc(func([]core.PartPlacement) (string, string, bool, error) {
			moved, anchor := randomPartPair(random, parts)
			return relicReorderPartID(moved), relicReorderPartID(anchor), random.Intn(2) == 0, nil
		}), nil
	case RelicReorderFront:
		order := make([]string, parts)
		for i := range order {
			order[i] = relicReorderPartID(i)
		}
		return &relicFront{order: order}, nil
	case RelicReorderPingPong:
		return &relicPingPong{
			ring:   []string{relicReorderPartID(1), relicReorderPartID(2), relicReorderPartID(3)},
			newest: relicReorderPartID(3),
			second: relicReorderPartID(0),
		}, nil
	default:
		return nil, fmt.Errorf("unknown reorder pattern %q", pattern)
	}
}

// relicFront moves the last part to the front. It keeps the order itself,
// rather than sorting the parts again, since every placed move only rotates
// it.
type relicFront struct{ order []string }

func (f *relicFront) next([]core.PartPlacement) (string, string, bool, error) {
	return f.order[len(f.order)-1], f.order[0], true, nil
}

func (f *relicFront) placed() {
	last := f.order[len(f.order)-1]
	copy(f.order[1:], f.order[:len(f.order)-1])
	f.order[0] = last
}

// relicPingPong moves parts 1, 2 and 3 in turn. Each move places the oldest
// of the three beside the one placed last, on the side of the one placed
// before it: into the gap between the two. Part 0 stands in for the one
// placed before the first move.
type relicPingPong struct {
	ring           []string
	turn           int
	newest, second string
	moving         string
}

func (p *relicPingPong) next(parts []core.PartPlacement) (string, string, bool, error) {
	p.moving = p.ring[p.turn%len(p.ring)]
	before, err := sortsBefore(parts, p.second, p.newest)
	if err != nil {
		return "", "", false, err
	}
	return p.moving, p.newest, before, nil
}

func (p *relicPingPong) placed() {
	p.turn++
	p.second, p.newest = p.newest, p.moving
}

// sortsBefore reports whether part a sorts before part b.
func sortsBefore(parts []core.PartPlacement, a, b string) (bool, error) {
	var positionA, positionB string
	for _, part := range parts {
		switch part.ID {
		case a:
			positionA = part.Position
		case b:
			positionB = part.Position
		}
	}
	left, err := core.ParsePartPosition(positionA)
	if err != nil {
		return false, err
	}
	right, err := core.ParsePartPosition(positionB)
	if err != nil {
		return false, err
	}
	if c := left.Cmp(right); c != 0 {
		return c < 0, nil
	}
	return a < b, nil
}
