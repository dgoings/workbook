package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

const relicTestActor = "workbook@example.test"

var relicTestTime = time.Date(2026, time.November, 23, 10, 0, 0, 0, time.UTC)

// relicTestID builds the canonical ULID relic tests name things with. Parts
// and operations use small numbers; the helpers below keep relic IDs, create
// operations and history generations in ranges of their own.
func relicTestID(n int) string {
	return fmt.Sprintf("01M9A%021d", n)
}

func relicTestRelicID(n int) string {
	return core.NewRelicID("WB", relicTestID(600000+n))
}

func relicRepository(t *testing.T, objectFormat string) (*Repository, core.ProjectConfig) {
	t.Helper()
	testrepo.RequireObjectFormat(t, objectFormat)
	return writeRepositoryWithObjectFormat(t, objectFormat)
}

// forBothRelicFormats runs one test body against a SHA-1 and a SHA-256
// repository.
func forBothRelicFormats(t *testing.T, run func(t *testing.T, repo *Repository, config core.ProjectConfig)) {
	t.Helper()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repo, config := relicRepository(t, objectFormat)
			run(t, repo, config)
		})
	}
}

// relicTestPart is one part a test creates: its ID, and the content that goes
// in the tree beside the part.add that names it.
type relicTestPart struct {
	id      string
	fields  map[string]json.RawMessage
	body    []byte
	hasBody bool
	format  string
}

func relicTestPartNumbered(n int) relicTestPart {
	return relicTestPart{id: relicTestID(n)}
}

func (part relicTestPart) withField(name, value string) relicTestPart {
	fields := make(map[string]json.RawMessage, len(part.fields)+1)
	for existing, stored := range part.fields {
		fields[existing] = stored
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	fields[name] = encoded
	part.fields = fields
	return part
}

func (part relicTestPart) withBody(format string, body []byte) relicTestPart {
	part.body, part.hasBody, part.format = body, true, format
	return part
}

// relicRootPack builds the create pack for relic number relicNumber and the
// content its parts carry.
func relicRootPack(
	config core.ProjectConfig,
	relicNumber int,
	parts ...relicTestPart,
) (core.RelicOperationPack, map[string]core.RelicPartContent) {
	operations := []core.RelicOperation{{
		ID:    relicTestID(800000 + relicNumber),
		Type:  core.RelicOperationCreate,
		Relic: &core.RelicCreateData{Title: "Task archive compaction", Type: "spec", CreatedAt: relicTestTime},
	}}
	content := make(map[string]core.RelicPartContent, len(parts))
	for index, part := range parts {
		operation := core.RelicOperation{
			ID:       part.id,
			Type:     core.RelicOperationPartAdd,
			PartType: "section",
			Position: fmt.Sprintf("%d/1", index+1),
		}
		if part.hasBody {
			operation.Body = &core.RelicBodyChange{Format: part.format, Bytes: int64(len(part.body))}
		}
		operations = append(operations, operation)
		content[part.id] = core.RelicPartContent{Fields: part.fields, Body: part.body, HasBody: part.hasBody}
	}
	pack := core.NewRelicOperationPack(
		config.ProjectID,
		relicTestRelicID(relicNumber),
		relicTestID(900000+relicNumber),
		relicTestActor,
		1,
		relicTestTime,
		operations,
	)
	return pack, content
}

func foldRelic(t *testing.T, parent *core.RelicSnapshot, pack core.RelicOperationPack) core.RelicRecord {
	t.Helper()
	var parentRecord *core.RelicRecord
	if parent != nil {
		parentRecord = &parent.Record
	}
	record, err := core.ApplyRelic(parentRecord, pack)
	if err != nil {
		t.Fatalf("ApplyRelic() error = %v", err)
	}
	return record
}

func writeRelicRoot(t *testing.T, repo *Repository, config core.ProjectConfig, parts ...relicTestPart) core.RelicSnapshot {
	t.Helper()
	return writeRelicRootNumbered(t, repo, config, 1, parts...)
}

func writeRelicRootNumbered(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	relicNumber int,
	parts ...relicTestPart,
) core.RelicSnapshot {
	t.Helper()
	pack, content := relicRootPack(config, relicNumber, parts...)
	record := foldRelic(t, nil, pack)
	snapshot, err := repo.WriteRelic(context.Background(), config, nil, pack, record, content, "create relic "+pack.RelicID)
	if err != nil {
		t.Fatalf("WriteRelic(create %s) error = %v", pack.RelicID, err)
	}
	return snapshot
}

// relicChildPack builds the next pack of the relic parent is the tip of.
func relicChildPack(parent core.RelicSnapshot, operations ...core.RelicOperation) core.RelicOperationPack {
	state := parent.Record.State
	return core.NewRelicOperationPack(
		state.ProjectID,
		state.RelicID,
		state.History.Generation,
		relicTestActor,
		state.LogicalClock+1,
		relicTestTime.Add(time.Duration(state.LogicalClock)*time.Minute),
		operations,
	)
}

func writeRelicChild(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	parent core.RelicSnapshot,
	content map[string]core.RelicPartContent,
	operations ...core.RelicOperation,
) core.RelicSnapshot {
	t.Helper()
	pack := relicChildPack(parent, operations...)
	record := foldRelic(t, &parent, pack)
	snapshot, err := repo.WriteRelic(context.Background(), config, &parent, pack, record, content, "update relic "+pack.RelicID)
	if err != nil {
		t.Fatalf("WriteRelic(update %s) error = %v", pack.RelicID, err)
	}
	return snapshot
}

func relicBodySet(operationNumber int, partID, base string, body []byte) core.RelicOperation {
	return core.RelicOperation{
		ID:   relicTestID(operationNumber),
		Type: core.RelicOperationPartBodySet,
		Part: partID,
		Body: &core.RelicBodyChange{Format: "md", Bytes: int64(len(body)), Base: base},
	}
}

func relicTitleSet(operationNumber int, title string) core.RelicOperation {
	return core.RelicOperation{ID: relicTestID(operationNumber), Type: core.RelicOperationTitleSet, Title: title}
}

func relicRef(relicID string) string { return "refs/workbook/relics/" + relicID }

// relicBlob reads one object's exact bytes, which gitOutput would trim.
func relicBlob(t *testing.T, repo *Repository, name string) []byte {
	t.Helper()
	contents, err := repo.Git(context.Background(), nil, "cat-file", "blob", name)
	if err != nil {
		t.Fatalf("cat-file blob %s: %v", name, err)
	}
	return contents
}

func relicLines(output string) []string {
	if output == "" {
		return nil
	}
	return strings.Split(output, "\n")
}

// relicParents returns the parent object IDs of a commit.
func relicParents(t *testing.T, repo *Repository, commit string) []string {
	t.Helper()
	return strings.Fields(gitOutput(t, repo, "rev-list", "--parents", "-n", "1", commit))[1:]
}

func readRelicTip(t *testing.T, repo *Repository, config core.ProjectConfig, relicID string) core.RelicSnapshot {
	t.Helper()
	heads, err := repo.ListRelicHeads(context.Background(), config)
	if err != nil {
		t.Fatalf("ListRelicHeads() error = %v", err)
	}
	for _, head := range heads {
		if head.RelicID != relicID {
			continue
		}
		snapshots, err := repo.ReadRelicTips(context.Background(), config, []core.RelicHead{head})
		if err != nil {
			t.Fatalf("ReadRelicTips(%s) error = %v", relicID, err)
		}
		return snapshots[0]
	}
	t.Fatalf("relic %s has no ref", relicID)
	return core.RelicSnapshot{}
}

func encodedRelicDocument(t *testing.T, document any) []byte {
	t.Helper()
	encoded, err := core.EncodeDocument(document)
	if err != nil {
		t.Fatalf("EncodeDocument(%T) error = %v", document, err)
	}
	return encoded
}

// assertSameRelicSnapshot compares two snapshots by what they store.
func assertSameRelicSnapshot(t *testing.T, got, want core.RelicSnapshot) {
	t.Helper()
	if got.Head != want.Head {
		t.Fatalf("snapshot head = %q, want %q", got.Head, want.Head)
	}
	for name, pair := range map[string][2]any{
		"operation": {got.Operation, want.Operation},
		"state":     {got.Record.State, want.Record.State},
		"links":     {got.Record.Links, want.Record.Links},
	} {
		if gotBytes, wantBytes := encodedRelicDocument(t, pair[0]), encodedRelicDocument(t, pair[1]); !bytes.Equal(gotBytes, wantBytes) {
			t.Fatalf("snapshot %s = %s, want %s", name, gotBytes, wantBytes)
		}
	}
}

func assertRelicCategory(t *testing.T, err error, want core.Category, fragments ...string) {
	t.Helper()
	if got := core.CategoryOf(err); got != want {
		t.Fatalf("category = %q, want %q; error = %v", got, want, err)
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error %q does not mention %q", err, fragment)
		}
	}
}

// relicFieldsOfSize returns fields whose stored fields.json is exactly size
// bytes, in as many values as it takes to keep each within the limit for one
// field value.
func relicFieldsOfSize(t *testing.T, size int) map[string]json.RawMessage {
	t.Helper()
	const perValue = core.MaxRelicFieldValueBytes - 1024
	stored := func(fields map[string]json.RawMessage) int {
		encoded, err := json.Marshal(core.NewRelicFields(fields))
		if err != nil {
			t.Fatal(err)
		}
		return len(encoded) + 1
	}
	count := size/perValue + 1
	fields := make(map[string]json.RawMessage, count)
	for index := range count {
		fields[fmt.Sprintf("pad%04d", index)] = json.RawMessage(`""`)
	}
	fill := size - stored(fields)
	if fill < 0 {
		t.Fatalf("fields.json cannot be %d bytes", size)
	}
	for index := range count {
		share := fill / (count - index)
		fill -= share
		fields[fmt.Sprintf("pad%04d", index)] = json.RawMessage(`"` + strings.Repeat("x", share) + `"`)
	}
	if got := stored(fields); got != size {
		t.Fatalf("built fields.json of %d bytes, want %d", got, size)
	}
	return fields
}

// relicFieldValueOfSize returns fields holding one value of exactly size
// stored bytes.
func relicFieldValueOfSize(size int) map[string]json.RawMessage {
	return map[string]json.RawMessage{"pad": json.RawMessage(`"` + strings.Repeat("x", size-2) + `"`)}
}

// relicWriteLeftovers lists what a relic write left in Workbook's private
// directory.
func relicWriteLeftovers(t *testing.T, repo *Repository) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repo.CommonGitDir, "workbook"))
	if err != nil {
		t.Fatal(err)
	}
	var leftovers []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "relic-write-") {
			leftovers = append(leftovers, entry.Name())
		}
	}
	return leftovers
}

// forgeRelicTree writes one tree from `git ls-tree` lines.
func forgeRelicTree(t *testing.T, repo *Repository, entries []string) string {
	t.Helper()
	return gitOutputWithInput(t, repo, []byte(strings.Join(entries, "\n")+"\n"), "mktree")
}

func forgeRelicCommitOf(t *testing.T, repo *Repository, tree string, parents ...string) string {
	t.Helper()
	args := []string{"commit-tree", tree, "-m", "forged"}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	return gitOutput(t, repo, args...)
}

func forgeRelicBlob(t *testing.T, repo *Repository, contents []byte) string {
	t.Helper()
	return gitOutputWithInput(t, repo, contents, "hash-object", "-w", "--stdin")
}

// replaceRelicTreeEntry returns entries with the one named name dropped and,
// when replacement is not empty, replaced by it.
func replaceRelicTreeEntry(t *testing.T, entries []string, name, replacement string) []string {
	t.Helper()
	replaced := make([]string, 0, len(entries)+1)
	found := false
	for _, entry := range entries {
		if strings.HasSuffix(entry, "\t"+name) {
			found = true
			continue
		}
		replaced = append(replaced, entry)
	}
	if !found {
		t.Fatalf("tree has no entry %q in %q", name, entries)
	}
	if replacement != "" {
		replaced = append(replaced, replacement)
	}
	return replaced
}

// forgeRelicPart returns a commit that is snapshot's tree with one part's
// subtree replaced by the given blobs. An empty bodyBlob leaves the body out.
func forgeRelicPart(t *testing.T, repo *Repository, snapshot core.RelicSnapshot, partID, fieldsBlob, bodyBlob string) string {
	t.Helper()
	partEntries := []string{"100644 blob " + fieldsBlob + "\tfields.json"}
	if bodyBlob != "" {
		partEntries = append(partEntries, "100644 blob "+bodyBlob+"\tbody")
	}
	partTree := forgeRelicTree(t, repo, partEntries)
	parts := replaceRelicTreeEntry(t,
		relicLines(gitOutput(t, repo, "ls-tree", snapshot.Head+":parts")),
		partID, "040000 tree "+partTree+"\t"+partID)
	top := replaceRelicTreeEntry(t,
		relicLines(gitOutput(t, repo, "ls-tree", snapshot.Head)),
		"parts", "040000 tree "+forgeRelicTree(t, repo, parts)+"\tparts")
	return forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, top), relicParents(t, repo, snapshot.Head)...)
}

func TestWriteRelicStoresTheDocumentedTree(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		body := []byte(strings.Repeat("Compaction keeps the task's current state. ", 43)[:1840])
		partA := relicTestPartNumbered(1).withField("heading", "Goals").withBody("md", body)
		partB := relicTestPartNumbered(2).withField("status", "open")
		pack, content := relicRootPack(config, 1, partA, partB)
		record := foldRelic(t, nil, pack)

		snapshot, err := repo.WriteRelic(ctx, config, nil, pack, record, content, "create relic "+pack.RelicID)
		if err != nil {
			t.Fatalf("WriteRelic() error = %v", err)
		}

		wantPaths := []string{
			"links.json",
			"operation.json",
			"parts/" + partA.id + "/body",
			"parts/" + partA.id + "/fields.json",
			"parts/" + partB.id + "/fields.json",
			"state.json",
		}
		listed := relicLines(gitOutput(t, repo, "ls-tree", "-r", snapshot.Head))
		if len(listed) != len(wantPaths) {
			t.Fatalf("ls-tree -r = %q, want exactly %q", listed, wantPaths)
		}
		for index, line := range listed {
			meta, path, _ := strings.Cut(line, "\t")
			if path != wantPaths[index] || !strings.HasPrefix(meta, "100644 blob ") {
				t.Fatalf("tree entry %d = %q, want a 100644 blob at %q", index, line, wantPaths[index])
			}
		}
		if parents := relicParents(t, repo, snapshot.Head); len(parents) != 0 {
			t.Fatalf("root commit parents = %q, want none", parents)
		}
		if got, want := string(relicBlob(t, repo, snapshot.Head+":links.json")),
			`{"format":"workbook.relic-links","version":1,"links":[]}`+"\n"; got != want {
			t.Fatalf("links.json = %q, want %q", got, want)
		}
		if got, want := string(relicBlob(t, repo, snapshot.Head+":parts/"+partA.id+"/fields.json")),
			`{"format":"workbook.relic-part-fields","version":1,"fields":{"heading":"Goals"}}`+"\n"; got != want {
			t.Fatalf("fields.json = %q, want %q", got, want)
		}
		if got := relicBlob(t, repo, snapshot.Head+":parts/"+partA.id+"/body"); !bytes.Equal(got, body) {
			t.Fatalf("body blob is %d bytes and differs from the %d bytes given", len(got), len(body))
		}
		operation := relicBlob(t, repo, snapshot.Head+":operation.json")
		if !bytes.Equal(operation, encodedRelicDocument(t, pack)) {
			t.Fatalf("operation.json = %s, want the encoded pack", operation)
		}
		if bytes.Contains(operation, []byte("Compaction keeps")) || bytes.Contains(operation, []byte("Goals")) {
			t.Fatalf("operation.json carries part content: %s", operation)
		}
		if got := relicBlob(t, repo, snapshot.Head+":state.json"); !bytes.Equal(got, encodedRelicDocument(t, record.State)) {
			t.Fatalf("state.json = %s, want the encoded state", got)
		}
		if got := refValue(t, repo, relicRef(pack.RelicID)); got != snapshot.Head {
			t.Fatalf("relic ref = %q, want %q", got, snapshot.Head)
		}
		if got := gitOutput(t, repo, "reflog", "show", "--format=%gs", relicRef(pack.RelicID)); got != "workbook: create relic "+pack.RelicID {
			t.Fatalf("reflog message = %q", got)
		}

		want := core.RelicSnapshot{Head: snapshot.Head, Operation: pack, Record: record}
		assertSameRelicSnapshot(t, snapshot, want)
		assertSameRelicSnapshot(t, readRelicTip(t, repo, config, pack.RelicID), want)

		parts, err := repo.ReadRelicParts(ctx, config, snapshot, []string{partA.id, partB.id}, true)
		if err != nil {
			t.Fatalf("ReadRelicParts() error = %v", err)
		}
		wantParts := map[string]core.RelicPartContent{
			partA.id: {Fields: partA.fields, Body: body, HasBody: true},
			partB.id: {Fields: partB.fields},
		}
		if !reflect.DeepEqual(parts, wantParts) {
			t.Fatalf("ReadRelicParts() = %#v, want %#v", parts, wantParts)
		}
	})
}

func TestEditingOnePartRewritesOnlyThatPart(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		const partCount, editedIndex = 500, 250
		parts := make([]relicTestPart, partCount)
		for index := range parts {
			parts[index] = relicTestPartNumbered(index+1).
				withField("heading", fmt.Sprintf("Section %d", index+1)).
				withBody("md", []byte(fmt.Sprintf("Body of section %d.\n", index+1)))
		}
		root := writeRelicRoot(t, repo, config, parts...)
		edited := parts[editedIndex]
		newBody := []byte("The rewritten section.\n")

		// Nil fields: the body edit leaves the part's fields.json as it is.
		updated := writeRelicChild(t, repo, config, root,
			map[string]core.RelicPartContent{edited.id: {Body: newBody, HasBody: true}},
			relicBodySet(700001, edited.id, edited.id, newBody))

		if parents := relicParents(t, repo, updated.Head); len(parents) != 1 || parents[0] != root.Head {
			t.Fatalf("update parents = %q, want exactly %q", parents, root.Head)
		}
		var oldNames, newNames []string
		for index, part := range parts {
			if index == editedIndex {
				continue
			}
			oldNames = append(oldNames, root.Head+":parts/"+part.id)
			newNames = append(newNames, updated.Head+":parts/"+part.id)
		}
		oldTrees := relicLines(gitOutput(t, repo, append([]string{"rev-parse"}, oldNames...)...))
		newTrees := relicLines(gitOutput(t, repo, append([]string{"rev-parse"}, newNames...)...))
		if len(oldTrees) != partCount-1 || !reflect.DeepEqual(oldTrees, newTrees) {
			t.Fatalf("the %d untouched part subtrees changed object IDs", partCount-1)
		}
		if before, after := gitOutput(t, repo, "rev-parse", root.Head+":parts/"+edited.id),
			gitOutput(t, repo, "rev-parse", updated.Head+":parts/"+edited.id); before == after {
			t.Fatalf("the edited part kept subtree %s", before)
		}
		if before, after := gitOutput(t, repo, "rev-parse", root.Head+":parts/"+edited.id+"/fields.json"),
			gitOutput(t, repo, "rev-parse", updated.Head+":parts/"+edited.id+"/fields.json"); before != after {
			t.Fatalf("the edited part's fields.json changed from %s to %s", before, after)
		}
		if got := relicBlob(t, repo, updated.Head+":parts/"+edited.id+"/body"); !bytes.Equal(got, newBody) {
			t.Fatalf("edited body = %q, want %q", got, newBody)
		}
		if got := len(relicLines(gitOutput(t, repo, "ls-tree", updated.Head+":parts"))); got != partCount {
			t.Fatalf("parts tree holds %d subtrees, want %d", got, partCount)
		}
	})
}

// A change that names no part reuses the parent's whole parts tree, and one
// that adds the first part creates it.
func TestWriteRelicKeepsOrCreatesThePartsTree(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()

	empty := writeRelicRoot(t, repo, config)
	if got := relicLines(gitOutput(t, repo, "ls-tree", "--name-only", empty.Head)); !reflect.DeepEqual(got, []string{"links.json", "operation.json", "state.json"}) {
		t.Fatalf("tree of a relic with no part = %q", got)
	}

	added := relicTestPartNumbered(1).withField("heading", "Goals")
	first := writeRelicChild(t, repo, config, empty,
		map[string]core.RelicPartContent{added.id: {Fields: added.fields}},
		core.RelicOperation{ID: added.id, Type: core.RelicOperationPartAdd, PartType: "section", Position: "1/1"})
	if got := relicLines(gitOutput(t, repo, "ls-tree", "-r", "--name-only", first.Head)); !reflect.DeepEqual(got,
		[]string{"links.json", "operation.json", "parts/" + added.id + "/fields.json", "state.json"}) {
		t.Fatalf("tree after the first part = %q", got)
	}

	retitled := writeRelicChild(t, repo, config, first, nil, relicTitleSet(700001, "A new title"))
	if before, after := gitOutput(t, repo, "rev-parse", first.Head+":parts"),
		gitOutput(t, repo, "rev-parse", retitled.Head+":parts"); before != after {
		t.Fatalf("a title change moved the parts tree from %s to %s", before, after)
	}

	// New fields replace fields.json; a part added without content gets the
	// empty document.
	second := relicTestPartNumbered(2)
	changed := writeRelicChild(t, repo, config, retitled,
		map[string]core.RelicPartContent{added.id: {Fields: map[string]json.RawMessage{"heading": json.RawMessage(`"Aims"`)}}},
		core.RelicOperation{ID: relicTestID(700002), Type: core.RelicOperationPartFieldSet, Part: added.id, Name: "heading", Value: json.RawMessage(`"Aims"`)},
		core.RelicOperation{ID: second.id, Type: core.RelicOperationPartAdd, PartType: "section", Position: "2/1"})
	parts, err := repo.ReadRelicParts(ctx, config, changed, []string{added.id, second.id}, true)
	if err != nil {
		t.Fatalf("ReadRelicParts() error = %v", err)
	}
	want := map[string]core.RelicPartContent{
		added.id:  {Fields: map[string]json.RawMessage{"heading": json.RawMessage(`"Aims"`)}},
		second.id: {Fields: map[string]json.RawMessage{}},
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("ReadRelicParts() = %#v, want %#v", parts, want)
	}
	if got, want := string(relicBlob(t, repo, changed.Head+":parts/"+second.id+"/fields.json")),
		`{"format":"workbook.relic-part-fields","version":1,"fields":{}}`+"\n"; got != want {
		t.Fatalf("empty fields.json = %q, want %q", got, want)
	}

	// A first body for a part that had none joins its fields.json, and a
	// move leaves every subtree where it was.
	firstBody := []byte("now it has a body\n")
	withBody := writeRelicChild(t, repo, config, changed,
		map[string]core.RelicPartContent{second.id: {Body: firstBody, HasBody: true}},
		relicBodySet(700003, second.id, "", firstBody),
		core.RelicOperation{ID: relicTestID(700004), Type: core.RelicOperationPartMove, Part: added.id, Position: "3/1"})
	if got := relicLines(gitOutput(t, repo, "ls-tree", "-r", "--name-only", withBody.Head)); !reflect.DeepEqual(got, []string{
		"links.json", "operation.json",
		"parts/" + added.id + "/fields.json",
		"parts/" + second.id + "/body", "parts/" + second.id + "/fields.json",
		"state.json",
	}) {
		t.Fatalf("tree after a first body = %q", got)
	}
	if got := relicBlob(t, repo, withBody.Head+":parts/"+second.id+"/body"); !bytes.Equal(got, firstBody) {
		t.Fatalf("first body = %q, want %q", got, firstBody)
	}
	if before, after := gitOutput(t, repo, "rev-parse", changed.Head+":parts/"+added.id),
		gitOutput(t, repo, "rev-parse", withBody.Head+":parts/"+added.id); before != after {
		t.Fatalf("a move changed the part's subtree from %s to %s", before, after)
	}
}

func TestWriteRelicUsesABoundedNumberOfGitProcesses(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	var commands [][]string
	repo.commandObserver = func(args []string) { commands = append(commands, args) }
	count := func(relicNumber, partCount int) int {
		t.Helper()
		parts := make([]relicTestPart, partCount)
		for index := range parts {
			parts[index] = relicTestPartNumbered(relicNumber*1000+index).
				withField("heading", fmt.Sprintf("Section %d", index)).
				withBody("md", []byte(fmt.Sprintf("Body %d of relic %d.\n", index, relicNumber)))
		}
		commands = nil
		writeRelicRootNumbered(t, repo, config, relicNumber, parts...)
		return len(commands)
	}

	// The first write also loads the project's keys, so it is the most this
	// repository ever spends on one.
	const bound = 15
	cold := count(1, 3)
	small := count(2, 3)
	large := count(3, 300)
	largeCommands := commands
	if cold > bound || large > bound {
		t.Fatalf("a first write ran %d Git commands and a 300-part create ran %d, want at most %d each: %q",
			cold, large, bound, largeCommands)
	}
	if large != small {
		t.Fatalf("a 300-part create ran %d Git commands and a 3-part create ran %d; the count must not grow with the parts: %q",
			large, small, largeCommands)
	}

	// An update's count does not grow with the parts it rewrites either.
	tip := readRelicTip(t, repo, config, relicTestRelicID(3))
	update := func(parent core.RelicSnapshot, operationNumber int, partNumbers ...int) (core.RelicSnapshot, int) {
		t.Helper()
		content := map[string]core.RelicPartContent{}
		var operations []core.RelicOperation
		for _, number := range partNumbers {
			partID := relicTestID(3000 + number)
			part, _ := parent.Record.State.Relic.Part(partID)
			body := []byte(fmt.Sprintf("Rewritten by %d.\n", operationNumber+number))
			content[partID] = core.RelicPartContent{Body: body, HasBody: true}
			operations = append(operations, relicBodySet(operationNumber+number, partID, part.Body.Version, body))
		}
		commands = nil
		next := writeRelicChild(t, repo, config, parent, content, operations...)
		return next, len(commands)
	}
	many := make([]int, 200)
	for index := range many {
		many[index] = index
	}
	tip, one := update(tip, 710000, 0)
	_, twoHundred := update(tip, 720000, many...)
	t.Logf("Git commands: first write %d, 3-part create %d, 300-part create %d, 1-part update %d, 200-part update %d",
		cold, small, large, one, twoHundred)
	if one > bound || twoHundred != one {
		t.Fatalf("an update of one part ran %d Git commands and of 200 parts ran %d, want the same and at most %d: %q",
			one, twoHundred, bound, commands)
	}
}

// assertRelicLostRace asserts the refusal a write gets when another change to
// the relic landed first: a stale write that names the relic and ends by
// saying what to do. A command shows only the error's own message, so the
// advice has to be in that and not in a cause behind it.
func assertRelicLostRace(t *testing.T, err error, relicID string) {
	t.Helper()
	assertRelicCategory(t, err, core.CategoryStaleWrite, "relic ref changed concurrently", relicID)
	var typed *core.Error
	if !errors.As(err, &typed) || !strings.HasSuffix(typed.Message, "; run the command again") {
		t.Fatalf("a lost race says %q; it must end by saying to run the command again", err)
	}
}

func TestWriteRelicRefusesAStaleParent(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withBody("md", []byte("first\n"))
	root := writeRelicRoot(t, repo, config, part)

	first := writeRelicChild(t, repo, config, root, nil, relicTitleSet(700001, "The first writer's title"))

	secondPack := relicChildPack(root, relicTitleSet(700002, "The second writer's title"))
	_, err := repo.WriteRelic(ctx, config, &root, secondPack, foldRelic(t, &root, secondPack), nil, "update relic")
	assertRelicLostRace(t, err, secondPack.RelicID)
	if got := refValue(t, repo, relicRef(secondPack.RelicID)); got != first.Head {
		t.Fatalf("relic ref = %q after a refused write, want the first write's %q", got, first.Head)
	}
	assertSameRelicSnapshot(t, readRelicTip(t, repo, config, secondPack.RelicID), first)

	// A second create of the same relic never replaces the first.
	createPack, createContent := relicRootPack(config, 1, part)
	_, err = repo.WriteRelic(ctx, config, nil, createPack, foldRelic(t, nil, createPack), createContent, "create relic")
	assertRelicLostRace(t, err, createPack.RelicID)
	if got := refValue(t, repo, relicRef(createPack.RelicID)); got != first.Head {
		t.Fatalf("relic ref = %q after a refused create, want %q", got, first.Head)
	}
}

// The check before the objects are written only saves work. The ref move is
// what decides: two writers that both got as far as a commit cannot both land.
func TestRelicRefMoveIsACompareAndSwap(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	root := writeRelicRoot(t, repo, config, relicTestPartNumbered(1))
	ref := relicRef(root.Operation.RelicID)

	var commits []string
	for index, title := range []string{"One", "Two"} {
		pack := relicChildPack(root, relicTitleSet(700001+index, title))
		commit, err := repo.writeRelicObjects(ctx, &root, pack, foldRelic(t, &root, pack), nil, "update relic")
		if err != nil {
			t.Fatalf("writeRelicObjects(%s) error = %v", title, err)
		}
		commits = append(commits, commit)
	}
	if err := repo.moveRelicRef(ctx, ref, commits[0], root.Head, "update relic"); err != nil {
		t.Fatalf("moveRelicRef(first) error = %v", err)
	}
	err := repo.moveRelicRef(ctx, ref, commits[1], root.Head, "update relic")
	assertRelicLostRace(t, err, root.Operation.RelicID)
	if got := refValue(t, repo, ref); got != commits[0] {
		t.Fatalf("relic ref = %q, want the first mover's %q", got, commits[0])
	}

	// The same for two creates of one relic.
	createRef := relicRef(relicTestRelicID(2))
	var roots []string
	for _, parts := range [][]relicTestPart{nil, {relicTestPartNumbered(5)}} {
		pack, content := relicRootPack(config, 2, parts...)
		commit, err := repo.writeRelicObjects(ctx, nil, pack, foldRelic(t, nil, pack), content, "create relic")
		if err != nil {
			t.Fatalf("writeRelicObjects(create) error = %v", err)
		}
		roots = append(roots, commit)
	}
	if err := repo.moveRelicRef(ctx, createRef, roots[0], "", "create relic"); err != nil {
		t.Fatalf("moveRelicRef(first create) error = %v", err)
	}
	err = repo.moveRelicRef(ctx, createRef, roots[1], "", "create relic")
	assertRelicLostRace(t, err, relicTestRelicID(2))
	if got := refValue(t, repo, createRef); got != roots[0] {
		t.Fatalf("relic ref = %q, want the first create's %q", got, roots[0])
	}
}

// Several processes writing the same relic from the same tip: exactly one
// lands, every other is told its write was stale, and nothing is half-written.
func TestConcurrentRelicWritesLandExactlyOne(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withBody("md", []byte("first\n"))
	root := writeRelicRoot(t, repo, config, part)

	const writers = 8
	type outcome struct {
		snapshot core.RelicSnapshot
		err      error
	}
	outcomes := make([]outcome, writers)
	bodyOf := make(map[string]string, writers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range writers {
		// Each writer has its own handle, as each process would.
		writer, err := Open(ctx, repo.Root)
		if err != nil {
			t.Fatal(err)
		}
		body := []byte(fmt.Sprintf("writer %d\n", index))
		pack := relicChildPack(root, relicBodySet(700001+index, part.id, part.id, body))
		bodyOf[relicTestID(700001+index)] = string(body)
		record := foldRelic(t, &root, pack)
		content := map[string]core.RelicPartContent{part.id: {Body: body, HasBody: true}}
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			snapshot, err := writer.WriteRelic(ctx, config, &root, pack, record, content, "update relic")
			outcomes[index] = outcome{snapshot: snapshot, err: err}
		}()
	}
	close(start)
	group.Wait()

	var winners []core.RelicSnapshot
	for index, result := range outcomes {
		switch {
		case result.err == nil:
			winners = append(winners, result.snapshot)
		case core.CategoryOf(result.err) != core.CategoryStaleWrite:
			t.Fatalf("writer %d failed with %q, want a stale write: %v", index, core.CategoryOf(result.err), result.err)
		default:
			assertRelicLostRace(t, result.err, root.Operation.RelicID)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("%d writers landed, want exactly one", len(winners))
	}
	tip := readRelicTip(t, repo, config, root.Operation.RelicID)
	assertSameRelicSnapshot(t, tip, winners[0])
	if parents := relicParents(t, repo, tip.Head); len(parents) != 1 || parents[0] != root.Head {
		t.Fatalf("tip parents = %q, want exactly %q", parents, root.Head)
	}
	parts, err := repo.ReadRelicParts(ctx, config, tip, []string{part.id}, true)
	if err != nil {
		t.Fatalf("ReadRelicParts() error = %v", err)
	}
	winningPart, _ := tip.Record.State.Relic.Part(part.id)
	if want := bodyOf[winningPart.Body.Version]; want == "" || string(parts[part.id].Body) != want {
		t.Fatalf("stored body = %q, want the winner's %q", parts[part.id].Body, want)
	}
	gitOutput(t, repo, "fsck", "--no-dangling")
	if leftovers := relicWriteLeftovers(t, repo); len(leftovers) != 0 {
		t.Fatalf("the writers left %q behind", leftovers)
	}
}

func TestInterruptedRelicWriteChangesNothing(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	partA := relicTestPartNumbered(1).withField("heading", "Goals").withBody("md", []byte("first\n"))
	partB := relicTestPartNumbered(2)
	root := writeRelicRoot(t, repo, config, partA, partB)
	ref := relicRef(root.Operation.RelicID)

	newBody := []byte("second\n")
	added := relicTestPartNumbered(3).withBody("md", []byte("a new part\n"))
	pack := relicChildPack(root,
		relicBodySet(700001, partA.id, partA.id, newBody),
		core.RelicOperation{ID: added.id, Type: core.RelicOperationPartAdd, PartType: "section", Position: "3/1",
			Body: &core.RelicBodyChange{Format: "md", Bytes: int64(len(added.body))}})
	record := foldRelic(t, &root, pack)
	content := map[string]core.RelicPartContent{
		partA.id: {Body: newBody, HasBody: true},
		added.id: {Body: added.body, HasBody: true},
	}

	// Everything but the ref move: the write stopped here.
	orphan, err := repo.writeRelicObjects(ctx, &root, pack, record, content, "update relic")
	if err != nil {
		t.Fatalf("writeRelicObjects() error = %v", err)
	}
	if kind := gitOutput(t, repo, "cat-file", "-t", orphan); kind != "commit" {
		t.Fatalf("the interrupted write left a %s, want its commit", kind)
	}
	if got := refValue(t, repo, ref); got != root.Head {
		t.Fatalf("relic ref = %q after an interrupted write, want %q", got, root.Head)
	}
	assertSameRelicSnapshot(t, readRelicTip(t, repo, config, root.Operation.RelicID), root)
	parts, err := repo.ReadRelicParts(ctx, config, root, []string{partA.id}, true)
	if err != nil || string(parts[partA.id].Body) != "first\n" {
		t.Fatalf("ReadRelicParts() = %#v, %v; want the body from before the interrupted write", parts, err)
	}
	gitOutput(t, repo, "fsck", "--no-dangling")
	if leftovers := relicWriteLeftovers(t, repo); len(leftovers) != 0 {
		t.Fatalf("the interrupted write left %q behind", leftovers)
	}

	written, err := repo.WriteRelic(ctx, config, &root, pack, record, content, "update relic")
	if err != nil {
		t.Fatalf("WriteRelic() after an interrupted write error = %v", err)
	}
	if got := refValue(t, repo, ref); got != written.Head {
		t.Fatalf("relic ref = %q, want %q", got, written.Head)
	}
	if parents := relicParents(t, repo, written.Head); len(parents) != 1 || parents[0] != root.Head {
		t.Fatalf("parents = %q, want exactly %q", parents, root.Head)
	}
	assertSameRelicSnapshot(t, readRelicTip(t, repo, config, root.Operation.RelicID),
		core.RelicSnapshot{Head: written.Head, Operation: pack, Record: record})
	gitOutput(t, repo, "fsck", "--no-dangling")
}

// The temporary files a write hashes are removed whether it succeeds or fails.
func TestWriteRelicRemovesItsTemporaryFiles(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	pack, content := relicRootPack(config, 1, relicTestPartNumbered(1).withBody("md", []byte("a body\n")))
	record := foldRelic(t, nil, pack)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sawTemporaryFiles := false
	repo.commandObserver = func(args []string) {
		if !slices.Contains(args, "--stdin-paths") {
			return
		}
		// The files are on disk when Git is asked to hash them, and Git never
		// gets to: the process is stopped here.
		sawTemporaryFiles = len(relicWriteLeftovers(t, repo)) == 1
		cancel()
	}
	if _, err := repo.WriteRelic(ctx, config, nil, pack, record, content, "create relic"); err == nil {
		t.Fatal("WriteRelic() with a canceled hash-object succeeded")
	}
	repo.commandObserver = nil
	if !sawTemporaryFiles {
		t.Fatal("the write never had its temporary directory on disk; the test checks nothing")
	}
	if leftovers := relicWriteLeftovers(t, repo); len(leftovers) != 0 {
		t.Fatalf("a failed write left %q behind", leftovers)
	}
	if refExists(t, repo, relicRef(pack.RelicID)) {
		t.Fatal("a failed write created the relic ref")
	}

	if _, err := repo.WriteRelic(context.Background(), config, nil, pack, record, content, "create relic"); err != nil {
		t.Fatalf("WriteRelic() error = %v", err)
	}
	if leftovers := relicWriteLeftovers(t, repo); len(leftovers) != 0 {
		t.Fatalf("a finished write left %q behind", leftovers)
	}
}

func TestWriteRelicNeverStoresTextFromAStaleBodyEdit(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withBody("md", []byte("the first text\n"))
	root := writeRelicRoot(t, repo, config, part)
	current := []byte("the second text, saved from the first\n")
	edited := writeRelicChild(t, repo, config, root,
		map[string]core.RelicPartContent{part.id: {Body: current, HasBody: true}},
		relicBodySet(700001, part.id, part.id, current))

	// This edit also started from the first version, which is no longer the
	// current one, so the fold leaves the body alone.
	// It is the same size as the current text, so only the version rule
	// stands between it and the tree.
	stale := bytes.ToUpper(current)
	pack := relicChildPack(edited, relicBodySet(700002, part.id, part.id, stale))
	record := foldRelic(t, &edited, pack)
	if got, _ := record.State.Relic.Part(part.id); got.Body.Version != relicTestID(700001) {
		t.Fatalf("the stale edit folded to version %s; the test needs it to change nothing", got.Body.Version)
	}

	written, err := repo.WriteRelic(ctx, config, &edited, pack, record,
		map[string]core.RelicPartContent{part.id: {Body: stale, HasBody: true}}, "update relic")
	if err != nil {
		t.Fatalf("WriteRelic() error = %v", err)
	}
	if before, after := gitOutput(t, repo, "rev-parse", edited.Head+":parts/"+part.id+"/body"),
		gitOutput(t, repo, "rev-parse", written.Head+":parts/"+part.id+"/body"); before != after {
		t.Fatalf("the stale edit replaced body %s with %s", before, after)
	}
	if before, after := gitOutput(t, repo, "rev-parse", edited.Head+":parts"),
		gitOutput(t, repo, "rev-parse", written.Head+":parts"); before != after {
		t.Fatalf("the stale edit moved the parts tree from %s to %s", before, after)
	}
	parts, err := repo.ReadRelicParts(ctx, config, written, []string{part.id}, true)
	if err != nil {
		t.Fatalf("ReadRelicParts() error = %v", err)
	}
	if got := parts[part.id]; !got.HasBody || !bytes.Equal(got.Body, current) {
		t.Fatalf("body after the stale edit = %q, want %q", got.Body, current)
	}
	if !bytes.Contains(relicBlob(t, repo, written.Head+":operation.json"), []byte(relicTestID(700002))) {
		t.Fatal("the stale edit is not in history")
	}
}

// A second part.add for a part the relic already has takes the ID that is the
// version of that part's first body. The fold refuses it, so the writer has no
// record to store and writes nothing, whatever record the caller hands it.
func TestWriteRelicRefusesARepeatedPartAdd(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	current := []byte("the first text\n")
	part := relicTestPartNumbered(1).withField("heading", "Goals").withBody("md", current)
	root := writeRelicRoot(t, repo, config, part)
	relicID := root.Operation.RelicID

	// The same size as the current text, so nothing but the repeated ID stands
	// between it and the tree.
	replacement := bytes.ToUpper(current)
	pack := relicChildPack(root, relicPartAdd(part.id, "1/1", replacement))
	if _, err := core.ApplyRelic(&root.Record, pack); core.CategoryOf(err) != core.CategoryCorruptData {
		t.Fatalf("ApplyRelic(a repeated part.add) error = %v, want corrupt data", err)
	}
	// The record a caller that ignored the operation would hand over: the
	// parent's, one clock on.
	record := root.Record
	record.State.LogicalClock = pack.LogicalClock
	record.State.Relic.UpdatedAt = pack.WallTime

	_, err := repo.WriteRelic(ctx, config, &root, pack, record,
		map[string]core.RelicPartContent{part.id: {Body: replacement, HasBody: true}}, "update relic")
	if err == nil {
		t.Fatal("WriteRelic() stored a repeated part.add")
	}
	assertRelicCategory(t, err, core.CategoryCorruptData, part.id)
	if tip := gitOutput(t, repo, "rev-parse", relicRef(relicID)); tip != root.Head {
		t.Fatalf("the relic ref moved from %s to %s", root.Head, tip)
	}
	parts, err := repo.ReadRelicParts(ctx, config, root, []string{part.id}, true)
	if err != nil {
		t.Fatalf("ReadRelicParts() error = %v", err)
	}
	if got := parts[part.id]; !got.HasBody || !bytes.Equal(got.Body, current) {
		t.Fatalf("body after the refused part.add = %q, want %q", got.Body, current)
	}
	assertRelicHistoryValid(t, repo, config, relicID, root.Head, 1)
}

// Fields given for a part the parent has are stored only when the pack sets or
// clears one of its fields, as rule R7 holds every read to. Otherwise the part
// keeps the parent's fields.json, whatever content holds.
func TestWriteRelicKeepsFieldsWithoutAFieldOperation(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withField("heading", "Goals").withBody("md", []byte("the first text\n"))
	other := relicTestPartNumbered(2).withField("heading", "Risks")
	root := writeRelicRoot(t, repo, config, part, other)
	relicID := root.Operation.RelicID
	fieldsAt := func(head, partID string) string {
		return gitOutput(t, repo, "rev-parse", head+":parts/"+partID+"/fields.json")
	}
	changed := relicTestPartNumbered(1).withField("heading", "Something else").fields

	moved := writeRelicChild(t, repo, config, root,
		map[string]core.RelicPartContent{part.id: {Fields: changed}},
		relicPartMove(700101, part.id, "3/1"))
	if before, after := fieldsAt(root.Head, part.id), fieldsAt(moved.Head, part.id); before != after {
		t.Fatalf("a move stored fields.json %s in place of %s", after, before)
	}
	assertRelicHistoryValid(t, repo, config, relicID, moved.Head, 2)
	got, err := repo.GetRelic(ctx, config, relicID)
	if err != nil {
		t.Fatalf("GetRelic() after the move error = %v", err)
	}
	assertSameRelicSnapshot(t, got, moved)

	// A field edit of the part does store what the caller gives.
	edit := relicFieldSet(700102, part.id, "heading", "Something else")
	edited := writeRelicChild(t, repo, config, moved,
		map[string]core.RelicPartContent{part.id: {Fields: relicFieldsAfter(t, repo, config, moved, part.id, edit)}},
		edit)
	parts, err := repo.ReadRelicParts(ctx, config, edited, []string{part.id}, false)
	if err != nil {
		t.Fatalf("ReadRelicParts() error = %v", err)
	}
	if want := changed; !reflect.DeepEqual(parts[part.id].Fields, want) {
		t.Fatalf("fields after the field edit = %s, want %s", parts[part.id].Fields, want)
	}
	assertRelicHistoryValid(t, repo, config, relicID, edited.Head, 3)
	if got, err = repo.GetRelic(ctx, config, relicID); err != nil {
		t.Fatalf("GetRelic() after the field edit error = %v", err)
	}
	assertSameRelicSnapshot(t, got, edited)
}

// A part that keeps the body its parent stores has to keep the size the parent
// records. A body edit that names itself as its base is refused outright, and
// the guard in planRelicWrite is reached here with a forged record: a move
// whose state records another size for the part. That is a commit no read
// accepts, so the writer refuses to store it. The next test reaches the same
// guard through WriteRelic with a pack the fold accepts.
func TestWriteRelicRefusesABodySizeNoSavedBodyBacks(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withBody("md", []byte("the first text\n"))
	root := writeRelicRoot(t, repo, config, part)
	relicID := root.Operation.RelicID
	current := []byte("the second text, saved from the first\n")
	saved := relicTestID(700001)
	edited := writeRelicChild(t, repo, config, root,
		map[string]core.RelicPartContent{part.id: {Body: current, HasBody: true}},
		relicBodySet(700001, part.id, part.id, current))
	longer := append(bytes.ToUpper(current), "and more\n"...)

	// The edit repeated with the version it saved as its base names itself.
	selfBased := relicChildPack(edited, relicBodySet(700001, part.id, saved, longer))
	_, err := repo.WriteRelic(ctx, config, &edited, selfBased, edited.Record,
		map[string]core.RelicPartContent{part.id: {Body: longer, HasBody: true}}, "update relic")
	assertRelicCategory(t, err, core.CategoryCorruptData, saved)

	pack := relicChildPack(edited, relicPartMove(700002, part.id, "2/1"))
	record := foldRelic(t, &edited, pack)
	for index := range record.State.Relic.Parts {
		if record.State.Relic.Parts[index].ID == part.id {
			body := *record.State.Relic.Parts[index].Body
			body.Bytes = int64(len(longer))
			record.State.Relic.Parts[index].Body = &body
		}
	}
	_, err = planRelicWrite(&edited, pack, record,
		map[string]core.RelicPartContent{part.id: {Body: longer, HasBody: true}})
	assertRelicCategory(t, err, core.CategoryValidation, part.id, fmt.Sprintf("%d bytes", len(longer)))
	if bytes.Contains([]byte(err.Error()), bytes.ToUpper(current[:10])) {
		t.Fatalf("the refusal %q carries body text", err)
	}

	// Nothing landed, and the relic still reads as it did.
	got, err := repo.GetRelic(ctx, config, relicID)
	if err != nil {
		t.Fatalf("GetRelic() error = %v", err)
	}
	assertSameRelicSnapshot(t, got, edited)
}

// A pack whose last part.body.set takes as its ID the body version the parent
// records folds back to that version with another size. The fold cannot see
// the ID is reused, so planRelicWrite is what keeps WriteRelic from storing
// a body under a version that already named other text.
func TestWriteRelicRefusesAPackThatEndsOnTheParentsBodyVersion(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withBody("md", []byte("the first text\n"))
	root := writeRelicRoot(t, repo, config, part)
	relicID := root.Operation.RelicID
	second := []byte("the second text\n")
	edited := writeRelicChild(t, repo, config, root,
		map[string]core.RelicPartContent{part.id: {Body: second, HasBody: true}},
		relicBodySet(700001, part.id, part.id, second))
	last := []byte("a third text, longer than the second\n")
	pack := relicChildPack(edited,
		relicBodySet(700002, part.id, relicTestID(700001), []byte("between\n")),
		relicBodySet(700001, part.id, relicTestID(700002), last))
	record := foldRelic(t, &edited, pack)

	_, err := repo.WriteRelic(ctx, config, &edited, pack, record,
		map[string]core.RelicPartContent{part.id: {Body: last, HasBody: true}}, "update relic")
	assertRelicCategory(t, err, core.CategoryValidation, part.id, fmt.Sprintf("%d bytes", len(last)), "without saving one")

	got, err := repo.GetRelic(ctx, config, relicID)
	if err != nil {
		t.Fatalf("GetRelic() error = %v", err)
	}
	assertSameRelicSnapshot(t, got, edited)
}

func TestWriteRelicRefusesOversizedContent(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	partID := relicTestID(1)

	// create builds a one-part create whose operation records recordedBytes
	// for the body, and writes it with the given content.
	create := func(relicNumber int, recordedBytes int64, content core.RelicPartContent) (core.RelicSnapshot, string, error) {
		pack, _ := relicRootPack(config, relicNumber, relicTestPartNumbered(1).withBody("md", nil))
		pack.Operations[1].Body.Bytes = recordedBytes
		snapshot, err := repo.WriteRelic(ctx, config, nil, pack, foldRelic(t, nil, pack),
			map[string]core.RelicPartContent{partID: content}, "create relic")
		return snapshot, pack.RelicID, err
	}

	for name, test := range map[string]struct {
		recordedBytes int64
		content       core.RelicPartContent
		fragments     []string
	}{
		"a body over the limit": {
			recordedBytes: core.MaxRelicBodyBytes,
			content:       core.RelicPartContent{Body: bytes.Repeat([]byte("x"), core.MaxRelicBodyBytes+1), HasBody: true},
			fragments:     []string{partID, "1048577", "1048576"},
		},
		"a body of another length than recorded": {
			recordedBytes: 10,
			content:       core.RelicPartContent{Body: []byte("eleven byte"), HasBody: true},
			fragments:     []string{partID, "11 bytes", "10 bytes"},
		},
		"no body where one is recorded": {
			recordedBytes: 10,
			content:       core.RelicPartContent{},
			fragments:     []string{partID},
		},
		"one field value over the limit for one value": {
			recordedBytes: 4,
			content:       core.RelicPartContent{Fields: relicFieldValueOfSize(core.MaxRelicFieldValueBytes + 1), Body: []byte("body"), HasBody: true},
			fragments:     []string{partID, "16385", "16384"},
		},
		"fields over the ceiling for one stored object": {
			recordedBytes: 4,
			content:       core.RelicPartContent{Fields: relicFieldsOfSize(t, MaxObjectBytes+1), Body: []byte("body"), HasBody: true},
			fragments:     []string{partID, "4194305", "4194304"},
		},
	} {
		_, relicID, err := create(1, test.recordedBytes, test.content)
		if core.CategoryOf(err) != core.CategoryValidation {
			t.Fatalf("%s: category = %q, want %q; error = %v", name, core.CategoryOf(err), core.CategoryValidation, err)
		}
		for _, fragment := range test.fragments {
			if !strings.Contains(err.Error(), fragment) {
				t.Fatalf("%s: error %q does not mention %q", name, err, fragment)
			}
		}
		if len(err.Error()) > 500 {
			t.Fatalf("%s: the %d-byte error echoes the content back", name, len(err.Error()))
		}
		if refExists(t, repo, relicRef(relicID)) {
			t.Fatalf("%s: the refused write created the relic ref", name)
		}
		if leftovers := relicWriteLeftovers(t, repo); len(leftovers) != 0 {
			t.Fatalf("%s: the refused write left %q behind", name, leftovers)
		}
	}

	// Exactly at each limit is stored and read back: a body of 1 MiB, one
	// field value of 16 KiB, and a fields.json at the ceiling for one object.
	// How large a part's fields are together is a total the service checks
	// when a change makes it grow; the store holds whatever it is handed that
	// a reader will read.
	body := bytes.Repeat([]byte("y"), core.MaxRelicBodyBytes)
	for number, fields := range []map[string]json.RawMessage{
		relicFieldValueOfSize(core.MaxRelicFieldValueBytes),
		relicFieldsOfSize(t, MaxObjectBytes),
	} {
		snapshot, _, err := create(2+number, core.MaxRelicBodyBytes, core.RelicPartContent{Fields: fields, Body: body, HasBody: true})
		if err != nil {
			t.Fatalf("WriteRelic() at the limits error = %v", err)
		}
		parts, err := repo.ReadRelicParts(ctx, config, snapshot, []string{partID}, true)
		if err != nil {
			t.Fatalf("ReadRelicParts() at the limits error = %v", err)
		}
		if got := parts[partID]; !got.HasBody || !bytes.Equal(got.Body, body) || !reflect.DeepEqual(got.Fields, fields) {
			t.Fatalf("content at the limits did not read back: %d body bytes, %d fields", len(got.Body), len(got.Fields))
		}
		assertRelicHistoryValid(t, repo, config, snapshot.Record.State.RelicID, snapshot.Head, 1)
	}
}

// Content for a part the change does not name could only be dropped, so the
// write is refused instead.
func TestWriteRelicRefusesContentForAPartItDoesNotName(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	partA, partB := relicTestPartNumbered(1), relicTestPartNumbered(2)
	root := writeRelicRoot(t, repo, config, partA, partB)

	pack := relicChildPack(root, core.RelicOperation{
		ID: relicTestID(700001), Type: core.RelicOperationPartFieldSet, Part: partA.id,
		Name: "heading", Value: json.RawMessage(`"Goals"`),
	})
	fields := map[string]json.RawMessage{"heading": json.RawMessage(`"Goals"`)}
	_, err := repo.WriteRelic(ctx, config, &root, pack, foldRelic(t, &root, pack),
		map[string]core.RelicPartContent{partA.id: {Fields: fields}, partB.id: {Fields: fields}}, "update relic")
	assertRelicCategory(t, err, core.CategoryValidation, partB.id)
	if got := refValue(t, repo, relicRef(pack.RelicID)); got != root.Head {
		t.Fatalf("relic ref = %q after a refused write, want %q", got, root.Head)
	}

	// An operation naming a part the relic does not have folds to no change,
	// and the tree gains no subtree for it.
	missing := relicTestID(99)
	ghost := writeRelicChild(t, repo, config, root, nil, core.RelicOperation{
		ID: relicTestID(700002), Type: core.RelicOperationPartRemove, Part: missing,
	})
	if before, after := gitOutput(t, repo, "rev-parse", root.Head+":parts"),
		gitOutput(t, repo, "rev-parse", ghost.Head+":parts"); before != after {
		t.Fatalf("an operation on a missing part moved the parts tree from %s to %s", before, after)
	}
}

// Git's own content filters never touch a body: it is stored as given.
func TestWriteRelicStoresBodyBytesExactly(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		gitOutput(t, repo, "config", "core.autocrlf", "true")
		bodies := [][]byte{
			[]byte("windows\r\nline endings\r\n"),
			[]byte("unix\nline endings\n"),
			{},
			{0xff, 0xfe, 0x00, 0x80, '\r', '\n'},
		}
		parts := make([]relicTestPart, len(bodies))
		ids := make([]string, len(bodies))
		for index, body := range bodies {
			parts[index] = relicTestPartNumbered(index+1).withBody("", body)
			ids[index] = parts[index].id
		}
		root := writeRelicRoot(t, repo, config, parts...)

		read, err := repo.ReadRelicParts(context.Background(), config, root, ids, true)
		if err != nil {
			t.Fatalf("ReadRelicParts() error = %v", err)
		}
		for index, body := range bodies {
			if got := relicBlob(t, repo, root.Head+":parts/"+ids[index]+"/body"); !bytes.Equal(got, body) {
				t.Fatalf("body %d stored as %q, want %q", index, got, body)
			}
			if got := read[ids[index]]; !got.HasBody || !bytes.Equal(got.Body, body) {
				t.Fatalf("body %d read as %q (has body %t), want %q", index, got.Body, got.HasBody, body)
			}
		}

		// Without bodies only the fields are read.
		fieldsOnly, err := repo.ReadRelicParts(context.Background(), config, root, ids[:1], false)
		if err != nil {
			t.Fatalf("ReadRelicParts(no bodies) error = %v", err)
		}
		if want := map[string]core.RelicPartContent{ids[0]: {Fields: map[string]json.RawMessage{}}}; !reflect.DeepEqual(fieldsOnly, want) {
			t.Fatalf("ReadRelicParts(no bodies) = %#v, want %#v", fieldsOnly, want)
		}

		_, err = repo.ReadRelicParts(context.Background(), config, root, []string{relicTestID(99)}, true)
		assertRelicCategory(t, err, core.CategoryNotFound, relicTestID(99))
	})
}

func TestWriteRelicRefusesAnObjectOverTheCeiling(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)

	// Each part costs more in state.json, which also records the body's
	// version, than in operation.json, so the state passes the ceiling first.
	const partCount = 8500
	partType := strings.Repeat("t", core.MaxRelicPartTypeBytes)
	position := "1" + strings.Repeat("0", core.MaxRelicPartPositionBytes-3) + "/1"
	format := strings.Repeat("f", core.MaxRelicBodyFormatBytes)
	operations := []core.RelicOperation{{
		ID:    relicTestID(800001),
		Type:  core.RelicOperationCreate,
		Relic: &core.RelicCreateData{Title: "A relic too large to store", Type: "spec", CreatedAt: relicTestTime},
	}}
	content := make(map[string]core.RelicPartContent, partCount)
	for index := range partCount {
		id := relicTestID(index + 1)
		operations = append(operations, core.RelicOperation{
			ID: id, Type: core.RelicOperationPartAdd, PartType: partType, Position: position,
			Body: &core.RelicBodyChange{Format: format},
		})
		content[id] = core.RelicPartContent{HasBody: true}
	}
	pack := core.NewRelicOperationPack(config.ProjectID, relicTestRelicID(1), relicTestID(900001),
		relicTestActor, 1, relicTestTime, operations)
	record := foldRelic(t, nil, pack)
	if size := len(encodedRelicDocument(t, pack)); size > MaxObjectBytes {
		t.Fatalf("operation.json is %d bytes; the test needs it under the ceiling", size)
	}
	stateSize := len(encodedRelicDocument(t, record.State))
	if stateSize <= MaxObjectBytes {
		t.Fatalf("state.json is %d bytes; the test needs it over the ceiling", stateSize)
	}

	_, err := repo.WriteRelic(context.Background(), config, nil, pack, record, content, "create relic")
	assertRelicCategory(t, err, core.CategoryValidation, "4194304", fmt.Sprint(stateSize), "state")
	if len(err.Error()) > 500 {
		t.Fatalf("the %d-byte error echoes the document back", len(err.Error()))
	}
	if refExists(t, repo, relicRef(pack.RelicID)) {
		t.Fatal("the refused write created the relic ref")
	}
}

func TestReadRelicPartsRefusesOversizedBlobs(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	body := []byte(strings.Repeat("b", 1840))
	part := relicTestPartNumbered(1).withField("heading", "Goals").withBody("md", body)
	root := writeRelicRoot(t, repo, config, part)
	fieldsBlob := gitOutput(t, repo, "rev-parse", root.Head+":parts/"+part.id+"/fields.json")
	bodyBlob := gitOutput(t, repo, "rev-parse", root.Head+":parts/"+part.id+"/body")

	oversizedFields, err := json.Marshal(core.NewRelicFields(relicFieldValueOfSize(core.MaxRelicFieldValueBytes + 1)))
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		fieldsBlob, bodyBlob string
		fragments            []string
	}{
		"one field value one byte over the limit for one value": {
			fieldsBlob: forgeRelicBlob(t, repo, append(oversizedFields, '\n')),
			bodyBlob:   bodyBlob,
			fragments:  []string{part.id, "16385", "16384"},
		},
		"a body one byte over the limit": {
			fieldsBlob: fieldsBlob,
			bodyBlob:   forgeRelicBlob(t, repo, bytes.Repeat([]byte("b"), core.MaxRelicBodyBytes+1)),
			fragments:  []string{part.id, "1048577", "1048576"},
		},
		"a body over the object ceiling": {
			fieldsBlob: fieldsBlob,
			bodyBlob:   forgeRelicBlob(t, repo, bytes.Repeat([]byte("b"), MaxObjectBytes+1)),
			fragments:  []string{part.id, "4194305", "1048576"},
		},
		"fields over the object ceiling": {
			fieldsBlob: forgeRelicBlob(t, repo, bytes.Repeat([]byte("f"), MaxObjectBytes+1)),
			bodyBlob:   bodyBlob,
			fragments:  []string{part.id, "4194305", "4194304"},
		},
		"a body of another size than the state records": {
			fieldsBlob: fieldsBlob,
			bodyBlob:   forgeRelicBlob(t, repo, body[:1839]),
			fragments:  []string{part.id, "1839", "1840"},
		},
		"no body where the state records one": {
			fieldsBlob: fieldsBlob,
			fragments:  []string{part.id},
		},
		"fields that are not the canonical document": {
			fieldsBlob: forgeRelicBlob(t, repo, []byte(`{"format":"workbook.relic-part-fields","version":1,"fields":{ "heading":"Goals"}}`+"\n")),
			bodyBlob:   bodyBlob,
			fragments:  []string{part.id},
		},
	} {
		forged := core.RelicSnapshot{
			Head:      forgeRelicPart(t, repo, root, part.id, test.fieldsBlob, test.bodyBlob),
			Operation: root.Operation,
			Record:    root.Record,
		}
		_, err := repo.ReadRelicParts(ctx, config, forged, []string{part.id}, true)
		if core.CategoryOf(err) != core.CategoryCorruptData {
			t.Fatalf("%s: category = %q, want %q; error = %v", name, core.CategoryOf(err), core.CategoryCorruptData, err)
		}
		for _, fragment := range test.fragments {
			if !strings.Contains(err.Error(), fragment) {
				t.Fatalf("%s: error %q does not mention %q", name, err, fragment)
			}
		}
	}

	// The same forged body is never read when bodies are not asked for.
	forged := core.RelicSnapshot{
		Head:      forgeRelicPart(t, repo, root, part.id, fieldsBlob, forgeRelicBlob(t, repo, bytes.Repeat([]byte("b"), core.MaxRelicBodyBytes+1))),
		Operation: root.Operation,
		Record:    root.Record,
	}
	if _, err := repo.ReadRelicParts(ctx, config, forged, []string{part.id}, false); err != nil {
		t.Fatalf("ReadRelicParts(no bodies) error = %v", err)
	}
}

func TestReadRelicTipsRefusesAMalformedTree(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withBody("md", []byte("a body\n"))
	root := writeRelicRoot(t, repo, config, part)
	child := writeRelicChild(t, repo, config, root, nil, relicTitleSet(700001, "A new title"))
	other := writeRelicRootNumbered(t, repo, config, 2, relicTestPartNumbered(2))
	bare := writeRelicRootNumbered(t, repo, config, 3)
	relicID := root.Operation.RelicID

	rootEntries := relicLines(gitOutput(t, repo, "ls-tree", root.Head))
	childEntries := relicLines(gitOutput(t, repo, "ls-tree", child.Head))
	bareEntries := relicLines(gitOutput(t, repo, "ls-tree", bare.Head))
	blobOf := func(name string) string { return gitOutput(t, repo, "rev-parse", name) }
	entry := func(mode, kind, objectID, name string) string {
		return mode + " " + kind + " " + objectID + "\t" + name
	}
	commit := func(entries []string, parents ...string) string {
		return forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, entries), parents...)
	}
	loose := bytes.Replace(relicBlob(t, repo, root.Head+":operation.json"), []byte(`{"format"`), []byte(`{ "format"`), 1)

	for name, test := range map[string]struct {
		relicID string
		head    string
	}{
		"an extra top-level entry": {
			head: commit(append(append([]string{}, rootEntries...), entry("100644", "blob", blobOf(root.Head+":links.json"), "notes.txt"))),
		},
		"no links.json": {
			head: commit(replaceRelicTreeEntry(t, rootEntries, "links.json", "")),
		},
		"a parts blob": {
			head: commit(replaceRelicTreeEntry(t, rootEntries, "parts", entry("100644", "blob", blobOf(root.Head+":links.json"), "parts"))),
		},
		"no parts tree though the state lists a part": {
			head: commit(replaceRelicTreeEntry(t, rootEntries, "parts", "")),
		},
		"a parts tree though the state lists no part": {
			relicID: bare.Operation.RelicID,
			head:    commit(append(append([]string{}, bareEntries...), entry("040000", "tree", blobOf(root.Head+":parts"), "parts"))),
		},
		"an executable links.json": {
			head: commit(replaceRelicTreeEntry(t, rootEntries, "links.json", entry("100755", "blob", blobOf(root.Head+":links.json"), "links.json"))),
		},
		"links.json that is not the links document": {
			head: commit(replaceRelicTreeEntry(t, rootEntries, "links.json", entry("100644", "blob", blobOf(root.Head+":state.json"), "links.json"))),
		},
		"two parents": {
			head: commit(childEntries, root.Head, other.Head),
		},
		"a create with a parent": {
			head: commit(rootEntries, other.Head),
		},
		"an update with no parent": {
			head: commit(childEntries),
		},
		"a state.json naming another relic": {
			head: commit(replaceRelicTreeEntry(t, rootEntries, "state.json", entry("100644", "blob", blobOf(other.Head+":state.json"), "state.json"))),
		},
		"another relic's whole commit": {
			head: other.Head,
		},
		"an operation.json that is not canonical": {
			head: commit(replaceRelicTreeEntry(t, rootEntries, "operation.json", entry("100644", "blob", forgeRelicBlob(t, repo, loose), "operation.json"))),
		},
		"a tree instead of a commit": {
			head: blobOf(root.Head + "^{tree}"),
		},
		"an object Git does not have": {
			head: strings.Repeat("0", len(root.Head)-1) + "1",
		},
	} {
		head := core.RelicHead{RelicID: relicID, ObjectID: test.head}
		if test.relicID != "" {
			head.RelicID = test.relicID
		}
		// A valid head beside it shares the batch and must not hide the refusal.
		_, err := repo.ReadRelicTips(ctx, config, []core.RelicHead{{RelicID: other.Operation.RelicID, ObjectID: other.Head}, head})
		if core.CategoryOf(err) != core.CategoryCorruptData {
			t.Fatalf("%s: category = %q, want %q; error = %v", name, core.CategoryOf(err), core.CategoryCorruptData, err)
		}
	}

	// The commits Workbook wrote read back, in the order asked for.
	snapshots, err := repo.ReadRelicTips(ctx, config, []core.RelicHead{
		{RelicID: bare.Operation.RelicID, ObjectID: bare.Head},
		{RelicID: relicID, ObjectID: child.Head},
		{RelicID: relicID, ObjectID: root.Head},
	})
	if err != nil {
		t.Fatalf("ReadRelicTips() error = %v", err)
	}
	for index, want := range []core.RelicSnapshot{bare, child, root} {
		assertSameRelicSnapshot(t, snapshots[index], want)
	}
	if empty, err := repo.ReadRelicTips(ctx, config, nil); err != nil || len(empty) != 0 {
		t.Fatalf("ReadRelicTips(none) = %#v, %v", empty, err)
	}
}

// A commit written under rules this build does not have is served, not called
// corrupt: its tree is not judged, and nothing can be written on top of it.
func TestReadRelicTipsServesANewerGenerationWithoutJudgingItsTree(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1).withBody("md", []byte("a body\n"))
	root := writeRelicRoot(t, repo, config, part)
	ref := relicRef(root.Operation.RelicID)

	newer := func(name string) string {
		contents := relicBlob(t, repo, root.Head+":"+name)
		raised := bytes.Replace(contents, []byte(`"minReader":6`), []byte(`"minReader":10`), 1)
		if bytes.Equal(raised, contents) {
			t.Fatalf("%s carries no generation marker to raise", name)
		}
		return forgeRelicBlob(t, repo, raised)
	}
	entries := relicLines(gitOutput(t, repo, "ls-tree", root.Head))
	entries = replaceRelicTreeEntry(t, entries, "operation.json", "100644 blob "+newer("operation.json")+"\toperation.json")
	entries = replaceRelicTreeEntry(t, entries, "state.json", "100644 blob "+newer("state.json")+"\tstate.json")
	entries = append(entries, "100644 blob "+forgeRelicBlob(t, repo, []byte("something new\n"))+"\tindex.json")
	forged := forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, entries))
	gitOutput(t, repo, "update-ref", ref, forged, root.Head)

	tip := readRelicTip(t, repo, config, root.Operation.RelicID)
	if tip.Head != forged || !tip.Operation.RequiresNewerReader() || !tip.Record.State.RequiresNewerReader() {
		t.Fatalf("tip = %s (pack generation %d, state generation %d), want the forged newer commit %s",
			tip.Head, tip.Operation.MinReader, tip.Record.State.MinReader, forged)
	}
	if tip.Record.State.Relic.Title != root.Record.State.Relic.Title {
		t.Fatalf("newer tip title = %q, want %q", tip.Record.State.Relic.Title, root.Record.State.Relic.Title)
	}
	parts, err := repo.ReadRelicParts(ctx, config, tip, []string{part.id}, true)
	if err != nil || string(parts[part.id].Body) != "a body\n" {
		t.Fatalf("ReadRelicParts(newer tip) = %#v, %v", parts, err)
	}

	// A merge is still not a relic commit at any generation.
	merge := forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, entries), root.Head, forged)
	_, err = repo.ReadRelicTips(ctx, config, []core.RelicHead{{RelicID: root.Operation.RelicID, ObjectID: merge}})
	assertRelicCategory(t, err, core.CategoryCorruptData)

	pack := relicChildPack(root, relicTitleSet(700001, "A title from the older build"))
	_, err = repo.WriteRelic(ctx, config, &tip, pack, foldRelic(t, &root, pack), nil, "update relic")
	assertRelicCategory(t, err, core.CategoryNewerWriter)
	if got := refValue(t, repo, ref); got != forged {
		t.Fatalf("relic ref = %q after a refused write, want %q", got, forged)
	}
}

func TestListRelicHeadsIgnoresTaskRefsAndRefusesStrangers(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()

	if heads, err := repo.ListRelicHeads(ctx, config); err != nil || len(heads) != 0 {
		t.Fatalf("ListRelicHeads() with no relic = %#v, %v", heads, err)
	}

	const secondTaskID = "WB-01K0M6B8A4FTT8C39MXXYTW7D2"
	writeIndependentRoot(t, repo, config, writeTaskID, writeGenerationID, "01K0M6B8A4FTT8C39MXXYTW7C4", "First task")
	writeIndependentRoot(t, repo, config, secondTaskID, "01K0M6B8A4FTT8C39MXXYTW7D3", "01K0M6B8A4FTT8C39MXXYTW7D4", "Second task")
	// Written out of ID order.
	later := writeRelicRootNumbered(t, repo, config, 2, relicTestPartNumbered(1))
	earlier := writeRelicRootNumbered(t, repo, config, 1)

	heads, err := repo.ListRelicHeads(ctx, config)
	if err != nil {
		t.Fatalf("ListRelicHeads() error = %v", err)
	}
	want := []core.RelicHead{
		{RelicID: relicTestRelicID(1), ObjectID: earlier.Head},
		{RelicID: relicTestRelicID(2), ObjectID: later.Head},
	}
	if !reflect.DeepEqual(heads, want) {
		t.Fatalf("ListRelicHeads() = %#v, want %#v", heads, want)
	}
	taskHeads, err := repo.ListTaskHeads(ctx, config)
	if err != nil || len(taskHeads) != 2 {
		t.Fatalf("ListTaskHeads() beside relics = %#v, %v; want the two tasks", taskHeads, err)
	}

	for name, stranger := range map[string]struct {
		create []string
		remove []string
	}{
		"a task ID": {
			create: []string{"update-ref", relicRef(writeTaskID), earlier.Head},
			remove: []string{"update-ref", "-d", relicRef(writeTaskID)},
		},
		"another project's key": {
			create: []string{"update-ref", relicRef(core.NewRelicID("OTHER", relicTestID(1))), earlier.Head},
			remove: []string{"update-ref", "-d", relicRef(core.NewRelicID("OTHER", relicTestID(1)))},
		},
		"a nested name": {
			create: []string{"update-ref", relicRef(relicTestRelicID(3)) + "/draft", earlier.Head},
			remove: []string{"update-ref", "-d", relicRef(relicTestRelicID(3)) + "/draft"},
		},
		"a symbolic ref": {
			create: []string{"symbolic-ref", relicRef(relicTestRelicID(3)), relicRef(relicTestRelicID(1))},
			remove: []string{"symbolic-ref", "-d", relicRef(relicTestRelicID(3))},
		},
	} {
		gitOutput(t, repo, stranger.create...)
		_, err := repo.ListRelicHeads(ctx, config)
		if core.CategoryOf(err) != core.CategoryCorruptData {
			t.Fatalf("%s: category = %q, want %q; error = %v", name, core.CategoryOf(err), core.CategoryCorruptData, err)
		}
		gitOutput(t, repo, stranger.remove...)
	}
	if heads, err := repo.ListRelicHeads(ctx, config); err != nil || !reflect.DeepEqual(heads, want) {
		t.Fatalf("ListRelicHeads() after the strangers were removed = %#v, %v", heads, err)
	}
}

// A relic ref that became symbolic is never written through.
func TestWriteRelicRefusesASymbolicRef(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	root := writeRelicRoot(t, repo, config)
	ref := relicRef(root.Operation.RelicID)
	shadow := "refs/workbook/shadow"
	gitOutput(t, repo, "update-ref", shadow, root.Head)
	gitOutput(t, repo, "update-ref", "--no-deref", "-d", ref)
	gitOutput(t, repo, "symbolic-ref", ref, shadow)

	pack := relicChildPack(root, relicTitleSet(700001, "A new title"))
	_, err := repo.WriteRelic(context.Background(), config, &root, pack, foldRelic(t, &root, pack), nil, "update relic")
	assertRelicCategory(t, err, core.CategoryCorruptData, "symbolic")
	if got := refValue(t, repo, shadow); got != root.Head {
		t.Fatalf("the ref behind the symbolic relic ref moved to %q", got)
	}
	if got := gitOutput(t, repo, "symbolic-ref", ref); got != shadow {
		t.Fatalf("the symbolic relic ref now reads %q", got)
	}
}

// The parent a caller hands over must be the commit Git stores at that head:
// the new tree is built from that commit's parts.
func TestWriteRelicRefusesAParentThatIsNotTheStoredCommit(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	part := relicTestPartNumbered(1)
	root := writeRelicRoot(t, repo, config, part)
	child := writeRelicChild(t, repo, config, root, nil, relicTitleSet(700001, "A new title"))

	// The tip's head with the root's record: a fold onto a state the commit
	// does not hold.
	mismatched := core.RelicSnapshot{Head: child.Head, Operation: root.Operation, Record: root.Record}
	pack := relicChildPack(root, relicTitleSet(700002, "Another title"))
	_, err := repo.WriteRelic(ctx, config, &mismatched, pack, foldRelic(t, &root, pack), nil, "update relic")
	assertRelicCategory(t, err, core.CategoryValidation, child.Head)
	if got := refValue(t, repo, relicRef(pack.RelicID)); got != child.Head {
		t.Fatalf("relic ref = %q after a refused write, want %q", got, child.Head)
	}

	// A head that is not an object ID at all is refused as one, and an
	// abbreviated one is simply not what the ref holds.
	next := relicChildPack(child, relicTitleSet(700003, "Another title"))
	for head, want := range map[string]core.Category{
		strings.ToUpper(child.Head): core.CategoryValidation,
		"HEAD":                      core.CategoryValidation,
		child.Head[:12]:             core.CategoryStaleWrite,
	} {
		odd := core.RelicSnapshot{Head: head, Operation: child.Operation, Record: child.Record}
		_, err = repo.WriteRelic(ctx, config, &odd, next, foldRelic(t, &child, next), nil, "update relic")
		assertRelicCategory(t, err, want)
	}
	if got := refValue(t, repo, relicRef(pack.RelicID)); got != child.Head {
		t.Fatalf("relic ref = %q after refused writes, want %q", got, child.Head)
	}

	// A relic of another project's key is not this project's to write.
	foreign, content := relicRootPack(config, 5)
	foreign.RelicID = core.NewRelicID("OTHER", relicTestID(5))
	_, err = repo.WriteRelic(ctx, config, nil, foreign, foldRelic(t, nil, foreign), content, "create relic")
	assertRelicCategory(t, err, core.CategoryValidation)
	if refExists(t, repo, relicRef(foreign.RelicID)) {
		t.Fatal("a relic under another project's key was written")
	}
}

func relicMigrate(operationNumber int, relicType, schema string) core.RelicOperation {
	return core.RelicOperation{ID: relicTestID(operationNumber), Type: core.RelicOperationMigrate, RelicType: relicType, Schema: schema}
}

func TestReplayAppliesMigrateAndEditTogether(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		p := relicTestPartNumbered(1).withField("heading", "Goals")
		root := writeRelicRoot(t, repo, config, p)
		relicID := root.Record.State.RelicID
		remote := relicHistoryFrom(t, repo, config, root, relicOps(relicMigrate(301, "plan", relicTestID(350))))
		local := relicHistoryFrom(t, repo, config, root,
			relicOps(relicPropertySet(101, "status", "draft"), relicFieldSet(102, p.id, "heading", "Aims")))

		outcome := replayDivergedSnapshots(t, repo, config, local, remote)
		requireReplayed(t, outcome)
		if outcome.Conflict != nil || outcome.Replayed != 1 {
			t.Fatalf("outcome = %+v, want one pack replayed and no conflict", outcome)
		}
		next := relicCommitAt(t, repo, config, relicID, outcome.Next)
		relic := next.Record.State.Relic
		if relic.Type != "plan" || relic.Schema != relicTestID(350) || string(relic.Properties["status"]) != `"draft"` {
			t.Fatalf("replayed relic type %q, schema %q, properties %v; want both sides applied", relic.Type, relic.Schema, relic.Properties)
		}
		assertRelicFieldValue(t, relicFieldsAt(t, repo, outcome.Next, p.id), "heading", "Aims")
		assertReplayedHistory(t, repo, config, relicID, outcome.Next)
	})
}

func TestConcurrentMigrationsFoldLastSyncWins(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		root := writeRelicRoot(t, repo, config, relicTestPartNumbered(1))
		relicID := root.Record.State.RelicID
		first := relicHistoryFrom(t, repo, config, root, relicOps(relicMigrate(101, "spec", relicTestID(350))))
		second := relicHistoryFrom(t, repo, config, root, relicOps(relicMigrate(201, "plan", relicTestID(351))))

		// The clone that syncs second replays its migration onto the other's
		// and ends on its own.
		for label, test := range map[string]struct {
			local, remote core.RelicSnapshot
			wantType      string
			wantSchema    string
		}{
			"second clone syncs last": {second, first, "plan", relicTestID(351)},
			"first clone syncs last":  {first, second, "spec", relicTestID(350)},
		} {
			outcome := replayDivergedSnapshots(t, repo, config, test.local, test.remote)
			requireReplayed(t, outcome)
			if outcome.Conflict != nil || outcome.Replayed != 1 {
				t.Fatalf("%s: outcome = %+v, want one pack replayed and no conflict", label, outcome)
			}
			relic := relicCommitAt(t, repo, config, relicID, outcome.Next).Record.State.Relic
			if relic.Type != test.wantType || relic.Schema != test.wantSchema {
				t.Fatalf("%s: replayed relic = %s %s, want %s %s", label, relic.Type, relic.Schema, test.wantType, test.wantSchema)
			}
			assertReplayedHistory(t, repo, config, relicID, outcome.Next)
			// The other clone's tip is the replayed tip's parent, so its next
			// sync moves it forward to the same relic.
			if parents := relicParents(t, repo, outcome.Next); len(parents) != 1 || parents[0] != test.remote.Head {
				t.Fatalf("%s: replayed tip's parents = %v, want %s", label, parents, test.remote.Head)
			}
		}
	})
}

// relicToCompact writes relic number relicNumber with two parts, part 1 with
// a body and a field and part 2 with a field, links from part 1 and from the
// relic, removes part 2 and sets a property, then retires the relic or, when
// deleted is set, deletes it. It returns the tip.
func relicToCompact(t *testing.T, repo *Repository, config core.ProjectConfig, relicNumber int, deleted bool) core.RelicSnapshot {
	t.Helper()
	base := 710000 + relicNumber*100
	tip := writeRelicRootNumbered(t, repo, config, relicNumber,
		relicTestPartNumbered(1).withBody("md", []byte("the decision we kept\n")).withField("status", "open"),
		relicTestPartNumbered(2).withField("status", "draft"))
	tip = writeRelicChild(t, repo, config, tip, nil,
		relicLinkAddOp(base+1, relicTestID(1), relicLinkTaskID(1), "implemented-by"),
		relicLinkAddOp(base+2, "", relicLinkTaskID(2), "relates-to"))
	tip = writeRelicChild(t, repo, config, tip, nil, relicPartChange(base+3, core.RelicOperationPartRemove, relicTestID(2)))
	tip = writeRelicChild(t, repo, config, tip, nil, relicPropertySet(base+4, "outcome", "superseded"))
	if deleted {
		return writeRelicChild(t, repo, config, tip, nil,
			core.RelicOperation{ID: relicTestID(base + 5), Type: core.RelicOperationTombstone})
	}
	return writeRelicChild(t, repo, config, tip, nil,
		core.RelicOperation{ID: relicTestID(base + 5), Type: core.RelicOperationRetire})
}

// relicSubtree returns the object ID of one part's subtree in a commit.
func relicSubtree(t *testing.T, repo *Repository, commit, partID string) string {
	t.Helper()
	return gitOutput(t, repo, "rev-parse", commit+":parts/"+partID)
}

func TestReadRelicTipAcceptsCheckpointRoot(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		emptyFields := forgeRelicBlob(t, repo, encodedRelicDocument(t, core.NewRelicFields(nil)))
		for number, boundary := range map[int]string{1: core.RelicBoundaryRetired, 2: core.RelicBoundaryDeleted} {
			tip := relicToCompact(t, repo, config, number, boundary == core.RelicBoundaryDeleted)
			checkpoint := writeRelicCheckpoint(t, repo, config, tip, boundary)
			relicID := tip.Record.State.RelicID

			read, err := repo.GetRelic(ctx, config, relicID)
			if err != nil {
				t.Fatalf("%s: GetRelic() error = %v", boundary, err)
			}
			assertSameRelicSnapshot(t, read, checkpoint)
			assertSameRelicSnapshot(t, readRelicTip(t, repo, config, relicID), checkpoint)
			if parents := relicParents(t, repo, checkpoint.Head); len(parents) != 0 {
				t.Fatalf("%s: the checkpoint has parents %q", boundary, parents)
			}

			// Each stub holds the empty fields.json alone; at retired the live
			// part keeps the subtree object it had.
			for _, part := range checkpoint.Record.State.Relic.Parts {
				if part.Dropped == nil {
					if got, want := relicSubtree(t, repo, checkpoint.Head, part.ID), relicSubtree(t, repo, tip.Head, part.ID); got != want {
						t.Errorf("%s: live part %s has subtree %s, want the tip's %s", boundary, part.ID, got, want)
					}
					continue
				}
				listing := gitOutput(t, repo, "ls-tree", checkpoint.Head+":parts/"+part.ID)
				if want := "100644 blob " + emptyFields + "\tfields.json"; listing != want {
					t.Errorf("%s: stub %s stores %q, want %q", boundary, part.ID, listing, want)
				}
			}
		}
	})
}

func TestCheckpointWithParentIsCorrupt(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := relicToCompact(t, repo, config, 1, false)
		checkpoint := writeRelicCheckpoint(t, repo, config, tip, core.RelicBoundaryRetired)
		relicID := tip.Record.State.RelicID

		// The checkpoint's own tree, on top of the tip it replaced.
		withParent := forgeRelicCommitOf(t, repo, gitOutput(t, repo, "rev-parse", checkpoint.Head+"^{tree}"), tip.Head)
		pointRelicRef(t, repo, relicID, withParent)
		_, err := repo.GetRelic(ctx, config, relicID)
		assertGetRelicRefused(t, err)
		assertRelicCategory(t, err, core.CategoryCorruptData, "a checkpoint commit must have no parent")
		_, err = repo.ReadRelicTips(ctx, config, []core.RelicHead{{RelicID: relicID, ObjectID: withParent}})
		assertRelicCategory(t, err, core.CategoryCorruptData, "a checkpoint commit must have no parent")
	})
}

func TestStubSubtreeMustBeEmptyFields(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := relicToCompact(t, repo, config, 1, false)
		checkpoint := writeRelicCheckpoint(t, repo, config, tip, core.RelicBoundaryRetired)
		relicID := tip.Record.State.RelicID
		stub := relicTestID(2)
		emptyFields := forgeRelicBlob(t, repo, encodedRelicDocument(t, core.NewRelicFields(nil)))
		for label, forged := range map[string]string{
			"a stub with a body": forgeRelicPart(t, repo, checkpoint, stub, emptyFields,
				forgeRelicBlob(t, repo, []byte("dropped text\n"))),
			"a stub with fields": forgeRelicPart(t, repo, checkpoint, stub,
				forgeRelicBlob(t, repo, forgedFields(t, "status", "draft")), ""),
			"a stub with fields not written canonically": forgeRelicPart(t, repo, checkpoint, stub,
				forgeRelicBlob(t, repo, []byte(`{"format":"workbook.relic-part-fields","version":1, "fields":{}}`+"\n")), ""),
		} {
			pointRelicRef(t, repo, relicID, forged)
			_, err := repo.GetRelic(ctx, config, relicID)
			if core.CategoryOf(err) != core.CategoryCorruptData {
				t.Errorf("%s: GetRelic() error = %v, want corrupt data", label, err)
			}
			_, failure, err := repo.ValidateRelicHistory(ctx, config, core.RelicHead{RelicID: relicID, ObjectID: forged})
			if err != nil || failure == nil || failure.Commit != forged || core.CategoryOf(failure.Err) != core.CategoryCorruptData {
				t.Errorf("%s: ValidateRelicHistory() = %#v, %v; want it to name the forged root", label, failure, err)
			}
		}

		// A live part of the root whose fields.json no writer stores is refused
		// by the deep check.
		live := relicTestID(1)
		forged := forgeRelicPart(t, repo, checkpoint, live,
			forgeRelicBlob(t, repo, []byte(`{"format":"workbook.relic-part-fields","version":1, "fields":{}}`+"\n")),
			gitOutput(t, repo, "rev-parse", checkpoint.Head+":parts/"+live+"/body"))
		assertRelicHistoryStopsAt(t, repo, config, relicID, forged, forged, 0, core.CategoryCorruptData, live)

		// The root rules of core apply: a removed part that is not a stub.
		state := checkpoint.Record.State
		state.Relic.Parts = slices.Clone(state.Relic.Parts)
		state.Relic.Parts[1].Dropped = nil
		notStub := forgeRelicCommit(t, repo, "", encodedRelicDocument(t, checkpoint.Operation),
			encodedRelicDocument(t, state), forgedPartsOf(t, repo, checkpoint.Head))
		pointRelicRef(t, repo, relicID, notStub)
		_, err := repo.GetRelic(ctx, config, relicID)
		assertRelicCategory(t, err, core.CategoryCorruptData)
	})
}

func TestStubFieldsNeverChange(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		tip := relicToCompact(t, repo, config, 1, false)
		checkpoint := writeRelicCheckpoint(t, repo, config, tip, core.RelicBoundaryRetired)
		relicID := tip.Record.State.RelicID
		stub := relicTestID(2)
		stubTree := relicSubtree(t, repo, checkpoint.Head, stub)

		// A field set and a body save naming the stub, with content given for
		// it: the fold ignores both, and the stub's subtree stays the empty
		// fields.json.
		edited := writeRelicChild(t, repo, config, checkpoint,
			map[string]core.RelicPartContent{stub: {
				Fields:  map[string]json.RawMessage{"status": json.RawMessage(`"revived"`)},
				Body:    []byte("text for a dropped part\n"),
				HasBody: true,
			}},
			relicFieldSet(720001, stub, "status", "revived"),
			relicBodySet(720002, stub, stub, []byte("text for a dropped part\n")))
		if got := relicSubtree(t, repo, edited.Head, stub); got != stubTree {
			t.Fatalf("a field set and a body save naming a stub changed its subtree from %s to %s", stubTree, got)
		}
		if _, err := repo.GetRelic(ctx, config, relicID); err != nil {
			t.Fatalf("GetRelic() after edits naming a stub error = %v", err)
		}
		assertRelicHistoryValid(t, repo, config, relicID, edited.Head, 2)

		// A forged commit that does change the stub's fields is corrupt, on a
		// read and in validation.
		pack := relicChildPack(checkpoint, relicFieldSet(720003, stub, "status", "revived"))
		forgedParts := withForgedPart(forgedPartsOf(t, repo, checkpoint.Head), stub,
			forgedPart{fields: forgedFields(t, "status", "revived")})
		forged := forgeRelicCommit(t, repo, checkpoint.Head, encodedRelicDocument(t, pack),
			encodedRelicDocument(t, foldRelic(t, &checkpoint, pack).State), forgedParts)
		// forgeRelicCommit writes no links; the relic has some.
		forged = forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, replaceRelicTreeEntry(t,
			relicLines(gitOutput(t, repo, "ls-tree", forged)), "links.json",
			"100644 blob "+gitOutput(t, repo, "rev-parse", checkpoint.Head+":links.json")+"\tlinks.json")), checkpoint.Head)
		pointRelicRef(t, repo, relicID, forged)
		_, err := repo.GetRelic(ctx, config, relicID)
		assertGetRelicRefused(t, err)
		assertRelicCategory(t, err, core.CategoryCorruptData, stub)
		assertRelicHistoryStopsAt(t, repo, config, relicID, forged, forged, 1, core.CategoryCorruptData, stub)
	})
}

func TestWriteRelicRefusesACheckpointPack(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	tip := relicToCompact(t, repo, config, 1, false)
	checkpoint := writeRelicCheckpoint(t, repo, config, tip, core.RelicBoundaryRetired)
	pointRelicRef(t, repo, tip.Record.State.RelicID, tip.Head)
	_, err := repo.WriteRelic(context.Background(), config, nil, checkpoint.Operation, checkpoint.Record, nil, "compact")
	assertRelicCategory(t, err, core.CategoryValidation, "a relic checkpoint is written by the compaction")
	if head := gitOutput(t, repo, "rev-parse", relicRef(tip.Record.State.RelicID)); head != tip.Head {
		t.Fatalf("the refused checkpoint moved the ref to %s", head)
	}
}

// relicMerge is a relic whose one part was saved from its first version on
// two sides: the first save stands as saved, and pack holds the second, stale,
// save followed by its part.body.merge onto the first. content is what a
// caller hands WriteRelic for pack: the merged body, and the second save's
// text as the merge input.
type relicMerge struct {
	part    string
	saved   core.RelicSnapshot
	pack    core.RelicOperationPack
	record  core.RelicRecord
	content map[string]core.RelicPartContent
	// mergeID and otherID name the merge and the stale save it merges in.
	mergeID, otherID string
	merged, other    []byte
}

// relicMergeFor builds a relicMerge on a new relic numbered relicNumber.
func relicMergeFor(t *testing.T, repo *Repository, config core.ProjectConfig, relicNumber int) relicMerge {
	t.Helper()
	base := 730000 + relicNumber*100
	part := relicTestPartNumbered(base).withBody("md", []byte("one\ntwo\nthree\nfour\n"))
	root := writeRelicRootNumbered(t, repo, config, relicNumber, part)
	ours := []byte("ONE\ntwo\nthree\nfour\n")
	saved := writeRelicChild(t, repo, config, root,
		map[string]core.RelicPartContent{part.id: {Body: ours, HasBody: true}},
		relicBodySet(base+1, part.id, part.id, ours))
	other := []byte("one\ntwo\nthree\nFOUR, said the other side\n")
	merged := []byte("ONE\ntwo\nthree\nFOUR, said the other side\n")
	save := relicBodySet(base+2, part.id, part.id, other)
	merge := relicMergeOperation(relicChildPack(saved), part.id, save.ID, relicTestID(base+1), part.id, merged)
	pack := relicChildPack(saved, save, merge)
	return relicMerge{
		part:    part.id,
		saved:   saved,
		pack:    pack,
		record:  foldRelic(t, &saved, pack),
		content: map[string]core.RelicPartContent{part.id: {Body: merged, HasBody: true, MergeInput: other}},
		mergeID: merge.ID,
		otherID: save.ID,
		merged:  merged,
		other:   other,
	}
}

// relicMergeOperation is the part.body.merge that merges the save other onto
// version base of partID in a pack shaped like shape, with the ID a replay
// derives for it.
func relicMergeOperation(shape core.RelicOperationPack, partID, other, base, ancestor string, merged []byte) core.RelicOperation {
	return core.RelicOperation{
		ID:   core.RelicMergeOperationID(shape.RelicID, partID, other, base, core.RelicLineMergeMethod, shape.WallTime),
		Type: core.RelicOperationPartBodyMerge,
		Part: partID,
		Body: &core.RelicBodyChange{Format: "md", Bytes: int64(len(merged)), Base: base},
		Merge: &core.RelicBodyMerge{
			Ancestor: ancestor, Other: other, Method: core.RelicLineMergeMethod,
		},
	}
}

// write stores the merge through WriteRelic.
func (merge relicMerge) write(t *testing.T, repo *Repository, config core.ProjectConfig) core.RelicSnapshot {
	t.Helper()
	written, err := repo.WriteRelic(context.Background(), config, &merge.saved, merge.pack, merge.record, merge.content,
		"merge relic "+merge.pack.RelicID)
	if err != nil {
		t.Fatalf("WriteRelic(merge) error = %v", err)
	}
	return written
}

func TestWriteRelicStoresTheMergeInput(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		merge := relicMergeFor(t, repo, config, 1)
		relicID := merge.pack.RelicID
		if merge.pack.MinReader != core.RelicMergeMinReader {
			t.Fatalf("the merge pack declares %d, want %d", merge.pack.MinReader, core.RelicMergeMinReader)
		}
		written := merge.write(t, repo, config)

		otherBlob := gitOutputWithInput(t, repo, merge.other, "hash-object", "--stdin")
		if got, want := gitOutput(t, repo, "ls-tree", written.Head+":merges"), "100644 blob "+otherBlob+"\t"+merge.mergeID; got != want {
			t.Fatalf("merges/ lists %q, want %q", got, want)
		}
		if got := relicBlob(t, repo, written.Head+":merges/"+merge.mergeID); !bytes.Equal(got, merge.other) {
			t.Fatalf("merges/%s holds %q, want the other side's text %q", merge.mergeID, got, merge.other)
		}
		if got := relicBlob(t, repo, written.Head+":parts/"+merge.part+"/body"); !bytes.Equal(got, merge.merged) {
			t.Fatalf("the body is %q, want the merged text %q", got, merge.merged)
		}
		names := relicLines(gitOutput(t, repo, "ls-tree", "--name-only", written.Head))
		if want := []string{"links.json", "merges", "operation.json", "parts", "state.json"}; !slices.Equal(names, want) {
			t.Fatalf("the merge commit's tree holds %q, want %q", names, want)
		}
		if stored, _ := written.Record.State.Relic.Part(merge.part); stored.Body.Version != merge.mergeID {
			t.Fatalf("the body version is %s, want the merge %s", stored.Body.Version, merge.mergeID)
		}
		read, err := repo.GetRelic(ctx, config, relicID)
		if err != nil {
			t.Fatalf("GetRelic() of the merge error = %v", err)
		}
		assertSameRelicSnapshot(t, read, written)
		assertSameRelicSnapshot(t, readRelicTip(t, repo, config, relicID), written)

		// The next commit keeps the parts and has no merges entry.
		next := writeRelicChild(t, repo, config, written, nil, relicTitleSet(739001, "After the merge"))
		if listing := gitOutput(t, repo, "ls-tree", "--name-only", next.Head); strings.Contains(listing, "merges") {
			t.Fatalf("the commit after the merge lists %q, want no merges entry", listing)
		}
		if got, want := gitOutput(t, repo, "rev-parse", next.Head+":parts"), gitOutput(t, repo, "rev-parse", written.Head+":parts"); got != want {
			t.Fatalf("the commit after the merge has parts tree %s, want the merge's %s", got, want)
		}
		if _, err := repo.GetRelic(ctx, config, relicID); err != nil {
			t.Fatalf("GetRelic() after the merge error = %v", err)
		}
		assertRelicHistoryValid(t, repo, config, relicID, next.Head, 4)
	})
}

func TestWriteRelicRefusesAMergeWithoutItsInput(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		merge := relicMergeFor(t, repo, config, 1)
		relicID := merge.pack.RelicID
		plain := relicChildPack(merge.saved, relicTitleSet(739101, "No merge here"))
		saveOnly := relicChildPack(merge.saved, relicBodySet(739102, merge.part, relicTestID(730101), merge.merged))
		for label, test := range map[string]struct {
			pack    core.RelicOperationPack
			content map[string]core.RelicPartContent
		}{
			"a merge with no input": {
				pack:    merge.pack,
				content: map[string]core.RelicPartContent{merge.part: {Body: merge.merged, HasBody: true}},
			},
			"a merge input of another size than the save it names": {
				pack: merge.pack,
				content: map[string]core.RelicPartContent{merge.part: {
					Body: merge.merged, HasBody: true, MergeInput: append(slices.Clone(merge.other), '!'),
				}},
			},
			"a merge input that is not UTF-8": {
				pack: merge.pack,
				content: map[string]core.RelicPartContent{merge.part: {
					Body: merge.merged, HasBody: true, MergeInput: append([]byte{0xff}, merge.other[1:]...),
				}},
			},
			"an input with no merge in the pack": {
				pack:    plain,
				content: map[string]core.RelicPartContent{merge.part: {MergeInput: merge.other}},
			},
			"an input beside a save that is no merge": {
				pack: saveOnly,
				content: map[string]core.RelicPartContent{merge.part: {
					Body: merge.merged, HasBody: true, MergeInput: merge.other,
				}},
			},
			"an input for a part the pack does not name": {
				pack: merge.pack,
				content: map[string]core.RelicPartContent{
					merge.part:          {Body: merge.merged, HasBody: true, MergeInput: merge.other},
					relicTestID(739199): {MergeInput: merge.other},
				},
			},
		} {
			record := foldRelic(t, &merge.saved, test.pack)
			_, err := repo.WriteRelic(ctx, config, &merge.saved, test.pack, record, test.content, "refused merge")
			if core.CategoryOf(err) != core.CategoryCorruptData {
				t.Errorf("%s: WriteRelic() error = %v, want corrupt data", label, err)
			}
			if head := gitOutput(t, repo, "rev-parse", relicRef(relicID)); head != merge.saved.Head {
				t.Fatalf("%s: the refused write moved the ref to %s", label, head)
			}
		}
		// Nothing was written: the merged text, which no earlier commit holds,
		// is not in the object store.
		mergedBlob := gitOutputWithInput(t, repo, merge.merged, "hash-object", "--stdin")
		if _, err := repo.Git(ctx, nil, "cat-file", "-e", mergedBlob); err == nil {
			t.Fatalf("a refused merge wrote its merged text %s", mergedBlob)
		}
		if leftovers := relicWriteLeftovers(t, repo); len(leftovers) != 0 {
			t.Fatalf("a refused merge left %q behind", leftovers)
		}
	})
}

// forgeRelicMergesEntry returns a commit that is commit's tree with its merges
// entry replaced by replacement, or dropped when replacement is empty, or added
// when the tree has none, on commit's own parents.
func forgeRelicMergesEntry(t *testing.T, repo *Repository, commit, replacement string) string {
	t.Helper()
	entries := relicLines(gitOutput(t, repo, "ls-tree", commit))
	if strings.Contains(strings.Join(entries, "\n"), "\tmerges") {
		entries = replaceRelicTreeEntry(t, entries, "merges", replacement)
	} else if replacement != "" {
		entries = append(entries, replacement)
	}
	return forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, entries), relicParents(t, repo, commit)...)
}

func TestRelicTreeWithMergesFollowsItsPack(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		merge := relicMergeFor(t, repo, config, 1)
		relicID := merge.pack.RelicID
		written := merge.write(t, repo, config)
		next := writeRelicChild(t, repo, config, written, nil, relicTitleSet(739201, "After the merge"))

		otherBlob := gitOutput(t, repo, "rev-parse", written.Head+":merges/"+merge.mergeID)
		mergesTree := gitOutput(t, repo, "rev-parse", written.Head+":merges")
		entry := func(mode, kind, objectID, name string) string {
			return mode + " " + kind + " " + objectID + "\t" + name
		}
		merges := func(entries ...string) string {
			return entry("040000", "tree", forgeRelicTree(t, repo, entries), "merges")
		}
		emptyTree := gitOutputWithInput(t, repo, nil, "mktree")
		longer := forgeRelicBlob(t, repo, append(slices.Clone(merge.other), "and more\n"...))

		for label, test := range map[string]struct {
			head string
			// tip is set for a shape a tip read sees from the top tree alone.
			tip bool
		}{
			"merges with no merge in the pack": {
				head: forgeRelicMergesEntry(t, repo, next.Head, entry("040000", "tree", mergesTree, "merges")), tip: true,
			},
			"no merges though the pack holds a merge": {
				head: forgeRelicMergesEntry(t, repo, written.Head, ""), tip: true,
			},
			"merges as a blob": {
				head: forgeRelicMergesEntry(t, repo, written.Head, entry("100644", "blob", otherBlob, "merges")), tip: true,
			},
			"a missing entry": {
				head: forgeRelicMergesEntry(t, repo, written.Head, entry("040000", "tree", emptyTree, "merges")),
			},
			"an entry under another name": {
				head: forgeRelicMergesEntry(t, repo, written.Head, merges(entry("100644", "blob", otherBlob, merge.otherID))),
			},
			"an extra entry": {
				head: forgeRelicMergesEntry(t, repo, written.Head, merges(
					entry("100644", "blob", otherBlob, merge.mergeID),
					entry("100644", "blob", otherBlob, relicTestID(739299)))),
			},
			"an entry that is a tree": {
				head: forgeRelicMergesEntry(t, repo, written.Head, merges(entry("040000", "tree", mergesTree, merge.mergeID))),
			},
			"an executable entry": {
				head: forgeRelicMergesEntry(t, repo, written.Head, merges(entry("100755", "blob", otherBlob, merge.mergeID))),
			},
			"an entry of another size than the save it names": {
				head: forgeRelicMergesEntry(t, repo, written.Head, merges(entry("100644", "blob", longer, merge.mergeID))),
			},
			"an entry Git does not have": {
				head: forgeRelicMergesEntry(t, repo, written.Head, entry("040000", "tree", gitOutputWithInput(t, repo,
					[]byte(entry("100644", "blob", strings.Repeat("0", len(otherBlob)-1)+"1", merge.mergeID)+"\n"),
					"mktree", "--missing"), "merges")),
			},
		} {
			pointRelicRef(t, repo, relicID, test.head)
			_, err := repo.GetRelic(ctx, config, relicID)
			if core.CategoryOf(err) != core.CategoryCorruptData || !strings.HasSuffix(fmt.Sprint(err), relicValidateHint) {
				t.Errorf("%s: GetRelic() error = %v, want corrupt data", label, err)
			}
			checked, failure, err := repo.ValidateRelicHistory(ctx, config, core.RelicHead{RelicID: relicID, ObjectID: test.head})
			if err != nil || failure == nil || failure.Commit != test.head || core.CategoryOf(failure.Err) != core.CategoryCorruptData {
				t.Errorf("%s: ValidateRelicHistory() = %d, %#v, %v; want it to name the forged commit", label, checked, failure, err)
			}
			_, err = repo.ReadRelicCommit(ctx, config, relicID, test.head)
			if core.CategoryOf(err) != core.CategoryCorruptData {
				t.Errorf("%s: ReadRelicCommit() error = %v, want corrupt data", label, err)
			}
			if test.tip {
				_, err := repo.ReadRelicTips(ctx, config, []core.RelicHead{{RelicID: relicID, ObjectID: test.head}})
				if core.CategoryOf(err) != core.CategoryCorruptData {
					t.Errorf("%s: ReadRelicTips() error = %v, want corrupt data", label, err)
				}
			}
		}
	})
}

func TestCheckpointRootHasNoMerges(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		merge := relicMergeFor(t, repo, config, 1)
		relicID := merge.pack.RelicID
		written := merge.write(t, repo, config)
		retired := writeRelicChild(t, repo, config, written, nil,
			core.RelicOperation{ID: relicTestID(739301), Type: core.RelicOperationRetire})
		checkpoint := writeRelicCheckpoint(t, repo, config, retired, core.RelicBoundaryRetired)
		if listing := gitOutput(t, repo, "ls-tree", "--name-only", checkpoint.Head); strings.Contains(listing, "merges") {
			t.Fatalf("the checkpoint root lists %q, want no merges entry", listing)
		}
		if got, want := relicBlob(t, repo, checkpoint.Head+":parts/"+merge.part+"/body"), merge.merged; !bytes.Equal(got, want) {
			t.Fatalf("the checkpoint's body is %q, want the merged text %q", got, want)
		}

		// A checkpoint root holding the merge's entry is refused.
		forged := forgeRelicMergesEntry(t, repo, checkpoint.Head,
			"040000 tree "+gitOutput(t, repo, "rev-parse", written.Head+":merges")+"\tmerges")
		pointRelicRef(t, repo, relicID, forged)
		_, err := repo.GetRelic(ctx, config, relicID)
		assertGetRelicRefused(t, err)
		_, err = repo.ReadRelicTips(ctx, config, []core.RelicHead{{RelicID: relicID, ObjectID: forged}})
		assertRelicCategory(t, err, core.CategoryCorruptData)
		assertRelicHistoryStopsAt(t, repo, config, relicID, forged, forged, 0, core.CategoryCorruptData)
	})
}

func TestRelicSnapshotReadIgnoresMergesForParts(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		merge := relicMergeFor(t, repo, config, 1)
		relicID := merge.pack.RelicID
		written := merge.write(t, repo, config)
		next := writeRelicChild(t, repo, config, written, nil, relicTitleSet(739401, "After the merge"))
		without := written
		without.Head = forgeRelicMergesEntry(t, repo, written.Head, "")

		for _, bodies := range []bool{false, true} {
			want, err := repo.ReadRelicParts(ctx, config, written, []string{merge.part}, bodies)
			if err != nil {
				t.Fatalf("ReadRelicParts(merge, bodies %t) error = %v", bodies, err)
			}
			if bodies && !bytes.Equal(want[merge.part].Body, merge.merged) {
				t.Fatalf("ReadRelicParts() body = %q, want the merged text %q", want[merge.part].Body, merge.merged)
			}
			if want[merge.part].MergeInput != nil {
				t.Fatalf("ReadRelicParts() returned a merge input %q", want[merge.part].MergeInput)
			}
			for label, snapshot := range map[string]core.RelicSnapshot{"no merges entry": without, "the next commit": next} {
				got, err := repo.ReadRelicParts(ctx, config, snapshot, []string{merge.part}, bodies)
				if err != nil {
					t.Fatalf("ReadRelicParts(%s, bodies %t) error = %v", label, bodies, err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("ReadRelicParts(%s, bodies %t) = %#v, want %#v", label, bodies, got, want)
				}
			}
		}

		// The index pass reads the merge commit's parts as it reads the next
		// commit's, which shares its parts tree.
		indexed, errs := repo.ReadIndexedRelics(ctx, config, []RelicIndexRequest{
			{Head: core.RelicHead{RelicID: relicID, ObjectID: written.Head}},
			{Head: core.RelicHead{RelicID: relicID, ObjectID: next.Head}},
		})
		for index, err := range errs {
			if err != nil {
				t.Fatalf("ReadIndexedRelics()[%d] error = %v", index, err)
			}
		}
		if !reflect.DeepEqual(indexed[0].Parts, indexed[1].Parts) || !reflect.DeepEqual(indexed[0].Fields, indexed[1].Fields) {
			t.Fatalf("the index pass read %#v and %#v for the merge, want %#v and %#v as for the next commit",
				indexed[0].Parts, indexed[0].Fields, indexed[1].Parts, indexed[1].Fields)
		}
	})
}
