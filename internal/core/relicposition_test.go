package core

import (
	"fmt"
	"math/big"
	"math/rand"
	"strings"
	"testing"
)

func mustPosition(t *testing.T, text string) *big.Rat {
	t.Helper()
	if text == "" {
		return nil
	}
	rat, err := ParsePartPosition(text)
	if err != nil {
		t.Fatalf("ParsePartPosition(%q): %v", text, err)
	}
	return rat
}

func TestShortestPositionBetween(t *testing.T) {
	tests := []struct{ lower, upper, want string }{
		{"", "", "1/1"},
		{"1/1", "", "2/1"},
		{"7/2", "", "4/1"},
		{"", "1/1", "1/2"},
		{"", "1/2", "1/3"},
		{"1/1", "2/1", "3/2"},
		{"1/2", "1/1", "2/3"},
		{"1/3", "1/2", "2/5"},
		{"3/2", "2/1", "5/3"},
		{"5/2", "7/2", "3/1"},
		{"1/1", "3/1", "2/1"},
	}
	for _, test := range tests {
		t.Run(test.lower+"_"+test.upper, func(t *testing.T) {
			got := formatRank(ShortestPositionBetween(mustPosition(t, test.lower), mustPosition(t, test.upper)))
			if got != test.want {
				t.Fatalf("between %q and %q = %s, want %s", test.lower, test.upper, got, test.want)
			}
			if test.lower == "1/3" && got == "5/12" {
				t.Fatal("got the midpoint")
			}
		})
	}
}

func TestShortestPositionIsStrictlyBetweenAndSmallest(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 500; i++ {
		a := big.NewRat(int64(rng.Intn(3000)+1), int64(rng.Intn(1000)+1))
		b := big.NewRat(int64(rng.Intn(3000)+1), int64(rng.Intn(1000)+1))
		if a.Cmp(b) == 0 {
			continue
		}
		if a.Cmp(b) > 0 {
			a, b = b, a
		}
		got := ShortestPositionBetween(a, b)
		if got.Cmp(a) <= 0 || got.Cmp(b) >= 0 {
			t.Fatalf("%s is not strictly between %s and %s", got.RatString(), a.RatString(), b.RatString())
		}
		limit := got.Denom().Int64()
		for den := int64(1); den < limit; den++ {
			// Smallest numerator strictly above a over this denominator.
			num := new(big.Int).Mul(a.Num(), big.NewInt(den))
			num.Quo(num, a.Denom())
			num.Add(num, big.NewInt(1))
			candidate := new(big.Rat).SetFrac(num, big.NewInt(den))
			if candidate.Cmp(b) < 0 {
				t.Fatalf("%s lies between %s and %s with a smaller denominator than %s",
					candidate.RatString(), a.RatString(), b.RatString(), got.RatString())
			}
		}
	}
}

func TestTwoThousandFrontInsertsStayShort(t *testing.T) {
	parts := []PartPlacement{{ID: relicTestID(1), Position: "1/1"}}
	for i := 2; i <= 2001; i++ {
		first := parts[0]
		position, err := PlacePart(parts, relicTestID(i), first.ID, true)
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		if len(position) > 6 {
			t.Fatalf("insert %d got %q, longer than 6 bytes", i, position)
		}
		parts = append([]PartPlacement{{ID: relicTestID(i), Position: position}}, parts...)
	}
	if got := parts[0].Position; got != "1/2001" {
		t.Fatalf("last position = %s, want 1/2001", got)
	}
}

func TestAppendingCostsNothing(t *testing.T) {
	var parts []PartPlacement
	for i := 1; i <= 2000; i++ {
		position, err := AppendPartPosition(parts)
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("%d/1", i); position != want {
			t.Fatalf("append %d = %s, want %s", i, position, want)
		}
		parts = append(parts, PartPlacement{ID: relicTestID(i), Position: position})
	}
}

func TestPlacementPastThePositionLimitIsRefused(t *testing.T) {
	parts := []PartPlacement{
		{ID: relicTestID(1), Position: "1/1"},
		{ID: relicTestID(2), Position: "2/1"},
	}
	prev, last := parts[0], parts[1]
	var refusal error
	for i := 3; i < 5000; i++ {
		lower := prev
		if mustPosition(t, prev.Position).Cmp(mustPosition(t, last.Position)) > 0 {
			lower = last
		}
		position, err := PlacePart(parts, relicTestID(i), lower.ID, false)
		if err != nil {
			refusal = err
			break
		}
		if len(position) > MaxRelicPartPositionBytes {
			t.Fatalf("returned a %d-byte position", len(position))
		}
		placed := PartPlacement{ID: relicTestID(i), Position: position}
		parts = append(parts, placed)
		prev, last = last, placed
	}
	if refusal == nil {
		t.Fatal("placement was never refused")
	}
	if CategoryOf(refusal) != CategoryValidation {
		t.Fatalf("category = %v, want validation", CategoryOf(refusal))
	}
	if !strings.Contains(refusal.Error(), "256") {
		t.Fatalf("message %q does not name the limit", refusal)
	}
}

func TestParsePartPositionEnforcesTheLimit(t *testing.T) {
	valid := "1/" + strings.Repeat("9", MaxRelicPartPositionBytes-2)
	if _, err := ParsePartPosition(valid); err != nil {
		t.Fatalf("256-byte position: %v", err)
	}
	long := valid + "9"
	_, err := ParsePartPosition(long)
	if err == nil || CategoryOf(err) != CategoryValidation {
		t.Fatalf("257-byte position: %v", err)
	}
	if !strings.Contains(err.Error(), "257") || !strings.Contains(err.Error(), "256") {
		t.Fatalf("message %q lacks the size or the limit", err)
	}
	if strings.Contains(err.Error(), long) {
		t.Fatal("message echoes the value")
	}
	for _, bad := range []string{"2/4", "0/1", "1/0", "-1/2", "1", ""} {
		if _, err := ParsePartPosition(bad); err == nil || CategoryOf(err) != CategoryValidation {
			t.Fatalf("%q: err = %v, want validation", bad, err)
		}
	}
}

func TestPlacePartTies(t *testing.T) {
	parts := []PartPlacement{
		{ID: relicTestID(1), Position: "1/1"},
		{ID: relicTestID(3), Position: "1/1"},
	}
	got, err := PlacePart(parts, relicTestID(2), relicTestID(1), false)
	if err != nil || got != "1/1" {
		t.Fatalf("got %q, %v; want 1/1", got, err)
	}
	_, err = PlacePart(parts, relicTestID(4), relicTestID(1), false)
	if err == nil || CategoryOf(err) != CategoryValidation {
		t.Fatalf("err = %v, want validation", err)
	}
	for _, want := range []string{"share a position", "move one of them first"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q lacks %q", err, want)
		}
	}
}

func TestPlacePartRefusesSelfAndUnknownAnchor(t *testing.T) {
	parts := []PartPlacement{{ID: relicTestID(1), Position: "1/1"}}
	if _, err := PlacePart(parts, relicTestID(1), relicTestID(1), false); err == nil || CategoryOf(err) != CategoryValidation {
		t.Fatalf("self: %v", err)
	}
	if _, err := PlacePart(parts, relicTestID(2), relicTestID(9), false); err == nil || CategoryOf(err) != CategoryValidation {
		t.Fatalf("unknown anchor: %v", err)
	}
}

func TestPlacePartIgnoresTheMovedPart(t *testing.T) {
	parts := []PartPlacement{
		{ID: relicTestID(1), Position: "1/1"},
		{ID: relicTestID(2), Position: "2/1"},
		{ID: relicTestID(3), Position: "3/1"},
	}
	got, err := PlacePart(parts, relicTestID(2), relicTestID(1), false)
	if err != nil || got != "2/1" {
		t.Fatalf("got %q, %v; want 2/1", got, err)
	}
}

func TestSortPartPlacements(t *testing.T) {
	parts := []PartPlacement{
		{ID: relicTestID(3), Position: "2/1"},
		{ID: relicTestID(2), Position: "1/1"},
		{ID: relicTestID(1), Position: "2/1"},
	}
	if err := SortPartPlacements(parts); err != nil {
		t.Fatal(err)
	}
	want := []string{relicTestID(2), relicTestID(1), relicTestID(3)}
	for i, part := range parts {
		if part.ID != want[i] {
			t.Fatalf("order = %v", parts)
		}
	}
	if err := SortPartPlacements([]PartPlacement{{ID: "x", Position: "bad"}}); err == nil {
		t.Fatal("bad position accepted")
	}
}
