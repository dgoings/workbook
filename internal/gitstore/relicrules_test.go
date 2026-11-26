package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

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
	tip, _, b := relicRuleHistory(t, repo, config)
	stored := forgedPartsOf(t, repo, tip.Head)
	stranger := relicTestID(9)
	retitle := relicChildPack(tip, relicTitleSet(700010, "Another title"))

	// A subtree beside the two the relic lists makes the parts tree one entry
	// larger than the state says it is. It is refused by that size, without
	// being read, so the refusal gives the two sizes and cannot name the
	// stranger.
	listedBytes := relicObjectSize(t, repo, tip.Head+":parts")
	err := checkRelicRule(t, repo, config, tip, 2, relicRuleCase{
		pack:      retitle,
		honest:    stored,
		violating: withForgedPart(stored, stranger, forgedPart{fields: forgedFields(t)}),
		fragments: []string{fmt.Sprintf("is %d bytes", listedBytes/2*3), fmt.Sprintf("is %d bytes", listedBytes)},
	})
	assertGetRelicRefused(t, err)
	if strings.Contains(err.Error(), stranger) {
		t.Fatalf("the refusal names %s, which only reading the oversize tree could have found: %v", stranger, err)
	}

	// A stranger in the place of a listed part leaves the tree the size the
	// state says, so it is read, and the stranger is named.
	swapped := withForgedPart(stored, stranger, forgedPart{fields: forgedFields(t)})
	delete(swapped, b.id)
	err = checkRelicRule(t, repo, config, tip, 2, relicRuleCase{
		pack:      retitle,
		honest:    stored,
		violating: swapped,
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
// the state says. For fields.json the limit is the ceiling for one stored
// object: how large one part's fields are together is a total, which no
// reader checks.
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
		"fields.json one byte over the ceiling for one object": {
			pack:      relicChildPack(tip, relicPartAdd(added, "3/1", nil)),
			honest:    withForgedPart(stored, added, forgedPart{fields: fieldsOfSize(MaxObjectBytes)}),
			violating: withForgedPart(stored, added, forgedPart{fields: fieldsOfSize(MaxObjectBytes + 1)}),
			fragments: []string{added, "4194305 bytes", "4194304 bytes"},
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
// already has would take the ID of that body's version, and the text beside it
// would arrive under a version that already names other text. The fold refuses
// it, so a commit holding one is refused whatever state it claims.
func TestARepeatedPartAddIsRefusedOnReadAndNamedByValidate(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	tip, a, _ := relicRuleHistory(t, repo, config)
	relicID := tip.Operation.RelicID
	stored := forgedPartsOf(t, repo, tip.Head)
	// The same size as the body it replaces, so no size gives it away.
	replaced := stored[a.id]
	replaced.body = bytes.ToUpper(replaced.body)
	if bytes.Equal(replaced.body, stored[a.id].body) || len(replaced.body) != len(stored[a.id].body) {
		t.Fatal("the replacement body must differ from the stored one and be the same size")
	}

	pack := relicChildPack(tip, relicPartAdd(a.id, "1/1", replaced.body))
	if _, err := core.ApplyRelic(&tip.Record, pack); core.CategoryOf(err) != core.CategoryCorruptData {
		t.Fatalf("ApplyRelic(a repeated part.add) error = %v, want corrupt data", err)
	}
	// The state a build that ignored the operation would have computed: the
	// parent's, one clock on.
	claimed := tip.Record.State
	claimed.LogicalClock = pack.LogicalClock
	claimed.Relic.UpdatedAt = pack.WallTime
	for name, parts := range map[string]map[string]forgedPart{
		"with the tree left as it was": stored,
		"with the body replaced":       withForgedPart(stored, a.id, replaced),
	} {
		forged := forgeRelicCommit(t, repo, tip.Head,
			encodedRelicDocument(t, pack), encodedRelicDocument(t, claimed), parts)
		pointRelicRef(t, repo, relicID, forged)
		_, err := repo.GetRelic(ctx, config, relicID)
		if err == nil {
			t.Fatalf("%s: GetRelic() accepted a repeated part.add", name)
		}
		assertGetRelicRefused(t, err)
		assertRelicCategory(t, err, core.CategoryCorruptData, relicID, a.id)
		assertRelicHistoryStopsAt(t, repo, config, relicID, forged, forged, 2,
			core.CategoryCorruptData, relicID, a.id)
	}
}

// A part.body.set that carries the ID of the operation that saved the current
// body would save the version the body already has. Delivered again from the
// base it first had it is stale, so the fold ignores it and a single-relic
// read serves the commit while the tree keeps the body. It cannot replace the
// body, and the full walk refuses the commit either way: its operation ID is
// already in the history.
func TestRuleR6ARepeatedBodyEditCannotReplaceABody(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	tip, a, _ := relicRuleHistory(t, repo, config)
	relicID := tip.Operation.RelicID
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

	// With the tree left alone the commit breaks no rule a single-relic read
	// checks, and the walk names the ID it repeats.
	kept := forgeRelicChild(t, repo, edited, pack, current)
	pointRelicRef(t, repo, relicID, kept)
	if got, err := repo.GetRelic(ctx, config, relicID); err != nil || got.Head != kept {
		t.Fatalf("GetRelic() of the repeated edit that keeps the body = %s, %v; want %s", got.Head, err, kept)
	}
	assertRelicHistoryStopsAt(t, repo, config, relicID, kept, kept, 3,
		core.CategoryCorruptData, relicID, saved, edited.Head)

	// With the body replaced, both refuse it.
	violating := forgeRelicChild(t, repo, edited, pack, withForgedPart(current, a.id, replaced))
	pointRelicRef(t, repo, relicID, violating)
	assertRelicHistoryStopsAt(t, repo, config, relicID, violating, violating, 3,
		core.CategoryCorruptData, a.id, "body")
	_, err := repo.GetRelic(ctx, config, relicID)
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
