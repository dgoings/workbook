package cli

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// These helpers build relic histories for the CLI tests through the exported
// write path, and forge the commits that path refuses to write with Git
// plumbing alone. The relic commands are not used, so a test of something
// other than them does not depend on them.

const relicTestActor = "workbook@example.test"

var relicTestTime = time.Date(2026, time.November, 23, 10, 0, 0, 0, time.UTC)

// relicTestID builds the canonical ULID relic tests name things with. Parts
// and operations use small numbers; relic IDs, create operations and history
// generations use ranges of their own.
func relicTestID(n int) string {
	return fmt.Sprintf("01M9A%021d", n)
}

func relicTestRelicID(n int) string {
	return core.NewRelicID("WB", relicTestID(600000+n))
}

// relicFixture is an open repository relic tests write through.
type relicFixture struct {
	path   string
	repo   *gitstore.Repository
	config core.ProjectConfig
}

func openRelicFixture(t *testing.T, repository string) relicFixture {
	t.Helper()
	repo, err := gitstore.Open(context.Background(), repository)
	if err != nil {
		t.Fatalf("gitstore.Open() error = %v", err)
	}
	config, err := repo.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	return relicFixture{path: repository, repo: repo, config: config}
}

// write folds pack onto parent and stores the result with content.
func (f relicFixture) write(
	t *testing.T,
	parent *core.RelicSnapshot,
	pack core.RelicOperationPack,
	content map[string]core.RelicPartContent,
) core.RelicSnapshot {
	t.Helper()
	var parentRecord *core.RelicRecord
	if parent != nil {
		parentRecord = &parent.Record
	}
	record, err := core.ApplyRelic(parentRecord, pack)
	if err != nil {
		t.Fatalf("ApplyRelic() error = %v", err)
	}
	snapshot, err := f.repo.WriteRelic(context.Background(), f.config, parent, pack, record, content, "relic test "+pack.RelicID)
	if err != nil {
		t.Fatalf("WriteRelic(%s) error = %v", pack.RelicID, err)
	}
	return snapshot
}

// createRelic writes relic number n: one commit that creates it with one part,
// relicTestID(n*10+1), whose body is body.
func (f relicFixture) createRelic(t *testing.T, n int, body []byte) core.RelicSnapshot {
	t.Helper()
	partID := relicTestID(n*10 + 1)
	pack := core.NewRelicOperationPack(f.config.ProjectID, relicTestRelicID(n), relicTestID(900000+n), relicTestActor, 1, relicTestTime,
		[]core.RelicOperation{
			{
				ID:    relicTestID(800000 + n),
				Type:  core.RelicOperationCreate,
				Relic: &core.RelicCreateData{Title: "Task archive compaction", Type: "spec", CreatedAt: relicTestTime},
			},
			{
				ID:       partID,
				Type:     core.RelicOperationPartAdd,
				PartType: "section",
				Position: "1/1",
				Body:     &core.RelicBodyChange{Format: "md", Bytes: int64(len(body))},
			},
		})
	return f.write(t, nil, pack, map[string]core.RelicPartContent{partID: {Body: body, HasBody: true}})
}

// nextRelicPack builds the pack that follows parent, carrying operations.
func nextRelicPack(parent core.RelicSnapshot, operations ...core.RelicOperation) core.RelicOperationPack {
	state := parent.Record.State
	return core.NewRelicOperationPack(state.ProjectID, state.RelicID, state.History.Generation, relicTestActor,
		state.LogicalClock+1, relicTestTime.Add(time.Duration(state.LogicalClock)*time.Minute), operations)
}

func relicBodySetOperation(operationNumber int, partID, base string, body []byte) core.RelicOperation {
	return core.RelicOperation{
		ID:   relicTestID(operationNumber),
		Type: core.RelicOperationPartBodySet,
		Part: partID,
		Body: &core.RelicBodyChange{Format: "md", Bytes: int64(len(body)), Base: base},
	}
}

// setRelicBody writes a body edit of the relic's only part, from base.
func (f relicFixture) setRelicBody(t *testing.T, parent core.RelicSnapshot, operationNumber int, base string, body []byte) core.RelicSnapshot {
	t.Helper()
	partID := parent.Record.State.Relic.Parts[0].ID
	return f.write(t, &parent, nextRelicPack(parent, relicBodySetOperation(operationNumber, partID, base, body)),
		map[string]core.RelicPartContent{partID: {Body: body, HasBody: true}})
}

// setRelicTitle writes a title change.
func (f relicFixture) setRelicTitle(t *testing.T, parent core.RelicSnapshot, operationNumber int, title string) core.RelicSnapshot {
	t.Helper()
	return f.write(t, &parent, nextRelicPack(parent,
		core.RelicOperation{ID: relicTestID(operationNumber), Type: core.RelicOperationTitleSet, Title: title}), nil)
}

// threeCommitRelic writes relic number n as a create, a body edit and a title
// change.
func (f relicFixture) threeCommitRelic(t *testing.T, n int) core.RelicSnapshot {
	t.Helper()
	root := f.createRelic(t, n, []byte("the first text\n"))
	partID := root.Record.State.Relic.Parts[0].ID
	edited := f.setRelicBody(t, root, n*10+2, partID, []byte("the second text\n"))
	return f.setRelicTitle(t, edited, n*10+3, "Task archive compaction, revised")
}

func encodedRelicDocument(t *testing.T, document any) []byte {
	t.Helper()
	encoded, err := core.EncodeDocument(document)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	return encoded
}

// forgeRelicCommit writes a relic commit with Git plumbing alone, on top of
// parent: the two documents exactly as given, parent's links.json, and parts
// as the tree entry for parts/ (empty for none). Nothing checks it.
func (f relicFixture) forgeRelicCommit(t *testing.T, parent string, operationJSON, stateJSON []byte, parts string) string {
	t.Helper()
	entries := "100644 blob " + gitInput(t, f.path, operationJSON, "hash-object", "-w", "--stdin") + "\toperation.json\n" +
		"100644 blob " + gitInput(t, f.path, stateJSON, "hash-object", "-w", "--stdin") + "\tstate.json\n" +
		"100644 blob " + gitOutput(t, f.path, "rev-parse", parent+":links.json") + "\tlinks.json\n"
	if parts != "" {
		entries += "040000 tree " + parts + "\tparts\n"
	}
	tree := gitInput(t, f.path, []byte(entries), "mktree")
	return gitInput(t, f.path, nil, "commit-tree", tree, "-p", parent, "-m", "forged relic commit")
}

// forgeRelicOverwrite writes, on top of tip, a stale body edit of the relic's
// only part made to win: the pack edits from the part's first body version,
// which tip has moved past, yet the state claims the edit saved the body and
// the tree holds its text. The fold ignores such an edit, so the commit does
// not follow from its history. The relic's ref is moved to the forged commit,
// which is returned.
func (f relicFixture) forgeRelicOverwrite(t *testing.T, tip core.RelicSnapshot, operationNumber int) string {
	t.Helper()
	part := tip.Record.State.Relic.Parts[0]
	text := []byte("Bob's text, saved from the first version\n")
	pack := nextRelicPack(tip, relicBodySetOperation(operationNumber, part.ID, part.ID, text))
	record, err := core.ApplyRelic(&tip.Record, pack)
	if err != nil {
		t.Fatalf("ApplyRelic() error = %v", err)
	}
	claimed := record.State
	claimed.Relic.Parts = slices.Clone(claimed.Relic.Parts)
	claimed.Relic.Parts[0].Body = &core.RelicBody{Format: "md", Version: relicTestID(operationNumber), Bytes: int64(len(text))}

	fields := gitOutput(t, f.path, "rev-parse", tip.Head+":parts/"+part.ID+"/fields.json")
	body := gitInput(t, f.path, text, "hash-object", "-w", "--stdin")
	subtree := gitInput(t, f.path, []byte("100644 blob "+fields+"\tfields.json\n100644 blob "+body+"\tbody\n"), "mktree")
	parts := gitInput(t, f.path, []byte("040000 tree "+subtree+"\t"+part.ID+"\n"), "mktree")
	forged := f.forgeRelicCommit(t, tip.Head, encodedRelicDocument(t, pack), encodedRelicDocument(t, claimed), parts)
	gitInput(t, f.path, nil, "update-ref", "refs/workbook/relics/"+tip.Operation.RelicID, forged, tip.Head)
	return forged
}

// forgeNewerRelicCommit writes, on top of tip, what a later Workbook writes
// when it archives and retires the relic: an operation type and a state this
// build has no rules for, marked generation 9. The relic's ref is moved to it,
// and it is returned.
func (f relicFixture) forgeNewerRelicCommit(t *testing.T, tip core.RelicSnapshot, operationNumber int) string {
	t.Helper()
	state := tip.Record.State
	operationJSON := []byte(`{"format":"workbook.relic-operation-pack","version":1,"minReader":9,` +
		`"projectId":"` + f.config.ProjectID + `","relicId":"` + state.RelicID + `",` +
		`"historyGeneration":"` + state.History.Generation + `","actor":{"id":"` + relicTestActor + `"},` +
		fmt.Sprintf(`"logicalClock":%d,"wallTime":"2026-11-23T11:00:00Z",`, state.LogicalClock+1) +
		`"operations":[{"id":"` + relicTestID(operationNumber) + `","type":"relic.archive"}]}` + "\n")
	stateJSON := []byte(gitOutput(t, f.path, "cat-file", "blob", tip.Head+":state.json") + "\n")
	for _, replacement := range [][2]string{
		{fmt.Sprintf(`"minReader":%d`, state.MinReader), `"minReader":9`},
		{fmt.Sprintf(`"logicalClock":%d`, state.LogicalClock), fmt.Sprintf(`"logicalClock":%d`, state.LogicalClock+1)},
		{`"retired":false`, `"retired":true`},
	} {
		raised := bytes.Replace(stateJSON, []byte(replacement[0]), []byte(replacement[1]), 1)
		if bytes.Equal(raised, stateJSON) {
			t.Fatalf("the state holds no %s to replace", replacement[0])
		}
		stateJSON = raised
	}
	parts := ""
	if len(state.Relic.Parts) > 0 {
		parts = gitOutput(t, f.path, "rev-parse", tip.Head+":parts")
	}
	forged := f.forgeRelicCommit(t, tip.Head, operationJSON, stateJSON, parts)
	gitInput(t, f.path, nil, "update-ref", "refs/workbook/relics/"+state.RelicID, forged, tip.Head)
	return forged
}
