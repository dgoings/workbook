package core

import (
	"math/big"
	"sort"
)

// PartPlacement is one part's ID and its position, as the commands see them
// when they choose where a part goes.
type PartPlacement struct{ ID, Position string }

// ParsePartPosition parses a part position, an exact reduced fraction written
// num/den, the way task ranks are. A position longer than
// MaxRelicPartPositionBytes is refused without being echoed.
func ParsePartPosition(position string) (*big.Rat, error) {
	if len(position) > MaxRelicPartPositionBytes {
		return nil, Errorf(CategoryValidation,
			"a part position is %d bytes, and a position must not exceed %d", len(position), MaxRelicPartPositionBytes)
	}
	if !rankPattern.MatchString(position) {
		return nil, Errorf(CategoryValidation, "a part position must be a reduced fraction such as 3/2")
	}
	rat, ok := new(big.Rat).SetString(position)
	if !ok || formatRank(rat) != position {
		return nil, Errorf(CategoryValidation, "a part position must be a reduced fraction such as 3/2")
	}
	return rat, nil
}

// ShortestPositionBetween returns the fraction with the smallest denominator,
// and then the smallest numerator, strictly between lower and upper. A nil
// lower is the start (zero); a nil upper is the end.
func ShortestPositionBetween(lower, upper *big.Rat) *big.Rat {
	if lower == nil {
		lower = new(big.Rat)
	}
	return shortestPosition(lower, upper)
}

// shortestPosition requires lo >= 0, and lo < hi or hi nil.
func shortestPosition(lo, hi *big.Rat) *big.Rat {
	floor := new(big.Int).Quo(lo.Num(), lo.Denom())
	next := new(big.Rat).SetInt(new(big.Int).Add(floor, big.NewInt(1)))
	if hi == nil || next.Cmp(hi) < 0 {
		return next
	}
	base := new(big.Rat).SetInt(floor)
	hiFrac := new(big.Rat).Sub(hi, base) // in (0, 1]
	if lo.IsInt() {
		// floor + 1/k for the smallest k with 1/k < hiFrac.
		k := new(big.Int).Quo(hiFrac.Denom(), hiFrac.Num())
		k.Add(k, big.NewInt(1))
		return new(big.Rat).Add(base, new(big.Rat).SetFrac(big.NewInt(1), k))
	}
	loFrac := new(big.Rat).Sub(lo, base) // in (0, 1)
	inner := shortestPosition(new(big.Rat).Inv(hiFrac), new(big.Rat).Inv(loFrac))
	return new(big.Rat).Add(base, new(big.Rat).Inv(inner))
}

// formatPlacedPosition formats a chosen position and refuses one over the limit.
func formatPlacedPosition(position *big.Rat) (string, error) {
	text := formatRank(position)
	if len(text) > MaxRelicPartPositionBytes {
		return "", Errorf(CategoryValidation,
			"placing the part there needs a position of %d bytes, and a position must not exceed %d; place it somewhere else, or move a neighboring part first",
			len(text), MaxRelicPartPositionBytes)
	}
	return text, nil
}

// AppendPartPosition returns a position after every part, or 1/1 when there
// are none.
func AppendPartPosition(parts []PartPlacement) (string, error) {
	var highest *big.Rat
	for _, part := range parts {
		position, err := ParsePartPosition(part.Position)
		if err != nil {
			return "", err
		}
		if highest == nil || position.Cmp(highest) > 0 {
			highest = position
		}
	}
	return formatPlacedPosition(ShortestPositionBetween(highest, nil))
}

// SortPartPlacements orders parts by position, then by ID.
func SortPartPlacements(parts []PartPlacement) error {
	positions := make(map[string]*big.Rat, len(parts))
	for _, part := range parts {
		position, err := ParsePartPosition(part.Position)
		if err != nil {
			return err
		}
		positions[part.Position] = position
	}
	sort.SliceStable(parts, func(i, j int) bool {
		if c := positions[parts[i].Position].Cmp(positions[parts[j].Position]); c != 0 {
			return c < 0
		}
		return parts[i].ID < parts[j].ID
	})
	return nil
}

// PlacePart returns the position for the part moved so it sits directly after
// anchor, or directly before it when before is true. It ignores moved when it
// looks for neighbors, so callers pass live parts only. Parts order by
// position, then ID: a part can go between two parts that share a position
// only when its ID sorts between theirs.
func PlacePart(parts []PartPlacement, moved, anchor string, before bool) (string, error) {
	if moved == anchor {
		return "", Errorf(CategoryValidation, "a part cannot be placed relative to itself")
	}
	others := make([]PartPlacement, 0, len(parts))
	for _, part := range parts {
		if part.ID != moved {
			others = append(others, part)
		}
	}
	if err := SortPartPlacements(others); err != nil {
		return "", err
	}
	index := -1
	for i, part := range others {
		if part.ID == anchor {
			index = i
			break
		}
	}
	if index < 0 {
		return "", Errorf(CategoryValidation, "the part to place it next to is not one of this relic's parts")
	}
	anchorPosition, err := ParsePartPosition(others[index].Position)
	if err != nil {
		return "", err
	}

	neighborIndex := index + 1
	if before {
		neighborIndex = index - 1
	}
	if neighborIndex < 0 || neighborIndex >= len(others) {
		if before {
			return formatPlacedPosition(ShortestPositionBetween(nil, anchorPosition))
		}
		return formatPlacedPosition(ShortestPositionBetween(anchorPosition, nil))
	}

	neighbor := others[neighborIndex]
	neighborPosition, err := ParsePartPosition(neighbor.Position)
	if err != nil {
		return "", err
	}
	if neighborPosition.Cmp(anchorPosition) == 0 {
		low, high := others[index].ID, neighbor.ID
		if before {
			low, high = high, low
		}
		if moved > low && moved < high {
			return others[index].Position, nil
		}
		return "", Errorf(CategoryValidation,
			"parts %s and %s share a position, and the part cannot go between them; move one of them first", low, high)
	}
	if before {
		return formatPlacedPosition(ShortestPositionBetween(neighborPosition, anchorPosition))
	}
	return formatPlacedPosition(ShortestPositionBetween(anchorPosition, neighborPosition))
}
