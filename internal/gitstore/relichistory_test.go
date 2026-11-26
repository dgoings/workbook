package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// relicValidateHint is how a refusal from GetRelic ends.
const relicValidateHint = `; run "workbook validate" for details`

// forgedPart is one part's subtree in a commit written straight into Git.
type forgedPart struct {
	// fields is the fields.json blob; nil leaves the entry out.
	fields []byte
	// body is the body blob, stored when hasBody is set.
	body    []byte
	hasBody bool
	// extra holds further `git mktree` lines for the subtree.
	extra []string
}

// forgeRelicCommit writes a relic commit with Git plumbing alone: the two
// documents exactly as given, the empty links document, and one subtree per
// entry of parts. Nothing checks it, which is the point. An empty parent makes
// a root commit.
func forgeRelicCommit(
	t *testing.T,
	repo *Repository,
	parent string,
	operationJSON, stateJSON []byte,
	parts map[string]forgedPart,
) string {
	t.Helper()
	entries := []string{
		"100644 blob " + forgeRelicBlob(t, repo, operationJSON) + "\toperation.json",
		"100644 blob " + forgeRelicBlob(t, repo, stateJSON) + "\tstate.json",
		"100644 blob " + forgeRelicBlob(t, repo, encodedRelicDocument(t, core.EmptyRelicLinks())) + "\tlinks.json",
	}
	if len(parts) > 0 {
		partIDs := slices.Sorted(maps.Keys(parts))
		subtrees := make([]string, 0, len(partIDs))
		for _, partID := range partIDs {
			part := parts[partID]
			var lines []string
			if part.fields != nil {
				lines = append(lines, "100644 blob "+forgeRelicBlob(t, repo, part.fields)+"\tfields.json")
			}
			if part.hasBody {
				lines = append(lines, "100644 blob "+forgeRelicBlob(t, repo, part.body)+"\tbody")
			}
			lines = append(lines, part.extra...)
			subtrees = append(subtrees, "040000 tree "+forgeRelicTree(t, repo, lines)+"\t"+partID)
		}
		entries = append(entries, "040000 tree "+forgeRelicTree(t, repo, subtrees)+"\tparts")
	}
	if parent == "" {
		return forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, entries))
	}
	return forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, entries), parent)
}

// forgeRelicChild forges the commit pack makes on top of parent, with the
// state the fold computes for it and the parts exactly as given.
func forgeRelicChild(
	t *testing.T,
	repo *Repository,
	parent core.RelicSnapshot,
	pack core.RelicOperationPack,
	parts map[string]forgedPart,
) string {
	t.Helper()
	record := foldRelic(t, &parent, pack)
	return forgeRelicCommit(t, repo, parent.Head,
		encodedRelicDocument(t, pack), encodedRelicDocument(t, record.State), parts)
}

// forgedPartsOf returns the parts a commit stores, for a forged commit to
// keep or change. Git names objects by content, so a part stored again
// unchanged is the same subtree object.
func forgedPartsOf(t *testing.T, repo *Repository, commit string) map[string]forgedPart {
	t.Helper()
	parts := map[string]forgedPart{}
	for _, path := range relicLines(gitOutput(t, repo, "ls-tree", "-r", "--name-only", commit, "parts/")) {
		segments := strings.Split(path, "/")
		if len(segments) != 3 {
			t.Fatalf("commit %s stores %q, which is not a part's blob", commit, path)
		}
		part := parts[segments[1]]
		contents := relicBlob(t, repo, commit+":"+path)
		switch segments[2] {
		case "fields.json":
			part.fields = contents
		case "body":
			part.body, part.hasBody = contents, true
		default:
			t.Fatalf("commit %s stores %q, which is not a part's blob", commit, path)
		}
		parts[segments[1]] = part
	}
	return parts
}

// withForgedPart returns parts with one part replaced or added.
func withForgedPart(parts map[string]forgedPart, partID string, part forgedPart) map[string]forgedPart {
	changed := maps.Clone(parts)
	changed[partID] = part
	return changed
}

// forgedFields returns the fields.json of the given name and string value
// pairs, as Workbook writes it.
func forgedFields(t *testing.T, pairs ...string) []byte {
	t.Helper()
	if len(pairs)%2 != 0 {
		t.Fatalf("forgedFields needs name and value pairs, got %q", pairs)
	}
	fields := make(map[string]json.RawMessage, len(pairs)/2)
	for index := 0; index < len(pairs); index += 2 {
		encoded, err := json.Marshal(pairs[index+1])
		if err != nil {
			t.Fatal(err)
		}
		fields[pairs[index]] = encoded
	}
	return encodedRelicDocument(t, core.NewRelicFields(fields))
}

func pointRelicRef(t *testing.T, repo *Repository, relicID, commit string) {
	t.Helper()
	gitOutput(t, repo, "update-ref", relicRef(relicID), commit)
}

func relicPartAdd(partID, position string, body []byte) core.RelicOperation {
	operation := core.RelicOperation{ID: partID, Type: core.RelicOperationPartAdd, PartType: "section", Position: position}
	if body != nil {
		operation.Body = &core.RelicBodyChange{Format: "md", Bytes: int64(len(body))}
	}
	return operation
}

func relicPartMove(operationNumber int, partID, position string) core.RelicOperation {
	return core.RelicOperation{
		ID: relicTestID(operationNumber), Type: core.RelicOperationPartMove, Part: partID, Position: position,
	}
}

func relicPartChange(operationNumber int, kind core.RelicOperationType, partID string) core.RelicOperation {
	return core.RelicOperation{ID: relicTestID(operationNumber), Type: kind, Part: partID}
}

func relicFieldSet(operationNumber int, partID, name, value string) core.RelicOperation {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return core.RelicOperation{
		ID: relicTestID(operationNumber), Type: core.RelicOperationPartFieldSet, Part: partID, Name: name, Value: encoded,
	}
}

func relicFieldClear(operationNumber int, partID, name string) core.RelicOperation {
	return core.RelicOperation{
		ID: relicTestID(operationNumber), Type: core.RelicOperationPartFieldClear, Part: partID, Name: name,
	}
}

func relicPropertySet(operationNumber int, name, value string) core.RelicOperation {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return core.RelicOperation{
		ID: relicTestID(operationNumber), Type: core.RelicOperationPropertySet, Name: name, Value: encoded,
	}
}

// relicFieldsAfter returns a part's fields once operations apply to the ones
// parent stores, which is what a writer hands WriteRelic for a field edit.
func relicFieldsAfter(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	parent core.RelicSnapshot,
	partID string,
	operations ...core.RelicOperation,
) map[string]json.RawMessage {
	t.Helper()
	stored, err := repo.ReadRelicParts(context.Background(), config, parent, []string{partID}, false)
	if err != nil {
		t.Fatalf("ReadRelicParts(%s) error = %v", partID, err)
	}
	return core.ApplyRelicFields(stored[partID].Fields, partID, operations)
}

// assertRelicHistoryValid asserts that every commit up to head passes every
// rule, and that there are want of them.
func assertRelicHistoryValid(t *testing.T, repo *Repository, config core.ProjectConfig, relicID, head string, want int) {
	t.Helper()
	checked, failure, err := repo.ValidateRelicHistory(context.Background(), config,
		core.RelicHead{RelicID: relicID, ObjectID: head})
	if err != nil {
		t.Fatalf("ValidateRelicHistory() error = %v", err)
	}
	if failure != nil {
		t.Fatalf("ValidateRelicHistory() refused commit %s: %v", failure.Commit, failure.Err)
	}
	if checked != want {
		t.Fatalf("ValidateRelicHistory() checked %d commits, want %d", checked, want)
	}
}

// assertRelicHistoryStopsAt asserts that validation passes validBefore commits
// and then names commit, with the category and the words given.
func assertRelicHistoryStopsAt(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	relicID, head, commit string,
	validBefore int,
	category core.Category,
	fragments ...string,
) {
	t.Helper()
	checked, failure, err := repo.ValidateRelicHistory(context.Background(), config,
		core.RelicHead{RelicID: relicID, ObjectID: head})
	if err != nil {
		t.Fatalf("ValidateRelicHistory() error = %v", err)
	}
	if failure == nil {
		t.Fatalf("ValidateRelicHistory() accepted all %d commits, want it to refuse %s", checked, commit)
	}
	if failure.Commit != commit {
		t.Fatalf("ValidateRelicHistory() named commit %s (%v), want %s", failure.Commit, failure.Err, commit)
	}
	if checked != validBefore {
		t.Fatalf("ValidateRelicHistory() checked %d commits before the failure, want %d", checked, validBefore)
	}
	assertRelicCategory(t, failure.Err, category, fragments...)
}

// assertGetRelicRefused asserts the refusal a single-relic read gives for a
// commit that breaks a rule.
func assertGetRelicRefused(t *testing.T, err error) {
	t.Helper()
	assertRelicCategory(t, err, core.CategoryCorruptData)
	if !strings.HasSuffix(err.Error(), relicValidateHint) {
		t.Fatalf("GetRelic() error %q does not end with %q", err, relicValidateHint)
	}
}

func TestValidateRelicHistoryAcceptsWhatWriteRelicWrote(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		a := relicTestPartNumbered(1).withField("heading", "Goals").withBody("md", []byte("the goals\n"))
		b := relicTestPartNumbered(2)
		c := relicTestPartNumbered(3)
		seen := map[core.RelicOperationType]bool{}
		commits := 0
		var tip core.RelicSnapshot
		// landed records one written commit and reads it back the way a
		// single-relic command does.
		landed := func(written core.RelicSnapshot) {
			t.Helper()
			tip = written
			commits++
			for _, operation := range written.Operation.Operations {
				seen[operation.Type] = true
			}
			got, err := repo.GetRelic(ctx, config, written.Operation.RelicID)
			if err != nil {
				t.Fatalf("GetRelic() after commit %d error = %v", commits, err)
			}
			assertSameRelicSnapshot(t, got, written)
		}
		write := func(content map[string]core.RelicPartContent, operations ...core.RelicOperation) {
			t.Helper()
			landed(writeRelicChild(t, repo, config, tip, content, operations...))
		}

		// 1: the relic and two parts, one with a body and a field.
		landed(writeRelicRoot(t, repo, config, a, b))
		// 2: the relic's own values.
		write(nil,
			relicTitleSet(700001, "A new title"),
			relicPropertySet(700002, "status", "draft"),
			relicPropertySet(700003, "owner", "dylan@example.com"),
			core.RelicOperation{ID: relicTestID(700004), Type: core.RelicOperationPropertyClear, Name: "status"})
		// 3: a third part that arrives with fields, a body and a field
		// operation of its own, and field edits to the first.
		why := []byte("because\n")
		setStatus := relicFieldSet(700005, c.id, "status", "open")
		setOwner := relicFieldSet(700006, a.id, "owner", "dylan@example.com")
		clearHeading := relicFieldClear(700007, a.id, "heading")
		write(map[string]core.RelicPartContent{
			c.id: {
				Fields:  map[string]json.RawMessage{"heading": json.RawMessage(`"Why"`), "status": json.RawMessage(`"open"`)},
				Body:    why,
				HasBody: true,
			},
			a.id: {Fields: relicFieldsAfter(t, repo, config, tip, a.id, setOwner, clearHeading)},
		}, relicPartAdd(c.id, "3/1", why), setStatus, setOwner, clearHeading)
		// 4: a body edit from the current version.
		second := []byte("the goals, rewritten\n")
		write(map[string]core.RelicPartContent{a.id: {Body: second, HasBody: true}},
			relicBodySet(700008, a.id, a.id, second))
		// 5: a move, and a first body for a part that had none.
		first := []byte("a first body\n")
		write(map[string]core.RelicPartContent{b.id: {Body: first, HasBody: true}},
			relicPartMove(700009, b.id, "7/2"),
			relicBodySet(700010, b.id, "", first))
		// 6: a removal, beside three operations the fold ignores: a body edit
		// from a version that is no longer current, a field edit naming a part
		// the relic does not have, and clearing a field that is not set.
		stale := []byte("text from the first version\n")
		write(map[string]core.RelicPartContent{a.id: {Body: stale, HasBody: true}},
			relicPartChange(700011, core.RelicOperationPartRemove, c.id),
			relicBodySet(700012, a.id, a.id, stale),
			relicFieldSet(700013, relicTestID(99), "heading", "Nowhere"),
			relicFieldClear(700014, a.id, "heading"))
		// 7: the removed part comes back and gets a field.
		setHeading := relicFieldSet(700016, c.id, "heading", "Why, again")
		write(map[string]core.RelicPartContent{c.id: {Fields: relicFieldsAfter(t, repo, config, tip, c.id, setHeading)}},
			relicPartChange(700015, core.RelicOperationPartRestore, c.id), setHeading)
		// 8 and 9: the relic is deleted and restored.
		write(nil, core.RelicOperation{ID: relicTestID(700017), Type: core.RelicOperationTombstone})
		write(nil,
			core.RelicOperation{ID: relicTestID(700018), Type: core.RelicOperationRestore},
			relicTitleSet(700019, "A restored title"))

		for _, kind := range []core.RelicOperationType{
			core.RelicOperationCreate, core.RelicOperationTitleSet, core.RelicOperationTombstone,
			core.RelicOperationRestore, core.RelicOperationPropertySet, core.RelicOperationPropertyClear,
			core.RelicOperationPartAdd, core.RelicOperationPartFieldSet, core.RelicOperationPartFieldClear,
			core.RelicOperationPartBodySet, core.RelicOperationPartMove, core.RelicOperationPartRemove,
			core.RelicOperationPartRestore,
		} {
			if !seen[kind] {
				t.Fatalf("the history holds no %s", kind)
			}
		}
		if commits != 9 {
			t.Fatalf("the history holds %d commits, want 9", commits)
		}
		assertRelicHistoryValid(t, repo, config, tip.Operation.RelicID, tip.Head, 9)
	})
}

func TestAStaleBodyEditWrittenStraightIntoHistoryChangesNothing(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		part := relicTestPartNumbered(1).withBody("md", []byte("the first text\n"))
		root := writeRelicRoot(t, repo, config, part)
		relicID := root.Operation.RelicID
		// The part's first body is version c1, the part's own ID. Alice saves
		// c2 from it.
		c1, c2 := part.id, relicTestID(700001)
		alice := []byte("Alice's text, saved from the first version\n")
		edited := writeRelicChild(t, repo, config, root,
			map[string]core.RelicPartContent{part.id: {Body: alice, HasBody: true}},
			relicBodySet(700001, part.id, c1, alice))

		// Bob also started from c1. His edit is written into Git by hand, as a
		// push from a checkout that never saw Alice's would deliver it: the pack
		// holds his operation, the state is what the fold makes of it, and the
		// tree keeps the body it had.
		bob := relicChildPack(edited, relicBodySet(700002, part.id, c1, []byte("Bob's text, saved from the first version\n")))
		bob.Actor.ID = "bob@example.test"
		forged := forgeRelicChild(t, repo, edited, bob, forgedPartsOf(t, repo, edited.Head))
		pointRelicRef(t, repo, relicID, forged)

		got, err := repo.GetRelic(ctx, config, relicID)
		if err != nil {
			t.Fatalf("GetRelic() error = %v", err)
		}
		if got.Head != forged {
			t.Fatalf("GetRelic() head = %s, want the forged commit %s", got.Head, forged)
		}
		stored, _ := got.Record.State.Relic.Part(part.id)
		if stored.Body == nil || stored.Body.Version != c2 {
			t.Fatalf("body after the stale edit = %+v, want version %s", stored.Body, c2)
		}
		parts, err := repo.ReadRelicParts(ctx, config, got, []string{part.id}, true)
		if err != nil {
			t.Fatalf("ReadRelicParts() error = %v", err)
		}
		if content := parts[part.id]; !content.HasBody || !bytes.Equal(content.Body, alice) {
			t.Fatalf("body text after the stale edit = %q, want %q", content.Body, alice)
		}
		assertRelicHistoryValid(t, repo, config, relicID, forged, 3)

		// Bob's edit changed nothing, and it is still in history under his name.
		if got.Operation.Actor.ID != "bob@example.test" {
			t.Fatalf("the tip's pack is recorded under %q, want Bob", got.Operation.Actor.ID)
		}
		if operations := got.Operation.Operations; len(operations) != 1 ||
			operations[0].ID != relicTestID(700002) ||
			operations[0].Type != core.RelicOperationPartBodySet ||
			operations[0].Body.Base != c1 {
			t.Fatalf("the tip's pack holds %+v, want Bob's body edit from %s", operations, c1)
		}
	})
}

func TestAForgedOverwriteIsRefusedOnReadAndNamedByValidate(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withBody("md", []byte("the first text\n"))
	root := writeRelicRoot(t, repo, config, part)
	relicID := root.Operation.RelicID
	c1, c3 := part.id, relicTestID(700002)
	alice := []byte("Alice's text, saved from the first version\n")
	edited := writeRelicChild(t, repo, config, root,
		map[string]core.RelicPartContent{part.id: {Body: alice, HasBody: true}},
		relicBodySet(700001, part.id, c1, alice))

	// The same stale edit, but this time whoever wrote the commit made it win:
	// the state claims Bob's operation saved the body, and the tree holds his
	// text.
	bobText := []byte("Bob's text, saved from the first version\n")
	bob := relicChildPack(edited, relicBodySet(700002, part.id, c1, bobText))
	bob.Actor.ID = "bob@example.test"
	claimed := foldRelic(t, &edited, bob).State
	claimed.Relic.Parts = slices.Clone(claimed.Relic.Parts)
	claimed.Relic.Parts[0].Body = &core.RelicBody{Format: "md", Version: c3, Bytes: int64(len(bobText))}
	forged := forgeRelicCommit(t, repo, edited.Head,
		encodedRelicDocument(t, bob), encodedRelicDocument(t, claimed),
		map[string]forgedPart{part.id: {fields: forgedFields(t), body: bobText, hasBody: true}})
	pointRelicRef(t, repo, relicID, forged)

	_, err := repo.GetRelic(ctx, config, relicID)
	assertGetRelicRefused(t, err)
	assertRelicCategory(t, err, core.CategoryCorruptData, "workbook validate")
	assertRelicHistoryStopsAt(t, repo, config, relicID, forged, forged, 2, core.CategoryCorruptData)

	// The list read does not look at the parent, so it still serves the tip.
	listed := readRelicTip(t, repo, config, relicID)
	if stored, _ := listed.Record.State.Relic.Part(part.id); listed.Head != forged || stored.Body.Version != c3 {
		t.Fatalf("ReadRelicTips() = %s with body version %s, want the forged tip %s claiming %s",
			listed.Head, stored.Body.Version, forged, c3)
	}
}

// Validation reports the first commit that breaks a rule, however many valid
// commits were written on top of it. A single-relic read checks only the
// newest commit against the one before it.
func TestValidateRelicHistoryStopsAtTheFirstBadCommit(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	tip, a, _ := relicRuleHistory(t, repo, config)
	relicID := tip.Operation.RelicID

	// A field edit whose tree does not apply it.
	bad := forgeRelicChild(t, repo, tip,
		relicChildPack(tip, relicFieldSet(700010, a.id, "a", "new")), forgedPartsOf(t, repo, tip.Head))
	pointRelicRef(t, repo, relicID, bad)
	later := writeRelicChild(t, repo, config, readRelicTip(t, repo, config, relicID), nil,
		relicTitleSet(700011, "A later title"))
	later = writeRelicChild(t, repo, config, later, nil, relicTitleSet(700012, "A still later title"))

	assertRelicHistoryStopsAt(t, repo, config, relicID, later.Head, bad, 2, core.CategoryCorruptData, a.id)
	got, err := repo.GetRelic(ctx, config, relicID)
	if err != nil {
		t.Fatalf("GetRelic() error = %v", err)
	}
	assertSameRelicSnapshot(t, got, later)
}

// forgeNewerRelicTip puts on top of root, as the relic's tip, what a later
// Workbook writes when it retires the relic: an operation type and a state
// this build has no rules for, marked generation 7. It returns that commit.
func forgeNewerRelicTip(t *testing.T, repo *Repository, config core.ProjectConfig, root core.RelicSnapshot) string {
	t.Helper()
	relicID := root.Operation.RelicID
	raise := func(document []byte, replacements ...string) []byte {
		for index := 0; index < len(replacements); index += 2 {
			raised := bytes.Replace(document, []byte(replacements[index]), []byte(replacements[index+1]), 1)
			if bytes.Equal(raised, document) {
				t.Fatalf("the document holds no %s to replace", replacements[index])
			}
			document = raised
		}
		return document
	}
	operationJSON := []byte(`{"format":"workbook.relic-operation-pack","version":1,"minReader":7,` +
		`"projectId":"` + config.ProjectID + `","relicId":"` + relicID + `",` +
		`"historyGeneration":"` + root.Operation.HistoryGeneration + `","actor":{"id":"` + relicTestActor + `"},` +
		`"logicalClock":2,"wallTime":"2026-11-23T10:01:00Z",` +
		`"operations":[{"id":"` + relicTestID(700001) + `","type":"relic.retire"}]}` + "\n")
	stateJSON := raise(relicBlob(t, repo, root.Head+":state.json"),
		`"minReader":6`, `"minReader":7`,
		`"logicalClock":1`, `"logicalClock":2`,
		`"retired":false`, `"retired":true`)
	forged := forgeRelicCommit(t, repo, root.Head, operationJSON, stateJSON, forgedPartsOf(t, repo, root.Head))
	pointRelicRef(t, repo, relicID, forged)
	return forged
}

func TestGetRelicReturnsANewerWritersRelicUnverified(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withBody("md", []byte("a body\n"))
	root := writeRelicRoot(t, repo, config, part)
	relicID := root.Operation.RelicID
	forged := forgeNewerRelicTip(t, repo, config, root)

	got, err := repo.GetRelic(ctx, config, relicID)
	if err != nil {
		t.Fatalf("GetRelic() error = %v", err)
	}
	if got.Head != forged || !got.Record.State.Relic.Retired {
		t.Fatalf("GetRelic() = %s with retired %t, want the newer commit %s with retired true",
			got.Head, got.Record.State.Relic.Retired, forged)
	}
	if got.Operation.Operations[0].Type != "relic.retire" || !got.Operation.RequiresNewerReader() {
		t.Fatalf("GetRelic() pack = %+v, want the newer writer's relic.retire", got.Operation)
	}
	assertRelicHistoryStopsAt(t, repo, config, relicID, forged, forged, 1, core.CategoryNewerWriter, relicID)

	// This build cannot tell whether a commit follows from a newer one, so a
	// commit on top of the newer writer's is not served as verified.
	pack := core.NewRelicOperationPack(config.ProjectID, relicID, root.Operation.HistoryGeneration, relicTestActor,
		3, relicTestTime, []core.RelicOperation{relicTitleSet(700002, "A title from the older build")})
	state := root.Record.State
	state.LogicalClock, state.Relic.Title, state.Relic.UpdatedAt = 3, "A title from the older build", relicTestTime
	onTop := forgeRelicCommit(t, repo, forged,
		encodedRelicDocument(t, pack), encodedRelicDocument(t, state), forgedPartsOf(t, repo, root.Head))
	pointRelicRef(t, repo, relicID, onTop)
	_, err = repo.GetRelic(ctx, config, relicID)
	assertRelicCategory(t, err, core.CategoryNewerWriter, relicID)
	assertRelicHistoryStopsAt(t, repo, config, relicID, onTop, forged, 1, core.CategoryNewerWriter)
}

func TestGetRelicMissing(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	written := writeRelicRoot(t, repo, config)
	task := writeIndependentRoot(t, repo, config, writeTaskID, writeGenerationID, "01K0M6B8A4FTT8C39MXXYTW7C4", "A task")

	for name, relicID := range map[string]string{
		"a relic that was never created": relicTestRelicID(2),
		"a task ID":                      task.Operation.TaskID,
		"a relic ID under another key":   core.NewRelicID("OTHER", relicTestID(600001)),
		"not an ID at all":               "refs/workbook/relics/*",
		"nothing":                        "",
	} {
		_, err := repo.GetRelic(ctx, config, relicID)
		if core.CategoryOf(err) != core.CategoryNotFound || !strings.Contains(err.Error(), "was not found") {
			t.Fatalf("%s: GetRelic(%q) error = %v, want not-found", name, relicID, err)
		}
	}
	got, err := repo.GetRelic(ctx, config, written.Operation.RelicID)
	if err != nil {
		t.Fatalf("GetRelic() error = %v", err)
	}
	assertSameRelicSnapshot(t, got, written)
}

func TestReadRelicCommitReportsWhatTheCommitStores(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		a := relicTestPartNumbered(1).withField("heading", "Goals").withBody("md", []byte("the goals\n"))
		b := relicTestPartNumbered(2)
		root := writeRelicRoot(t, repo, config, a, b)
		child := writeRelicChild(t, repo, config, root, nil, relicTitleSet(700001, "A new title"))
		relicID := root.Operation.RelicID

		commit, err := repo.ReadRelicCommit(ctx, config, relicID, child.Head)
		if err != nil {
			t.Fatalf("ReadRelicCommit() error = %v", err)
		}
		if commit.ObjectID != child.Head || commit.Parent != root.Head {
			t.Fatalf("commit = %s with parent %q, want %s with parent %s", commit.ObjectID, commit.Parent, child.Head, root.Head)
		}
		assertSameRelicSnapshot(t,
			core.RelicSnapshot{Head: commit.ObjectID, Operation: commit.Operation, Record: commit.Record}, child)
		objectOf := func(path string) string { return gitOutput(t, repo, "rev-parse", child.Head+":"+path) }
		want := map[string]RelicPartObjects{
			a.id: {
				Tree:        objectOf("parts/" + a.id),
				Fields:      objectOf("parts/" + a.id + "/fields.json"),
				Body:        objectOf("parts/" + a.id + "/body"),
				FieldsBytes: int64(len(relicBlob(t, repo, child.Head+":parts/"+a.id+"/fields.json"))),
				BodyBytes:   int64(len(a.body)),
			},
			b.id: {
				Tree:        objectOf("parts/" + b.id),
				Fields:      objectOf("parts/" + b.id + "/fields.json"),
				FieldsBytes: int64(len(forgedFields(t))),
			},
		}
		if !maps.Equal(commit.Parts, want) {
			t.Fatalf("commit parts = %+v, want %+v", commit.Parts, want)
		}

		first, err := repo.ReadRelicCommit(ctx, config, relicID, root.Head)
		if err != nil {
			t.Fatalf("ReadRelicCommit(root) error = %v", err)
		}
		if first.Parent != "" {
			t.Fatalf("root commit parent = %q, want none", first.Parent)
		}

		// A relic with no part has no parts tree to list.
		bare := writeRelicRootNumbered(t, repo, config, 2)
		empty, err := repo.ReadRelicCommit(ctx, config, bare.Operation.RelicID, bare.Head)
		if err != nil || len(empty.Parts) != 0 {
			t.Fatalf("ReadRelicCommit(no parts) = %+v, %v", empty.Parts, err)
		}

		// A commit is read as the relic the caller names, or not at all.
		_, err = repo.ReadRelicCommit(ctx, config, bare.Operation.RelicID, child.Head)
		assertRelicCategory(t, err, core.CategoryCorruptData)
		_, err = repo.ReadRelicCommit(ctx, config, relicID, "not-an-object-id")
		assertRelicCategory(t, err, core.CategoryValidation)
	})
}

// What a read costs in Git processes depends on neither the number of parts
// nor, for a single-relic read, the length of the history.
func TestReadingARelicUsesABoundedNumberOfGitProcesses(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	var commands [][]string
	repo.commandObserver = func(args []string) { commands = append(commands, args) }
	// history writes a relic of partCount parts in three commits: the create,
	// a title change, and one field edit of every part.
	history := func(relicNumber, partCount int) core.RelicSnapshot {
		t.Helper()
		parts := make([]relicTestPart, partCount)
		content := make(map[string]core.RelicPartContent, partCount)
		edits := make([]core.RelicOperation, partCount)
		for index := range parts {
			parts[index] = relicTestPartNumbered(relicNumber*1000+index).
				withField("heading", fmt.Sprintf("Section %d", index)).
				withBody("md", []byte(fmt.Sprintf("Body %d of relic %d.\n", index, relicNumber)))
			edits[index] = relicFieldSet(relicNumber*100000+index, parts[index].id, "status", "open")
			content[parts[index].id] = core.RelicPartContent{
				Fields: core.ApplyRelicFields(parts[index].fields, parts[index].id, edits[index:index+1]),
			}
		}
		tip := writeRelicRootNumbered(t, repo, config, relicNumber, parts...)
		tip = writeRelicChild(t, repo, config, tip, nil, relicTitleSet(relicNumber*100000+90000, "A new title"))
		return writeRelicChild(t, repo, config, tip, content, edits...)
	}
	small, large := history(1, 3), history(2, 300)

	get := func(tip core.RelicSnapshot) int {
		t.Helper()
		commands = nil
		got, err := repo.GetRelic(ctx, config, tip.Operation.RelicID)
		if err != nil || got.Head != tip.Head {
			t.Fatalf("GetRelic() = %s, %v; want %s", got.Head, err, tip.Head)
		}
		return len(commands)
	}
	validate := func(tip core.RelicSnapshot) int {
		t.Helper()
		commands = nil
		assertRelicHistoryValid(t, repo, config, tip.Operation.RelicID, tip.Head, 3)
		return len(commands)
	}

	// The ref, then the documents of the tip and of its parent, then the parts
	// of both together: their parts trees, their parts' subtrees and the sizes
	// of their blobs.
	getSmall, getLarge := get(small), get(large)
	getCommands := commands
	if getLarge != getSmall || getLarge > 6 {
		t.Fatalf("GetRelic ran %d Git commands for 3 parts and %d for 300, want the same and at most 6: %q",
			getSmall, getLarge, getCommands)
	}
	// One listing of the history, then at most five per commit: its documents,
	// its parts tree, its parts' subtrees, the sizes of their blobs, and the
	// fields rule R7 has to read.
	validateSmall, validateLarge := validate(small), validate(large)
	if validateLarge != validateSmall || validateLarge > 1+3*5 {
		t.Fatalf("ValidateRelicHistory ran %d Git commands for 3 parts and %d for 300, want the same and at most 16: %q",
			validateSmall, validateLarge, commands)
	}
	t.Logf("Git commands: GetRelic %d, ValidateRelicHistory of 3 commits %d", getLarge, validateLarge)
}

func TestVerifyRelicTransitionRefusesACommitThatIsNotTheParent(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	root := writeRelicRoot(t, repo, config, relicTestPartNumbered(1))
	child := writeRelicChild(t, repo, config, root, nil, relicTitleSet(700001, "A new title"))
	grandchild := writeRelicChild(t, repo, config, child, nil, relicTitleSet(700002, "A newer title"))
	read := func(objectID string) RelicCommit {
		t.Helper()
		commit, err := repo.ReadRelicCommit(ctx, config, root.Operation.RelicID, objectID)
		if err != nil {
			t.Fatalf("ReadRelicCommit(%s) error = %v", objectID, err)
		}
		return commit
	}
	first, second, third := read(root.Head), read(child.Head), read(grandchild.Head)

	for _, deep := range []bool{false, true} {
		if err := repo.VerifyRelicTransition(ctx, nil, first, deep); err != nil {
			t.Fatalf("VerifyRelicTransition(root, deep %t) error = %v", deep, err)
		}
		if err := repo.VerifyRelicTransition(ctx, &second, third, deep); err != nil {
			t.Fatalf("VerifyRelicTransition(deep %t) error = %v", deep, err)
		}
		// The rules compare a commit with its own parent and no other commit.
		assertRelicCategory(t, repo.VerifyRelicTransition(ctx, &first, third, deep), core.CategoryValidation)
		assertRelicCategory(t, repo.VerifyRelicTransition(ctx, nil, third, deep), core.CategoryValidation)
		assertRelicCategory(t, repo.VerifyRelicTransition(ctx, &second, first, deep), core.CategoryValidation)
	}
}

// A head that is not a relic commit at all is a failure of that one relic,
// named like any other, and not an error that stops the whole validation.
func TestValidateRelicHistoryNamesAHeadThatIsNotARelicCommit(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	root := writeRelicRoot(t, repo, config, relicTestPartNumbered(1))
	other := writeRelicRootNumbered(t, repo, config, 2)
	relicID := root.Operation.RelicID

	heads := map[string]string{
		"a tree":                      gitOutput(t, repo, "rev-parse", root.Head+"^{tree}"),
		"an object Git does not have": strings.Repeat("0", len(root.Head)-1) + "1",
		"another relic's commit":      other.Head,
	}
	names := slices.Collect(maps.Keys(heads))
	sort.Strings(names)
	for _, name := range names {
		assertRelicHistoryStopsAt(t, repo, config, relicID, heads[name], heads[name], 0, core.CategoryCorruptData)
	}

	_, _, err := repo.ValidateRelicHistory(ctx, config, core.RelicHead{RelicID: relicID, ObjectID: "not-an-object-id"})
	assertRelicCategory(t, err, core.CategoryValidation)
}

// largeRelicString is a string value of exactly size stored bytes.
func largeRelicString(size int) string { return strings.Repeat("x", size-2) }

// All of a relic's properties together, and all of one part's fields
// together, are totals. Two changes that are each within every limit add up
// to a relic past both, and every reader still reads it: the tip read, the
// single-relic read, the part read and the whole-history check. A later story
// replays edits made on two machines, and gets such a relic honestly.
func TestARelicPastItsPropertyAndFieldTotalsStaysReadable(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		part := relicTestPartNumbered(1).withBody("md", []byte("the goals\n"))
		tip := writeRelicRoot(t, repo, config, part)
		relicID := tip.Record.State.RelicID
		tip = writeRelicChild(t, repo, config, tip, nil, relicPropertySet(700001, "a", largeRelicString(40000)))
		tip = writeRelicChild(t, repo, config, tip, nil, relicPropertySet(700002, "b", largeRelicString(40000)))
		for number, name := range []string{"a", "b"} {
			operation := relicFieldSet(700003+number, part.id, name, largeRelicString(9000))
			tip = writeRelicChild(t, repo, config, tip, map[string]core.RelicPartContent{
				part.id: {Fields: relicFieldsAfter(t, repo, config, tip, part.id, operation)},
			}, operation)
		}
		if size := len(encodedRelicDocument(t, tip.Record.State)); size <= core.MaxRelicPropertiesBytes {
			t.Fatalf("state.json is %d bytes; the test needs its properties past their total", size)
		}
		if size := len(relicBlob(t, repo, tip.Head+":parts/"+part.id+"/fields.json")); size <= core.MaxRelicPartFieldsBytes {
			t.Fatalf("fields.json is %d bytes; the test needs it past the total", size)
		}

		assertSameRelicSnapshot(t, readRelicTip(t, repo, config, relicID), tip)
		got, err := repo.GetRelic(ctx, config, relicID)
		if err != nil {
			t.Fatalf("GetRelic() error = %v", err)
		}
		assertSameRelicSnapshot(t, got, tip)
		parts, err := repo.ReadRelicParts(ctx, config, tip, []string{part.id}, true)
		if err != nil || len(parts[part.id].Fields) != 2 {
			t.Fatalf("ReadRelicParts() = %d fields, %v; want both", len(parts[part.id].Fields), err)
		}
		assertRelicHistoryValid(t, repo, config, relicID, tip.Head, 5)

		// And the service changes it in every way that does not grow a total.
		service := relicServiceFor(repo, config, 710000)
		for _, change := range []struct {
			label string
			edits []core.RelicEdit
		}{
			{"the title changed", []core.RelicEdit{{Kind: core.RelicEditTitle, Title: "Still editable"}}},
			{"a body saved", []core.RelicEdit{{Kind: core.RelicEditPart, Part: part.id, Base: part.id, Body: &core.RelicBodyInput{Content: []byte("new text\n")}}}},
			{"a property set to a value of the same size", []core.RelicEdit{{Kind: core.RelicEditSetProperty, Name: "b", Value: relicJSONString(t, strings.Repeat("y", 40000-2))}}},
			{"a field set to a value of the same size", []core.RelicEdit{{Kind: core.RelicEditPart, Part: part.id, Fields: []core.RelicFieldEdit{{Name: "b", Value: relicJSONString(t, strings.Repeat("y", 9000-2))}}}}},
		} {
			result, err := service.Update(ctx, relicID, change.edits)
			if err != nil || !result.Changed {
				t.Fatalf("%s: Update() = %+v, %v; want it written", change.label, result.Relic, err)
			}
		}
		for _, change := range []struct {
			label string
			edits []core.RelicEdit
		}{
			{"a property added", []core.RelicEdit{{Kind: core.RelicEditSetProperty, Name: "c", Value: relicJSONString(t, "more")}}},
			{"a field added", []core.RelicEdit{{Kind: core.RelicEditPart, Part: part.id, Fields: []core.RelicFieldEdit{{Name: "c", Value: relicJSONString(t, "more")}}}}},
		} {
			_, err := service.Update(ctx, relicID, change.edits)
			assertRelicCategory(t, err, core.CategoryValidation, "the limit is")
		}
		for _, change := range []struct {
			label string
			edits []core.RelicEdit
		}{
			{"a property cleared", []core.RelicEdit{{Kind: core.RelicEditClearProperty, Name: "a"}}},
			{"a field set to a smaller value", []core.RelicEdit{{Kind: core.RelicEditPart, Part: part.id, Fields: []core.RelicFieldEdit{{Name: "b", Value: relicJSONString(t, "short")}}}}},
		} {
			result, err := service.Update(ctx, relicID, change.edits)
			if err != nil || !result.Changed {
				t.Fatalf("%s: Update() = %+v, %v; want it written", change.label, result.Relic, err)
			}
		}
		head := relicRefHead(t, repo, relicID)
		assertRelicHistoryValid(t, repo, config, relicID, head, 11)
	})
}

// One property value, or one field value, past the limit for one value is not
// a total. It is corrupt data to every reader that opens the document holding
// it.
func TestOneRelicValuePastItsLimitIsCorruptData(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1)
	root := writeRelicRoot(t, repo, config, part)
	relicID := root.Record.State.RelicID
	stored := forgedPartsOf(t, repo, root.Head)

	// A property: the pack and the state are forged around the fold, which
	// refuses the value itself.
	value := largeRelicString(core.MaxRelicPropertyValueBytes + 1)
	honest := relicChildPack(root, relicPropertySet(700001, "long", "short"))
	record := foldRelic(t, &root, honest)
	operationJSON := bytes.Replace(encodedRelicDocument(t, honest), []byte(`"short"`), []byte(`"`+value+`"`), 1)
	stateJSON := bytes.Replace(encodedRelicDocument(t, record.State), []byte(`"short"`), []byte(`"`+value+`"`), 1)
	forged := forgeRelicCommit(t, repo, root.Head, operationJSON, stateJSON, stored)
	pointRelicRef(t, repo, relicID, forged)
	assertRelicHistoryStopsAt(t, repo, config, relicID, forged, forged, 1, core.CategoryCorruptData, "65537", "65536")
	_, err := repo.GetRelic(ctx, config, relicID)
	assertGetRelicRefused(t, err)
	heads, err := repo.ListRelicHeads(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.ReadRelicTips(ctx, config, heads)
	assertRelicCategory(t, err, core.CategoryCorruptData, "65537", "65536")
	if len(err.Error()) > 500 {
		t.Fatalf("the refusal is %d bytes long; it must not carry the value", len(err.Error()))
	}

	// A field: the fields.json a new part arrives with holds the value.
	added := relicTestID(3)
	oversized, err := json.Marshal(core.NewRelicFields(relicFieldValueOfSize(core.MaxRelicFieldValueBytes + 1)))
	if err != nil {
		t.Fatal(err)
	}
	pack := relicChildPack(root, relicPartAdd(added, "3/1", nil))
	forged = forgeRelicChild(t, repo, root, pack, withForgedPart(stored, added, forgedPart{fields: append(oversized, '\n')}))
	pointRelicRef(t, repo, relicID, forged)
	assertRelicHistoryStopsAt(t, repo, config, relicID, forged, forged, 1, core.CategoryCorruptData, added, "16385", "16384")
	tip, err := repo.GetRelic(ctx, config, relicID)
	if err != nil {
		t.Fatalf("GetRelic() error = %v; a single-relic read does not open fields.json", err)
	}
	_, err = repo.ReadRelicParts(ctx, config, tip, []string{added}, false)
	assertRelicCategory(t, err, core.CategoryCorruptData, added, "16385", "16384")
	if len(err.Error()) > 500 {
		t.Fatalf("the refusal is %d bytes long; it must not carry the value", len(err.Error()))
	}
}

// The fold sees an ID used twice only where the state still holds the first
// use. A body's earlier versions and every other kind of operation leave no
// such trace, so a commit that takes one of those IDs again folds like any
// other, and a single-relic read serves it. The full walk has every pack in
// front of it and refuses the commit that repeats.
func TestValidateRelicHistoryRefusesAnOperationIDInTwoCommits(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		part := relicTestPartNumbered(1).withBody("md", []byte("the first text\n"))
		root := writeRelicRoot(t, repo, config, part)
		relicID := root.Operation.RelicID
		write := func(parent core.RelicSnapshot, number int, base string, text string) core.RelicSnapshot {
			t.Helper()
			return writeRelicChild(t, repo, config, parent,
				map[string]core.RelicPartContent{part.id: {Body: []byte(text), HasBody: true}},
				relicBodySet(number, part.id, base, []byte(text)))
		}
		// The body goes from its first version to n1, then to n2.
		n1, n2 := relicTestID(700001), relicTestID(700002)
		first := write(root, 700001, part.id, "the second text\n")
		second := write(first, 700002, n1, "the third text\n")
		titled := writeRelicChild(t, repo, config, second, nil, relicTitleSet(700003, "A new title"))
		assertRelicHistoryValid(t, repo, config, relicID, titled.Head, 4)

		// forged writes pack onto titled the way an honest writer would have,
		// had the ID been new: the state the fold computes and the tree that
		// state describes.
		forged := func(pack core.RelicOperationPack, parts map[string]forgedPart) string {
			t.Helper()
			commit := forgeRelicChild(t, repo, titled, pack, parts)
			pointRelicRef(t, repo, relicID, commit)
			got, err := repo.GetRelic(ctx, config, relicID)
			if err != nil || got.Head != commit {
				t.Fatalf("GetRelic() = %s, %v; want %s: a single-relic read does not walk history", got.Head, err, commit)
			}
			return commit
		}
		stored := forgedPartsOf(t, repo, titled.Head)

		// A body version that named the second text now names a fourth: a save
		// made from n1 by someone who read the second text would overwrite it.
		again := []byte("a fourth text under the second one's version\n")
		body := stored[part.id]
		body.body = again
		bodyAgain := forged(relicChildPack(titled, relicBodySet(700001, part.id, n2, again)),
			withForgedPart(stored, part.id, body))
		if got := readRelicTip(t, repo, config, relicID); func() string {
			stored, _ := got.Record.State.Relic.Part(part.id)
			return stored.Body.Version
		}() != n1 {
			t.Fatal("the forged commit did not give the body the version n1 again; the test needs it to")
		}
		assertRelicHistoryStopsAt(t, repo, config, relicID, bodyAgain, bodyAgain, 4,
			core.CategoryCorruptData, relicID, n1, first.Head)

		// Any other operation's ID, here a title change's.
		titleAgain := forged(relicChildPack(titled, relicTitleSet(700003, "Another title")), stored)
		assertRelicHistoryStopsAt(t, repo, config, relicID, titleAgain, titleAgain, 4,
			core.CategoryCorruptData, relicID, relicTestID(700003), titled.Head)

		// The ID of the relic.create itself.
		created := root.Operation.Operations[0]
		if created.Type != core.RelicOperationCreate {
			t.Fatalf("the root pack opens with %s; the test needs relic.create", created.Type)
		}
		reused := core.RelicOperation{ID: created.ID, Type: core.RelicOperationTitleSet, Title: "A title under the create's ID"}
		createAgain := forged(relicChildPack(titled, reused), stored)
		assertRelicHistoryStopsAt(t, repo, config, relicID, createAgain, createAgain, 4,
			core.CategoryCorruptData, relicID, created.ID, root.Head)

		// The honest history is untouched by all of it.
		pointRelicRef(t, repo, relicID, titled.Head)
		assertRelicHistoryValid(t, repo, config, relicID, titled.Head, 4)
	})
}

// An operation ID is used once along one relic's own history. Nothing compares
// one ref with another: two relics whose packs carry the same IDs are both
// valid, and so is a second walk that starts at an older commit of a relic,
// which shares every commit and every ID with the walk from its tip.
func TestValidateRelicHistoryNeverComparesOperationIDsAcrossRefs(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	part := relicTestPartNumbered(1).withBody("md", []byte("the first text\n"))
	edit := func(root core.RelicSnapshot) core.RelicSnapshot {
		t.Helper()
		titled := writeRelicChild(t, repo, config, root, nil, relicTitleSet(700001, "A new title"))
		return writeRelicChild(t, repo, config, titled,
			map[string]core.RelicPartContent{part.id: {Body: []byte("the second text\n"), HasBody: true}},
			relicBodySet(700002, part.id, part.id, []byte("the second text\n")))
	}
	one := edit(writeRelicRootNumbered(t, repo, config, 1, part))
	two := edit(writeRelicRootNumbered(t, repo, config, 2, part))
	if one.Operation.RelicID == two.Operation.RelicID {
		t.Fatal("the two relics have one ID; the test needs two")
	}
	shared := map[string]bool{}
	for _, operation := range one.Operation.Operations {
		shared[operation.ID] = true
	}
	for _, operation := range two.Operation.Operations {
		if !shared[operation.ID] {
			t.Fatalf("the two relics' newest packs do not share operation %s; the test needs them to", operation.ID)
		}
	}
	assertRelicHistoryValid(t, repo, config, one.Operation.RelicID, one.Head, 3)
	assertRelicHistoryValid(t, repo, config, two.Operation.RelicID, two.Head, 3)

	// An older commit of the first relic, walked on its own as a second ref
	// holding it would be.
	older := relicParents(t, repo, one.Head)[0]
	assertRelicHistoryValid(t, repo, config, one.Operation.RelicID, older, 2)
	assertRelicHistoryValid(t, repo, config, one.Operation.RelicID, one.Head, 3)
}

// A stored time may carry an offset. A pack and a state that agree on one are
// served, keep their bytes, and pass validation; a state whose time is not the
// pack's, in the same bytes, does not follow from its operations. A zero
// offset written +00:00 is not the documents' own re-encoding, so every read
// refuses it.
func TestATimeStoredWithAnOffsetIsKept(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		root := writeRelicRoot(t, repo, config, relicTestPartNumbered(1).withBody("md", []byte("the first text\n")))
		relicID := root.Operation.RelicID
		pack := relicChildPack(root, relicTitleSet(700001, "A new title"))
		record := foldRelic(t, &root, pack)

		const written = `"2026-11-23T10:01:00Z"`
		rewrite := func(document []byte, member, elsewhere string) []byte {
			t.Helper()
			forged := bytes.Replace(document, []byte(member+written), []byte(member+elsewhere), 1)
			if bytes.Equal(forged, document) {
				t.Fatalf("the document holds no %s%s to rewrite: %s", member, written, document)
			}
			return forged
		}

		const elsewhere = `"2026-11-23T15:01:00+05:00"`
		statePacked := rewrite(encodedRelicDocument(t, record.State), `"updatedAt":`, elsewhere)
		forged := forgeRelicCommit(t, repo, root.Head,
			rewrite(encodedRelicDocument(t, pack), `"wallTime":`, elsewhere), statePacked, forgedPartsOf(t, repo, root.Head))
		pointRelicRef(t, repo, relicID, forged)
		snapshot, err := repo.GetRelic(ctx, config, relicID)
		if err != nil {
			t.Fatalf("GetRelic() with an offset in the pack and the state: %v", err)
		}
		if again := encodedRelicDocument(t, snapshot.Record.State); !bytes.Equal(again, statePacked) {
			t.Fatalf("the state read back encodes as\n%s\nnot\n%s", again, statePacked)
		}
		if _, err := repo.ReadRelicTips(ctx, config, []core.RelicHead{{RelicID: relicID, ObjectID: forged}}); err != nil {
			t.Fatalf("ReadRelicTips() with an offset in the pack and the state: %v", err)
		}
		assertRelicHistoryValid(t, repo, config, relicID, forged, 2)

		for name, documents := range map[string][2][]byte{
			"in the pack alone":  {rewrite(encodedRelicDocument(t, pack), `"wallTime":`, elsewhere), encodedRelicDocument(t, record.State)},
			"in the state alone": {encodedRelicDocument(t, pack), statePacked},
		} {
			forged := forgeRelicCommit(t, repo, root.Head, documents[0], documents[1], forgedPartsOf(t, repo, root.Head))
			pointRelicRef(t, repo, relicID, forged)
			if _, err := repo.GetRelic(ctx, config, relicID); err == nil {
				t.Fatalf("%s: GetRelic() served a state that does not follow from its pack", name)
			} else {
				assertGetRelicRefused(t, err)
				assertRelicCategory(t, err, core.CategoryCorruptData, relicID, "")
			}
		}

		for _, zero := range []string{`"2026-11-23T10:01:00+00:00"`, `"2026-11-23T10:01:00-00:00"`} {
			forged := forgeRelicCommit(t, repo, root.Head,
				rewrite(encodedRelicDocument(t, pack), `"wallTime":`, zero),
				rewrite(encodedRelicDocument(t, record.State), `"updatedAt":`, zero),
				forgedPartsOf(t, repo, root.Head))
			pointRelicRef(t, repo, relicID, forged)
			_, err := repo.GetRelic(ctx, config, relicID)
			if err == nil {
				t.Fatalf("GetRelic() served a time written %s", zero)
			}
			assertGetRelicRefused(t, err)
			assertRelicCategory(t, err, core.CategoryCorruptData, relicID, "a zero offset is written Z")
			assertRelicHistoryStopsAt(t, repo, config, relicID, forged, forged, 1,
				core.CategoryCorruptData, relicID, "a zero offset is written Z")
		}
	})
}

// Tips are verified together as a single-relic read verifies one: each against
// its own parent, each failing alone, through the same Git processes however
// many there are.
func TestVerifyRelicTipsChecksEachTipAgainstItsParent(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	text := func(number int) []byte { return []byte(fmt.Sprintf("the text of relic %d\n", number)) }
	rootOf := func(number int) core.RelicSnapshot {
		t.Helper()
		return writeRelicRootNumbered(t, repo, config, number,
			relicTestPartNumbered(1).withBody("md", text(number)), relicTestPartNumbered(2).withField("status", "open"))
	}
	head := func(snapshot core.RelicSnapshot, objectID string) core.RelicHead {
		return core.RelicHead{RelicID: snapshot.Operation.RelicID, ObjectID: objectID}
	}

	alone := rootOf(1)
	parent := rootOf(2)
	child := writeRelicChild(t, repo, config, parent, nil, relicTitleSet(700001, "A new title"))

	// A commit that replaces a body no operation in it saved.
	overwritten := rootOf(3)
	forgedParts := forgedPartsOf(t, repo, overwritten.Head)
	edit := forgedParts[relicTestID(1)]
	edit.body = []byte("text no operation saved\n")
	forged := forgeRelicChild(t, repo, overwritten, relicChildPack(overwritten, relicTitleSet(700002, "Another title")),
		withForgedPart(forgedParts, relicTestID(1), edit))

	// A tip a newer Workbook wrote, and this build's commit on top of one.
	newerRoot := rootOf(4)
	newer := forgeNewerRelicTip(t, repo, config, newerRoot)
	belowRoot := rootOf(5)
	below := forgeNewerRelicTip(t, repo, config, belowRoot)
	pack := core.NewRelicOperationPack(config.ProjectID, belowRoot.Operation.RelicID, belowRoot.Operation.HistoryGeneration,
		relicTestActor, 3, relicTestTime, []core.RelicOperation{relicTitleSet(700003, "A title from the older build")})
	state := belowRoot.Record.State
	state.LogicalClock, state.Relic.Title, state.Relic.UpdatedAt = 3, "A title from the older build", relicTestTime
	onTop := forgeRelicCommit(t, repo, below,
		encodedRelicDocument(t, pack), encodedRelicDocument(t, state), forgedPartsOf(t, repo, belowRoot.Head))

	// A tip whose parent Git does not have.
	orphanRoot := rootOf(6)
	orphan := writeRelicChild(t, repo, config, orphanRoot, nil, relicTitleSet(700004, "A new title"))

	heads := []core.RelicHead{
		head(alone, alone.Head), head(child, child.Head), head(overwritten, forged),
		head(newerRoot, newer), head(belowRoot, onTop), head(orphanRoot, orphan.Head),
	}
	loseRelicObject(t, repo, orphanRoot.Head)

	commands := observeCommands(repo)
	requests := observeRelicObjects(repo)
	commits, errs := repo.verifyRelicTips(ctx, config, heads)
	ran, asked := *commands, *requests
	repo.commandObserver, repo.relicObjectObserver = nil, nil
	if len(commits) != len(heads) || len(errs) != len(heads) {
		t.Fatalf("verifyRelicTips() returned %d commits and %d errors for %d heads", len(commits), len(errs), len(heads))
	}

	for index, want := range []core.RelicSnapshot{alone, child} {
		if errs[index] != nil {
			t.Fatalf("verifyRelicTips() error for a sound tip = %v", errs[index])
		}
		assertSameRelicSnapshot(t, core.RelicSnapshot{
			Head: commits[index].ObjectID, Operation: commits[index].Operation, Record: commits[index].Record,
		}, want)
		if len(commits[index].Parts) != 2 {
			t.Fatalf("a verified tip carries %d parts, want 2", len(commits[index].Parts))
		}
	}
	if commits[1].Parent != parent.Head {
		t.Fatalf("the child's parent = %q, want %s", commits[1].Parent, parent.Head)
	}

	assertRelicCategory(t, errs[2], core.CategoryCorruptData, overwritten.Operation.RelicID, relicTestID(1))
	assertRelicCategory(t, errs[5], core.CategoryCorruptData, orphanRoot.Operation.RelicID, orphanRoot.Head)
	for _, index := range []int{2, 5} {
		if commits[index].ObjectID != "" {
			t.Fatalf("a tip that failed its check was returned all the same: %+v", commits[index])
		}
	}
	// A newer writer's tip cannot be checked, and is returned as far as it
	// could be read so that a caller can tell it from a commit this build
	// wrote on top of one.
	assertRelicCategory(t, errs[3], core.CategoryNewerWriter, newerRoot.Operation.RelicID)
	if commits[3].ObjectID != newer || !commits[3].needsNewerReader() || commits[3].Parts != nil {
		t.Fatalf("the newer writer's tip = %+v, want commit %s unverified and without parts", commits[3], newer)
	}
	assertRelicCategory(t, errs[4], core.CategoryNewerWriter, belowRoot.Operation.RelicID)
	if commits[4].ObjectID != "" {
		t.Fatalf("a commit on top of a newer writer's was returned all the same: %+v", commits[4])
	}

	// The tips' documents, their parents' documents, then every parts tree,
	// every subtree and every blob size, each in one process.
	if len(ran) != 5 || countCommand(ran, "cat-file", "--batch") != 4 || countCommand(ran, "cat-file", "--batch-check") != 1 {
		t.Fatalf("verifyRelicTips() ran %q, want four cat-file --batch and one cat-file --batch-check", ran)
	}
	// A tip and its parent mostly hold the same subtrees. Each is asked for
	// once.
	seen := map[string]struct{}{}
	for _, request := range asked {
		if _, repeated := seen[request]; repeated {
			t.Fatalf("verifyRelicTips() asked for %s twice: %q", request, asked)
		}
		seen[request] = struct{}{}
	}
	for _, partID := range []string{relicTestID(1), relicTestID(2)} {
		subtree := gitOutput(t, repo, "rev-parse", child.Head+":parts/"+partID)
		if subtree != gitOutput(t, repo, "rev-parse", parent.Head+":parts/"+partID) {
			t.Fatalf("the child and its parent hold different subtrees for part %s", partID)
		}
		if _, found := seen[subtree]; !found {
			t.Fatalf("verifyRelicTips() never asked for subtree %s of part %s: %q", subtree, partID, asked)
		}
	}
	// No body and no fields were opened: the check reads trees and sizes.
	for _, request := range asked {
		if strings.HasSuffix(request, "/"+relicBodyPath) || strings.HasSuffix(request, "/"+relicFieldsPath) {
			t.Fatalf("verifyRelicTips() opened %s", request)
		}
	}
	for _, snapshot := range []core.RelicSnapshot{alone, child} {
		for _, partID := range []string{relicTestID(1), relicTestID(2)} {
			for _, blob := range []string{relicFieldsPath, relicBodyPath} {
				output, err := repo.Git(ctx, nil, "rev-parse", "--verify", "--quiet", snapshot.Head+":parts/"+partID+"/"+blob)
				if err != nil {
					continue
				}
				if _, opened := seen[strings.TrimSpace(string(output))]; opened {
					t.Fatalf("verifyRelicTips() opened the %s of part %s", blob, partID)
				}
			}
		}
	}

	// What it accepts and refuses is what a single-relic read does.
	for index, relicHead := range heads {
		pointRelicRef(t, repo, relicHead.RelicID, relicHead.ObjectID)
		_, err := repo.GetRelic(ctx, config, relicHead.RelicID)
		switch {
		case index == 3:
			if err != nil {
				t.Fatalf("GetRelic() of the newer writer's tip error = %v, want it served unverified", err)
			}
		case (err == nil) != (errs[index] == nil) || core.CategoryOf(err) != core.CategoryOf(errs[index]):
			t.Fatalf("GetRelic() of head %d error = %v, but verifyRelicTips() said %v", index, err, errs[index])
		}
	}
}
