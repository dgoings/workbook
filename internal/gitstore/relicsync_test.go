package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
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
