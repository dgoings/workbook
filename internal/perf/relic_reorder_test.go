package perf

import "testing"

// Mutation witness: a random pattern drawn from an unseeded generator, or a
// result that depends on map order, makes two runs of one seed disagree; a
// pattern that ignores the seed gives two seeds one result.
func TestMeasureRelicReorderIsDeterministic(t *testing.T) {
	t.Parallel()
	for _, pattern := range []RelicReorderPattern{RelicReorderRandom, RelicReorderFront, RelicReorderPingPong} {
		first, err := MeasureRelicReorder(pattern, 50, 200, 7)
		if err != nil {
			t.Fatal(err)
		}
		second, err := MeasureRelicReorder(pattern, 50, 200, 7)
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Fatalf("%s: %+v then %+v", pattern, first, second)
		}
		if first.Pattern != pattern || first.Parts != 50 || first.Moves != 200 {
			t.Fatalf("%s: result names %+v", pattern, first)
		}
	}
	// One seed gives the same result twice; a different one moves different
	// parts, which shows in the positions.
	base, err := MeasureRelicReorder(RelicReorderRandom, 50, 200, 1)
	if err != nil {
		t.Fatal(err)
	}
	for seed := int64(2); seed < 6; seed++ {
		other, err := MeasureRelicReorder(RelicReorderRandom, 50, 200, seed)
		if err != nil {
			t.Fatal(err)
		}
		if other != base {
			return
		}
	}
	t.Fatalf("random: seeds 1 to 5 all give %+v", base)
}

// Mutation witness: a placement rule that lengthens positions on every move to
// the front, or a measurement that counts a refused move as placed, or stops
// at the first refusal, misreports what a user meets at the 256-byte limit.
func TestMeasureRelicReorderReportsTheLimit(t *testing.T) {
	t.Parallel()
	front, err := MeasureRelicReorder(RelicReorderFront, 2000, 2000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if front.LongestPositionBytes > 16 || front.Refused != 0 {
		t.Fatalf("front = %+v, want positions of at most 16 bytes and no refusal", front)
	}
	pingPong, err := MeasureRelicReorder(RelicReorderPingPong, 50, 10000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pingPong.Refused == 0 || pingPong.FirstRefusedMove == 0 || pingPong.LongestPositionBytes > 256 {
		t.Fatalf("ping-pong = %+v, want refusals, a first refused move and positions of at most 256 bytes", pingPong)
	}
	if pingPong.Moves != 10000 || pingPong.Refused >= pingPong.Moves {
		t.Fatalf("ping-pong = %+v, want every move attempted and some placed", pingPong)
	}
}
