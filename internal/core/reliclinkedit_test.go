package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// linkTestTaskID is the ID of a task the link tests point at.
func linkTestTaskID(number int) string {
	return serviceTestConfig.Key + "-" + relicTestID(500000+number)
}

// linkTestTasks holds three tasks: 1 and 2 live, 3 deleted.
func linkTestTasks() *memoryTaskStore {
	live := TaskData{Title: "Live", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1"}
	deleted := live
	deleted.Title, deleted.Deleted = "Deleted", true
	return newMemoryTaskStore(
		serviceSnapshot(linkTestTaskID(1), live),
		serviceSnapshot(linkTestTaskID(2), live),
		serviceSnapshot(linkTestTaskID(3), deleted),
	)
}

// linkedRelicForUpdate is relicCreatedForUpdate with a task reader.
func linkedRelicForUpdate(t *testing.T) (store *memoryRelicStore, tasks *memoryTaskStore, service RelicService, relicID, goals, decision string) {
	t.Helper()
	store, service, relicID, goals, decision, _ = relicCreatedForUpdate(t)
	tasks = linkTestTasks()
	service.Tasks = tasks
	return store, tasks, service, relicID, goals, decision
}

func storedLinks(store *memoryRelicStore, relicID string) []RelicLink {
	return store.snapshots[relicID].Record.Links.Links
}

func TestUpdateAddsLinksToATask(t *testing.T) {
	store, _, service, relicID, goals, _ := linkedRelicForUpdate(t)
	taskID := linkTestTaskID(1)
	result, err := service.Update(context.Background(), relicID, []RelicEdit{
		{Kind: RelicEditAddLink, Task: strings.ToLower(taskID), Rel: "relates-to"},
		{Kind: RelicEditAddLink, Task: taskID, From: strings.ToLower(goals), Rel: "implemented-by"},
	})
	write := requireRelicWritten(t, "Update", store, 2, result, err)
	if got, want := relicOperationTypes(write.pack), []RelicOperationType{RelicOperationLinkAdd, RelicOperationLinkAdd}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	links := storedLinks(store, relicID)
	want := []RelicLink{
		{ID: write.pack.Operations[0].ID, To: RelicLinkTarget{Task: taskID}, Rel: "relates-to"},
		{ID: write.pack.Operations[1].ID, From: goals, To: RelicLinkTarget{Task: taskID}, Rel: "implemented-by"},
	}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("links = %#v, want %#v", links, want)
	}
}

func TestUpdateLinkRefusals(t *testing.T) {
	store, _, service, relicID, goals, decision := linkedRelicForUpdate(t)
	ctx := context.Background()
	if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemovePart, Part: decision}}); err != nil {
		t.Fatalf("remove part: %v", err)
	}
	writes := len(store.writes)
	cases := []struct {
		name     string
		edit     RelicEdit
		category Category
		fragment string
	}{
		{"unknown task", RelicEdit{Kind: RelicEditAddLink, Task: "WB-7ZZZ", Rel: "relates-to"}, CategoryNotFound, "no task matches"},
		{"deleted task", RelicEdit{Kind: RelicEditAddLink, Task: linkTestTaskID(3), Rel: "relates-to"}, CategoryValidation, "is deleted; restore it first"},
		{"relic ID", RelicEdit{Kind: RelicEditAddLink, Task: relicID, Rel: "relates-to"}, CategoryValidation, "a relic links only to tasks so far"},
		{"removed from part", RelicEdit{Kind: RelicEditAddLink, Task: linkTestTaskID(1), From: decision, Rel: "relates-to"}, CategoryValidation, "was removed"},
		{"unknown from part", RelicEdit{Kind: RelicEditAddLink, Task: linkTestTaskID(1), From: "7ZZZ", Rel: "relates-to"}, CategoryNotFound, "no part"},
		{"capital rel", RelicEdit{Kind: RelicEditAddLink, Task: linkTestTaskID(1), Rel: "Implemented-By"}, CategoryValidation, "relationship"},
		{"long rel", RelicEdit{Kind: RelicEditAddLink, Task: linkTestTaskID(1), Rel: strings.Repeat("a", 101)}, CategoryValidation, "relationship"},
		{"empty rel", RelicEdit{Kind: RelicEditAddLink, Task: linkTestTaskID(1)}, CategoryValidation, "relationship"},
	}
	for _, test := range cases {
		// A good edit first, so a refusal is seen to refuse the whole update.
		_, err := service.Update(ctx, relicID, []RelicEdit{
			{Kind: RelicEditAddLink, Task: linkTestTaskID(2), From: goals, Rel: "relates-to"},
			test.edit,
		})
		requireRelicCategory(t, test.name, err, test.category, test.fragment)
		requireRelicWrites(t, test.name, store, writes)
	}

	if _, err := service.Delete(ctx, relicID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	writes = len(store.writes)
	_, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditAddLink, Task: linkTestTaskID(1), Rel: "relates-to"}})
	requireRelicCategory(t, "deleted relic", err, CategoryValidation, "is deleted")
	requireRelicWrites(t, "deleted relic", store, writes)
}

func TestUpdateLinkNeedsATaskReader(t *testing.T) {
	store, service, relicID, _, _, _ := relicCreatedForUpdate(t)
	_, err := service.Update(context.Background(), relicID, []RelicEdit{{Kind: RelicEditAddLink, Task: linkTestTaskID(1), Rel: "relates-to"}})
	requireRelicCategory(t, "no task reader", err, CategoryOperational, "relic service has no task reader")
	requireRelicWrites(t, "no task reader", store, 1)
}

func TestRepeatedLinkAndUnlinkChangeNothing(t *testing.T) {
	store, _, service, relicID, goals, _ := linkedRelicForUpdate(t)
	ctx := context.Background()
	link := RelicEdit{Kind: RelicEditAddLink, Task: linkTestTaskID(1), From: goals, Rel: "implemented-by"}
	first, err := service.Update(ctx, relicID, []RelicEdit{link})
	write := requireRelicWritten(t, "first link", store, 2, first, err)
	linkID := write.pack.Operations[0].ID

	again, err := service.Update(ctx, relicID, []RelicEdit{link})
	if err != nil || again.Changed {
		t.Fatalf("second link: changed = %v, err = %v", again.Changed, err)
	}
	requireRelicWrites(t, "second link", store, 2)

	// The same link twice in one update is one link.
	other := RelicEdit{Kind: RelicEditAddLink, Task: linkTestTaskID(2), Rel: "relates-to"}
	twice, err := service.Update(ctx, relicID, []RelicEdit{other, other})
	write = requireRelicWritten(t, "link twice in one update", store, 3, twice, err)
	if got := len(write.pack.Operations); got != 1 {
		t.Fatalf("link twice in one update recorded %d operations, want 1", got)
	}

	unlink := RelicEdit{Kind: RelicEditRemoveLink, Link: linkID}
	removed, err := service.Update(ctx, relicID, []RelicEdit{unlink})
	requireRelicWritten(t, "first unlink", store, 4, removed, err)
	if _, live := store.snapshots[relicID].Record.Links.Link(linkID); live {
		t.Fatalf("link %s is still live after the unlink", linkID)
	}
	again, err = service.Update(ctx, relicID, []RelicEdit{unlink})
	if err != nil || again.Changed {
		t.Fatalf("second unlink: changed = %v, err = %v", again.Changed, err)
	}
	// In lower case, as a full ID is typed by hand.
	again, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemoveLink, Link: strings.ToLower(linkID)}})
	if err != nil || again.Changed {
		t.Fatalf("lower-case unlink: changed = %v, err = %v", again.Changed, err)
	}
	requireRelicWrites(t, "repeated unlinks", store, 4)

	_, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemoveLink, Link: "01M9B"}})
	requireRelicCategory(t, "unknown prefix", err, CategoryNotFound, "has no link", `"01M9B"`)
}

func TestUnlinkResolvesAPrefixAmongTheRelicsLinks(t *testing.T) {
	store, _, service, relicID, _, _ := linkedRelicForUpdate(t)
	ctx := context.Background()
	result, err := service.Update(ctx, relicID, []RelicEdit{
		{Kind: RelicEditAddLink, Task: linkTestTaskID(1), Rel: "relates-to"},
		{Kind: RelicEditAddLink, Task: linkTestTaskID(2), Rel: "relates-to"},
	})
	write := requireRelicWritten(t, "links", store, 2, result, err)
	first, second := write.pack.Operations[0].ID, write.pack.Operations[1].ID
	shared := 0
	for shared < len(first) && first[shared] == second[shared] {
		shared++
	}
	_, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemoveLink, Link: first[:shared]}})
	requireRelicCategory(t, "ambiguous prefix", err, CategoryValidation, "matches 2 links")
	result, err = service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemoveLink, Link: strings.ToLower(second[:shared+1])}})
	write = requireRelicWritten(t, "unlink by prefix", store, 3, result, err)
	if got := write.pack.Operations[0].LinkID; got != second {
		t.Fatalf("unlink removed %s, want %s", got, second)
	}
}

// relicLinkedToTaskOne stores relic 1 with one part and count links to task 1.
func relicLinkedToTaskOne(t *testing.T, count int) (*memoryRelicStore, RelicService, string) {
	t.Helper()
	snapshot := relicFixture(t, 1, relicFixtureParts(1, 0, -1))
	operations := make([]RelicOperation, count)
	for index := range operations {
		operations[index] = RelicOperation{
			ID: relicTestID(600000 + index), Type: RelicOperationLinkAdd,
			Link: &RelicLinkChange{To: RelicLinkTarget{Task: linkTestTaskID(1)}, Rel: "relates-to"},
		}
	}
	state := snapshot.Record.State
	pack := NewRelicOperationPack(serviceTestConfig.ProjectID, state.RelicID, state.History.Generation, relicServiceTestActor,
		state.LogicalClock+1, relicServiceTestTime.Add(2*time.Minute), operations)
	record, err := ApplyRelic(&snapshot.Record, pack)
	if err != nil {
		t.Fatalf("ApplyRelic(links): %v", err)
	}
	store := newMemoryRelicStore()
	store.put(RelicSnapshot{Head: "fixture-links", Operation: pack, Record: record})
	service := newRelicTestService(store, relicServiceIDs(900000, 100))
	service.Tasks = linkTestTasks()
	return store, service, state.RelicID
}

func TestUpdateRefusesALinkPastTheLimit(t *testing.T) {
	store, service, relicID := relicLinkedToTaskOne(t, MaxRelicLinks)
	ctx := context.Background()
	_, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditAddLink, Task: linkTestTaskID(2), Rel: "relates-to"}})
	requireRelicCategory(t, "one more link", err, CategoryValidation, "2001 links; the limit is 2000")
	requireRelicWrites(t, "one more link", store, 0)

	result, err := service.Update(ctx, relicID, []RelicEdit{
		{Kind: RelicEditRemoveLink, Link: relicTestID(600000)},
		{Kind: RelicEditAddLink, Task: linkTestTaskID(2), Rel: "relates-to"},
	})
	requireRelicWritten(t, "remove then add", store, 1, result, err)
	if got := len(storedLinks(store, relicID)); got != MaxRelicLinks {
		t.Fatalf("links after remove then add = %d, want %d", got, MaxRelicLinks)
	}
}

// relicWithLinksToTasks links the relic of relicCreatedForUpdate to tasks
// 1 (whole relic), 2 (from goals) and 3 (from decision), then deletes task 3
// in the task reader and drops task 2 from it.
func relicWithLinksToTasks(t *testing.T) (*memoryRelicStore, *memoryTaskStore, RelicService, string, string, string, []string) {
	t.Helper()
	store, tasks, service, relicID, goals, decision := linkedRelicForUpdate(t)
	live := TaskData{Title: "Soon deleted", Status: StatusReady, Priority: PriorityHigh, Rank: "1/1"}
	tasks.snapshots[linkTestTaskID(3)] = serviceSnapshot(linkTestTaskID(3), live)
	result, err := service.Update(context.Background(), relicID, []RelicEdit{
		{Kind: RelicEditAddLink, Task: linkTestTaskID(1), Rel: "relates-to"},
		{Kind: RelicEditAddLink, Task: linkTestTaskID(2), From: goals, Rel: "implemented-by"},
		{Kind: RelicEditAddLink, Task: linkTestTaskID(3), From: decision, Rel: "decided-by"},
	})
	write := requireRelicWritten(t, "links", store, 2, result, err)
	ids := []string{write.pack.Operations[0].ID, write.pack.Operations[1].ID, write.pack.Operations[2].ID}
	deleted := live
	deleted.Deleted = true
	tasks.snapshots[linkTestTaskID(3)] = serviceSnapshot(linkTestTaskID(3), deleted)
	delete(tasks.snapshots, linkTestTaskID(2))
	return store, tasks, service, relicID, goals, decision, ids
}

func TestShowMarksLinksToRemovedTasks(t *testing.T) {
	_, tasks, service, relicID, goals, decision, ids := relicWithLinksToTasks(t)
	ctx := context.Background()
	tasks.listCalls = 0
	document, err := service.Show(ctx, relicID, RelicShowOptions{})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	want := []RelicDocumentLink{
		{RelicLink: RelicLink{ID: ids[0], To: RelicLinkTarget{Task: linkTestTaskID(1)}, Rel: "relates-to"}},
		{RelicLink: RelicLink{ID: ids[1], From: goals, To: RelicLinkTarget{Task: linkTestTaskID(2)}, Rel: "implemented-by"}, TargetRemoved: true},
		{RelicLink: RelicLink{ID: ids[2], From: decision, To: RelicLinkTarget{Task: linkTestTaskID(3)}, Rel: "decided-by"}, TargetRemoved: true},
	}
	if !reflect.DeepEqual(document.Links, want) {
		t.Fatalf("Show links = %#v, want %#v", document.Links, want)
	}
	if tasks.listCalls != 1 {
		t.Fatalf("Show listed tasks %d times, want 1", tasks.listCalls)
	}
	if document.Warnings != nil {
		t.Fatalf("Show warnings = %#v, want none", document.Warnings)
	}

	encoded, err := json.Marshal(document.Links[1])
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"id":"`+ids[1]+`","from":"`+goals+`","to":{"task":"`+linkTestTaskID(2)+`"},"rel":"implemented-by","targetRemoved":true}`; got != want {
		t.Fatalf("link JSON = %s, want %s", got, want)
	}

	// Export lists every link as stored and reads no task.
	service.Tasks = nil
	exported, err := service.Export(ctx, relicID)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	for index := range want {
		want[index].TargetRemoved = false
	}
	if !reflect.DeepEqual(exported.Links, want) {
		t.Fatalf("Export links = %#v, want %#v", exported.Links, want)
	}

	// Show of a relic with links needs a task reader.
	_, err = service.Show(ctx, relicID, RelicShowOptions{})
	requireRelicCategory(t, "Show without a task reader", err, CategoryOperational, "relic service has no task reader")
}

// A task list that cannot be read leaves a relic's links listed, unmarked,
// with one warning that says why; only a cancelled read is an error.
func TestShowListsLinksUnmarkedWhenTheTasksCannotBeRead(t *testing.T) {
	_, tasks, service, relicID, goals, _, ids := relicWithLinksToTasks(t)
	tasks.listErr = Errorf(CategoryCorruptData, "requested task object 0123 is missing")
	ctx := context.Background()
	wantWarnings := []Warning{{
		Code: WarningRelicLinkTargetsUnread,
		Message: "the tasks cannot be read, so the targets of relic " + relicID +
			"'s links are not checked and links to removed tasks are not marked: requested task object 0123 is missing; run `workbook validate`",
	}}
	for _, address := range []string{relicID, relicID + "#" + goals} {
		document, err := service.Show(ctx, address, RelicShowOptions{})
		if err != nil {
			t.Fatalf("Show(%s): %v", address, err)
		}
		if len(document.Links) == 0 {
			t.Fatalf("Show(%s) listed no links", address)
		}
		for _, link := range document.Links {
			if link.TargetRemoved {
				t.Fatalf("Show(%s) marked link %s with the tasks unread", address, link.ID)
			}
		}
		if !reflect.DeepEqual(document.Warnings, wantWarnings) {
			t.Fatalf("Show(%s) warnings = %#v, want %#v", address, document.Warnings, wantWarnings)
		}
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "relic-link-targets-unread") {
			t.Fatalf("Show(%s) JSON carries the warning in the document: %s", address, encoded)
		}
	}
	whole, err := service.Show(ctx, relicID, RelicShowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(whole.Links); got != len(ids) {
		t.Fatalf("Show links = %d, want %d", got, len(ids))
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	tasks.listErr = cancelled.Err()
	_, err = service.Show(cancelled, relicID, RelicShowOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Show with a cancelled context = %v, want the context's error", err)
	}
}

func TestShowOfARelicWithoutLinksReadsNoTasks(t *testing.T) {
	_, service, relicID, _, _, _ := relicCreatedForUpdate(t)
	document, err := service.Show(context.Background(), relicID, RelicShowOptions{})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if document.Links == nil || len(document.Links) != 0 {
		t.Fatalf("Show links = %#v, want an empty list", document.Links)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"links":[]`) {
		t.Fatalf("Show JSON %s has no empty links list", encoded)
	}
}

func TestShowHidesLinksFromRemovedParts(t *testing.T) {
	store, _, service, relicID, goals, decision, ids := relicWithLinksToTasks(t)
	ctx := context.Background()
	if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRemovePart, Part: goals}}); err != nil {
		t.Fatalf("remove part: %v", err)
	}
	linkIDs := func(document RelicDocument) []string {
		got := []string{}
		for _, link := range document.Links {
			got = append(got, link.ID)
		}
		return got
	}
	show := func(label, address string, options RelicShowOptions, want []string) {
		t.Helper()
		document, err := service.Show(ctx, address, options)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got := linkIDs(document); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: links = %v, want %v", label, got, want)
		}
	}
	show("without removed", relicID, RelicShowOptions{}, []string{ids[0], ids[2]})
	show("with removed", relicID, RelicShowOptions{Removed: true}, ids)
	show("removed part alone", relicID+"#"+goals, RelicShowOptions{}, []string{ids[1]})
	show("other part alone", relicID+"#"+decision, RelicShowOptions{}, []string{ids[2]})
	if got := len(storedLinks(store, relicID)); got != 3 {
		t.Fatalf("stored links = %d, want 3", got)
	}

	if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditRestorePart, Part: goals}}); err != nil {
		t.Fatalf("restore part: %v", err)
	}
	show("after restore", relicID, RelicShowOptions{}, ids)
}

func TestRelicCreateFromADocumentWithLinksIsRefused(t *testing.T) {
	_, _, service, relicID, _, _ := linkedRelicForUpdate(t)
	ctx := context.Background()
	if _, err := service.Update(ctx, relicID, []RelicEdit{{Kind: RelicEditAddLink, Task: linkTestTaskID(1), Rel: "relates-to"}}); err != nil {
		t.Fatalf("link: %v", err)
	}
	exported, err := service.Export(ctx, relicID)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	encoded, err := json.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	document, err := DecodeRelicDocument(encoded)
	if err != nil {
		t.Fatalf("DecodeRelicDocument: %v", err)
	}
	_, err = RelicCreateInputFromDocument(document)
	requireRelicCategory(t, "create from a linked document", err, CategoryValidation,
		"cannot create a relic with links", "relic update", "--link")
}

type fixedRelicLinkIndex struct {
	lookup  RelicLinkLookup
	targets []RelicLinkTarget
}

func (f *fixedRelicLinkIndex) RelicLinksTo(_ context.Context, _ ProjectConfig, target RelicLinkTarget) (RelicLinkLookup, error) {
	f.targets = append(f.targets, target)
	return f.lookup, nil
}

// workbook show has already resolved its task, so LinkedToTask answers for
// that ID without a task reader; LinkedTo resolves a prefix first.
func TestLinkedToTaskReadsNoTask(t *testing.T) {
	ctx := context.Background()
	relicID := serviceTestConfig.Key + "-R-" + relicTestID(1)
	index := &fixedRelicLinkIndex{lookup: RelicLinkLookup{
		Sources: []RelicLinkSource{{
			Relic: RelicSummary{ID: relicID, Title: "Plan", Type: "spec"},
			Link:  RelicLink{ID: relicTestID(2), To: RelicLinkTarget{Task: linkTestTaskID(1)}, Rel: "relates-to"},
		}},
		Unread: []string{serviceTestConfig.Key + "-R-" + relicTestID(3)},
	}}
	service := RelicService{Config: serviceTestConfig, Links: index}
	summaries, warnings, err := service.LinkedToTask(ctx, linkTestTaskID(1), RelicListFilter{})
	if err != nil {
		t.Fatalf("LinkedToTask: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ID != relicID || len(summaries[0].LinkedBy) != 1 || len(warnings) != 1 {
		t.Fatalf("LinkedToTask = %+v, %+v", summaries, warnings)
	}
	if want := []RelicLinkTarget{{Task: linkTestTaskID(1)}}; !reflect.DeepEqual(index.targets, want) {
		t.Fatalf("looked up %+v, want %+v", index.targets, want)
	}

	tasks := linkTestTasks()
	service.Tasks = tasks
	viaPrefix, _, err := service.LinkedTo(ctx, strings.ToLower(linkTestTaskID(1)), RelicListFilter{})
	if err != nil || !reflect.DeepEqual(viaPrefix, summaries) {
		t.Fatalf("LinkedTo(prefix) = %+v, %v; want %+v", viaPrefix, err, summaries)
	}
}
