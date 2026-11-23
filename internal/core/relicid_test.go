package core

import (
	"fmt"
	"strings"
	"testing"
)

// relicTestID returns a canonical ULID that is distinct for each n.
func relicTestID(n int) string {
	return fmt.Sprintf("01M9A%021d", n)
}

func TestParseRelicID(t *testing.T) {
	key, body, ok := ParseRelicID("WB-R-" + relicTestID(1))
	if key != "WB" || body != relicTestID(1) || !ok {
		t.Fatalf("ParseRelicID = (%q, %q, %v)", key, body, ok)
	}
	for _, bad := range []string{
		"WB-" + relicTestID(1),
		"WB-R-",
		"WB-R-" + strings.ToLower(relicTestID(1)),
		"wb-R-" + relicTestID(1),
		"WB-r-" + relicTestID(1),
		"WB-R-R-" + relicTestID(1),
		"W-R-" + relicTestID(1),
		"WB-X-" + relicTestID(1),
		"WB-R-" + relicTestID(1) + "#" + relicTestID(2),
	} {
		if _, _, ok := ParseRelicID(bad); ok {
			t.Errorf("ParseRelicID(%q) is ok, want not ok", bad)
		}
	}
	if err := ValidateRelicIDShape("WB-" + relicTestID(1)); err == nil || !strings.Contains(err.Error(), "<KEY>-R-<ULID>") {
		t.Errorf("ValidateRelicIDShape error = %v", err)
	}
	if err := ValidateRelicIDShape("WB-R-" + relicTestID(1)); err != nil {
		t.Errorf("ValidateRelicIDShape of a valid ID = %v", err)
	}
}

func TestTaskAndRelicIDsNeverParseAsEachOther(t *testing.T) {
	ulids := []string{"00000000000000000000000000", "7ZZZZZZZZZZZZZZZZZZZZZZZZZ"}
	for range 1000 {
		u, err := CryptoULIDSource{}.New()
		if err != nil {
			t.Fatal(err)
		}
		ulids = append(ulids, u)
	}
	for _, key := range []string{"WB", "R2", "RR", "WBR", "A0", "ABCDEFGHIJ"} {
		for _, u := range ulids {
			if u[0] < '0' || u[0] > '7' {
				t.Fatalf("ULID %q does not start with 0-7", u)
			}
			task := key + "-" + u
			if _, _, ok := ParseTaskID(task); !ok {
				t.Fatalf("ParseTaskID(%q) not ok", task)
			}
			if _, _, ok := ParseRelicID(task); ok {
				t.Fatalf("ParseRelicID(%q) ok", task)
			}
			relic := NewRelicID(key, u)
			if _, _, ok := ParseRelicID(relic); !ok {
				t.Fatalf("ParseRelicID(%q) not ok", relic)
			}
			if _, _, ok := ParseTaskID(relic); ok {
				t.Fatalf("ParseTaskID(%q) ok", relic)
			}
			if ValidateTaskIDShape(relic) == nil {
				t.Fatalf("ValidateTaskIDShape(%q) is nil", relic)
			}
		}
	}
}

func TestKeySetOwnsRelic(t *testing.T) {
	set := FoundingKeySet("WB")
	if !set.OwnsRelic("WB-R-" + relicTestID(1)) {
		t.Error("does not own WB relic")
	}
	if set.OwnsRelic("XX-R-" + relicTestID(1)) {
		t.Error("owns XX relic")
	}
	if set.Owns("WB-R-" + relicTestID(1)) {
		t.Error("Owns accepts a relic ID")
	}
}

func TestParsePartAddress(t *testing.T) {
	relic, part, has, err := ParsePartAddress("A#B")
	if relic != "A" || part != "B" || !has || err != nil {
		t.Errorf("A#B = (%q, %q, %v, %v)", relic, part, has, err)
	}
	relic, part, has, err = ParsePartAddress("A")
	if relic != "A" || part != "" || has || err != nil {
		t.Errorf("A = (%q, %q, %v, %v)", relic, part, has, err)
	}
	for _, bad := range []string{"#B", "A#", "", "A#B#C"} {
		_, _, _, err := ParsePartAddress(bad)
		if CategoryOf(err) != CategoryValidation {
			t.Errorf("ParsePartAddress(%q) error = %v", bad, err)
		}
	}
	if err := ValidatePartID(relicTestID(3)); err != nil {
		t.Errorf("ValidatePartID: %v", err)
	}
	if err := ValidatePartID(strings.ToLower(relicTestID(3))); CategoryOf(err) != CategoryValidation {
		t.Errorf("lowercase part ID error = %v", err)
	}
}
