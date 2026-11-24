package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// publishRelicRefs writes every relic ref of repo to origin as it stands,
// the way a teammate's push would, without asking Workbook.
func publishRelicRefs(t *testing.T, repo *Repository) {
	t.Helper()
	syncGit(t, repo.Root, "push", "--quiet", "origin", relicRefPrefix+"*:"+relicRefPrefix+"*")
}

// assertRelicSyncOutcome requires the result to list relicID with status
// want, and returns the entry.
func assertRelicSyncOutcome(t *testing.T, result SyncResult, relicID string, want SyncStatus) SyncRelicResult {
	t.Helper()
	for _, item := range result.Relics {
		if item.RelicID == relicID {
			if item.Status != want {
				t.Fatalf("relic %s status = %q, want %q; relics = %+v", relicID, item.Status, want, result.Relics)
			}
			return item
		}
	}
	t.Fatalf("result has no relic %s: %+v", relicID, result.Relics)
	return SyncRelicResult{}
}

// forBothSyncFormats runs one test body against a sync fixture in each
// object format.
func forBothSyncFormats(t *testing.T, run func(t *testing.T, first, second *Repository, config core.ProjectConfig)) {
	t.Helper()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			first, second, config := syncRepositoriesWithObjectFormat(t, objectFormat)
			run(t, first, second, config)
		})
	}
}

// sharedRelic writes relic number n in first, publishes it and fetches it
// into second, so both clones hold the same root.
func sharedRelic(
	t *testing.T,
	first, second *Repository,
	config core.ProjectConfig,
	n int,
	parts ...relicTestPart,
) core.RelicSnapshot {
	t.Helper()
	root := writeRelicRootNumbered(t, first, config, n, parts...)
	publishRelicRefs(t, first)
	fetched, err := second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(relic %s) error = %v; result = %+v", root.Operation.RelicID, err, fetched)
	}
	assertRelicSyncOutcome(t, fetched, root.Operation.RelicID, SyncCreated)
	return root
}

func relicTrackingRef(relicID string) string {
	return relicTrackingRefPrefixFor("origin") + relicID
}

func fetchRelics(t *testing.T, repo *Repository, config core.ProjectConfig) SyncResult {
	t.Helper()
	fetched, err := repo.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch() error = %v; result = %+v", err, fetched)
	}
	return fetched
}

func TestFetchCreatesAndFastForwardsRelics(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		root := writeRelicRoot(t, first, config, relicTestPartNumbered(1).withBody("md", []byte("notes\n")))
		relicID := root.Operation.RelicID
		publishRelicRefs(t, first)
		fetched := fetchRelics(t, second, config)
		assertRelicSyncOutcome(t, fetched, relicID, SyncCreated)
		if got := relicRefHead(t, second, relicID); got != root.Head {
			t.Fatalf("second's relic ref = %s, want first's %s", got, root.Head)
		}
		if got := refValue(t, second, relicTrackingRef(relicID)); got != root.Head {
			t.Fatalf("second's tracking ref = %s, want %s", got, root.Head)
		}

		titled := writeRelicChild(t, first, config, root, nil, relicTitleSet(700001, "A new title"))
		publishRelicRefs(t, first)
		fetched = fetchRelics(t, second, config)
		assertRelicSyncOutcome(t, fetched, relicID, SyncFastForwarded)
		if got := relicRefHead(t, second, relicID); got != titled.Head {
			t.Fatalf("second's relic ref = %s, want first's %s", got, titled.Head)
		}
	})
}

func TestFetchListsOnlyRelicsThatChanged(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	var roots []core.RelicSnapshot
	for n := 1; n <= 3; n++ {
		roots = append(roots, writeRelicRootNumbered(t, first, config, n))
	}
	publishRelicRefs(t, first)
	if fetched := fetchRelics(t, second, config); len(fetched.Relics) != 3 {
		t.Fatalf("first fetch relics = %+v, want three created", fetched.Relics)
	}

	writeRelicChild(t, first, config, roots[1], nil, relicTitleSet(700001, "Changed"))
	publishRelicRefs(t, first)
	fetched := fetchRelics(t, second, config)
	if len(fetched.Relics) != 1 {
		t.Fatalf("relics = %+v, want only the changed one", fetched.Relics)
	}
	assertRelicSyncOutcome(t, fetched, roots[1].Operation.RelicID, SyncFastForwarded)

	fetched = fetchRelics(t, second, config)
	encoded, err := json.Marshal(fetched)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"relics"`)) {
		t.Fatalf("a fetch with no relic change marshals a relics member: %s", encoded)
	}
}

// forgeBrokenRelicChild forges a child of parent whose pack sets the title
// and whose state claims nothing changed, which rule R1 refuses.
func forgeBrokenRelicChild(t *testing.T, repo *Repository, parent core.RelicSnapshot, operationNumber int) string {
	t.Helper()
	pack := relicChildPack(parent, relicTitleSet(operationNumber, "A forged title"))
	claimed := parent.Record.State
	claimed.LogicalClock = pack.LogicalClock
	claimed.Relic.UpdatedAt = pack.WallTime
	return forgeRelicCommit(t, repo, parent.Head,
		encodedRelicDocument(t, pack), encodedRelicDocument(t, claimed), forgedPartsOf(t, repo, parent.Head))
}

func TestSyncDoesNotReadAnUnchangedRelic(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	root := sharedRelic(t, first, second, config, 1)
	relicID := root.Operation.RelicID
	forged := forgeBrokenRelicChild(t, first, root, 700001)
	pointRelicRef(t, first, relicID, forged)
	publishRelicRefs(t, first)
	// The forged commit reaches the second clone's tracking ref, and the
	// test puts it at its canonical ref too, as if an older build had taken it.
	syncGit(t, second.Root, "fetch", "--quiet", "origin", "+"+relicRef(relicID)+":"+relicTrackingRef(relicID))
	pointRelicRef(t, second, relicID, forged)

	requests := observeRelicObjects(second)
	result, err := second.Sync(context.Background(), config)
	second.relicObjectObserver = nil
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %+v", err, result)
	}
	if len(result.Fetch.Relics) != 0 || len(result.Push.Relics) != 0 {
		t.Fatalf("Sync() reported relics %+v and %+v, want none", result.Fetch.Relics, result.Push.Relics)
	}
	if len(*requests) != 0 {
		t.Fatalf("Sync() read relic objects %q, want none", *requests)
	}
}

func TestSyncCommandsDoNotGrowWithUnchangedRelics(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	ctx := context.Background()
	sync := func() {
		t.Helper()
		if result, err := second.Sync(ctx, config); err != nil {
			t.Fatalf("Sync() error = %v; result = %+v", err, result)
		}
	}
	recorded := func() [][]string {
		t.Helper()
		commands := observeCommands(second)
		sync()
		second.commandObserver = nil
		return *commands
	}
	sync()
	none := recorded()

	writeRelicRootNumbered(t, first, config, 1)
	publishRelicRefs(t, first)
	sync()
	one := recorded()

	for n := 2; n <= 40; n++ {
		writeRelicRootNumbered(t, first, config, n)
	}
	publishRelicRefs(t, first)
	sync()
	if count := len(relicRefNames(t, second, relicRefPrefix)); count != 40 {
		t.Fatalf("second holds %d relics, want 40", count)
	}
	forty := recorded()

	if !reflect.DeepEqual(none, one) || !reflect.DeepEqual(one, forty) {
		t.Fatalf("Sync() commands differ:\nno relic: %q\none relic: %q\nforty relics: %q", none, one, forty)
	}
	listings := 0
	for _, command := range forty {
		if len(command) > 0 && command[0] == "for-each-ref" && strings.Contains(strings.Join(command, " "), relicRefPrefix) {
			listings++
		}
	}
	if listings != 1 {
		t.Fatalf("Sync() listed the relic namespace %d times, want once: %q", listings, forty)
	}
}

func relicRefNames(t *testing.T, repo *Repository, prefix string) []string {
	t.Helper()
	return relicLines(gitOutput(t, repo, "for-each-ref", "--format=%(refname)", prefix))
}

func TestFetchIsolatesAnInvalidRelic(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		a := sharedRelic(t, first, second, config, 1)
		forged := forgeBrokenRelicChild(t, first, a, 700001)
		pointRelicRef(t, first, a.Operation.RelicID, forged)
		b := writeRelicRootNumbered(t, first, config, 2)
		task := createSyncTask(t, first, config, "Task beside a forged relic")
		publishRelicRefs(t, first)
		publishTaskRefs(t, first)

		result, err := second.Fetch(ctx, config)
		if core.CategoryOf(err) != core.CategoryCorruptData {
			t.Fatalf("Fetch() error = %v, want corrupt data", err)
		}
		if result.Status != SyncPhaseCompleted {
			t.Fatalf("Fetch() status = %q, want completed", result.Status)
		}
		invalid := assertRelicSyncOutcome(t, result, a.Operation.RelicID, SyncInvalid)
		if !strings.HasPrefix(invalid.Detail, "relic "+a.Operation.RelicID+":") {
			t.Fatalf("invalid detail = %q, want it to name the relic", invalid.Detail)
		}
		if got := relicRefHead(t, second, a.Operation.RelicID); got != a.Head {
			t.Fatalf("the invalid relic's ref moved to %s, want %s", got, a.Head)
		}
		if got := refValue(t, second, relicTrackingRef(a.Operation.RelicID)); got != forged {
			t.Fatalf("the invalid relic's tracking ref = %s, want the forged tip %s", got, forged)
		}
		assertRelicSyncOutcome(t, result, b.Operation.RelicID, SyncCreated)
		if got := relicRefHead(t, second, b.Operation.RelicID); got != b.Head {
			t.Fatalf("the valid relic's ref = %s, want %s", got, b.Head)
		}
		assertSyncOutcome(t, result, task.ID, SyncCreated)
	})
}

// strictFormCase forges one tip on top of parent that breaks a rule a
// fetched tip is checked by.
type strictFormCase struct {
	name  string
	parts []relicTestPart
	forge func(t *testing.T, repo *Repository, parent core.RelicSnapshot) string
}

// replacedRelicChild forges the commit an honest writer would have made for
// operations on parent, with every replacement applied to both documents.
func replacedRelicChild(
	t *testing.T,
	repo *Repository,
	parent core.RelicSnapshot,
	parts map[string]forgedPart,
	operations []core.RelicOperation,
	replacements ...string,
) string {
	t.Helper()
	pack := relicChildPack(parent, operations...)
	record := foldRelic(t, &parent, pack)
	operationJSON, stateJSON := encodedRelicDocument(t, pack), encodedRelicDocument(t, record.State)
	for index := 0; index < len(replacements); index += 2 {
		replaced := bytes.ReplaceAll(operationJSON, []byte(replacements[index]), []byte(replacements[index+1]))
		if bytes.Equal(replaced, operationJSON) {
			t.Fatalf("the pack holds no %s to replace: %s", replacements[index], operationJSON)
		}
		operationJSON = replaced
		stateJSON = bytes.ReplaceAll(stateJSON, []byte(replacements[index]), []byte(replacements[index+1]))
	}
	if parts == nil {
		parts = forgedPartsOf(t, repo, parent.Head)
	}
	return forgeRelicCommit(t, repo, parent.Head, operationJSON, stateJSON, parts)
}

func nestedRelicValue(depth int) string {
	return strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
}

func TestFetchRefusesARelicThatBreaksAStrictFormOrReusesAnID(t *testing.T) {
	t.Parallel()
	withBody := func(n int, text string) relicTestPart {
		return relicTestPartNumbered(n).withField("heading", "Goals").withBody("md", []byte(text))
	}
	cases := []strictFormCase{
		{
			name: "a property value nested 33 levels",
			forge: func(t *testing.T, repo *Repository, parent core.RelicSnapshot) string {
				return replacedRelicChild(t, repo, parent, nil,
					[]core.RelicOperation{relicPropertySet(700001, "deep", "short")},
					`"short"`, nestedRelicValue(33))
			},
		},
		{
			name:  "a field name with a trailing space",
			parts: []relicTestPart{withBody(1, "first\n")},
			forge: func(t *testing.T, repo *Repository, parent core.RelicSnapshot) string {
				partID := relicTestID(1)
				operation := relicFieldSet(700001, partID, "heading", "Changed")
				stored := forgedPartsOf(t, repo, parent.Head)
				part := stored[partID]
				// The encoder refuses the name, so the document is spelled out.
				part.fields = bytes.Replace(forgedFields(t, "heading", "Changed"),
					[]byte(`"heading"`), []byte(`"heading "`), 1)
				return replacedRelicChild(t, repo, parent, withForgedPart(stored, partID, part),
					[]core.RelicOperation{operation}, `"name":"heading"`, `"name":"heading "`)
			},
		},
		{
			name: "a pack time with an offset",
			forge: func(t *testing.T, repo *Repository, parent core.RelicSnapshot) string {
				return replacedRelicChild(t, repo, parent, nil,
					[]core.RelicOperation{relicTitleSet(700001, "A new title")},
					`"2026-11-23T10:01:00Z"`, `"2026-11-23T12:01:00+02:00"`)
			},
		},
		{
			name: "one property value over its own limit",
			forge: func(t *testing.T, repo *Repository, parent core.RelicSnapshot) string {
				return replacedRelicChild(t, repo, parent, nil,
					[]core.RelicOperation{relicPropertySet(700001, "long", "short")},
					`"short"`, `"`+largeRelicString(core.MaxRelicPropertyValueBytes+1)+`"`)
			},
		},
		{
			name:  "a body save whose ID is an existing part's",
			parts: []relicTestPart{withBody(1, "first\n"), withBody(2, "second\n")},
			forge: func(t *testing.T, repo *Repository, parent core.RelicSnapshot) string {
				p, q := relicTestID(1), relicTestID(2)
				text := []byte("a forged text\n")
				stored := forgedPartsOf(t, repo, parent.Head)
				part := stored[p]
				part.body = text
				return replacedRelicChild(t, repo, parent, withForgedPart(stored, p, part),
					[]core.RelicOperation{relicBodySet(700001, p, p, text)}, relicTestID(700001), q)
			},
		},
		{
			name:  "a body save whose ID is the part's current body version",
			parts: []relicTestPart{withBody(1, "first\n")},
			forge: func(t *testing.T, repo *Repository, parent core.RelicSnapshot) string {
				p := relicTestID(1)
				text := []byte("a forged text\n")
				stored := forgedPartsOf(t, repo, parent.Head)
				part := stored[p]
				part.body = text
				return replacedRelicChild(t, repo, parent, withForgedPart(stored, p, part),
					[]core.RelicOperation{relicBodySet(700001, p, p, text)}, relicTestID(700001), p)
			},
		},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			first, second, config := syncRepositories(t)
			root := sharedRelic(t, first, second, config, 10+index, test.parts...)
			relicID := root.Operation.RelicID
			forged := test.forge(t, first, root)
			pointRelicRef(t, first, relicID, forged)
			valid := writeRelicRootNumbered(t, first, config, 50)
			publishRelicRefs(t, first)

			result, err := second.Fetch(context.Background(), config)
			if core.CategoryOf(err) != core.CategoryCorruptData {
				t.Fatalf("Fetch() error = %v, want corrupt data; result = %+v", err, result)
			}
			invalid := assertRelicSyncOutcome(t, result, relicID, SyncInvalid)
			if !strings.HasPrefix(invalid.Detail, "relic "+relicID+":") {
				t.Fatalf("detail = %q, want it to start with the relic", invalid.Detail)
			}
			if got := relicRefHead(t, second, relicID); got != root.Head {
				t.Fatalf("the refused relic's ref moved to %s, want %s", got, root.Head)
			}
			assertRelicSyncOutcome(t, result, valid.Operation.RelicID, SyncCreated)
		})
	}
}

func TestFetchDoesNotSeeAnOperationIDReusedDeepInHistory(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	ctx := context.Background()
	root := sharedRelic(t, first, second, config, 1)
	relicID := root.Operation.RelicID
	titled := writeRelicChild(t, first, config, root, nil, relicTitleSet(700001, "A new title"))
	// The same ID again, which a single-relic read cannot see.
	again := forgeRelicChild(t, first, titled, relicChildPack(titled, relicTitleSet(700001, "Another title")),
		forgedPartsOf(t, first, titled.Head))
	pointRelicRef(t, first, relicID, again)
	repeated := readRelicTip(t, first, config, relicID)
	third := writeRelicChild(t, first, config, repeated, nil, relicTitleSet(700003, "A third title"))
	tip := writeRelicChild(t, first, config, third, nil, relicTitleSet(700004, "A fourth title"))
	publishRelicRefs(t, first)

	fetched := fetchRelics(t, second, config)
	assertRelicSyncOutcome(t, fetched, relicID, SyncFastForwarded)
	if got := relicRefHead(t, second, relicID); got != tip.Head {
		t.Fatalf("relic ref = %s, want %s", got, tip.Head)
	}
	_, failure, err := second.ValidateRelicHistory(ctx, config, core.RelicHead{RelicID: relicID, ObjectID: tip.Head})
	if err != nil {
		t.Fatalf("ValidateRelicHistory() error = %v", err)
	}
	if failure == nil {
		t.Fatal("ValidateRelicHistory() accepted an operation ID recorded twice")
	}
}

func TestSyncLeavesADamagedLocalRelicWhereItIs(t *testing.T) {
	t.Parallel()
	for name, damage := range map[string]func(t *testing.T, repo *Repository, root core.RelicSnapshot) string{
		"a commit that is not a relic's": func(t *testing.T, repo *Repository, root core.RelicSnapshot) string {
			return forgeRelicCommit(t, repo, root.Head, []byte("{}\n"), []byte("{}\n"), nil)
		},
		"an object that is not a commit": func(t *testing.T, repo *Repository, _ core.RelicSnapshot) string {
			return forgeRelicBlob(t, repo, []byte("not a commit\n"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, second, config := syncRepositories(t)
			root := sharedRelic(t, first, second, config, 1)
			relicID := root.Operation.RelicID
			damaged := damage(t, second, root)
			pointRelicRef(t, second, relicID, damaged)
			edited := writeRelicChild(t, first, config, root, nil, relicTitleSet(700001, "A new title"))
			publishRelicRefs(t, first)

			result, err := second.Sync(context.Background(), config)
			if core.CategoryOf(err) != core.CategoryCorruptData {
				t.Fatalf("Sync() error = %v, want corrupt data; result = %+v", err, result)
			}
			invalid := assertRelicSyncOutcome(t, result.Fetch, relicID, SyncInvalid)
			if !strings.HasPrefix(invalid.Detail, "relic "+relicID+":") {
				t.Fatalf("detail = %q, want it to name the relic", invalid.Detail)
			}
			if got := relicRefHead(t, second, relicID); got != damaged {
				t.Fatalf("the damaged relic ref moved to %s, want %s", got, damaged)
			}
			remoteTip := strings.Fields(syncGit(t, second.Root, "ls-remote", "origin", relicRef(relicID)))
			if len(remoteTip) == 0 || remoteTip[0] != edited.Head {
				t.Fatalf("origin's relic ref = %v, want first's %s", remoteTip, edited.Head)
			}
			if _, withheld := relicsWithheldByFetch(t, second, config)[relicID]; !withheld {
				t.Fatalf("the damaged relic is not withheld from publication")
			}
		})
	}
}

// relicsWithheldByFetch runs a fetch and returns the relics it withholds
// from publication.
func relicsWithheldByFetch(t *testing.T, repo *Repository, config core.ProjectConfig) map[string]struct{} {
	t.Helper()
	state, _, _ := repo.fetch(context.Background(), config, false, false)
	return state.Relics.Withheld
}

func TestARelicRefThatMovedDuringTheSyncSaysToRunTheCommandAgain(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	a := sharedRelic(t, first, second, config, 1)
	b := sharedRelic(t, first, second, config, 2)
	editedA := writeRelicChild(t, first, config, a, nil, relicTitleSet(700001, "A changed"))
	editedB := writeRelicChild(t, first, config, b, nil, relicTitleSet(700002, "B changed"))
	task := createSyncTask(t, first, config, "Task beside moving relics")
	publishRelicRefs(t, first)
	publishTaskRefs(t, first)

	moved := false
	second.commandObserver = func(args []string) {
		if moved || len(args) == 0 || args[0] != "update-ref" || !strings.Contains(strings.Join(args, " "), relicFetchReflogMessage) {
			return
		}
		moved = true
		// Somebody else moves relic A just before the transaction runs.
		syncGit(t, second.Root, "update-ref", relicRef(a.Operation.RelicID), editedA.Head)
	}
	result, err := second.Fetch(context.Background(), config)
	second.commandObserver = nil
	if !moved {
		t.Fatal("the relic transaction never ran")
	}
	if core.CategoryOf(err) != core.CategoryStaleWrite {
		t.Fatalf("Fetch() error = %v, want stale write", err)
	}
	message := err.Error()
	if !strings.HasPrefix(message, "relic "+a.Operation.RelicID+":") || !strings.HasSuffix(message, "run the command again") {
		t.Fatalf("Fetch() error = %q, want it to name relic %s and say to run the command again", message, a.Operation.RelicID)
	}
	if got := relicRefHead(t, second, b.Operation.RelicID); got != b.Head {
		t.Fatalf("relic B moved to %s although the transaction failed; want %s (first's tip is %s)", got, b.Head, editedB.Head)
	}
	if result.Status != SyncPhaseCompleted {
		t.Fatalf("Fetch() status = %q, want completed", result.Status)
	}
	assertSyncOutcome(t, result, task.ID, SyncCreated)
	if got := refValue(t, second, taskRefPrefix+task.ID); got != refValue(t, first, taskRefPrefix+task.ID) {
		t.Fatalf("the task ref = %s, want first's", got)
	}
}

func TestARelicProblemNeverChangesTaskResults(t *testing.T) {
	t.Parallel()
	first, _, config := syncRepositories(t)
	createSyncTask(t, first, config, "One task")
	createSyncTask(t, first, config, "Another task")
	publishTaskRefs(t, first)
	bare := syncGit(t, first.Root, "remote", "get-url", "origin")

	fetchTasks := func() []byte {
		t.Helper()
		clone := openSyncClone(t, bare)
		result, _ := clone.Fetch(context.Background(), config)
		encoded, err := json.Marshal(result.Tasks)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	without := fetchTasks()

	root := writeRelicRoot(t, first, config)
	pointRelicRef(t, first, root.Operation.RelicID, forgeBrokenRelicChild(t, first, root, 700001))
	publishRelicRefs(t, first)
	with := fetchTasks()
	if !bytes.Equal(without, with) {
		t.Fatalf("task results with a forged relic at origin = %s, without = %s", with, without)
	}
}

func TestFetchIgnoresNamesItDoesNotRecognizeUnderRelics(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	ctx := context.Background()
	valid := writeRelicRoot(t, first, config)

	// Another project's relic under this project's key.
	otherProject := config
	otherProject.ProjectID = relicTestID(990001)
	pack, _ := relicRootPack(otherProject, 2)
	record := foldRelic(t, nil, pack)
	foreign := forgeRelicCommit(t, first, "", encodedRelicDocument(t, pack), encodedRelicDocument(t, record.State), nil)
	unknownKey := core.NewRelicID("OTHER", relicTestID(990002))
	for ref, commit := range map[string]string{
		relicRefPrefix + "notes":             valid.Head,
		relicRefPrefix + unknownKey:          valid.Head,
		relicRefPrefix + pack.RelicID:        foreign,
		relicRefPrefix + "nested/" + "child": valid.Head,
	} {
		syncGit(t, first.Root, "push", "--quiet", "origin", commit+":"+ref)
	}

	result, err := second.Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch() error = %v; result = %+v", err, result)
	}
	if len(result.Relics) != 0 {
		t.Fatalf("Fetch() relics = %+v, want none", result.Relics)
	}
	ignored := map[string]IgnoredRef{}
	for _, entry := range result.Ignored {
		ignored[entry.Ref] = entry
	}
	for _, ref := range []string{
		relicRefPrefix + "notes",
		relicRefPrefix + unknownKey,
		relicRefPrefix + pack.RelicID,
		relicRefPrefix + "nested/child",
	} {
		entry, found := ignored[ref]
		if !found {
			t.Fatalf("ignored refs %+v do not name %s", result.Ignored, ref)
		}
		if !entry.PlausibleTask || entry.Reason == "" {
			t.Fatalf("ignored entry %+v, want a reason and plausibleTask true", entry)
		}
	}
	if names := relicRefNames(t, second, relicRefPrefix); len(names) != 0 {
		t.Fatalf("second holds relic refs %q, want none", names)
	}
	if !strings.Contains(ignored[relicRefPrefix+pack.RelicID].Reason, otherProject.ProjectID) {
		t.Fatalf("the foreign relic's reason %q does not name its project", ignored[relicRefPrefix+pack.RelicID].Reason)
	}
}

func TestFetchLeavesADivergedNewerRelicForAnUpgrade(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	root := sharedRelic(t, first, second, config, 1)
	relicID := root.Operation.RelicID
	forgeNewerRelicTip(t, first, config, root)
	publishRelicRefs(t, first)
	local := writeRelicChild(t, second, config, root, nil, relicTitleSet(700101, "A local title"))

	result, err := second.Fetch(context.Background(), config)
	if core.CategoryOf(err) != core.CategoryNewerWriter {
		t.Fatalf("Fetch() error = %v, want newer writer", err)
	}
	entry := assertRelicSyncOutcome(t, result, relicID, SyncNeedsUpgrade)
	if !strings.HasPrefix(entry.Detail, "relic "+relicID+":") || !strings.HasPrefix(err.Error(), "relic "+relicID+":") {
		t.Fatalf("detail %q and error %q, want both to name the relic", entry.Detail, err)
	}
	if got := relicRefHead(t, second, relicID); got != local.Head {
		t.Fatalf("relic ref moved to %s, want the local tip %s", got, local.Head)
	}
}

func TestFetchTakesANewerRelicWithNothingToReplay(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	root := sharedRelic(t, first, second, config, 1)
	newer := forgeNewerRelicTip(t, first, config, root)
	publishRelicRefs(t, first)
	result := fetchRelics(t, second, config)
	assertRelicSyncOutcome(t, result, root.Operation.RelicID, SyncFastForwarded)
	if got := relicRefHead(t, second, root.Operation.RelicID); got != newer {
		t.Fatalf("relic ref = %s, want the newer tip %s", got, newer)
	}
}

func TestFetchReplaysADivergedRelic(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		p := relicTestPartNumbered(1).withField("heading", "Goals")
		root := sharedRelic(t, first, second, config, 1, p)
		relicID := root.Operation.RelicID
		remote := relicHistoryFrom(t, first, config, root, relicOps(relicFieldSet(700001, p.id, "a", "from there")))
		publishRelicRefs(t, first)
		writeRelicChild(t, second, config, root, nil, relicTitleSet(700101, "A local title"))

		result := fetchRelics(t, second, config)
		assertRelicSyncOutcome(t, result, relicID, SyncReconciled)
		next := relicRefHead(t, second, relicID)
		if next == remote.Head {
			t.Fatalf("the relic ref is the fetched tip; the local change was not replayed")
		}
		if _, err := second.Git(ctx, nil, "merge-base", "--is-ancestor", refValue(t, second, relicTrackingRef(relicID)), next); err != nil {
			t.Fatalf("the relic ref %s does not descend from the tracking ref: %v", next, err)
		}
		assertReplayedHistory(t, second, config, relicID, next)
		if got := readRelicTip(t, second, config, relicID).Record.State.Relic.Title; got != "A local title" {
			t.Fatalf("title = %q, want the local one", got)
		}
	})
}

func TestFetchParksTheLocalTipWhenReplayStops(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	p := relicTestPartNumbered(1).withBody("md", []byte("shared text\n"))
	root := sharedRelic(t, first, second, config, 1, p)
	relicID := root.Operation.RelicID
	saveBody := func(repo *Repository, number int, text string) core.RelicSnapshot {
		t.Helper()
		parent := readRelicTip(t, repo, config, relicID)
		part, _ := parent.Record.State.Relic.Part(p.id)
		return writeRelicChild(t, repo, config, parent,
			map[string]core.RelicPartContent{p.id: {Body: []byte(text), HasBody: true}},
			relicBodySet(number, p.id, part.Body.Version, []byte(text)))
	}

	for round, numbers := range [][2]int{{700001, 700101}, {700002, 700102}} {
		theirs := saveBody(first, numbers[0], "saved there\n"+strings.Repeat("x", round))
		publishRelicRefs(t, first)
		ours := saveBody(second, numbers[1], "saved here\n"+strings.Repeat("y", round))

		result, err := second.Fetch(context.Background(), config)
		if core.CategoryOf(err) != core.CategoryConflict {
			t.Fatalf("round %d: Fetch() error = %v, want a conflict", round+1, err)
		}
		assertRelicSyncOutcome(t, result, relicID, SyncConflicted)
		parked := relicParkedRefPrefix + relicID + "/" + []string{"1", "2"}[round]
		if len(result.RelicConflicts) != 1 || result.RelicConflicts[0].ParkedRef != parked {
			t.Fatalf("round %d: conflicts = %+v, want one parked at %s", round+1, result.RelicConflicts, parked)
		}
		if got := refValue(t, second, parked); got != ours.Head {
			t.Fatalf("round %d: %s = %s, want the old local tip %s", round+1, parked, got, ours.Head)
		}
		if got := relicRefHead(t, second, relicID); got != theirs.Head {
			t.Fatalf("round %d: relic ref = %s, want the fetched tip %s", round+1, got, theirs.Head)
		}
	}
}

func TestFetchReadsNoBodyText(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	p := relicTestPartNumbered(1).withBody("md", []byte("the first text\n"))
	q := relicTestPartNumbered(2).withField("heading", "Goals")
	root := writeRelicRoot(t, first, config, p, q)
	relicID := root.Operation.RelicID
	publishRelicRefs(t, first)
	requests := observeRelicObjects(second)
	assertRelicSyncOutcome(t, fetchRelics(t, second, config), relicID, SyncCreated)

	second2 := []byte("the second text\n")
	remote := relicHistoryFrom(t, first, config, root, relicOps(relicBodySet(700001, p.id, p.id, second2)).withBody(p.id, second2))
	publishRelicRefs(t, first)
	assertRelicSyncOutcome(t, fetchRelics(t, second, config), relicID, SyncFastForwarded)

	third := []byte("the third text\n")
	remote = relicHistoryFrom(t, first, config, remote, relicOps(relicBodySet(700002, p.id, relicTestID(700001), third)).withBody(p.id, third))
	publishRelicRefs(t, first)
	second.relicObjectObserver = nil
	fetchedSnapshot := readRelicTip(t, second, config, relicID)
	local := relicHistoryFrom(t, second, config, fetchedSnapshot, relicOps(relicFieldSet(700101, q.id, "a", "from here")))
	second.relicObjectObserver = func(request string) { *requests = append(*requests, request) }
	assertRelicSyncOutcome(t, fetchRelics(t, second, config), relicID, SyncReconciled)
	second.relicObjectObserver = nil

	bodies := map[string]bool{}
	for _, commit := range []string{root.Head, remote.Head, local.Head, relicRefHead(t, second, relicID)} {
		for _, line := range relicLines(gitOutput(t, second, "ls-tree", "-r", commit, "parts/")) {
			meta, path, _ := strings.Cut(line, "\t")
			if strings.HasSuffix(path, "/body") {
				bodies[strings.Fields(meta)[2]] = true
			}
		}
	}
	if len(bodies) < 3 {
		t.Fatalf("found %d body blobs, want three", len(bodies))
	}
	if len(*requests) == 0 {
		t.Fatal("the fetches read no relic object")
	}
	for _, request := range *requests {
		if bodies[request] || strings.HasSuffix(request, "/body") {
			t.Fatalf("a fetch asked for body %q; all requests: %q", request, *requests)
		}
	}
}

// relicClones is the two-clone fixture of the row tests: each clone edits
// through its own relic service, which mints IDs from a range of its own.
type relicClones struct {
	first, second *Repository
	config        core.ProjectConfig
	a, b          core.RelicService
}

func newRelicClones(first, second *Repository, config core.ProjectConfig) relicClones {
	return relicClones{
		first: first, second: second, config: config,
		a: relicServiceFor(first, config, 100000),
		b: relicServiceFor(second, config, 200000),
	}
}

// syncClean runs Sync and requires it to succeed.
func syncClean(t *testing.T, repo *Repository, config core.ProjectConfig) SyncRunResult {
	t.Helper()
	result, err := repo.Sync(context.Background(), config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %+v", err, result)
	}
	return result
}

// share creates a relic in the first clone and brings it to the second by
// synchronizing both.
func (c relicClones) share(t *testing.T, input core.RelicCreateInput) core.RelicMutationResult {
	t.Helper()
	if input.Type == "" {
		input.Type = "spec"
	}
	created, err := c.a.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	pushed := syncClean(t, c.first, c.config)
	if len(pushed.Push.Relics) != 1 || pushed.Push.Relics[0].Status != SyncPublished {
		t.Fatalf("first Sync() pushed relics %+v, want the new one published", pushed.Push.Relics)
	}
	assertRelicSyncOutcome(t, syncClean(t, c.second, c.config).Fetch, created.Relic.ID, SyncCreated)
	return created
}

// update changes a relic through service and returns the IDs of the
// operations the change recorded.
func (c relicClones) update(t *testing.T, service core.RelicService, relicID string, edits ...core.RelicEdit) []string {
	t.Helper()
	result, err := service.Update(context.Background(), relicID, edits)
	if err != nil {
		t.Fatalf("Update(%s) error = %v", relicID, err)
	}
	if !result.Changed {
		t.Fatalf("Update(%s) changed nothing", relicID)
	}
	repo := c.first
	if service.Writer == c.second {
		repo = c.second
	}
	var ids []string
	for _, operation := range readRelicTip(t, repo, c.config, relicID).Operation.Operations {
		ids = append(ids, operation.ID)
	}
	return ids
}

// round runs the row tests' three synchronizations, each of which must
// succeed, and returns the second clone's.
func (c relicClones) round(t *testing.T) SyncRunResult {
	t.Helper()
	syncClean(t, c.first, c.config)
	second := syncClean(t, c.second, c.config)
	syncClean(t, c.first, c.config)
	return second
}

// assertConverged requires both clones and origin to hold one tip for the
// relic, a valid history in which no operation ID is recorded twice, and
// returns that tip.
func (c relicClones) assertConverged(t *testing.T, relicID string) string {
	t.Helper()
	head := relicRefHead(t, c.first, relicID)
	if second := relicRefHead(t, c.second, relicID); second != head {
		t.Fatalf("relic %s: second's tip %s, first's %s", relicID, second, head)
	}
	if origin := remoteRefValue(t, c.first, relicRef(relicID)); origin != head {
		t.Fatalf("relic %s: origin's tip %s, the clones' %s", relicID, origin, head)
	}
	assertReplayedHistory(t, c.first, c.config, relicID, head)
	assertReplayedHistory(t, c.second, c.config, relicID, head)
	return head
}

// relicHistoryOperationIDs returns every operation ID of head's first-parent
// chain.
func relicHistoryOperationIDs(t *testing.T, repo *Repository, head string) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for _, commit := range strings.Fields(gitOutput(t, repo, "rev-list", "--first-parent", head)) {
		pack, err := core.DecodeRelicOperationPack(relicBlob(t, repo, commit+":operation.json"))
		if err != nil {
			t.Fatalf("DecodeRelicOperationPack(%s) error = %v", commit, err)
		}
		for _, operation := range pack.Operations {
			ids[operation.ID] = true
		}
	}
	return ids
}

func assertHistoryHolds(t *testing.T, repo *Repository, head string, operationIDs ...[]string) {
	t.Helper()
	held := relicHistoryOperationIDs(t, repo, head)
	for _, ids := range operationIDs {
		for _, id := range ids {
			if !held[id] {
				t.Fatalf("the history at %s lacks operation %s", head, id)
			}
		}
	}
}

func shownRelic(t *testing.T, service core.RelicService, relicID string) core.RelicDocument {
	t.Helper()
	document, err := service.Show(context.Background(), relicID, core.RelicShowOptions{Bodies: true, Removed: true})
	if err != nil {
		t.Fatalf("Show(%s) error = %v", relicID, err)
	}
	return document
}

func shownPart(t *testing.T, document core.RelicDocument, partID string) core.RelicDocumentPart {
	t.Helper()
	for _, part := range document.Parts {
		if part.ID == partID {
			return part
		}
	}
	t.Fatalf("relic %s has no part %s: %+v", document.ID, partID, document.Parts)
	return core.RelicDocumentPart{}
}

func assertRawString(t *testing.T, what string, got json.RawMessage, want string) {
	t.Helper()
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, encoded) {
		shown := string(got)
		if len(shown) > 80 {
			shown = shown[:80] + "..."
		}
		t.Fatalf("%s = %s, want %q", what, shown, want)
	}
}

func setProperty(t *testing.T, name, value string) core.RelicEdit {
	return core.RelicEdit{Kind: core.RelicEditSetProperty, Name: name, Value: relicJSONString(t, value)}
}

func setField(t *testing.T, partID, name, value string) core.RelicEdit {
	return core.RelicEdit{Kind: core.RelicEditPart, Part: partID,
		Fields: []core.RelicFieldEdit{{Name: name, Value: relicJSONString(t, value)}}}
}

func saveBody(partID, base, text string) core.RelicEdit {
	return core.RelicEdit{Kind: core.RelicEditPart, Part: partID, Base: base,
		Body: &core.RelicBodyInput{Format: "md", Content: []byte(text)}}
}

func relicParts(t *testing.T, count int, withBody bool) []core.RelicPartInput {
	parts := make([]core.RelicPartInput, count)
	for index := range parts {
		parts[index] = core.RelicPartInput{
			Type:   "section",
			Fields: map[string]json.RawMessage{"heading": relicJSONString(t, fmt.Sprintf("Section %d", index+1))},
		}
		if withBody {
			parts[index].Body = &core.RelicBodyInput{Format: "md", Content: []byte(fmt.Sprintf("text %d\n", index+1))}
		}
	}
	return parts
}

func assertNoRelicReport(t *testing.T, result SyncRunResult) {
	t.Helper()
	if len(result.Fetch.RelicConflicts) != 0 || len(result.Fetch.RelicWarnings) != 0 {
		t.Fatalf("Sync() reported conflicts %+v and warnings %+v, want neither",
			result.Fetch.RelicConflicts, result.Fetch.RelicWarnings)
	}
}

func assertOneWarning(t *testing.T, result SyncRunResult, code string) {
	t.Helper()
	warnings := result.Fetch.RelicWarnings
	if len(warnings) != 1 || warnings[0].Code != code {
		t.Fatalf("Sync() warnings = %+v, want one %s", warnings, code)
	}
	if len(result.Fetch.RelicConflicts) != 0 {
		t.Fatalf("Sync() conflicts = %+v, want none", result.Fetch.RelicConflicts)
	}
}

func TestRelicEditsToDifferentPartsFieldsAndPropertiesBothApply(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		c := newRelicClones(first, second, config)
		t.Run("different parts", func(t *testing.T) {
			created := c.share(t, core.RelicCreateInput{Title: "Parts", Parts: relicParts(t, 2, true)})
			relicID, p, q := created.Relic.ID, created.Parts[0], created.Parts[1]
			c.update(t, c.a, relicID, saveBody(p.ID, p.Body.Version, "first's text\n"))
			c.update(t, c.b, relicID, saveBody(q.ID, q.Body.Version, "second's text\n"))
			result := c.round(t)
			assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
			assertNoRelicReport(t, result)
			c.assertConverged(t, relicID)
			for _, service := range []core.RelicService{c.a, c.b} {
				document := shownRelic(t, service, relicID)
				if got := *shownPart(t, document, p.ID).Body.Text; got != "first's text\n" {
					t.Fatalf("part %s body = %q, want first's", p.ID, got)
				}
				if got := *shownPart(t, document, q.ID).Body.Text; got != "second's text\n" {
					t.Fatalf("part %s body = %q, want second's", q.ID, got)
				}
			}
		})
		t.Run("different fields of one part", func(t *testing.T) {
			created := c.share(t, core.RelicCreateInput{Title: "Fields", Parts: relicParts(t, 1, false)})
			relicID, p := created.Relic.ID, created.Parts[0].ID
			c.update(t, c.a, relicID, setField(t, p, "a", "from first"))
			c.update(t, c.b, relicID, setField(t, p, "b", "from second"))
			result := c.round(t)
			assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
			assertNoRelicReport(t, result)
			c.assertConverged(t, relicID)
			for _, service := range []core.RelicService{c.a, c.b} {
				fields := shownPart(t, shownRelic(t, service, relicID), p).Fields
				assertRawString(t, "field a", fields["a"], "from first")
				assertRawString(t, "field b", fields["b"], "from second")
			}
		})
		t.Run("different properties", func(t *testing.T) {
			relicID := c.share(t, core.RelicCreateInput{Title: "Properties"}).Relic.ID
			c.update(t, c.a, relicID, setProperty(t, "x", "from first"))
			c.update(t, c.b, relicID, setProperty(t, "y", "from second"))
			result := c.round(t)
			assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
			assertNoRelicReport(t, result)
			c.assertConverged(t, relicID)
			for _, service := range []core.RelicService{c.a, c.b} {
				properties := shownRelic(t, service, relicID).Properties
				assertRawString(t, "property x", properties["x"], "from first")
				assertRawString(t, "property y", properties["y"], "from second")
			}
		})
		t.Run("properties that pass the total together", func(t *testing.T) {
			relicID := c.share(t, core.RelicCreateInput{Title: "Large properties"}).Relic.ID
			x, y := strings.Repeat("x", 40*1024), strings.Repeat("y", 40*1024)
			c.update(t, c.a, relicID, setProperty(t, "x", x))
			c.update(t, c.b, relicID, setProperty(t, "y", y))
			result := c.round(t)
			assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
			assertOneWarning(t, result, core.WarningRelicOverLimit)
			c.assertConverged(t, relicID)
			for _, service := range []core.RelicService{c.a, c.b} {
				properties := shownRelic(t, service, relicID).Properties
				assertRawString(t, "property x", properties["x"], x)
				assertRawString(t, "property y", properties["y"], y)
			}
			_, err := c.a.Update(context.Background(), relicID, []core.RelicEdit{setProperty(t, "z", "more")})
			if core.CategoryOf(err) != core.CategoryValidation {
				t.Fatalf("Update() growing the properties error = %v, want a validation error", err)
			}
		})
		t.Run("fields of one part that pass the total together", func(t *testing.T) {
			created := c.share(t, core.RelicCreateInput{Title: "Large fields", Parts: relicParts(t, 1, false)})
			relicID, p := created.Relic.ID, created.Parts[0].ID
			a, b := strings.Repeat("a", 9*1024), strings.Repeat("b", 9*1024)
			c.update(t, c.a, relicID, setField(t, p, "a", a))
			c.update(t, c.b, relicID, setField(t, p, "b", b))
			result := c.round(t)
			assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
			assertOneWarning(t, result, core.WarningRelicOverLimit)
			c.assertConverged(t, relicID)
			for _, service := range []core.RelicService{c.a, c.b} {
				fields := shownPart(t, shownRelic(t, service, relicID), p).Fields
				assertRawString(t, "field a", fields["a"], a)
				assertRawString(t, "field b", fields["b"], b)
			}
			_, err := c.a.Update(context.Background(), relicID, []core.RelicEdit{setField(t, p, "c", "more")})
			if core.CategoryOf(err) != core.CategoryValidation {
				t.Fatalf("Update() growing the fields error = %v, want a validation error", err)
			}
		})
	})
}

func TestRelicSameFieldOrPropertyLaterSyncWins(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		c := newRelicClones(first, second, config)
		t.Run("field", func(t *testing.T) {
			created := c.share(t, core.RelicCreateInput{Title: "Field", Parts: relicParts(t, 1, false)})
			relicID, p := created.Relic.ID, created.Parts[0].ID
			ours := c.update(t, c.a, relicID, setField(t, p, "heading", "first's heading"))
			theirs := c.update(t, c.b, relicID, setField(t, p, "heading", "second's heading"))
			result := c.round(t)
			assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
			assertNoRelicReport(t, result)
			head := c.assertConverged(t, relicID)
			assertHistoryHolds(t, first, head, ours, theirs)
			for _, service := range []core.RelicService{c.a, c.b} {
				assertRawString(t, "heading", shownPart(t, shownRelic(t, service, relicID), p).Fields["heading"], "second's heading")
			}
		})
		t.Run("property", func(t *testing.T) {
			relicID := c.share(t, core.RelicCreateInput{Title: "Property"}).Relic.ID
			ours := c.update(t, c.a, relicID, setProperty(t, "owner", "first"))
			theirs := c.update(t, c.b, relicID, setProperty(t, "owner", "second"))
			result := c.round(t)
			assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
			assertNoRelicReport(t, result)
			head := c.assertConverged(t, relicID)
			assertHistoryHolds(t, first, head, ours, theirs)
			for _, service := range []core.RelicService{c.a, c.b} {
				assertRawString(t, "owner", shownRelic(t, service, relicID).Properties["owner"], "second")
			}
		})
	})
}

func relicPartOrder(t *testing.T, service core.RelicService, relicID string) []string {
	t.Helper()
	var order []string
	for _, part := range shownRelic(t, service, relicID).Parts {
		order = append(order, part.ID)
	}
	return order
}

func TestRelicTitleAndPositionLaterSyncWins(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		c := newRelicClones(first, second, config)
		t.Run("title", func(t *testing.T) {
			relicID := c.share(t, core.RelicCreateInput{Title: "Title"}).Relic.ID
			ours := c.update(t, c.a, relicID, core.RelicEdit{Kind: core.RelicEditTitle, Title: "First's title"})
			theirs := c.update(t, c.b, relicID, core.RelicEdit{Kind: core.RelicEditTitle, Title: "Second's title"})
			result := c.round(t)
			assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
			assertNoRelicReport(t, result)
			head := c.assertConverged(t, relicID)
			assertHistoryHolds(t, first, head, ours, theirs)
			for _, service := range []core.RelicService{c.a, c.b} {
				if got := shownRelic(t, service, relicID).Title; got != "Second's title" {
					t.Fatalf("title = %q, want second's", got)
				}
			}
		})
		t.Run("position", func(t *testing.T) {
			created := c.share(t, core.RelicCreateInput{Title: "Position", Parts: relicParts(t, 3, false)})
			relicID := created.Relic.ID
			p1, p2, p3 := created.Parts[0].ID, created.Parts[1].ID, created.Parts[2].ID
			ours := c.update(t, c.a, relicID, core.RelicEdit{Kind: core.RelicEditMovePart, Part: p1, After: p2})
			theirs := c.update(t, c.b, relicID, core.RelicEdit{Kind: core.RelicEditMovePart, Part: p1, After: p3})
			want := relicPartOrder(t, c.b, relicID)
			if !reflect.DeepEqual(want, []string{p2, p3, p1}) {
				t.Fatalf("second's order before the sync = %q", want)
			}
			result := c.round(t)
			assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
			assertNoRelicReport(t, result)
			head := c.assertConverged(t, relicID)
			assertHistoryHolds(t, first, head, ours, theirs)
			for _, service := range []core.RelicService{c.a, c.b} {
				if got := relicPartOrder(t, service, relicID); !reflect.DeepEqual(got, want) {
					t.Fatalf("part order = %q, want second's %q", got, want)
				}
			}
		})
	})
}

func TestRelicSameBodyIsAConflict(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		c := newRelicClones(first, second, config)
		created := c.share(t, core.RelicCreateInput{Title: "Body", Parts: relicParts(t, 1, true)})
		relicID, p := created.Relic.ID, created.Parts[0]
		c.update(t, c.a, relicID, saveBody(p.ID, p.Body.Version, "first's text\n"))
		c.update(t, c.b, relicID, saveBody(p.ID, p.Body.Version, "second's text\n"))

		syncClean(t, first, config)
		result, err := second.Sync(context.Background(), config)
		if core.CategoryOf(err) != core.CategoryConflict {
			t.Fatalf("second Sync() error = %v, want a conflict; result = %+v", err, result)
		}
		assertRelicSyncOutcome(t, result.Fetch, relicID, SyncConflicted)
		conflicts := result.Fetch.RelicConflicts
		if len(conflicts) != 1 || conflicts[0].Type != core.RelicConflictBody || conflicts[0].Part != p.ID {
			t.Fatalf("conflicts = %+v, want one body conflict on part %s", conflicts, p.ID)
		}
		syncClean(t, first, config)
		c.assertConverged(t, relicID)
		for _, service := range []core.RelicService{c.a, c.b} {
			if got := *shownPart(t, shownRelic(t, service, relicID), p.ID).Body.Text; got != "first's text\n" {
				t.Fatalf("body = %q, want first's", got)
			}
		}
		if got := string(relicBlob(t, second, conflicts[0].ParkedRef+":parts/"+p.ID+"/body")); got != "second's text\n" {
			t.Fatalf("the parked ref holds %q, want second's text", got)
		}
	})
}

func TestRelicEditOnARemovedPartAppliesAndWarns(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		c := newRelicClones(first, second, config)
		for _, remover := range []string{"first", "second"} {
			t.Run("removed by "+remover, func(t *testing.T) {
				created := c.share(t, core.RelicCreateInput{Title: "Removed by " + remover, Parts: relicParts(t, 1, false)})
				relicID, p := created.Relic.ID, created.Parts[0].ID
				remove := core.RelicEdit{Kind: core.RelicEditRemovePart, Part: p}
				edit := setField(t, p, "heading", "edited")
				if remover == "first" {
					c.update(t, c.a, relicID, remove)
					c.update(t, c.b, relicID, edit)
				} else {
					c.update(t, c.a, relicID, edit)
					c.update(t, c.b, relicID, remove)
				}
				result := c.round(t)
				assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
				assertOneWarning(t, result, core.WarningRelicEditOnRemovedPart)
				c.assertConverged(t, relicID)
				for _, service := range []core.RelicService{c.a, c.b} {
					part := shownPart(t, shownRelic(t, service, relicID), p)
					if !part.Removed {
						t.Fatalf("part %s is live, want it removed", p)
					}
					assertRawString(t, "heading", part.Fields["heading"], "edited")
				}
			})
		}
	})
}

func TestRelicDeletedOnOneSideAndEditedOnTheOther(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		c := newRelicClones(first, second, config)
		ctx := context.Background()
		t.Run("deleter first", func(t *testing.T) {
			relicID := c.share(t, core.RelicCreateInput{Title: "Deleter first"}).Relic.ID
			if _, err := c.a.Delete(ctx, relicID); err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
			c.update(t, c.b, relicID, core.RelicEdit{Kind: core.RelicEditTitle, Title: "Edited"})
			syncClean(t, first, config)
			result, err := second.Sync(ctx, config)
			if core.CategoryOf(err) != core.CategoryConflict {
				t.Fatalf("second Sync() error = %v, want a conflict; result = %+v", err, result)
			}
			conflicts := result.Fetch.RelicConflicts
			if len(conflicts) != 1 || conflicts[0].Type != core.RelicConflictTombstone {
				t.Fatalf("conflicts = %+v, want one tombstone conflict", conflicts)
			}
			syncClean(t, first, config)
			c.assertConverged(t, relicID)
			state := readRelicTip(t, second, config, relicID).Record.State.Relic
			if !state.Deleted || state.Title != "Deleter first" {
				t.Fatalf("relic deleted %t, title %q; want deleted with the first title", state.Deleted, state.Title)
			}
		})
		t.Run("deleter second", func(t *testing.T) {
			relicID := c.share(t, core.RelicCreateInput{Title: "Deleter second"}).Relic.ID
			edit := c.update(t, c.a, relicID, core.RelicEdit{Kind: core.RelicEditTitle, Title: "Edited"})
			if _, err := c.b.Delete(ctx, relicID); err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
			result := c.round(t)
			assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
			assertNoRelicReport(t, result)
			head := c.assertConverged(t, relicID)
			assertHistoryHolds(t, first, head, edit)
			state := readRelicTip(t, first, config, relicID).Record.State.Relic
			if !state.Deleted || state.Title != "Edited" {
				t.Fatalf("relic deleted %t, title %q; want deleted holding the edit", state.Deleted, state.Title)
			}
		})
	})
}

// allRefs lists every ref of the repository at directory with its object,
// apart from the tracking refs. Only a fetch writes those, so a clone's
// tracking ref names origin's tip as the clone's last fetch found it, which
// is before its own push.
func allRefs(t *testing.T, directory string) string {
	t.Helper()
	var kept []string
	for _, line := range strings.Split(syncGit(t, directory, "for-each-ref", "--format=%(refname) %(objectname)"), "\n") {
		if !strings.HasPrefix(line, "refs/workbook/remotes/") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func TestRelicSyncIsTheSameAfterRetriesAndDuplicates(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		c := newRelicClones(first, second, config)
		bare := syncGit(t, first.Root, "remote", "get-url", "origin")

		t.Run("syncing again changes nothing", func(t *testing.T) {
			relicID := c.share(t, core.RelicCreateInput{Title: "Retried"}).Relic.ID
			c.update(t, c.a, relicID, setProperty(t, "x", "from first"))
			c.update(t, c.b, relicID, setProperty(t, "y", "from second"))
			c.round(t)
			c.assertConverged(t, relicID)
			before := []string{allRefs(t, first.Root), allRefs(t, second.Root), allRefs(t, bare)}
			for range 2 {
				syncClean(t, first, config)
				syncClean(t, second, config)
			}
			after := []string{allRefs(t, first.Root), allRefs(t, second.Root), allRefs(t, bare)}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("syncing again moved refs:\nbefore %q\nafter  %q", before, after)
			}
			for _, repo := range []*Repository{first, second} {
				if got, want := refValue(t, repo, relicTrackingRef(relicID)), relicRefHead(t, repo, relicID); got != want {
					t.Fatalf("tracking ref %s, relic ref %s; want them equal once synchronized", got, want)
				}
			}
		})

		t.Run("a commit two clones publish lands once", func(t *testing.T) {
			relicID := c.share(t, core.RelicCreateInput{Title: "Duplicated"}).Relic.ID
			ours := c.update(t, c.a, relicID, setProperty(t, "x", "from first"))
			third := openSyncClone(t, bare)
			if _, err := third.LoadConfig(); err != nil {
				t.Fatal(err)
			}
			syncGit(t, third.Root, "fetch", "--quiet", first.Root, relicRef(relicID)+":"+relicRef(relicID))
			c.update(t, c.b, relicID, setProperty(t, "y", "from second"))
			syncClean(t, second, config)

			replayed := syncClean(t, third, config)
			assertRelicSyncOutcome(t, replayed.Fetch, relicID, SyncReconciled)
			assertRelicSyncOutcome(t, replayed.Push, relicID, SyncPublished)
			published := remoteRefValue(t, first, relicRef(relicID))
			if published != relicRefHead(t, third, relicID) {
				t.Fatalf("origin holds %s, want the third clone's %s", published, relicRefHead(t, third, relicID))
			}
			result := syncClean(t, first, config)
			if len(result.Push.Relics) != 0 {
				t.Fatalf("first Sync() pushed relics %+v, want none", result.Push.Relics)
			}
			syncClean(t, second, config)
			head := c.assertConverged(t, relicID)
			if head != published {
				t.Fatalf("first ended on %s, want origin's %s: its own pack was written again", head, published)
			}
			assertHistoryHolds(t, first, head, ours)
		})

		t.Run("a replay whose ref never moved", func(t *testing.T) {
			relicID := c.share(t, core.RelicCreateInput{Title: "Interrupted"}).Relic.ID
			c.update(t, c.a, relicID, setProperty(t, "x", "from first"))
			c.update(t, c.b, relicID, setProperty(t, "y", "from second"))
			syncClean(t, first, config)

			// The second clone fetches and writes the replayed objects, and
			// stops before its relic ref moves.
			syncGit(t, second.Root, "fetch", "--quiet", "origin", relicFetchRefspecFor("origin"))
			local := relicRefHead(t, second, relicID)
			request := relicReconcileRequest{RelicID: relicID, Local: local, Remote: refValue(t, second, relicTrackingRef(relicID))}
			_, _, diverged, graph, err := second.classifyRelicHeads(ctx, []relicReconcileRequest{request})
			if err != nil || len(diverged) != 1 {
				t.Fatalf("classifyRelicHeads() = %v, %v; want the relic diverged", diverged, err)
			}
			interrupted := second.reconcileDivergentRelics(ctx, config, diverged, graph)
			if len(interrupted) != 1 || interrupted[0].Err != nil || interrupted[0].Next == "" {
				t.Fatalf("reconcileDivergentRelics() = %+v", interrupted)
			}
			if got := relicRefHead(t, second, relicID); got != local {
				t.Fatalf("the relic ref moved to %s, want %s", got, local)
			}

			result := syncClean(t, second, config)
			assertRelicSyncOutcome(t, result.Fetch, relicID, SyncReconciled)
			syncClean(t, first, config)
			head := c.assertConverged(t, relicID)
			if got, want := refValue(t, second, head+"^{tree}"), refValue(t, second, interrupted[0].Next+"^{tree}"); got != want {
				t.Fatalf("the synchronized tip's tree %s differs from the interrupted replay's %s", got, want)
			}
			if got, want := gitOutput(t, second, "rev-list", "--count", head), gitOutput(t, second, "rev-list", "--count", interrupted[0].Next); got != want {
				t.Fatalf("the synchronized history has %s commits, the interrupted replay %s", got, want)
			}
		})
	})
}

// relicPushes returns the push commands that named a relic ref.
func relicPushes(commands [][]string) [][]string {
	var pushes [][]string
	for _, command := range commands {
		if len(command) > 0 && command[0] == "push" && strings.Contains(strings.Join(command, " "), relicRefPrefix) {
			pushes = append(pushes, command)
		}
	}
	return pushes
}

func TestPushPublishesOnlyRelicsThatDiffer(t *testing.T) {
	t.Parallel()
	first, _, config := syncRepositories(t)
	unchanged := writeRelicRootNumbered(t, first, config, 1)
	changed := writeRelicRootNumbered(t, first, config, 2)
	publishRelicRefs(t, first)
	edited := writeRelicChild(t, first, config, changed, nil, relicTitleSet(700001, "Changed"))
	added := writeRelicRootNumbered(t, first, config, 3)

	commands := observeCommands(first)
	result, err := first.Push(context.Background(), config)
	first.commandObserver = nil
	if err != nil {
		t.Fatalf("Push() error = %v; result = %+v", err, result)
	}
	want := []SyncRelicResult{
		{RelicID: changed.Operation.RelicID, Status: SyncPublished},
		{RelicID: added.Operation.RelicID, Status: SyncPublished},
	}
	if !reflect.DeepEqual(result.Relics, want) {
		t.Fatalf("Push() relics = %+v, want %+v", result.Relics, want)
	}
	pushes := relicPushes(*commands)
	if len(pushes) != 1 {
		t.Fatalf("Push() ran relic pushes %q, want one", pushes)
	}
	refspecs := strings.Join(pushes[0], " ")
	if strings.Contains(refspecs, unchanged.Operation.RelicID) {
		t.Fatalf("Push() pushed the unchanged relic: %q", pushes[0])
	}
	for _, snapshot := range []core.RelicSnapshot{unchanged, edited, added} {
		relicID := snapshot.Operation.RelicID
		if got := remoteRefValue(t, first, relicRef(relicID)); got != snapshot.Head {
			t.Fatalf("origin's relic %s = %s, want %s", relicID, got, snapshot.Head)
		}
	}
}

func TestPushNeverForcesOrDeletesARelicRef(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	ctx := context.Background()
	root := sharedRelic(t, first, second, config, 1)
	relicID := root.Operation.RelicID
	commands := observeCommands(second)
	writeRelicChild(t, second, config, root, nil, relicTitleSet(700101, "Second's title"))
	writeRelicChild(t, first, config, root, nil, relicTitleSet(700001, "First's title"))
	publishRelicRefs(t, first)
	// Rejected, by Push and by PushRelic; then replayed and published.
	if _, err := second.Push(ctx, config); err == nil {
		t.Fatal("Push() over a later remote tip succeeded")
	}
	if _, err := second.PushRelic(ctx, config, relicID); err == nil {
		t.Fatal("PushRelic() over a later remote tip succeeded")
	}
	syncClean(t, second, config)
	writeRelicChild(t, second, config, readRelicTip(t, second, config, relicID), nil, relicTitleSet(700102, "Again"))
	if result, err := second.PushRelic(ctx, config, relicID); err != nil || result.Status != SyncPublished {
		t.Fatalf("PushRelic() = %+v, %v; want published", result, err)
	}
	second.commandObserver = nil

	pushes := relicPushes(*commands)
	if len(pushes) < 4 {
		t.Fatalf("observed relic pushes %q, want four", pushes)
	}
	for _, command := range pushes {
		for _, arg := range command {
			if strings.HasPrefix(arg, "+") || strings.HasPrefix(arg, ":") || strings.HasPrefix(arg, "--force") || arg == "-f" ||
				arg == "--delete" || arg == "-d" || arg == "--mirror" || arg == "--prune" {
				t.Fatalf("a relic push forced or deleted: %q", command)
			}
		}
	}
}

func TestPushOfRelicsIsOneConnectionForAnyNumber(t *testing.T) {
	t.Parallel()
	first, _, config := syncRepositories(t)
	push := func() int {
		t.Helper()
		commands := observeCommands(first)
		result, err := first.Push(context.Background(), config)
		first.commandObserver = nil
		if err != nil {
			t.Fatalf("Push() error = %v; result = %+v", err, result)
		}
		return len(relicPushes(*commands))
	}
	writeRelicRootNumbered(t, first, config, 1)
	if got := push(); got != 1 {
		t.Fatalf("one changed relic ran %d relic pushes, want one", got)
	}
	for n := 2; n <= 11; n++ {
		writeRelicRootNumbered(t, first, config, n)
	}
	if got := push(); got != 1 {
		t.Fatalf("ten changed relics ran %d relic pushes, want one", got)
	}
	if got := push(); got != 0 {
		t.Fatalf("no changed relic ran %d relic pushes, want none", got)
	}
}

func TestPushRelicRejectedByALaterRemoteTipIsReported(t *testing.T) {
	t.Parallel()
	first, second, config := syncRepositories(t)
	ctx := context.Background()
	root := sharedRelic(t, first, second, config, 1)
	relicID := root.Operation.RelicID
	local := writeRelicChild(t, second, config, root, nil, relicTitleSet(700101, "Second's title"))
	fetchRelics(t, second, config)
	// Origin moves after the fetch.
	theirs := writeRelicChild(t, first, config, root, nil, relicTitleSet(700001, "First's title"))
	publishRelicRefs(t, first)

	single, err := second.PushRelicAfterFetch(ctx, config, relicID)
	if core.CategoryOf(err) != core.CategoryOperational || single.Status != SyncRejected {
		t.Fatalf("PushRelicAfterFetch() = %+v, %v; want rejected with an operational error", single, err)
	}
	if !strings.HasPrefix(single.Detail, "relic "+relicID+":") || !strings.HasPrefix(err.Error(), "relic "+relicID+":") {
		t.Fatalf("detail %q and error %q, want both to name the relic", single.Detail, err)
	}
	result, err := second.Push(ctx, config)
	if core.CategoryOf(err) != core.CategoryOperational {
		t.Fatalf("Push() error = %v, want operational", err)
	}
	assertRelicSyncOutcome(t, result, relicID, SyncRejected)
	if got := relicRefHead(t, second, relicID); got != local.Head {
		t.Fatalf("the local relic ref moved to %s, want %s", got, local.Head)
	}
	if got := remoteRefValue(t, second, relicRef(relicID)); got != theirs.Head {
		t.Fatalf("origin's relic ref = %s, want first's %s", got, theirs.Head)
	}

	synced := syncClean(t, second, config)
	assertRelicSyncOutcome(t, synced.Fetch, relicID, SyncReconciled)
	assertRelicSyncOutcome(t, synced.Push, relicID, SyncPublished)
	if got := remoteRefValue(t, second, relicRef(relicID)); got != relicRefHead(t, second, relicID) {
		t.Fatalf("origin's relic ref = %s, want second's replayed tip", got)
	}
	assertReplayedHistory(t, second, config, relicID, relicRefHead(t, second, relicID))
}

// TestThePreviousReleasesFetchNeverSeesRelics runs by hand the fetch v0.6.1
// issues, with the three refspecs recorded in the CLI's unconfigured-session
// transcript, against an origin that holds relics. A mixed-version team keeps
// working only if that fetch stays blind to them, and does not prune the
// relic refs a clone already holds.
func TestThePreviousReleasesFetchNeverSeesRelics(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		one := createSyncTask(t, first, config, "First task")
		two := createSyncTask(t, first, config, "Second task")
		a := writeRelicRootNumbered(t, first, config, 1)
		b := writeRelicRootNumbered(t, first, config, 2)
		if _, err := first.Sync(ctx, config); err != nil {
			t.Fatalf("first Sync() error = %v", err)
		}
		publishRelicRefs(t, first)
		if listing := syncGit(t, first.Root, "ls-remote", "origin", relicRefPrefix+"*"); strings.Count(listing, relicRefPrefix) != 2 {
			t.Fatalf("origin's relic refs = %q, want both relics", listing)
		}

		// The previous release's fetch, verbatim.
		oldFetch := func(repo *Repository) {
			t.Helper()
			syncGit(t, repo.Root, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", "origin",
				"+refs/workbook/tasks/*:refs/workbook/remotes/origin/tasks/*",
				"+refs/workbook/project*:refs/workbook/remotes/origin/project*",
				"+refs/workbook/config*:refs/workbook/remotes/origin/config*")
		}

		oldFetch(second)
		if refs := syncGit(t, second.Root, "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "/relics/") {
			t.Fatalf("the previous release's fetch created a relic ref:\n%s", refs)
		}
		for _, task := range []core.Task{one, two} {
			want := strings.Fields(syncGit(t, second.Root, "ls-remote", "origin", taskRefPrefix+task.ID))
			if len(want) == 0 {
				t.Fatalf("origin has no ref for task %s", task.ID)
			}
			if got := refValue(t, second, remoteTaskRefPrefixFor("origin")+task.ID); got != want[0] {
				t.Fatalf("tracking ref for task %s = %s, want origin's %s", task.ID, got, want[0])
			}
		}

		// A clone that already synced with this build keeps every relic ref
		// and tracking ref through the same fetch, --prune included.
		if _, err := second.Sync(ctx, config); err != nil {
			t.Fatalf("second Sync() error = %v", err)
		}
		localBefore := relicRefNames(t, second, relicRefPrefix)
		trackingBefore := relicRefNames(t, second, relicTrackingRefPrefixFor("origin"))
		if len(localBefore) != 2 || len(trackingBefore) != 2 {
			t.Fatalf("after Sync() the clone holds relic refs %v and tracking refs %v, want two of each", localBefore, trackingBefore)
		}
		oldFetch(second)
		if got := relicRefNames(t, second, relicRefPrefix); !reflect.DeepEqual(got, localBefore) {
			t.Fatalf("relic refs after the previous release's fetch = %v, want %v", got, localBefore)
		}
		if got := relicRefNames(t, second, relicTrackingRefPrefixFor("origin")); !reflect.DeepEqual(got, trackingBefore) {
			t.Fatalf("relic tracking refs after the previous release's fetch = %v, want %v", got, trackingBefore)
		}
		for _, root := range []core.RelicSnapshot{a, b} {
			if got := relicRefHead(t, second, root.Operation.RelicID); got != root.Head {
				t.Fatalf("relic %s moved to %s, want %s", root.Operation.RelicID, got, root.Head)
			}
		}
	})
}
