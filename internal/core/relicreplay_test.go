package core

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// replayStepFor plans one replay step and fails the test on an error.
func replayStepFor(t *testing.T, parent RelicRecord, pack RelicOperationPack, input RelicReplayInput) RelicReplayStep {
	t.Helper()
	step, err := PlanRelicReplay(parent, pack, input)
	if err != nil {
		t.Fatalf("PlanRelicReplay: %v", err)
	}
	return step
}

// requireReplayApplied checks a step that neither skipped nor stopped, and
// that its record is a child of parent.
func requireReplayApplied(t *testing.T, label string, parent RelicRecord, step RelicReplayStep) {
	t.Helper()
	if step.Skip || step.Conflict != nil {
		t.Fatalf("%s: skip %v, conflict %#v; want the pack applied", label, step.Skip, step.Conflict)
	}
	if got, want := step.Record.State.LogicalClock, parent.State.LogicalClock+1; got != want {
		t.Errorf("%s: logical clock = %d, want %d", label, got, want)
	}
}

// requireReplayRefused checks an error PlanRelicReplay returns for a relic it
// cannot replay: corrupt data, naming the relic first, and nothing planned.
func requireReplayRefused(t *testing.T, label string, step RelicReplayStep, err error) {
	t.Helper()
	requireCorruptData(t, label, err)
	if err != nil && !strings.HasPrefix(err.Error(), "relic "+relicDocRelic+": ") {
		t.Errorf("%s: error %q does not start with the relic", label, err)
	}
	if !reflect.DeepEqual(step, RelicReplayStep{}) {
		t.Errorf("%s: step = %#v, want nothing planned", label, step)
	}
}

func relicProperty(t *testing.T, record RelicRecord, name string) string {
	t.Helper()
	value, ok := record.State.Relic.Properties[name]
	if !ok {
		t.Fatalf("property %q is missing", name)
	}
	return string(value)
}

func TestPlanRelicReplayAppliesIndependentChanges(t *testing.T) {
	parent := mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2,
		RelicOperation{ID: relicTestID(300), Type: RelicOperationPartFieldSet, Part: relicDocPartA, Name: "heading",
			Value: json.RawMessage(`"Theirs"`)},
		RelicOperation{ID: relicTestID(301), Type: RelicOperationPropertySet, Name: "p", Value: json.RawMessage(`1`)}))

	// The pack was written at clock 2, beside the parent's own pack.
	pack := relicFoldPack(2,
		RelicOperation{ID: relicTestID(302), Type: RelicOperationPropertySet, Name: "q", Value: json.RawMessage(`2`)},
		RelicOperation{ID: relicTestID(303), Type: RelicOperationPartMove, Part: relicDocPartB, Position: "1/2"})
	step := replayStepFor(t, parent, pack, RelicReplayInput{})
	requireReplayApplied(t, "independent changes", parent, step)

	if got := relicProperty(t, step.Record, "p"); got != "1" {
		t.Errorf("property p = %s, want the parent's 1", got)
	}
	if got := relicProperty(t, step.Record, "q"); got != "2" {
		t.Errorf("property q = %s, want the pack's 2", got)
	}
	if got := requirePartForTest(t, step.Record.State.Relic, relicDocPartB).Position; got != "1/2" {
		t.Errorf("part B position = %s, want 1/2", got)
	}
	if got := step.Record.State.Relic.Parts[0].ID; got != relicDocPartB {
		t.Errorf("first part = %s, want part B, which moved before part A", got)
	}
	if pack.LogicalClock != 2 {
		t.Errorf("the caller's pack was changed: clock = %d", pack.LogicalClock)
	}
}

func TestPlanRelicReplayLaterValueWins(t *testing.T) {
	parent := mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2,
		RelicOperation{ID: relicTestID(310), Type: RelicOperationTitleSet, Title: "Theirs"},
		RelicOperation{ID: relicTestID(311), Type: RelicOperationPropertySet, Name: "p", Value: json.RawMessage(`"theirs"`)},
		RelicOperation{ID: relicTestID(312), Type: RelicOperationPartMove, Part: relicDocPartB, Position: "5/1"}))

	step := replayStepFor(t, parent, relicFoldPack(2,
		RelicOperation{ID: relicTestID(313), Type: RelicOperationTitleSet, Title: "Ours"},
		RelicOperation{ID: relicTestID(314), Type: RelicOperationPropertySet, Name: "p", Value: json.RawMessage(`"ours"`)},
		RelicOperation{ID: relicTestID(315), Type: RelicOperationPartMove, Part: relicDocPartB, Position: "1/2"}),
		RelicReplayInput{})
	requireReplayApplied(t, "the same title, property and position", parent, step)

	if got := step.Record.State.Relic.Title; got != "Ours" {
		t.Errorf("title = %q, want the pack's", got)
	}
	if got := relicProperty(t, step.Record, "p"); got != `"ours"` {
		t.Errorf("property p = %s, want the pack's", got)
	}
	if got := requirePartForTest(t, step.Record.State.Relic, relicDocPartB).Position; got != "1/2" {
		t.Errorf("part B position = %s, want the pack's 1/2", got)
	}
}

func TestPlanRelicReplayStopsOnAStaleBody(t *testing.T) {
	theirs, ours := relicTestID(320), relicTestID(321)
	// Origin saved part A's body from its first version, which is the part's ID.
	parent := mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2, RelicOperation{ID: theirs,
		Type: RelicOperationPartBodySet, Part: relicDocPartA, Body: &RelicBodyChange{Format: "md", Bytes: 20, Base: relicDocPartA}}))
	// So did this clone, from the same version.
	stale := relicFoldPack(2, RelicOperation{ID: ours, Type: RelicOperationPartBodySet, Part: relicDocPartA,
		Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: relicDocPartA}})

	t.Run("different text is a conflict", func(t *testing.T) {
		for label, input := range map[string]RelicReplayInput{
			"no account of the bodies": {},
			"another part agrees":      {SameBody: map[string]bool{relicDocPartB: true}},
			"the part disagrees":       {SameBody: map[string]bool{relicDocPartA: false}},
		} {
			step := replayStepFor(t, parent, stale, input)
			if step.Conflict == nil {
				t.Fatalf("%s: no conflict; the fold would drop the saved text without a word", label)
			}
			if step.Conflict.Type != RelicConflictBody || step.Conflict.Part != relicDocPartA ||
				step.Conflict.RelicID != relicDocRelic {
				t.Errorf("%s: conflict = %#v, want a body conflict for part A of the relic", label, step.Conflict)
			}
			if step.ConflictOperation != ours {
				t.Errorf("%s: conflict operation = %q, want the stale save %s", label, step.ConflictOperation, ours)
			}
			if step.Skip || !reflect.DeepEqual(step.Record, RelicRecord{}) {
				t.Errorf("%s: skip %v, record %#v; want neither", label, step.Skip, step.Record)
			}
		}
	})

	t.Run("the same text and label is agreement", func(t *testing.T) {
		step := replayStepFor(t, parent, stale, RelicReplayInput{SameBody: map[string]bool{relicDocPartA: true}})
		requireReplayApplied(t, "agreeing bodies", parent, step)
		body := requirePartForTest(t, step.Record.State.Relic, relicDocPartA).Body
		if body == nil || body.Version != theirs || body.Bytes != 20 {
			t.Errorf("body = %#v, want the parent's version", body)
		}
	})

	t.Run("a save from the current version applies", func(t *testing.T) {
		second := relicTestID(322)
		step := replayStepFor(t, parent, relicFoldPack(2,
			RelicOperation{ID: ours, Type: RelicOperationPartBodySet, Part: relicDocPartA,
				Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: theirs}},
			// A second save in the same pack starts from the first.
			RelicOperation{ID: second, Type: RelicOperationPartBodySet, Part: relicDocPartA,
				Body: &RelicBodyChange{Format: "md", Bytes: 40, Base: ours}}),
			RelicReplayInput{})
		requireReplayApplied(t, "a current base", parent, step)
		if body := requirePartForTest(t, step.Record.State.Relic, relicDocPartA).Body; body == nil || body.Version != second {
			t.Errorf("body = %#v, want the pack's last save", body)
		}
	})

	t.Run("two first bodies for one part are a conflict", func(t *testing.T) {
		withBody := mustApplyRelic(t, parent, relicFoldPack(3, RelicOperation{ID: relicTestID(323),
			Type: RelicOperationPartBodySet, Part: relicDocPartB, Body: &RelicBodyChange{Format: "md", Bytes: 5}}))
		step := replayStepFor(t, withBody, relicFoldPack(2, RelicOperation{ID: relicTestID(324),
			Type: RelicOperationPartBodySet, Part: relicDocPartB, Body: &RelicBodyChange{Format: "md", Bytes: 6}}),
			RelicReplayInput{})
		if step.Conflict == nil || step.Conflict.Type != RelicConflictBody || step.Conflict.Part != relicDocPartB {
			t.Errorf("conflict = %#v, want a body conflict for part B", step.Conflict)
		}
		if step.ConflictOperation != relicTestID(324) {
			t.Errorf("conflict operation = %q, want %s", step.ConflictOperation, relicTestID(324))
		}
	})

	t.Run("a body for a part the relic does not have is refused", func(t *testing.T) {
		step, err := PlanRelicReplay(parent, relicFoldPack(2, RelicOperation{ID: relicTestID(325),
			Type: RelicOperationPartBodySet, Part: relicTestID(326), Body: &RelicBodyChange{Format: "md", Bytes: 6}}),
			RelicReplayInput{})
		requireReplayRefused(t, "an unknown part", step, err)
	})
}

func TestPlanRelicReplayOnADeletedRelic(t *testing.T) {
	live := goldenRelicRecord(t)
	deleted := mustApplyRelic(t, live, relicFoldPack(2,
		RelicOperation{ID: relicTestID(330), Type: RelicOperationTombstone}))
	title := RelicOperation{ID: relicTestID(331), Type: RelicOperationTitleSet, Title: "Edited"}
	restore := RelicOperation{ID: relicTestID(332), Type: RelicOperationRestore}
	tombstone := RelicOperation{ID: relicTestID(333), Type: RelicOperationTombstone}

	for label, tc := range map[string]struct {
		operations []RelicOperation
		blocked    RelicOperation
	}{
		"an edit":                 {[]RelicOperation{title}, title},
		"an edit, then a delete":  {[]RelicOperation{title, tombstone}, title},
		"a restore":               {[]RelicOperation{restore}, restore},
		"a restore, then an edit": {[]RelicOperation{restore, title}, title},
	} {
		step := replayStepFor(t, deleted, relicFoldPack(2, tc.operations...), RelicReplayInput{})
		if step.Conflict == nil || step.Conflict.Type != RelicConflictTombstone || step.Conflict.Tombstone == nil {
			t.Fatalf("%s onto a deleted relic: conflict = %#v, want a tombstone conflict", label, step.Conflict)
		}
		if step.Conflict.RelicID != relicDocRelic || step.Conflict.Part != "" || step.Conflict.Body != nil {
			t.Errorf("%s: conflict = %#v, want the relic named and no part or body", label, step.Conflict)
		}
		if got := *step.Conflict.Tombstone; got.OperationID != tc.blocked.ID || got.Operation != tc.blocked.Type {
			t.Errorf("%s: blocked operation = %#v, want %s %s", label, got, tc.blocked.Type, tc.blocked.ID)
		}
		if step.ConflictOperation != tc.blocked.ID {
			t.Errorf("%s: conflict operation = %q, want %s", label, step.ConflictOperation, tc.blocked.ID)
		}
		if step.Skip || !reflect.DeepEqual(step.Record, RelicRecord{}) {
			t.Errorf("%s: skip %v, record %#v; want neither", label, step.Skip, step.Record)
		}
	}

	// The deleted-relic rule is the plan's rule 1 and runs before rule 2, so
	// a pack origin already holds still stops on a relic origin then deleted.
	held := replayStepFor(t, deleted, relicFoldPack(2, title),
		RelicReplayInput{Held: map[string]RelicOperation{title.ID: title}})
	if held.Conflict == nil || held.Conflict.Type != RelicConflictTombstone || held.Skip {
		t.Errorf("a held edit onto a deleted relic: step = %#v, want a tombstone conflict", held)
	}

	for label, step := range map[string]RelicReplayStep{
		"a delete of a deleted relic": replayStepFor(t, deleted, relicFoldPack(2, tombstone), RelicReplayInput{}),
		"a restore of a live relic":   replayStepFor(t, live, relicFoldPack(2, restore), RelicReplayInput{}),
	} {
		if !step.Skip || step.Conflict != nil || !reflect.DeepEqual(step.Record, RelicRecord{}) {
			t.Errorf("%s: step = %#v, want only a skip", label, step)
		}
	}

	// A delete of a live relic applies, whatever origin did to it first.
	edited := mustApplyRelic(t, live, relicFoldPack(2, title))
	step := replayStepFor(t, edited, relicFoldPack(2, tombstone), RelicReplayInput{})
	requireReplayApplied(t, "a delete over an edit", edited, step)
	if !step.Record.State.Relic.Deleted || step.Record.State.Relic.Title != "Edited" {
		t.Errorf("relic = %#v, want it deleted and holding the edit", step.Record.State.Relic)
	}
}

func TestPlanRelicReplaySkipsWhatIsAlreadyHeld(t *testing.T) {
	parent := goldenRelicRecord(t)
	title := RelicOperation{ID: relicTestID(340), Type: RelicOperationTitleSet, Title: "Edited"}
	add := RelicOperation{ID: relicTestID(341), Type: RelicOperationPartAdd, PartType: "note", Position: "9/1",
		Body: &RelicBodyChange{Format: "md", Bytes: 12}}
	pack := relicFoldPack(2, title, add)

	// Origin's side holds both operations, under whatever commit, and more.
	withBoth := mustApplyRelic(t, parent, relicFoldPack(2, title, add))
	other := RelicOperation{ID: relicTestID(342), Type: RelicOperationPropertyClear, Name: "status"}
	step := replayStepFor(t, withBoth, pack, RelicReplayInput{Held: map[string]RelicOperation{
		title.ID: title,
		// A copy read back from storage is equal without being the same value.
		add.ID:   {ID: add.ID, Type: add.Type, PartType: "note", Position: "9/1", Body: &RelicBodyChange{Format: "md", Bytes: 12}},
		other.ID: other,
	}})
	if !step.Skip || step.Conflict != nil || !reflect.DeepEqual(step.Record, RelicRecord{}) {
		t.Errorf("every operation held: step = %#v, want only a skip", step)
	}

	step, err := PlanRelicReplay(parent, pack, RelicReplayInput{Held: map[string]RelicOperation{title.ID: title}})
	requireReplayRefused(t, "one of two operations held", step, err)

	retitled := title
	retitled.Title = "Something else"
	step, err = PlanRelicReplay(parent, relicFoldPack(2, title),
		RelicReplayInput{Held: map[string]RelicOperation{title.ID: retitled}})
	requireReplayRefused(t, "an ID held with another title", step, err)
	if err != nil && !strings.Contains(err.Error(), title.ID) {
		t.Errorf("error %q does not name the operation", err)
	}

	resized := add
	resized.Body = &RelicBodyChange{Format: "md", Bytes: 13}
	step, err = PlanRelicReplay(parent, relicFoldPack(2, title, add),
		RelicReplayInput{Held: map[string]RelicOperation{title.ID: title, add.ID: resized}})
	requireReplayRefused(t, "an ID held with another body", step, err)

	// Nothing held is nothing skipped.
	requireReplayApplied(t, "nothing held", parent, replayStepFor(t, parent, pack, RelicReplayInput{
		Held: map[string]RelicOperation{other.ID: other},
	}))
}

// relicValueOfSize is one stored string value of exactly size bytes.
func relicValueOfSize(size int) json.RawMessage {
	return json.RawMessage(`"` + strings.Repeat("x", size-2) + `"`)
}

func TestPlanRelicReplayNeverRefusesATotal(t *testing.T) {
	parent := mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2,
		RelicOperation{ID: relicTestID(350), Type: RelicOperationPropertySet, Name: "theirs", Value: relicValueOfSize(40 * 1024)}))
	step := replayStepFor(t, parent, relicFoldPack(2,
		RelicOperation{ID: relicTestID(351), Type: RelicOperationPropertySet, Name: "ours", Value: relicValueOfSize(30 * 1024)}),
		RelicReplayInput{})
	requireReplayApplied(t, "two properties past their total", parent, step)
	if got := len(relicProperty(t, step.Record, "theirs")); got != 40*1024 {
		t.Errorf("the parent's property is %d bytes, want it kept", got)
	}
	if got := len(relicProperty(t, step.Record, "ours")); got != 30*1024 {
		t.Errorf("the pack's property is %d bytes, want it applied", got)
	}

	encoded, err := json.Marshal(step.Record.State.Relic.Properties)
	if err != nil || len(encoded) <= MaxRelicPropertiesBytes {
		t.Fatalf("the properties are %d bytes, %v; want them past %d", len(encoded), err, MaxRelicPropertiesBytes)
	}
	warnings := RelicOverLimitWarnings(relicDocRelic, parent, step.Record)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want one", warnings)
	}
	warning := warnings[0]
	if warning.Code != "relic-over-limit" {
		t.Errorf("code = %q, want relic-over-limit", warning.Code)
	}
	if !strings.HasPrefix(warning.Message, "relic "+relicDocRelic+": ") {
		t.Errorf("message %q does not start with the relic", warning.Message)
	}
	for _, want := range []string{strconv.Itoa(len(encoded)), "65536"} {
		if !strings.Contains(warning.Message, want) {
			t.Errorf("message %q does not hold %s", warning.Message, want)
		}
	}
	if strings.Contains(warning.Message, "xxxx") {
		t.Errorf("the message repeats a property value")
	}
	// The size is the one a save is refused for.
	refusal := checkRelicPropertiesGrowth(parent.State.Relic.Properties, step.Record.State.Relic.Properties)
	if refusal == nil || !strings.Contains(refusal.Error(), strconv.Itoa(len(encoded))+" bytes") {
		t.Errorf("a save past the total is refused with %v, want the same size", refusal)
	}

	// A relic that was already over is not reported again.
	again := mustApplyRelic(t, step.Record, relicFoldPack(4,
		RelicOperation{ID: relicTestID(352), Type: RelicOperationPropertySet, Name: "more", Value: relicValueOfSize(1024)}))
	if got := RelicOverLimitWarnings(relicDocRelic, step.Record, again); len(got) != 0 {
		t.Errorf("warnings for a relic already past the total = %#v, want none", got)
	}
}

func TestPlanRelicReplayNeverRefusesACount(t *testing.T) {
	operations := []RelicOperation{{ID: relicDocCreateOp, Type: RelicOperationCreate,
		Relic: &RelicCreateData{Title: "Many parts", Type: "spec", CreatedAt: relicDocTime}}}
	for index := 0; index < MaxRelicLiveParts; index++ {
		operations = append(operations, RelicOperation{ID: relicTestID(10000 + index), Type: RelicOperationPartAdd,
			PartType: "section", Position: strconv.Itoa(index+1) + "/1"})
	}
	parent, err := ApplyRelic(nil, relicFoldPack(1, operations...))
	if err != nil {
		t.Fatalf("ApplyRelic(a relic of %d parts): %v", MaxRelicLiveParts, err)
	}
	if got := RelicOverLimitWarnings(relicDocRelic, RelicRecord{}, parent); len(got) != 0 {
		t.Fatalf("warnings at the limit = %#v, want none", got)
	}

	step := replayStepFor(t, parent, relicFoldPack(2, RelicOperation{ID: relicTestID(360), Type: RelicOperationPartAdd,
		PartType: "section", Position: "1/2"}), RelicReplayInput{})
	requireReplayApplied(t, "one part past the count", parent, step)
	if got := len(step.Record.State.Relic.LiveParts()); got != MaxRelicLiveParts+1 {
		t.Fatalf("live parts = %d, want %d", got, MaxRelicLiveParts+1)
	}

	warnings := RelicOverLimitWarnings(relicDocRelic, parent, step.Record)
	if len(warnings) != 1 || warnings[0].Code != WarningRelicOverLimit {
		t.Fatalf("warnings = %#v, want one relic-over-limit", warnings)
	}
	for _, want := range []string{"relic " + relicDocRelic + ": ", "2001", "2000"} {
		if !strings.Contains(warnings[0].Message, want) {
			t.Errorf("message %q does not hold %q", warnings[0].Message, want)
		}
	}
}

func TestRelicOverLimitWarnings(t *testing.T) {
	// relicWith is a relic of the given parts; removed says how many of them,
	// from the front, are removed, and each live one has a body of bodyBytes.
	relicWith := func(parts, removed int, bodyBytes int64) RelicRecord {
		record := RelicRecord{State: RelicStateDocument{RelicID: relicDocRelic,
			Relic: RelicData{Properties: map[string]json.RawMessage{}, Parts: make([]RelicPart, parts)}}}
		for index := range record.State.Relic.Parts {
			part := RelicPart{ID: relicTestID(20000 + index), Type: "section", Position: strconv.Itoa(index+1) + "/1"}
			if index < removed {
				part.Removed = true
			} else if bodyBytes > 0 {
				part.Body = &RelicBody{Version: part.ID, Bytes: bodyBytes}
			}
			record.State.Relic.Parts[index] = part
		}
		return record
	}

	for label, tc := range map[string]struct {
		before, after RelicRecord
		want          []string
	}{
		"nothing over": {relicWith(10, 0, 100), relicWith(11, 1, 100), nil},
		"the parts counting removed ones": {relicWith(MaxRelicParts, 3000, 0), relicWith(MaxRelicParts+1, 3000, 0),
			[]string{"4001", "4000", "removed"}},
		"the live bodies together": {relicWith(16, 0, MaxRelicBodyBytes), relicWith(17, 0, MaxRelicBodyBytes),
			[]string{strconv.Itoa(17 * MaxRelicBodyBytes), strconv.Itoa(MaxRelicLiveBodyBytes)}},
		"already over, and larger": {relicWith(MaxRelicParts+1, 3000, 0), relicWith(MaxRelicParts+2, 3000, 0), nil},
		"over, and brought back":   {relicWith(MaxRelicParts+1, 3000, 0), relicWith(MaxRelicParts, 3000, 0), nil},
	} {
		warnings := RelicOverLimitWarnings(relicDocRelic, tc.before, tc.after)
		if tc.want == nil {
			if len(warnings) != 0 {
				t.Errorf("%s: warnings = %#v, want none", label, warnings)
			}
			continue
		}
		if len(warnings) != 1 || warnings[0].Code != WarningRelicOverLimit ||
			!strings.HasPrefix(warnings[0].Message, "relic "+relicDocRelic+": ") {
			t.Errorf("%s: warnings = %#v, want one relic-over-limit naming the relic", label, warnings)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(warnings[0].Message, want) {
				t.Errorf("%s: message %q does not hold %q", label, warnings[0].Message, want)
			}
		}
	}

	// Each limit passed is its own warning: live parts and all parts here.
	both := RelicOverLimitWarnings(relicDocRelic, relicWith(MaxRelicLiveParts, 0, 0), relicWith(MaxRelicParts+1, 0, 0))
	if len(both) != 2 {
		t.Errorf("warnings for two limits passed = %#v, want two", both)
	}

	fields := RelicFieldsOverLimitWarning(relicDocRelic, relicDocPartA, 18500)
	if fields.Code != WarningRelicOverLimit {
		t.Errorf("fields warning code = %q, want relic-over-limit", fields.Code)
	}
	for _, want := range []string{"relic " + relicDocRelic + ": ", relicDocPartA, "18500", "16384"} {
		if !strings.Contains(fields.Message, want) {
			t.Errorf("fields warning %q does not hold %q", fields.Message, want)
		}
	}
}

func TestPlanRelicReplayReturnsWhatTheFoldRefuses(t *testing.T) {
	parent := goldenRelicRecord(t)
	for label, operation := range map[string]RelicOperation{
		"a body save that takes the ID of a part": {ID: relicDocPartB, Type: RelicOperationPartBodySet,
			Part: relicDocPartA, Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: relicDocPartA}},
		// The fold's refusal is not turned into a conflict by a stale base.
		"a stale body save that takes the ID of a part": {ID: relicDocPartB, Type: RelicOperationPartBodySet,
			Part: relicDocPartA, Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: relicTestID(370)}},
		"one property value over its own limit": {ID: relicTestID(371), Type: RelicOperationPropertySet, Name: "k",
			Value: relicValueOfSize(MaxRelicPropertyValueBytes + 1)},
		"a restore with an edit, onto a live relic": {ID: relicTestID(372), Type: RelicOperationRestore},
	} {
		operations := []RelicOperation{operation}
		if operation.Type == RelicOperationRestore {
			operations = append(operations, RelicOperation{ID: relicTestID(373), Type: RelicOperationTitleSet, Title: "Edited"})
		}
		step, err := PlanRelicReplay(parent, relicFoldPack(2, operations...), RelicReplayInput{})
		requireReplayRefused(t, label, step, err)
	}

	// A relic from a newer Workbook is refused as that, before any rule here
	// judges a document this build may not understand.
	newer := parent
	newer.State.MinReader = SupportedFormatGeneration + 1
	step, err := PlanRelicReplay(newer, relicFoldPack(2,
		RelicOperation{ID: relicTestID(374), Type: RelicOperationTitleSet, Title: "Edited"}), RelicReplayInput{})
	if CategoryOf(err) != CategoryNewerWriter || !strings.HasPrefix(err.Error(), "relic "+relicDocRelic) {
		t.Errorf("a newer relic: error = %v, want newer-writer naming the relic", err)
	}
	if !reflect.DeepEqual(step, RelicReplayStep{}) {
		t.Errorf("a newer relic: step = %#v, want nothing planned", step)
	}
}

// relicBodyConflictForTest is a body conflict as a sync reports it, with the
// three texts filled in.
func relicBodyConflictForTest() (RelicConflict, []string) {
	texts := []string{"zebra words both started from\n", "quagga words saved here\n", "okapi words origin holds\n"}
	base := RelicBodyVersion{Version: relicDocPartA, Format: "md", Bytes: int64(len(texts[0])), Object: "aaaa1111"}
	ours := RelicBodyVersion{Version: relicTestID(380), Format: "md", Bytes: int64(len(texts[1])), Object: "bbbb2222"}
	theirs := RelicBodyVersion{Version: relicTestID(381), Format: "md", Bytes: int64(len(texts[2])), Object: "cccc3333"}
	SetRelicBodyVersionContent(&base, []byte(texts[0]))
	SetRelicBodyVersionContent(&ours, []byte(texts[1]))
	SetRelicBodyVersionContent(&theirs, []byte(texts[2]))
	return RelicConflict{
		RelicID:    relicDocRelic,
		Type:       RelicConflictBody,
		Part:       relicDocPartA,
		Body:       &RelicBodyConflict{Base: &base, Ours: ours, Theirs: theirs},
		ParkedRef:  "refs/workbook/reconciled-relics/" + relicDocRelic + "/1",
		Unreplayed: 2,
	}, texts
}

func TestRelicConflictDetailIsSafeAndComplete(t *testing.T) {
	conflict, texts := relicBodyConflictForTest()
	detail := RelicConflictDetail(conflict)
	theirs := conflict.Body.Theirs.Version
	for label, want := range map[string]string{
		"the relic first":    "relic " + relicDocRelic + ": ",
		"the part":           "part " + relicDocPartA,
		"the parked ref":     conflict.ParkedRef,
		"the remote":         "origin",
		"the first command":  "git cat-file blob " + conflict.ParkedRef + ":parts/" + relicDocPartA + "/body > mine.md",
		"the second command": "workbook relic show " + relicDocRelic + "#" + relicDocPartA + " --body > current.md",
		"the third command": "workbook relic update " + relicDocRelic + " --part " + relicDocPartA +
			" --body-file mine.md --base " + theirs,
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("the detail does not hold %s, %q:\n%s", label, want, detail)
		}
	}
	if !strings.HasPrefix(detail, "relic "+relicDocRelic+": ") {
		t.Errorf("the detail does not start with the relic:\n%s", detail)
	}
	for _, text := range texts {
		if strings.Contains(detail, strings.TrimSpace(text)) {
			t.Errorf("the detail holds body text %q:\n%s", text, detail)
		}
	}
	if strings.ContainsAny(detail, "\n\r") {
		t.Errorf("the detail is more than one line:\n%s", detail)
	}

	conflict.Remote = "upstream"
	if named := RelicConflictDetail(conflict); !strings.Contains(named, "upstream") || strings.Contains(named, "origin") {
		t.Errorf("the detail for another remote does not name it alone:\n%s", named)
	}

	// Before a sync has parked the local tip and read the versions, the detail
	// still names the relic and the part and says what to do.
	bare := RelicConflictDetail(RelicConflict{RelicID: relicDocRelic, Type: RelicConflictBody, Part: relicDocPartA})
	for _, want := range []string{"relic " + relicDocRelic + ": ", "part " + relicDocPartA, "workbook relic update"} {
		if !strings.Contains(bare, want) {
			t.Errorf("the detail without a parked ref does not hold %q:\n%s", want, bare)
		}
	}

	tombstone := RelicConflict{
		RelicID:   relicDocRelic,
		Type:      RelicConflictTombstone,
		Tombstone: &RelicTombstoneConflict{OperationID: relicTestID(382), Operation: RelicOperationTitleSet},
		ParkedRef: "refs/workbook/reconciled-relics/" + relicDocRelic + "/2",
	}
	deleted := RelicConflictDetail(tombstone)
	for _, want := range []string{"relic " + relicDocRelic + ": ", "origin", "title.set", tombstone.ParkedRef,
		"workbook relic restore " + relicDocRelic} {
		if !strings.Contains(deleted, want) {
			t.Errorf("the tombstone detail does not hold %q:\n%s", want, deleted)
		}
	}
	if !strings.HasPrefix(deleted, "relic "+relicDocRelic+": ") || strings.ContainsAny(deleted, "\n\r") {
		t.Errorf("the tombstone detail is not one line that starts with the relic:\n%s", deleted)
	}

	one := RelicConflictError([]RelicConflict{conflict})
	if CategoryOf(one) != CategoryConflict || one.Error() != RelicConflictDetail(conflict) {
		t.Errorf("RelicConflictError(one) = %v, want a conflict saying the detail", one)
	}
	several := RelicConflictError([]RelicConflict{conflict, tombstone})
	if CategoryOf(several) != CategoryConflict || !strings.Contains(several.Error(), "2 relic") {
		t.Errorf("RelicConflictError(two) = %v, want a conflict counting them", several)
	}
	for _, text := range texts {
		if strings.Contains(one.Error(), strings.TrimSpace(text)) || strings.Contains(several.Error(), strings.TrimSpace(text)) {
			t.Errorf("a conflict error holds body text %q", text)
		}
	}
}

// relicUnappliedForTest is count local operations a stopped replay did not
// apply, alternating property.set and title.set, the first two in one commit.
func relicUnappliedForTest(count int) []RelicUnappliedOperation {
	unapplied := make([]RelicUnappliedOperation, count)
	for index := range unapplied {
		kind := RelicOperationPropertySet
		if index%2 == 1 {
			kind = RelicOperationTitleSet
		}
		commit := "c0ffee" + strconv.Itoa(index)
		if index == 1 {
			commit = unapplied[0].Commit
		}
		unapplied[index] = RelicUnappliedOperation{OperationID: relicTestID(400 + index), Operation: kind, Commit: commit}
	}
	return unapplied
}

func TestRelicConflictDetailNamesTheUnappliedChanges(t *testing.T) {
	conflict, _ := relicBodyConflictForTest()
	theirs := conflict.Body.Theirs.Version
	body := "relic " + relicDocRelic + ": part " + relicDocPartA + ": the body was saved here and on origin from the same version; " +
		"the text from origin stands; the text saved here is kept at " + conflict.ParkedRef + "; run `git cat-file blob " +
		conflict.ParkedRef + ":parts/" + relicDocPartA + "/body > mine.md` to write it, `git cat-file blob " + conflict.Body.Base.Object +
		" > base.md` to write the text the save here started from, `workbook relic show " + relicDocRelic +
		"#" + relicDocPartA + " --body > current.md` to write the text that stands, combine the edits in mine.md, then run " +
		"`workbook relic update " + relicDocRelic + " --part " + relicDocPartA + " --body-file mine.md --base " + theirs + "`"
	tombstone := RelicConflict{
		RelicID:   relicDocRelic,
		Type:      RelicConflictTombstone,
		Tombstone: &RelicTombstoneConflict{OperationID: relicTestID(382), Operation: RelicOperationTitleSet},
		ParkedRef: "refs/workbook/reconciled-relics/" + relicDocRelic + "/2",
	}
	deleted := "relic " + relicDocRelic + ": origin deleted this relic, so the local title.set operation " + relicTestID(382) +
		" cannot apply; the local changes are kept at " + tombstone.ParkedRef + "; to keep them, run `workbook relic restore " +
		relicDocRelic + "` and make the changes again"

	// With nothing else unapplied, each detail is the one it always was.
	for _, unapplied := range [][]RelicUnappliedOperation{nil, {}} {
		conflict.Unapplied, tombstone.Unapplied = unapplied, unapplied
		if got := RelicConflictDetail(conflict); got != body {
			t.Errorf("body detail with %#v unapplied =\n%s\nwant\n%s", unapplied, got, body)
		}
		if got := RelicConflictDetail(tombstone); got != deleted {
			t.Errorf("tombstone detail with %#v unapplied =\n%s\nwant\n%s", unapplied, got, deleted)
		}
	}

	named := func(unapplied []RelicUnappliedOperation) string {
		listed := make([]string, len(unapplied))
		for index, operation := range unapplied {
			listed[index] = string(operation.Operation) + " " + operation.OperationID
		}
		return strings.Join(listed, ", ")
	}
	all := relicUnappliedForTest(7)
	for _, check := range []struct {
		count int
		names string
	}{
		{1, named(all[:1])},
		{5, named(all[:5])},
		{7, named(all[:5]) + " and 2 more"},
	} {
		unapplied := relicUnappliedForTest(check.count)
		for _, kind := range []struct {
			name, detail, after string
			conflict            RelicConflict
		}{
			{"body", body, "saving the body", conflict},
			{"tombstone", deleted, "the restore", tombstone},
		} {
			kind.conflict.Unapplied = unapplied
			want := kind.detail + "; " + strconv.Itoa(check.count) + " other local change(s) were not applied either: " +
				check.names + "; they are kept at " + kind.conflict.ParkedRef + "; `git cat-file blob " + unapplied[0].Commit +
				":operation.json` shows the first, and the report's `unapplied` list under --json gives the commit of each; " +
				"make them again after " + kind.after
			if got := RelicConflictDetail(kind.conflict); got != want {
				t.Errorf("%s detail with %d unapplied =\n%s\nwant\n%s", kind.name, check.count, got, want)
			}
		}
	}
}

func TestRelicRemovedPartWarnings(t *testing.T) {
	remove := relicFoldPack(2, RelicOperation{ID: relicTestID(390), Type: RelicOperationPartRemove, Part: relicDocPartA})
	field := relicFoldPack(2, RelicOperation{ID: relicTestID(391), Type: RelicOperationPartFieldSet, Part: relicDocPartA,
		Name: "heading", Value: json.RawMessage(`"Edited"`)})
	body := relicFoldPack(2, RelicOperation{ID: relicTestID(392), Type: RelicOperationPartBodySet, Part: relicDocPartA,
		Body: &RelicBodyChange{Format: "md", Bytes: 30, Base: relicDocPartA}})
	move := relicFoldPack(3, RelicOperation{ID: relicTestID(393), Type: RelicOperationPartMove, Part: relicDocPartA, Position: "9/1"})
	clear := relicFoldPack(2, RelicOperation{ID: relicTestID(394), Type: RelicOperationPartFieldClear, Part: relicDocPartA, Name: "heading"})
	elsewhere := relicFoldPack(2,
		RelicOperation{ID: relicTestID(395), Type: RelicOperationPartMove, Part: relicDocPartB, Position: "7/1"},
		RelicOperation{ID: relicTestID(396), Type: RelicOperationTitleSet, Title: "Edited"})
	restore := relicFoldPack(3, RelicOperation{ID: relicTestID(397), Type: RelicOperationPartRestore, Part: relicDocPartA})

	removed := mustApplyRelic(t, goldenRelicRecord(t), remove)
	live := goldenRelicRecord(t)

	for label, tc := range map[string]struct {
		local, remote []RelicOperationPack
		result        RelicRecord
		want          int
	}{
		"removed here, a field set on origin":     {[]RelicOperationPack{remove}, []RelicOperationPack{field}, removed, 1},
		"removed on origin, a body saved here":    {[]RelicOperationPack{body}, []RelicOperationPack{remove}, removed, 1},
		"removed on origin, moved here":           {[]RelicOperationPack{elsewhere, move}, []RelicOperationPack{remove}, removed, 1},
		"removed here, a field cleared on origin": {[]RelicOperationPack{remove}, []RelicOperationPack{clear}, removed, 1},
		"several edits are one warning":           {[]RelicOperationPack{remove}, []RelicOperationPack{field, clear, move}, removed, 1},
		"one side did both":                       {[]RelicOperationPack{remove, move}, []RelicOperationPack{elsewhere}, removed, 0},
		"the other side edited another part":      {[]RelicOperationPack{remove}, []RelicOperationPack{elsewhere}, removed, 0},
		"removed on both sides, edited on none":   {[]RelicOperationPack{remove}, []RelicOperationPack{remove}, removed, 0},
		"the part is live again":                  {[]RelicOperationPack{remove, restore}, []RelicOperationPack{field}, live, 0},
		// Operations both sides hold were not made apart from each other.
		"both sides hold the same operations": {[]RelicOperationPack{remove, move}, []RelicOperationPack{remove, move}, removed, 0},
		"nothing on either side":              {nil, nil, removed, 0},
	} {
		warnings := RelicRemovedPartWarnings(relicDocRelic, tc.local, tc.remote, tc.result)
		if len(warnings) != tc.want {
			t.Errorf("%s: warnings = %#v, want %d", label, warnings, tc.want)
			continue
		}
		for _, warning := range warnings {
			if warning.Code != "relic-edit-on-removed-part" {
				t.Errorf("%s: code = %q, want relic-edit-on-removed-part", label, warning.Code)
			}
			if !strings.HasPrefix(warning.Message, "relic "+relicDocRelic+": ") ||
				!strings.Contains(warning.Message, "part "+relicDocPartA) {
				t.Errorf("%s: message %q does not name the relic and the part", label, warning.Message)
			}
			// Showing the part is the read that changes nothing, so it comes
			// first; restoring it is a shared change.
			show := strings.Index(warning.Message, "`workbook relic show "+relicDocRelic+"#"+relicDocPartA+"`")
			restore := strings.Index(warning.Message, "--restore-part")
			if show < 0 || (restore >= 0 && restore < show) {
				t.Errorf("%s: message %q does not point at relic show before --restore-part", label, warning.Message)
			}
		}
	}

	// One warning per part, in the order the relic lists its parts.
	bothRemoved := mustApplyRelic(t, removed, relicFoldPack(3,
		RelicOperation{ID: relicTestID(398), Type: RelicOperationPartRemove, Part: relicDocPartB}))
	warnings := RelicRemovedPartWarnings(relicDocRelic,
		[]RelicOperationPack{remove, relicFoldPack(3, RelicOperation{ID: relicTestID(398), Type: RelicOperationPartRemove, Part: relicDocPartB})},
		[]RelicOperationPack{elsewhere, field}, bothRemoved)
	if len(warnings) != 2 || !strings.Contains(warnings[0].Message, relicDocPartA) || !strings.Contains(warnings[1].Message, relicDocPartB) {
		t.Errorf("warnings for two parts = %#v, want part A's then part B's", warnings)
	}
}

func TestRelicConflictJSONShape(t *testing.T) {
	conflict, texts := relicBodyConflictForTest()
	// The text saved here is not valid UTF-8, so it travels as base64.
	conflict.Body.Ours.Format = ""
	conflict.Body.Ours.Bytes = 3
	SetRelicBodyVersionContent(&conflict.Body.Ours, []byte{0xff, 0xfe, 0x00})

	requireJSON := func(label string, value any, want string) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s: json.Marshal: %v", label, err)
		}
		if string(encoded) != want {
			t.Errorf("%s:\n got %s\nwant %s", label, encoded, want)
		}
	}
	quoted := func(text string) string {
		encoded, _ := json.Marshal(text)
		return string(encoded)
	}

	requireJSON("a body conflict", conflict, `{"relicId":"`+relicDocRelic+`","type":"body","part":"`+relicDocPartA+`",`+
		`"body":{`+
		`"base":{"version":"`+relicDocPartA+`","format":"md","bytes":`+strconv.Itoa(len(texts[0]))+`,"object":"aaaa1111","text":`+quoted(texts[0])+`},`+
		`"ours":{"version":"`+relicTestID(380)+`","bytes":3,"object":"bbbb2222","base64":"//4A"},`+
		`"theirs":{"version":"`+relicTestID(381)+`","format":"md","bytes":`+strconv.Itoa(len(texts[2]))+`,"object":"cccc3333","text":`+quoted(texts[2])+`}},`+
		`"parkedRef":"refs/workbook/reconciled-relics/`+relicDocRelic+`/1","unreplayed":2}`)

	// A part that had no body where the two histories met has no base, and a
	// version whose content was not read carries neither text nor base64.
	requireJSON("a body conflict with no base", RelicConflict{
		RelicID: relicDocRelic,
		Type:    RelicConflictBody,
		Part:    relicDocPartB,
		Body: &RelicBodyConflict{
			Ours:   RelicBodyVersion{Version: relicTestID(380), Bytes: 4, Object: "bbbb2222"},
			Theirs: RelicBodyVersion{Version: relicTestID(381), Format: "txt", Bytes: 0, Object: "cccc3333"},
		},
		Remote: "upstream",
	}, `{"relicId":"`+relicDocRelic+`","type":"body","part":"`+relicDocPartB+`",`+
		`"body":{"ours":{"version":"`+relicTestID(380)+`","bytes":4,"object":"bbbb2222"},`+
		`"theirs":{"version":"`+relicTestID(381)+`","format":"txt","bytes":0,"object":"cccc3333"}},`+
		`"unreplayed":0,"remote":"upstream"}`)

	requireJSON("a tombstone conflict", RelicConflict{
		RelicID:    relicDocRelic,
		Type:       RelicConflictTombstone,
		Tombstone:  &RelicTombstoneConflict{OperationID: relicTestID(382), Operation: RelicOperationTitleSet},
		ParkedRef:  "refs/workbook/reconciled-relics/" + relicDocRelic + "/1",
		Unreplayed: 1,
	}, `{"relicId":"`+relicDocRelic+`","type":"tombstone",`+
		`"tombstone":{"operationId":"`+relicTestID(382)+`","operation":"title.set"},`+
		`"parkedRef":"refs/workbook/reconciled-relics/`+relicDocRelic+`/1","unreplayed":1}`)

	requireJSON("a tombstone conflict with other changes unapplied", RelicConflict{
		RelicID:    relicDocRelic,
		Type:       RelicConflictTombstone,
		Tombstone:  &RelicTombstoneConflict{OperationID: relicTestID(382), Operation: RelicOperationTitleSet},
		ParkedRef:  "refs/workbook/reconciled-relics/" + relicDocRelic + "/1",
		Unreplayed: 2,
		Unapplied:  []RelicUnappliedOperation{{OperationID: relicTestID(383), Operation: RelicOperationPropertySet, Commit: "dddd4444"}},
	}, `{"relicId":"`+relicDocRelic+`","type":"tombstone",`+
		`"tombstone":{"operationId":"`+relicTestID(382)+`","operation":"title.set"},`+
		`"parkedRef":"refs/workbook/reconciled-relics/`+relicDocRelic+`/1","unreplayed":2,`+
		`"unapplied":[{"operationId":"`+relicTestID(383)+`","operation":"property.set","commit":"dddd4444"}]}`)

	// The content of a version is its text or its base64, never both, and an
	// empty body is the empty text.
	version := RelicBodyVersion{}
	SetRelicBodyVersionContent(&version, []byte{0xff})
	SetRelicBodyVersionContent(&version, []byte("now text"))
	if version.Base64 != nil || version.Text == nil || *version.Text != "now text" {
		t.Errorf("after text replaced bytes: %#v", version)
	}
	SetRelicBodyVersionContent(&version, []byte{0xc3, 0x28})
	if version.Text != nil || version.Base64 == nil || *version.Base64 != "wyg=" {
		t.Errorf("after bytes replaced text: %#v", version)
	}
	SetRelicBodyVersionContent(&version, nil)
	if version.Base64 != nil || version.Text == nil || *version.Text != "" {
		t.Errorf("after an empty body: %#v", version)
	}

	// A relic document makes the same choice for the same bytes.
	for _, content := range [][]byte{[]byte("plain text"), {0xff, 0xfe, 0x00}, {}} {
		var conflictVersion RelicBodyVersion
		SetRelicBodyVersionContent(&conflictVersion, content)
		part := newRelicDocumentPart(
			RelicPart{ID: relicDocPartA, Type: "section", Position: "1/1", Body: &RelicBody{Version: relicDocPartA, Bytes: int64(len(content))}},
			RelicPartContent{Body: content, HasBody: true})
		if !reflect.DeepEqual(part.Body.Text, conflictVersion.Text) || !reflect.DeepEqual(part.Body.Base64, conflictVersion.Base64) {
			t.Errorf("content %q: a document says text %v base64 %v, a conflict text %v base64 %v", content,
				part.Body.Text, part.Body.Base64, conflictVersion.Text, conflictVersion.Base64)
		}
	}
}

func TestReplayStopsOnAStubWithDroppedConflict(t *testing.T) {
	_, parent := relicCheckpointForTest(RelicBoundaryRetired)
	local := func(operations ...RelicOperation) RelicOperationPack {
		return NewRelicOperationPack(relicDocProject, relicDocRelic, relicCheckpointGeneration, relicDocActor, 2,
			relicDocTime.Add(time.Hour), operations)
	}
	restore := RelicOperation{ID: relicTestID(7500), Type: RelicOperationPartRestore, Part: relicDocPartB}
	step := replayStepFor(t, parent, local(
		RelicOperation{ID: relicTestID(7501), Type: RelicOperationPropertySet, Name: "q", Value: json.RawMessage(`1`)},
		restore), RelicReplayInput{})
	if RelicConflictDropped != "dropped" {
		t.Fatalf("RelicConflictDropped = %q, want dropped", RelicConflictDropped)
	}
	if step.Conflict == nil || step.Conflict.Type != RelicConflictDropped ||
		step.Conflict.Part != relicDocPartB || step.Conflict.RelicID != relicDocRelic {
		t.Fatalf("a local restore of a stub: step %#v, conflict %#v; want a dropped conflict naming part B", step, step.Conflict)
	}
	if step.ConflictOperation != restore.ID || step.Skip {
		t.Errorf("the dropped conflict names operation %q (skip %v), want %q", step.ConflictOperation, step.Skip, restore.ID)
	}

	// Every operation that names a stub stops the replay the same way, and a
	// body save to one stops on that before it is judged stale.
	for label, operation := range map[string]RelicOperation{
		"field set": {ID: relicTestID(7502), Type: RelicOperationPartFieldSet, Part: relicDocPartB, Name: "h",
			Value: json.RawMessage(`"x"`)},
		"body set": {ID: relicTestID(7503), Type: RelicOperationPartBodySet, Part: relicDocPartB,
			Body: &RelicBodyChange{Format: "md", Bytes: 2, Base: relicDocPartB}},
		"move": {ID: relicTestID(7504), Type: RelicOperationPartMove, Part: relicDocPartB, Position: "9/1"},
		"link from it": {ID: relicTestID(7505), Type: RelicOperationLinkAdd, Link: &RelicLinkChange{From: relicDocPartB,
			To: RelicLinkTarget{Task: relicCheckpointLinkedTask}, Rel: "implements"}},
	} {
		step := replayStepFor(t, parent, local(operation), RelicReplayInput{})
		if step.Conflict == nil || step.Conflict.Type != RelicConflictDropped || step.ConflictOperation != operation.ID {
			t.Errorf("%s on a stub: conflict %#v, operation %q; want dropped naming %s",
				label, step.Conflict, step.ConflictOperation, operation.ID)
		}
	}

	// A pack that names only live parts replays as usual.
	applied := replayStepFor(t, parent, local(RelicOperation{ID: relicTestID(7506), Type: RelicOperationPartMove,
		Part: relicDocPartA, Position: "5/1"}), RelicReplayInput{})
	requireReplayApplied(t, "a move of a live part of a compacted relic", parent, applied)

	// A part.remove of a stub agrees with the checkpoint: both sides wanted
	// the part gone, so it replays as the no-op the fold makes it.
	remove := RelicOperation{ID: relicTestID(7507), Type: RelicOperationPartRemove, Part: relicDocPartB}
	removed := replayStepFor(t, parent, local(remove), RelicReplayInput{})
	requireReplayApplied(t, "a local removal of a stub", parent, removed)
	if got, want := removed.Record.State.Relic.Parts, parent.State.Relic.Parts; !reflect.DeepEqual(got, want) {
		t.Errorf("a local removal of a stub changed the parts: %#v, want %#v", got, want)
	}
	// Beside another operation on the same stub, the pack still stops, on
	// that other operation.
	field := RelicOperation{ID: relicTestID(7508), Type: RelicOperationPartFieldSet, Part: relicDocPartB, Name: "h",
		Value: json.RawMessage(`"x"`)}
	for label, pack := range map[string]RelicOperationPack{
		"remove then field set": local(remove, field),
		"field set then remove": local(field, remove),
	} {
		step := replayStepFor(t, parent, pack, RelicReplayInput{})
		if step.Conflict == nil || step.Conflict.Type != RelicConflictDropped || step.ConflictOperation != field.ID ||
			step.Conflict.Part != relicDocPartB {
			t.Errorf("%s on a stub: conflict %#v, operation %q; want dropped naming %s",
				label, step.Conflict, step.ConflictOperation, field.ID)
		}
	}

	detail := RelicConflictDetail(RelicConflict{RelicID: relicDocRelic, Type: RelicConflictDropped, Part: relicDocPartB,
		ParkedRef: "refs/workbook/reconciled-relics/" + relicDocRelic})
	if !strings.Contains(detail, relicDocPartB) || !strings.Contains(detail, "dropped") ||
		!strings.Contains(detail, "refs/workbook/reconciled-relics/") {
		t.Errorf("RelicConflictDetail(dropped) = %q", detail)
	}
}

func TestConcurrentRetireAndEditBothApply(t *testing.T) {
	retire := RelicOperation{ID: relicTestID(7600), Type: RelicOperationRetire}
	edit := RelicOperation{ID: relicTestID(7601), Type: RelicOperationPartFieldSet, Part: relicDocPartA, Name: "heading",
		Value: json.RawMessage(`"Ours"`)}
	property := RelicOperation{ID: relicTestID(7602), Type: RelicOperationPropertySet, Name: "status",
		Value: json.RawMessage(`"superseded"`)}

	// The remote retired; the local side edited.
	remoteRetired := mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2, retire))
	step := replayStepFor(t, remoteRetired, relicFoldPack(2, edit, property), RelicReplayInput{})
	requireReplayApplied(t, "a local edit onto a remote retire", remoteRetired, step)
	if !step.Record.State.Relic.Retired || relicProperty(t, step.Record, "status") != `"superseded"` {
		t.Errorf("a local edit onto a remote retire: retired %v, status %s; want both",
			step.Record.State.Relic.Retired, relicProperty(t, step.Record, "status"))
	}
	if step.Record.State.MinReader != RelicCheckpointMinReader {
		t.Errorf("the replayed edit declares %d, want 7", step.Record.State.MinReader)
	}

	// The remote edited; the local side retired.
	remoteEdited := mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2, edit, property))
	step = replayStepFor(t, remoteEdited, relicFoldPack(2, retire), RelicReplayInput{})
	requireReplayApplied(t, "a local retire onto a remote edit", remoteEdited, step)
	if !step.Record.State.Relic.Retired || relicProperty(t, step.Record, "status") != `"superseded"` {
		t.Errorf("a local retire onto a remote edit: retired %v, status %s; want both",
			step.Record.State.Relic.Retired, relicProperty(t, step.Record, "status"))
	}
}

func TestConcurrentRetireMarksLaterSyncWins(t *testing.T) {
	retire := RelicOperation{ID: relicTestID(7700), Type: RelicOperationRetire}
	unretire := RelicOperation{ID: relicTestID(7701), Type: RelicOperationUnretire}

	// The remote retired; the local side, from the same live relic, unretired.
	remoteRetired := mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2, retire))
	step := replayStepFor(t, remoteRetired, relicFoldPack(2, unretire), RelicReplayInput{})
	requireReplayApplied(t, "a local unretire onto a remote retire", remoteRetired, step)
	if step.Record.State.Relic.Retired {
		t.Errorf("a local unretire replayed onto a remote retire left the relic retired")
	}

	// The relic was retired; the remote unretired it, and the local side
	// retired it again.
	base := mustApplyRelic(t, goldenRelicRecord(t), relicFoldPack(2, retire))
	remoteUnretired := mustApplyRelic(t, base, relicFoldPack(3, unretire))
	step = replayStepFor(t, remoteUnretired, relicFoldPack(3, RelicOperation{ID: relicTestID(7702), Type: RelicOperationRetire}),
		RelicReplayInput{})
	requireReplayApplied(t, "a local retire onto a remote unretire", remoteUnretired, step)
	if !step.Record.State.Relic.Retired {
		t.Errorf("a local retire replayed onto a remote unretire left the relic live")
	}
}

// relicSaveOp is a part.body.set of part from base, of size bytes.
func relicSaveOp(id, part, base string, size int64) RelicOperation {
	return RelicOperation{ID: id, Type: RelicOperationPartBodySet, Part: part,
		Body: &RelicBodyChange{Format: "md", Bytes: size, Base: base}}
}

// relicMergeReplayInput is the replay input for a pack first written onto
// sourceParent, giving source.
func relicMergeReplayInput(t *testing.T, sourceParent RelicRecord, pack RelicOperationPack) RelicReplayInput {
	t.Helper()
	source := mustApplyRelic(t, sourceParent, pack)
	return RelicReplayInput{Source: &source, SourceParent: &sourceParent}
}

func TestPlanReplayAsksToMergeAStaleSave(t *testing.T) {
	golden := goldenRelicRecord(t)
	theirs, ours := relicTestID(400), relicTestID(401)
	parent := mustApplyRelic(t, golden, relicFoldPack(2, relicSaveOp(theirs, relicDocPartA, relicDocPartA, 20)))
	pack := relicFoldPack(2, relicSaveOp(ours, relicDocPartA, relicDocPartA, 30))

	step := replayStepFor(t, parent, pack, relicMergeReplayInput(t, golden, pack))
	want := []RelicBodyMergeNeed{{
		Part:           relicDocPartA,
		Ancestor:       RelicBody{Format: "md", Version: relicDocPartA, Bytes: 1840},
		Current:        RelicBody{Format: "md", Version: theirs, Bytes: 20},
		Other:          RelicBody{Format: "md", Version: ours, Bytes: 30},
		OtherOperation: ours,
		Stale:          ours,
	}}
	if !reflect.DeepEqual(step.Merges, want) {
		t.Fatalf("merges = %#v, want %#v", step.Merges, want)
	}
	if step.Skip || step.Conflict != nil || step.ConflictOperation != "" || !reflect.DeepEqual(step.Record, RelicRecord{}) {
		t.Fatalf("step = %#v, want only the merges", step)
	}

	t.Run("two saves in the pack merge the last", func(t *testing.T) {
		second := relicTestID(402)
		pack := relicFoldPack(2, relicSaveOp(ours, relicDocPartA, relicDocPartA, 30), relicSaveOp(second, relicDocPartA, ours, 31))
		step := replayStepFor(t, parent, pack, relicMergeReplayInput(t, golden, pack))
		if len(step.Merges) != 1 || step.Merges[0].OtherOperation != second || step.Merges[0].Stale != ours ||
			step.Merges[0].Other.Version != second {
			t.Fatalf("merges = %#v, want the second save merged in and the first named", step.Merges)
		}
	})

	t.Run("a merged pack replayed again merges its merge", func(t *testing.T) {
		// This clone merged its save s onto theirs as m; origin then moved on
		// to carol's save from theirs.
		saved, merge, carol := relicTestID(403), relicTestID(404), relicTestID(405)
		merged := mustApplyRelic(t, parent, relicFoldPack(3,
			relicSaveOp(saved, relicDocPartA, relicDocPartA, 30),
			RelicOperation{ID: merge, Type: RelicOperationPartBodyMerge, Part: relicDocPartA,
				Body:  &RelicBodyChange{Format: "md", Bytes: 33, Base: theirs},
				Merge: &RelicBodyMerge{Ancestor: relicDocPartA, Other: saved, Method: RelicLineMergeMethod}}))
		_ = merged
		pack := relicFoldPack(3,
			relicSaveOp(saved, relicDocPartA, relicDocPartA, 30),
			RelicOperation{ID: merge, Type: RelicOperationPartBodyMerge, Part: relicDocPartA,
				Body:  &RelicBodyChange{Format: "md", Bytes: 33, Base: theirs},
				Merge: &RelicBodyMerge{Ancestor: relicDocPartA, Other: saved, Method: RelicLineMergeMethod}})
		newer := mustApplyRelic(t, parent, relicFoldPack(3, relicSaveOp(carol, relicDocPartA, theirs, 25)))
		step := replayStepFor(t, newer, pack, relicMergeReplayInput(t, parent, pack))
		want := []RelicBodyMergeNeed{{
			Part:           relicDocPartA,
			Ancestor:       RelicBody{Format: "md", Version: theirs, Bytes: 20},
			Current:        RelicBody{Format: "md", Version: carol, Bytes: 25},
			Other:          RelicBody{Format: "md", Version: merge, Bytes: 33},
			OtherOperation: merge,
			Stale:          merge,
		}}
		if step.Conflict != nil || !reflect.DeepEqual(step.Merges, want) {
			t.Fatalf("step = %#v, want merges %#v", step, want)
		}
	})

	t.Run("an agreeing body needs no merge", func(t *testing.T) {
		input := relicMergeReplayInput(t, golden, pack)
		input.SameBody = map[string]bool{relicDocPartA: true}
		step := replayStepFor(t, parent, pack, input)
		requireReplayApplied(t, "agreeing bodies", parent, step)
		if step.Merges != nil {
			t.Fatalf("merges = %#v, want none", step.Merges)
		}
	})

	t.Run("a part with no body at the ancestor is still asked about", func(t *testing.T) {
		withB := mustApplyRelic(t, golden, relicFoldPack(2, RelicOperation{ID: relicTestID(406),
			Type: RelicOperationPartBodySet, Part: relicDocPartB, Body: &RelicBodyChange{Format: "md", Bytes: 5}}))
		pack := relicFoldPack(2, RelicOperation{ID: relicTestID(407),
			Type: RelicOperationPartBodySet, Part: relicDocPartB, Body: &RelicBodyChange{Format: "md", Bytes: 6}})
		step := replayStepFor(t, withB, pack, relicMergeReplayInput(t, golden, pack))
		if len(step.Merges) != 1 || step.Merges[0].Ancestor.Version != "" ||
			CheckRelicBodyMerge(step.Merges[0]) != RelicMergeNoBase {
			t.Fatalf("merges = %#v, want one need with no ancestor body, refused as no-base", step.Merges)
		}
	})
}

func TestPlanReplayStaleSaveIgnoredLocallyIsNoConflict(t *testing.T) {
	golden := goldenRelicRecord(t)
	mine, theirs, ignored := relicTestID(410), relicTestID(411), relicTestID(412)
	sourceParent := mustApplyRelic(t, golden, relicFoldPack(2, relicSaveOp(mine, relicDocPartA, relicDocPartA, 10)))
	parent := mustApplyRelic(t, golden, relicFoldPack(2, relicSaveOp(theirs, relicDocPartA, relicDocPartA, 20)))
	// The save starts from the first version, which the local fold had
	// already moved past, so it changed nothing here.
	pack := relicFoldPack(3, relicSaveOp(ignored, relicDocPartA, relicDocPartA, 30),
		RelicOperation{ID: relicTestID(413), Type: RelicOperationTitleSet, Title: "Kept"})
	step := replayStepFor(t, parent, pack, relicMergeReplayInput(t, sourceParent, pack))
	requireReplayApplied(t, "an ignored save", parent, step)
	if step.Merges != nil {
		t.Fatalf("merges = %#v, want none", step.Merges)
	}
	if body := requirePartForTest(t, step.Record.State.Relic, relicDocPartA).Body; body == nil || body.Version != theirs {
		t.Fatalf("body = %#v, want the parent's", body)
	}
	if step.Record.State.Relic.Title != "Kept" {
		t.Fatalf("title = %q, want the pack's", step.Record.State.Relic.Title)
	}
}

func TestPlanReplayForgedStaleShapeIsUnmergeable(t *testing.T) {
	golden := goldenRelicRecord(t)
	mine, theirs := relicTestID(420), relicTestID(421)
	sourceParent := mustApplyRelic(t, golden, relicFoldPack(2, relicSaveOp(mine, relicDocPartA, relicDocPartA, 10)))
	parent := mustApplyRelic(t, golden, relicFoldPack(2, relicSaveOp(theirs, relicDocPartA, relicDocPartA, 20)))
	for label, pack := range map[string]RelicOperationPack{
		// A save from a version only origin has: ignored here, taken there.
		"a save from origin's version": relicFoldPack(3, relicSaveOp(relicTestID(422), relicDocPartA, theirs, 30)),
		// The first save is stale onto origin, the second is taken there.
		"a chain that rejoins origin": relicFoldPack(3,
			relicSaveOp(relicTestID(423), relicDocPartA, mine, 30),
			relicSaveOp(relicTestID(424), relicDocPartA, theirs, 31)),
	} {
		step := replayStepFor(t, parent, pack, relicMergeReplayInput(t, sourceParent, pack))
		if step.Conflict == nil || step.Conflict.Type != RelicConflictBody || step.Conflict.Part != relicDocPartA ||
			step.Conflict.Body == nil || step.Conflict.Body.Reason != RelicMergeUnmergeable {
			t.Fatalf("%s: conflict = %#v, want an unmergeable body conflict on part A", label, step.Conflict)
		}
		if step.Merges != nil || step.ConflictOperation == "" {
			t.Fatalf("%s: step = %#v, want no merges and a named operation", label, step)
		}
	}
}

func TestMergeRelicBodyReasons(t *testing.T) {
	base := RelicBody{Format: "md", Version: relicTestID(430)}
	need := RelicBodyMergeNeed{Part: relicDocPartA, Ancestor: base,
		Current: RelicBody{Format: "md", Version: relicTestID(431)}, Other: RelicBody{Format: "md", Version: relicTestID(432)},
		OtherOperation: relicTestID(432)}
	six := "1\n2\n3\n4\n5\n6\n"
	var many, manyA, manyB strings.Builder
	for line := 0; line < 4200; line++ {
		fmt := strconv.Itoa(line) + "\n"
		many.WriteString(fmt)
		if line%2 == 0 && line < 4002 {
			manyA.WriteString("a" + fmt)
		} else {
			manyA.WriteString(fmt)
		}
		manyB.WriteString(fmt)
	}
	big := strings.Repeat("y", 600*1024)
	without := func(change func(*RelicBodyMergeNeed)) RelicBodyMergeNeed {
		changed := need
		change(&changed)
		return changed
	}
	for _, row := range []struct {
		name     string
		need     RelicBodyMergeNeed
		texts    RelicBodyMergeTexts
		merged   string
		reason   RelicMergeReason
		overlaps []LineMergeOverlap
	}{
		{"clean", need, RelicBodyMergeTexts{[]byte(six), []byte("1\nTWO\n3\n4\n5\n6\n"), []byte("1\n2\n3\n4\nFIVE\n6\n")},
			"1\nTWO\n3\n4\nFIVE\n6\n", "", nil},
		{"overlap", need, RelicBodyMergeTexts{[]byte(six), []byte("1\nA\n3\n4\n5\n6\n"), []byte("1\nB\n3\n4\n5\n6\n")},
			"", RelicMergeOverlap, []LineMergeOverlap{{BaseStart: 2, BaseLines: 1}}},
		{"no ancestor body", without(func(n *RelicBodyMergeNeed) { n.Ancestor = RelicBody{} }), RelicBodyMergeTexts{}, "", RelicMergeNoBase, nil},
		{"no current body", without(func(n *RelicBodyMergeNeed) { n.Current = RelicBody{} }), RelicBodyMergeTexts{}, "", RelicMergeNoBase, nil},
		{"format", without(func(n *RelicBodyMergeNeed) { n.Current.Format = "txt" }), RelicBodyMergeTexts{}, "", RelicMergeFormat, nil},
		{"not text", need, RelicBodyMergeTexts{[]byte(six), []byte(six), []byte("1\n\xff\n")}, "", RelicMergeNotText, nil},
		{"too many changes", need, RelicBodyMergeTexts{[]byte(many.String()), []byte(manyA.String()), []byte(manyB.String() + "end\n")},
			"", RelicMergeTooManyChanges, nil},
		{"too large", need, RelicBodyMergeTexts{[]byte("a\nb\nc\nd\n"), []byte(big + "\nb\nc\nd\n"), []byte("a\nb\nc\n" + big + "\n")},
			"", RelicMergeTooLarge, nil},
	} {
		merged, reason, overlaps := MergeRelicBody(row.need, row.texts)
		if reason != row.reason || !reflect.DeepEqual(overlaps, row.overlaps) {
			t.Errorf("%s: reason %q, overlaps %v; want %q, %v", row.name, reason, overlaps, row.reason, row.overlaps)
			continue
		}
		if row.reason == "" && string(merged) != row.merged {
			t.Errorf("%s: merged %q, want %q", row.name, merged, row.merged)
		}
		if row.reason != "" && merged != nil {
			t.Errorf("%s: merged text %q given beside a reason", row.name, merged)
		}
	}
}

func TestAppendRelicBodyMergesFoldsAtTheNextClock(t *testing.T) {
	golden := goldenRelicRecord(t)
	withB := mustApplyRelic(t, golden, relicFoldPack(2, RelicOperation{ID: relicTestID(440),
		Type: RelicOperationPartBodySet, Part: relicDocPartB, Body: &RelicBodyChange{Format: "md", Bytes: 5}}))
	parent := mustApplyRelic(t, withB, relicFoldPack(3,
		relicSaveOp(relicTestID(441), relicDocPartA, relicDocPartA, 20),
		relicSaveOp(relicTestID(442), relicDocPartB, relicTestID(440), 21)))
	saveA, saveB := relicTestID(443), relicTestID(444)
	pack := relicFoldPack(3, relicSaveOp(saveB, relicDocPartB, relicTestID(440), 9), relicSaveOp(saveA, relicDocPartA, relicDocPartA, 8))
	step := replayStepFor(t, parent, pack, relicMergeReplayInput(t, withB, pack))
	if len(step.Merges) != 2 || step.Merges[0].Part != relicDocPartA || step.Merges[1].Part != relicDocPartB {
		t.Fatalf("merges = %#v, want parts A and B in order", step.Merges)
	}
	operations := []RelicOperation{
		RelicBodyMergeOperation(relicDocRelic, step.Merges[1], []byte("merged b\n"), pack.WallTime),
		RelicBodyMergeOperation(relicDocRelic, step.Merges[0], []byte("merged a text\n"), pack.WallTime),
	}
	appended, record, err := AppendRelicBodyMerges(parent, pack, operations)
	if err != nil {
		t.Fatalf("AppendRelicBodyMerges: %v", err)
	}
	if appended.MinReader != RelicMergeMinReader || appended.LogicalClock != parent.State.LogicalClock+1 {
		t.Fatalf("pack minReader %d, clock %d; want %d and %d", appended.MinReader, appended.LogicalClock,
			RelicMergeMinReader, parent.State.LogicalClock+1)
	}
	if len(appended.Operations) != 4 || appended.Operations[2].ID != operations[1].ID || appended.Operations[3].ID != operations[0].ID {
		t.Fatalf("operations = %#v, want the saves then the merges of A and B", appended.Operations)
	}
	if len(pack.Operations) != 2 || pack.MinReader == RelicMergeMinReader {
		t.Fatalf("the caller's pack was changed: %#v", pack)
	}
	for _, check := range []struct {
		part, version string
		bytes         int64
	}{
		{relicDocPartA, operations[1].ID, int64(len("merged a text\n"))},
		{relicDocPartB, operations[0].ID, int64(len("merged b\n"))},
	} {
		body := requirePartForTest(t, record.State.Relic, check.part).Body
		if body == nil || body.Version != check.version || body.Bytes != check.bytes {
			t.Fatalf("part %s body = %#v, want version %s of %d bytes", check.part, body, check.version, check.bytes)
		}
	}
	if record.State.MinReader != RelicMergeMinReader {
		t.Fatalf("state minReader = %d, want %d", record.State.MinReader, RelicMergeMinReader)
	}
	again := RelicBodyMergeOperation(relicDocRelic, step.Merges[0], []byte("merged a text\n"), pack.WallTime)
	if again.ID != operations[1].ID {
		t.Fatalf("the same merge has IDs %s and %s", again.ID, operations[1].ID)
	}
	if merge := operations[1].Merge; merge.Ancestor != relicDocPartA || merge.Other != saveA || operations[1].Body.Base != relicTestID(441) {
		t.Fatalf("merge of A = %#v base %s, want ancestor A, other %s, base %s", merge, operations[1].Body.Base, saveA, relicTestID(441))
	}
}
