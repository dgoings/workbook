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

// relicRuleHistory writes the valid two-commit history the rule tests forge on
// top of: part A with two fields and a body and part B with one field and no
// body, then a title change.
func relicRuleHistory(t *testing.T, repo *Repository, config core.ProjectConfig) (tip core.RelicSnapshot, a, b relicTestPart) {
	t.Helper()
	a = relicTestPartNumbered(1).withField("a", "old").withField("b", "kept").withBody("md", []byte("the goals\n"))
	b = relicTestPartNumbered(2).withField("heading", "Decision")
	root := writeRelicRoot(t, repo, config, a, b)
	return writeRelicChild(t, repo, config, root, nil, relicTitleSet(700001, "A new title")), a, b
}

// relicRuleCase is one pack forged twice on top of a valid history: once with
// the parts the rules require, and once with a single violation.
type relicRuleCase struct {
	pack      core.RelicOperationPack
	honest    map[string]forgedPart
	violating map[string]forgedPart
	// fragments are words the refusal must carry.
	fragments []string
}

// checkRelicRule forges both commits of a case on top of parent, the tip of a
// valid history of validBefore commits. The honest one must pass validation
// and a single-relic read, so that the violating one can only be refused for
// its one difference. Validation must name the violating one. What GetRelic
// says about the violating commit as the relic's tip is returned.
func checkRelicRule(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	parent core.RelicSnapshot,
	validBefore int,
	test relicRuleCase,
) error {
	t.Helper()
	ctx := context.Background()
	relicID := parent.Operation.RelicID

	honest := forgeRelicChild(t, repo, parent, test.pack, test.honest)
	pointRelicRef(t, repo, relicID, honest)
	assertRelicHistoryValid(t, repo, config, relicID, honest, validBefore+1)
	if got, err := repo.GetRelic(ctx, config, relicID); err != nil || got.Head != honest {
		t.Fatalf("GetRelic() of the commit that breaks no rule = %s, %v; want %s", got.Head, err, honest)
	}

	violating := forgeRelicChild(t, repo, parent, test.pack, test.violating)
	if violating == honest {
		t.Fatal("the violating commit is the honest one; the case forges no violation")
	}
	pointRelicRef(t, repo, relicID, violating)
	assertRelicHistoryStopsAt(t, repo, config, relicID, violating, violating, validBefore,
		core.CategoryCorruptData, test.fragments...)
	_, err := repo.GetRelic(ctx, config, relicID)
	return err
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

func TestRuleR1RootStateDoesNotFollowFromItsPack(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withField("heading", "Goals")
	pack, _ := relicRootPack(config, 1, part)
	parts := map[string]forgedPart{part.id: {fields: forgedFields(t, "heading", "Goals")}}
	state := foldRelic(t, nil, pack).State

	honest := forgeRelicCommit(t, repo, "", encodedRelicDocument(t, pack), encodedRelicDocument(t, state), parts)
	pointRelicRef(t, repo, pack.RelicID, honest)
	assertRelicHistoryValid(t, repo, config, pack.RelicID, honest, 1)
	if got, err := repo.GetRelic(ctx, config, pack.RelicID); err != nil || got.Head != honest {
		t.Fatalf("GetRelic() of a root commit = %s, %v; want %s", got.Head, err, honest)
	}

	state.Relic.Title = "A title no operation set"
	forged := forgeRelicCommit(t, repo, "", encodedRelicDocument(t, pack), encodedRelicDocument(t, state), parts)
	pointRelicRef(t, repo, pack.RelicID, forged)
	assertRelicHistoryStopsAt(t, repo, config, pack.RelicID, forged, forged, 0, core.CategoryCorruptData)
	_, err := repo.GetRelic(ctx, config, pack.RelicID)
	assertGetRelicRefused(t, err)
}

func TestRuleR3ExtraPartSubtree(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, _, _ := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	stranger := relicTestID(9)

	err := checkRelicRule(t, repo, config, tip, 2, relicRuleCase{
		pack:      relicChildPack(tip, relicTitleSet(700010, "Another title")),
		honest:    stored,
		violating: withForgedPart(stored, stranger, forgedPart{fields: forgedFields(t)}),
		fragments: []string{stranger},
	})
	assertGetRelicRefused(t, err)
}

func TestRuleR3MissingFields(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, _, _ := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	added := relicTestID(3)
	body := []byte("a new part\n")

	err := checkRelicRule(t, repo, config, tip, 2, relicRuleCase{
		pack:      relicChildPack(tip, relicPartAdd(added, "3/1", body)),
		honest:    withForgedPart(stored, added, forgedPart{fields: forgedFields(t), body: body, hasBody: true}),
		violating: withForgedPart(stored, added, forgedPart{body: body, hasBody: true}),
		fragments: []string{added, "fields.json"},
	})
	assertGetRelicRefused(t, err)
}

func TestRuleR3BodyWithoutStateEntry(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, _, _ := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	added := relicTestID(3)

	err := checkRelicRule(t, repo, config, tip, 2, relicRuleCase{
		pack:   relicChildPack(tip, relicPartAdd(added, "3/1", nil)),
		honest: withForgedPart(stored, added, forgedPart{fields: forgedFields(t)}),
		violating: withForgedPart(stored, added, forgedPart{
			fields: forgedFields(t), body: []byte("a body the relic does not record\n"), hasBody: true,
		}),
		fragments: []string{added, "body"},
	})
	assertGetRelicRefused(t, err)
}

// Rule R3 allows a part's subtree fields.json, body when the relic records
// one, and nothing else.
func TestRuleR3RefusesEveryOtherShapeOfAPartSubtree(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, a, b := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	added := relicTestID(3)
	body := []byte("a new part\n")
	bodyBlob := forgeRelicBlob(t, repo, body)
	fieldsBlob := forgeRelicBlob(t, repo, forgedFields(t))
	someTree := gitOutput(t, repo, "rev-parse", tip.Head+":parts")
	withBody := forgedPart{fields: forgedFields(t), body: body, hasBody: true}
	addWithBody := relicChildPack(tip, relicPartAdd(added, "3/1", body))
	retitle := relicChildPack(tip, relicTitleSet(700010, "Another title"))

	withoutPart := func(partID string) map[string]forgedPart {
		remaining := maps.Clone(stored)
		delete(remaining, partID)
		return remaining
	}
	withoutBody := stored[a.id]
	withoutBody.body, withoutBody.hasBody = nil, false

	for name, test := range map[string]relicRuleCase{
		"a file beside fields.json and body": {
			pack:   addWithBody,
			honest: withForgedPart(stored, added, withBody),
			violating: withForgedPart(stored, added, forgedPart{
				fields: forgedFields(t), body: body, hasBody: true,
				extra: []string{"100644 blob " + bodyBlob + "\tnotes.md"},
			}),
			fragments: []string{added},
		},
		"a directory inside a part": {
			pack:   addWithBody,
			honest: withForgedPart(stored, added, withBody),
			violating: withForgedPart(stored, added, forgedPart{
				fields: forgedFields(t), body: body, hasBody: true,
				extra: []string{"040000 tree " + someTree + "\tattachments"},
			}),
			fragments: []string{added},
		},
		"an executable fields.json": {
			pack:   addWithBody,
			honest: withForgedPart(stored, added, withBody),
			violating: withForgedPart(stored, added, forgedPart{
				body: body, hasBody: true,
				extra: []string{"100755 blob " + fieldsBlob + "\tfields.json"},
			}),
			fragments: []string{added, "fields.json"},
		},
		"a body that is a symbolic link": {
			pack:   addWithBody,
			honest: withForgedPart(stored, added, withBody),
			violating: withForgedPart(stored, added, forgedPart{
				fields: forgedFields(t),
				extra:  []string{"120000 blob " + bodyBlob + "\tbody"},
			}),
			fragments: []string{added, "body"},
		},
		"no subtree for a part the relic lists": {
			pack:      retitle,
			honest:    stored,
			violating: withoutPart(b.id),
			fragments: []string{b.id},
		},
		"no body for a part the relic records one for": {
			pack:      retitle,
			honest:    stored,
			violating: withForgedPart(stored, a.id, withoutBody),
			fragments: []string{a.id, "body"},
		},
	} {
		err := checkRelicRule(t, repo, config, tip, 2, test)
		if core.CategoryOf(err) != core.CategoryCorruptData || !strings.HasSuffix(err.Error(), relicValidateHint) {
			t.Fatalf("%s: GetRelic() error = %v, want corrupt data naming workbook validate", name, err)
		}
	}
}

// A tree can name a blob Git does not have. Git lists it without a size, and
// that is a part without its content, not a failure to run Git.
func TestRuleR3RefusesABlobGitDoesNotHave(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	tip, a, _ := relicRuleHistory(t, repo, config)
	relicID := tip.Operation.RelicID
	absent := strings.Repeat("0", len(tip.Head)-1) + "1"
	fields := gitOutput(t, repo, "rev-parse", tip.Head+":parts/"+a.id+"/fields.json")

	// The tip's own commit again, with part A's body pointing at nothing.
	subtree := gitOutputWithInput(t, repo,
		[]byte("100644 blob "+fields+"\tfields.json\n100644 blob "+absent+"\tbody\n"), "mktree", "--missing")
	parts := replaceRelicTreeEntry(t,
		relicLines(gitOutput(t, repo, "ls-tree", tip.Head+":parts")), a.id, "040000 tree "+subtree+"\t"+a.id)
	top := replaceRelicTreeEntry(t,
		relicLines(gitOutput(t, repo, "ls-tree", tip.Head)), "parts", "040000 tree "+forgeRelicTree(t, repo, parts)+"\tparts")
	forged := forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, top), relicParents(t, repo, tip.Head)...)
	pointRelicRef(t, repo, relicID, forged)

	assertRelicHistoryStopsAt(t, repo, config, relicID, forged, forged, 1, core.CategoryCorruptData, a.id, "body", "missing")
	_, err := repo.GetRelic(ctx, config, relicID)
	assertGetRelicRefused(t, err)
}

func TestRuleR4BodySizeDiffersFromState(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, a, _ := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	recorded := []byte("ten bytes\n")
	longer := []byte("twelve bytes")
	if len(recorded) != 10 || len(longer) != 12 {
		t.Fatalf("the test's bodies are %d and %d bytes", len(recorded), len(longer))
	}
	edit := stored[a.id]

	edit.body = recorded
	honest := withForgedPart(stored, a.id, edit)
	edit.body = longer
	err := checkRelicRule(t, repo, config, tip, 2, relicRuleCase{
		pack:      relicChildPack(tip, relicBodySet(700010, a.id, a.id, recorded)),
		honest:    honest,
		violating: withForgedPart(stored, a.id, edit),
		fragments: []string{a.id, "12 bytes", "10 bytes"},
	})
	assertGetRelicRefused(t, err)
}

// Rule R4's two size limits are judged from the sizes Git reports, whatever
// the state says.
func TestRuleR4FieldsAndBodyLimits(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, a, _ := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	added := relicTestID(3)
	fieldsOfSize := func(size int) []byte {
		encoded, err := json.Marshal(core.NewRelicFields(relicFieldsOfSize(t, size)))
		if err != nil {
			t.Fatal(err)
		}
		return append(encoded, '\n')
	}
	atLimit := bytes.Repeat([]byte("b"), core.MaxRelicBodyBytes)
	edit := func(body []byte) map[string]forgedPart {
		part := stored[a.id]
		part.body = body
		return withForgedPart(stored, a.id, part)
	}

	for name, test := range map[string]relicRuleCase{
		"fields.json one byte over its limit": {
			pack:      relicChildPack(tip, relicPartAdd(added, "3/1", nil)),
			honest:    withForgedPart(stored, added, forgedPart{fields: fieldsOfSize(core.MaxRelicPartFieldsBytes)}),
			violating: withForgedPart(stored, added, forgedPart{fields: fieldsOfSize(core.MaxRelicPartFieldsBytes + 1)}),
			fragments: []string{added, "16385 bytes", "16384 bytes"},
		},
		"a body one byte over its limit": {
			pack:      relicChildPack(tip, relicBodySet(700010, a.id, a.id, atLimit)),
			honest:    edit(atLimit),
			violating: edit(append(bytes.Clone(atLimit), 'b')),
			fragments: []string{a.id, "1048577 bytes", "1048576 bytes"},
		},
	} {
		err := checkRelicRule(t, repo, config, tip, 2, test)
		if core.CategoryOf(err) != core.CategoryCorruptData || !strings.HasSuffix(err.Error(), relicValidateHint) {
			t.Fatalf("%s: GetRelic() error = %v, want corrupt data naming workbook validate", name, err)
		}
		if len(err.Error()) > 500 {
			t.Fatalf("%s: the refusal is %d bytes long; it must not carry content", name, len(err.Error()))
		}
	}
}

func TestRuleR5UntouchedPartChanged(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, a, b := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)

	// The pack names part A, so part B is the one it must leave alone.
	err := checkRelicRule(t, repo, config, tip, 2, relicRuleCase{
		pack:      relicChildPack(tip, relicPartMove(700010, a.id, "5/2")),
		honest:    stored,
		violating: withForgedPart(stored, b.id, forgedPart{fields: forgedFields(t, "heading", "Changed")}),
		fragments: []string{b.id},
	})
	assertGetRelicRefused(t, err)
}

func TestRuleR6BodyReplacedWithoutABodyEdit(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, a, _ := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	// The same size as the body it replaces, so no size gives it away.
	replaced := stored[a.id]
	replaced.body = bytes.ToUpper(replaced.body)
	if bytes.Equal(replaced.body, stored[a.id].body) || len(replaced.body) != len(stored[a.id].body) {
		t.Fatal("the replacement body must differ from the stored one and be the same size")
	}

	// An operation that names the part, and so may change its subtree, but
	// saves no body.
	err := checkRelicRule(t, repo, config, tip, 2, relicRuleCase{
		pack:      relicChildPack(tip, relicPartMove(700010, a.id, "5/2")),
		honest:    stored,
		violating: withForgedPart(stored, a.id, replaced),
		fragments: []string{a.id, "body"},
	})
	assertGetRelicRefused(t, err)

	// A body edit the fold ignored, whose state is honest about that and whose
	// tree stores the text anyway.
	pointRelicRef(t, repo, tip.Operation.RelicID, tip.Head)
	alice := []byte("Alice's text, saved from the first version\n")
	edited := writeRelicChild(t, repo, config, tip,
		map[string]core.RelicPartContent{a.id: {Body: alice, HasBody: true}},
		relicBodySet(700011, a.id, a.id, alice))
	current := forgedPartsOf(t, repo, edited.Head)
	overwritten := current[a.id]
	overwritten.body = bytes.ToUpper(alice)
	err = checkRelicRule(t, repo, config, edited, 3, relicRuleCase{
		pack:      relicChildPack(edited, relicBodySet(700012, a.id, a.id, overwritten.body)),
		honest:    current,
		violating: withForgedPart(current, a.id, overwritten),
		fragments: []string{a.id, "body"},
	})
	assertGetRelicRefused(t, err)
}

// A part's first body has the part's own ID as its version, and a part.add
// operation's ID is the part's ID. So a second part.add for a part the relic
// already has carries the ID of that body's version, and the fold ignores it.
// It saved nothing, and the body must stay the one the parent stores.
func TestRuleR6ARepeatedPartAddCannotReplaceABody(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, a, _ := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	// The same size as the body it replaces, so no size gives it away.
	replaced := stored[a.id]
	replaced.body = bytes.ToUpper(replaced.body)
	if bytes.Equal(replaced.body, stored[a.id].body) || len(replaced.body) != len(stored[a.id].body) {
		t.Fatal("the replacement body must differ from the stored one and be the same size")
	}

	pack := relicChildPack(tip, relicPartAdd(a.id, "1/1", replaced.body))
	before, _ := tip.Record.State.Relic.Part(a.id)
	after, _ := foldRelic(t, &tip, pack).State.Relic.Part(a.id)
	if before.Body.Version != a.id || *after.Body != *before.Body {
		t.Fatalf("the repeated part.add folded the body from %+v to %+v; the test needs it to change nothing",
			*before.Body, *after.Body)
	}

	// forgeRelicChild stores the state the fold computes, so the forged commit
	// is honest about the version and wrong only in its tree.
	err := checkRelicRule(t, repo, config, tip, 2, relicRuleCase{
		pack:      pack,
		honest:    stored,
		violating: withForgedPart(stored, a.id, replaced),
		fragments: []string{a.id, "body"},
	})
	assertGetRelicRefused(t, err)
}

// The same for a part.body.set that carries the ID of the operation that saved
// the current body: the version it would save is the one the body already has.
func TestRuleR6ARepeatedBodyEditCannotReplaceABody(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, a, _ := relicRuleHistory(t, repo, config)
	alice := []byte("Alice's text, saved from the first version\n")
	saved := relicTestID(700011)
	edited := writeRelicChild(t, repo, config, tip,
		map[string]core.RelicPartContent{a.id: {Body: alice, HasBody: true}},
		relicBodySet(700011, a.id, a.id, alice))
	current := forgedPartsOf(t, repo, edited.Head)
	replaced := current[a.id]
	replaced.body = bytes.ToUpper(alice)
	if bytes.Equal(replaced.body, alice) || len(replaced.body) != len(alice) {
		t.Fatal("the replacement body must differ from the stored one and be the same size")
	}

	// The operation exactly as it was first delivered. Its base is no longer
	// current, so the fold ignores it.
	pack := relicChildPack(edited, relicBodySet(700011, a.id, a.id, replaced.body))
	before, _ := edited.Record.State.Relic.Part(a.id)
	ignored := foldRelic(t, &edited, pack)
	after, _ := ignored.State.Relic.Part(a.id)
	if before.Body.Version != saved || *after.Body != *before.Body {
		t.Fatalf("the repeated body edit folded the body from %+v to %+v; the test needs it to change nothing",
			*before.Body, *after.Body)
	}
	err := checkRelicRule(t, repo, config, edited, 3, relicRuleCase{
		pack:      pack,
		honest:    current,
		violating: withForgedPart(current, a.id, replaced),
		fragments: []string{a.id, "body"},
	})
	assertGetRelicRefused(t, err)

	// The operation naming the version it saved as its base names itself,
	// which no pack may do, so it never reaches the fold. A commit holding it
	// is refused with the body it replaces left as it was.
	selfBased := relicChildPack(edited, relicBodySet(700011, a.id, saved, alice))
	operationJSON, marshalErr := json.Marshal(selfBased)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	forged := forgeRelicCommit(t, repo, edited.Head, append(operationJSON, '\n'),
		encodedRelicDocument(t, ignored.State), current)
	pointRelicRef(t, repo, edited.Operation.RelicID, forged)
	assertRelicHistoryStopsAt(t, repo, config, edited.Operation.RelicID, forged, forged, 3,
		core.CategoryCorruptData, saved, "base")
	_, err = repo.GetRelic(context.Background(), config, edited.Operation.RelicID)
	assertGetRelicRefused(t, err)
}

func TestRuleR7FieldsDoNotFollowFromTheOperations(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, a, _ := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	set := func(pairs ...string) map[string]forgedPart {
		part := stored[a.id]
		part.fields = forgedFields(t, pairs...)
		return withForgedPart(stored, a.id, part)
	}

	// The pack sets field a. The tree applies that, and changes b as well.
	err := checkRelicRule(t, repo, config, tip, 2, relicRuleCase{
		pack:      relicChildPack(tip, relicFieldSet(700010, a.id, "a", "new")),
		honest:    set("a", "new", "b", "kept"),
		violating: set("a", "new", "b", "changed"),
		fragments: []string{a.id, "fields"},
	})
	// A single-relic read does not open fields.json, so it serves the commit.
	if err != nil {
		t.Fatalf("GetRelic() error = %v; only workbook validate reads fields.json", err)
	}
}

func TestRuleR7FieldEditNotApplied(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, a, _ := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	applied := stored[a.id]
	applied.fields = forgedFields(t, "a", "new", "b", "kept")

	err := checkRelicRule(t, repo, config, tip, 2, relicRuleCase{
		pack:      relicChildPack(tip, relicFieldSet(700010, a.id, "a", "new")),
		honest:    withForgedPart(stored, a.id, applied),
		violating: stored,
		fragments: []string{a.id, "fields"},
	})
	if err != nil {
		t.Fatalf("GetRelic() error = %v; only workbook validate reads fields.json", err)
	}
}

// The part of rule R7 that needs no read: a part the pack names without
// setting or clearing a field keeps the fields.json object it had. Object IDs
// settle it, so a single-relic read refuses the commit as validation does.
func TestGetRelicRefusesAChangedFieldsBlobWithNoFieldOperation(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, a, b := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	changed := func(parts map[string]forgedPart) map[string]forgedPart {
		part := parts[a.id]
		part.fields = forgedFields(t, "a", "old", "b", "changed")
		return withForgedPart(parts, a.id, part)
	}
	newBody := []byte("the goals, edited\n")
	edited := stored[a.id]
	edited.body = newBody
	withNewBody := withForgedPart(stored, a.id, edited)

	for name, test := range map[string]relicRuleCase{
		"a move": {
			pack:      relicChildPack(tip, relicPartMove(700010, a.id, "5/2")),
			honest:    stored,
			violating: changed(stored),
		},
		"a removal": {
			pack:      relicChildPack(tip, relicPartChange(700010, core.RelicOperationPartRemove, a.id)),
			honest:    stored,
			violating: changed(stored),
		},
		// The body may change here, and the fields still may not.
		"a body edit": {
			pack:      relicChildPack(tip, relicBodySet(700010, a.id, a.id, newBody)),
			honest:    withNewBody,
			violating: changed(withNewBody),
		},
		"a repeated part.add": {
			pack:      relicChildPack(tip, relicPartAdd(a.id, "1/1", stored[a.id].body)),
			honest:    stored,
			violating: changed(stored),
		},
		// A field operation for another part does not cover this one.
		"a move beside a field edit of another part": {
			pack: relicChildPack(tip,
				relicPartMove(700010, a.id, "5/2"), relicFieldSet(700011, b.id, "heading", "Decision")),
			honest:    stored,
			violating: changed(stored),
		},
	} {
		test.fragments = []string{a.id, "fields"}
		err := checkRelicRule(t, repo, config, tip, 2, test)
		if core.CategoryOf(err) != core.CategoryCorruptData || !strings.HasSuffix(err.Error(), relicValidateHint) {
			t.Fatalf("%s: GetRelic() error = %v, want corrupt data naming workbook validate", name, err)
		}
	}
}

// Rule R7 also covers a part the pack names without a field operation, whose
// fields must stay as they were, and a part the pack adds, whose fields.json
// has no earlier version to follow from. The first is settled by object IDs,
// so a single-relic read refuses it too. The others take a read of
// fields.json, which only validation pays for.
func TestRuleR7CoversPartsWithoutFieldOperationsAndNewParts(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip, a, _ := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	added := relicTestID(3)
	fieldsOfA := func(pairs ...string) map[string]forgedPart {
		part := stored[a.id]
		part.fields = forgedFields(t, pairs...)
		return withForgedPart(stored, a.id, part)
	}
	loose := bytes.Replace(forgedFields(t, "heading", "Why"), []byte(`"fields":{`), []byte(`"fields": {`), 1)
	const noFieldOperation = "a named part with no field operation"

	for name, test := range map[string]relicRuleCase{
		noFieldOperation: {
			pack:      relicChildPack(tip, relicPartMove(700010, a.id, "5/2")),
			honest:    stored,
			violating: fieldsOfA("a", "old", "b", "changed"),
			fragments: []string{a.id, "fields"},
		},
		"a field clear that was not applied": {
			pack:      relicChildPack(tip, relicFieldClear(700010, a.id, "b")),
			honest:    fieldsOfA("a", "old"),
			violating: stored,
			fragments: []string{a.id, "fields"},
		},
		"a new part whose fields disagree with its own field operation": {
			pack: relicChildPack(tip,
				relicPartAdd(added, "3/1", nil), relicFieldSet(700010, added, "status", "open")),
			honest:    withForgedPart(stored, added, forgedPart{fields: forgedFields(t, "heading", "Why", "status", "open")}),
			violating: withForgedPart(stored, added, forgedPart{fields: forgedFields(t, "heading", "Why", "status", "closed")}),
			fragments: []string{added, "fields"},
		},
		"a new part whose fields.json is not canonical": {
			pack:      relicChildPack(tip, relicPartAdd(added, "3/1", nil)),
			honest:    withForgedPart(stored, added, forgedPart{fields: forgedFields(t, "heading", "Why")}),
			violating: withForgedPart(stored, added, forgedPart{fields: loose}),
			fragments: []string{added, "fields"},
		},
		"a new part whose fields.json is not the fields document": {
			pack:      relicChildPack(tip, relicPartAdd(added, "3/1", nil)),
			honest:    withForgedPart(stored, added, forgedPart{fields: forgedFields(t)}),
			violating: withForgedPart(stored, added, forgedPart{fields: []byte("{}\n")}),
			fragments: []string{added, "fields"},
		},
	} {
		err := checkRelicRule(t, repo, config, tip, 2, test)
		if name == noFieldOperation {
			if core.CategoryOf(err) != core.CategoryCorruptData || !strings.HasSuffix(err.Error(), relicValidateHint) {
				t.Fatalf("%s: GetRelic() error = %v, want corrupt data naming workbook validate", name, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: GetRelic() error = %v; only workbook validate reads fields.json", name, err)
		}
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

	// The ref, then the documents and the tree listing of the tip and of its
	// parent.
	getSmall, getLarge := get(small), get(large)
	getCommands := commands
	if getLarge != getSmall || getLarge > 5 {
		t.Fatalf("GetRelic ran %d Git commands for 3 parts and %d for 300, want the same and at most 5: %q",
			getSmall, getLarge, getCommands)
	}
	// One listing of the history, then at most three per commit: its
	// documents, its tree listing, and the fields rule R7 has to read.
	validateSmall, validateLarge := validate(small), validate(large)
	if validateLarge != validateSmall || validateLarge > 1+3*3 {
		t.Fatalf("ValidateRelicHistory ran %d Git commands for 3 parts and %d for 300, want the same and at most 10: %q",
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
