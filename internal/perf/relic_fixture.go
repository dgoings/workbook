package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

const (
	relicFixtureBodyFormat = "md"
	relicFixturePartType   = "section"
	relicHotRelation       = "implemented-by"
	relicSpreadRelation    = "relates-to"
	// relicHotPartPointer is the pointer every link to the hot part carries.
	relicHotPartPointer = `{"at":"00:00:01"}`
	relicRefPrefix      = "refs/workbook/relics/"
	// relicDocumentVersion is the version of the workbook.relic document this
	// build reads. core.RelicCreateInputFromDocument refuses any other, so a
	// change there fails TestLargeRelicDocumentIsTheLargeRelic.
	relicDocumentVersion = 1
	// relicFixtureExtraRelics are the relics the fixture adds beside the
	// Relics fixture relics: the large, the body and the history relic.
	relicFixtureExtraRelics = 3
	// relicHistoryLinesPerEdit is how many lines each save of the history
	// relic rewrites.
	relicHistoryLinesPerEdit = 5
	// relicMergeFirstLine is the line the first clone saves in each merge
	// relic; see mergeEditLines.
	relicMergeFirstLine = 10
)

// relicFixtureTypes are the relic types the fixture relics take in turn. None
// has a built-in schema: the fixture's relics are open, and a relic of type
// spec or plan made through the CLI would be checked against one.
var relicFixtureTypes = []string{"paper", "agenda", "decision", "transcript", "note"}

// relicFixtureOrigin is where the relic fixture's IDs start and the instant
// every change it writes is recorded at: one year after the task fixture's
// origin, so no relic ID repeats a task fixture ID.
var relicFixtureOrigin = benchmarkOrigin.AddDate(1, 0, 0)

// RelicFixtureSpec is one scale of the relic benchmark set. See the story's
// table: FullRelicFixtureSpec is what scripts/benchmark.sh --relics measures,
// and SmokeRelicFixtureSpec is what the tests build.
type RelicFixtureSpec struct {
	Relics        int `json:"relics"`
	PartsPerRelic int `json:"partsPerRelic"`
	BodyBytes     int `json:"bodyBytes"`
	// LinksPerRelic is how many links to tasks each fixture relic has, and
	// RelicLinksPerRelic how many to parts of other relics, beside those.
	LinksPerRelic      int   `json:"linksPerRelic"`
	RelicLinksPerRelic int   `json:"relicLinksPerRelic"`
	LargeParts         int   `json:"largeParts"`
	LargePartBodyBytes int   `json:"largePartBodyBytes"`
	LargeBodyBytes     int   `json:"largeBodyBytes"`
	SmallBodyBytes     int   `json:"smallBodyBytes"`
	Moves              int   `json:"moves"`
	MovesPerUpdate     int   `json:"movesPerUpdate"`
	ChangedRelics      int   `json:"changedRelics"`
	Seed               int64 `json:"seed"`
	// FullBodiesBytes is the live body total of the relic relic-create-16mib
	// writes: parts of LargeBodyBytes each, then one that makes up the rest,
	// then one of SmallBodyBytes that relic-edit-part-16mib replaces. At the
	// full scale it is the live-body limit, core.MaxRelicLiveBodyBytes.
	FullBodiesBytes int `json:"fullBodiesBytes"`
	// HistoryBodyBytes is the body size of the history relic's one part, and
	// HistoryEdits how many times it is saved after it is created.
	HistoryBodyBytes int `json:"historyBodyBytes"`
	HistoryEdits     int `json:"historyEdits"`
	// MergeRelics is how many relics relic-sync-merge-100 has both clones
	// save, each a body of MergeBodyBytes in MergeBodyLines lines, and
	// RepetitiveBodyBytes the size of the one body of repeated short lines
	// relic-merge-repetitive-1mib merges. Neither set is part of the fixture
	// the other scenarios read; the scenarios build it in a copy.
	MergeRelics         int `json:"mergeRelics"`
	MergeBodyBytes      int `json:"mergeBodyBytes"`
	MergeBodyLines      int `json:"mergeBodyLines"`
	RepetitiveBodyBytes int `json:"repetitiveBodyBytes"`
}

// FullRelicFixtureSpec is the story's scale.
func FullRelicFixtureSpec() RelicFixtureSpec {
	return RelicFixtureSpec{
		Relics: 1000, PartsPerRelic: 20, BodyBytes: 1024, LinksPerRelic: 10, RelicLinksPerRelic: 5,
		LargeParts: 2000, LargePartBodyBytes: 524,
		LargeBodyBytes: 1024 * 1024, SmallBodyBytes: 2 * 1024,
		Moves: 10000, MovesPerUpdate: 100, ChangedRelics: 100, Seed: 1,
		FullBodiesBytes:  core.MaxRelicLiveBodyBytes,
		HistoryBodyBytes: 100 * 1024, HistoryEdits: 100,
		MergeRelics: 100, MergeBodyBytes: 100 * 1024, MergeBodyLines: 2500, RepetitiveBodyBytes: 1024 * 1024,
	}
}

// SmokeRelicFixtureSpec is the scale the tests build.
func SmokeRelicFixtureSpec() RelicFixtureSpec {
	return RelicFixtureSpec{
		Relics: 20, PartsPerRelic: 4, BodyBytes: 1024, LinksPerRelic: 2, RelicLinksPerRelic: 1,
		LargeParts: 50, LargePartBodyBytes: 524,
		LargeBodyBytes: 64 * 1024, SmallBodyBytes: 2 * 1024,
		Moves: 200, MovesPerUpdate: 50, ChangedRelics: 5, Seed: 1,
		FullBodiesBytes:  256 * 1024,
		HistoryBodyBytes: 16 * 1024, HistoryEdits: 10,
		MergeRelics: 5, MergeBodyBytes: 8 * 1024, MergeBodyLines: 200, RepetitiveBodyBytes: 64 * 1024,
	}
}

// TaskFixture is the task fixture the relics are added to: the smoke task
// fixture for the smoke spec, and the default 500-task fixture otherwise. The
// smoke fixture has ten active tasks beside its tombstone, the fewest the
// cli-burst-independent-10 scenario of the task comparison can run on.
func (s RelicFixtureSpec) TaskFixture(objectFormat string) FixtureSpec {
	if s == SmokeRelicFixtureSpec() {
		return FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: objectFormat}
	}
	return FixtureSpec{TotalTasks: 500, ActiveTasks: 475, TombstonedTasks: 25, OperationsPerTask: 20, ObjectFormat: objectFormat}
}

func validateRelicFixtureSpec(spec RelicFixtureSpec, tasks Fixture) error {
	for _, count := range []struct {
		name  string
		value int
	}{
		{"relics", spec.Relics}, {"parts per relic", spec.PartsPerRelic}, {"body bytes", spec.BodyBytes},
		{"links per relic", spec.LinksPerRelic}, {"relic links per relic", spec.RelicLinksPerRelic},
		{"large parts", spec.LargeParts},
		{"large part body bytes", spec.LargePartBodyBytes}, {"large body bytes", spec.LargeBodyBytes},
		{"small body bytes", spec.SmallBodyBytes}, {"moves per update", spec.MovesPerUpdate},
		{"history body bytes", spec.HistoryBodyBytes}, {"history edits", spec.HistoryEdits},
		{"merge relics", spec.MergeRelics}, {"merge body bytes", spec.MergeBodyBytes},
		{"merge body lines", spec.MergeBodyLines}, {"repetitive body bytes", spec.RepetitiveBodyBytes},
	} {
		if count.value < 1 {
			return fmt.Errorf("relic fixture %s must be positive", count.name)
		}
	}
	if spec.Moves < 0 || spec.ChangedRelics < 0 {
		return fmt.Errorf("relic fixture moves and changed relics must not be negative")
	}
	if spec.FullBodiesBytes <= spec.SmallBodyBytes {
		return fmt.Errorf("relic fixture full bodies bytes (%d) must exceed small body bytes (%d): its last part is the small one",
			spec.FullBodiesBytes, spec.SmallBodyBytes)
	}
	if spec.LinksPerRelic+spec.RelicLinksPerRelic > spec.PartsPerRelic {
		return fmt.Errorf("relic fixture links and relic links per relic (%d and %d) must not exceed parts per relic (%d): each is from a part of its own",
			spec.LinksPerRelic, spec.RelicLinksPerRelic, spec.PartsPerRelic)
	}
	if spec.RelicLinksPerRelic >= spec.Relics {
		return fmt.Errorf("relic fixture relic links per relic (%d) must be fewer than relics (%d): they point at other relics",
			spec.RelicLinksPerRelic, spec.Relics)
	}
	// The two saves of a merge relic change lines that do not touch, and each
	// line holds the words that mark a save.
	if first, second := mergeEditLines(spec.MergeBodyLines); second <= first+1 {
		return fmt.Errorf("relic fixture merge body lines (%d) must leave the two saved lines apart", spec.MergeBodyLines)
	}
	if spec.MergeBodyBytes/spec.MergeBodyLines < len(mergeMarker("second", 999))+1 {
		return fmt.Errorf("relic fixture merge body bytes (%d) are too few for %d lines", spec.MergeBodyBytes, spec.MergeBodyLines)
	}
	if spec.RepetitiveBodyBytes%2 != 0 || spec.RepetitiveBodyBytes < 64 {
		return fmt.Errorf("relic fixture repetitive body bytes (%d) must be even and at least 64", spec.RepetitiveBodyBytes)
	}
	if spec.LargeParts < 2 {
		return fmt.Errorf("relic fixture large parts must be at least 2 to move one")
	}
	if len(tasks.ActiveTaskIDs) < 2 {
		return fmt.Errorf("relic fixture needs at least two active tasks, a hot one and another")
	}
	return nil
}

// RelicFixture is a task fixture with the relic benchmark set added. RelicIDs
// are the Relics fixture relics in creation order; LargeRelic, BodyRelic and
// HistoryRelic are the three relics added beside them. HistoryFirstVersion and
// HistoryLastVersion are the first and last body versions of HistoryPart.
type RelicFixture struct {
	Fixture
	Spec                RelicFixtureSpec
	RelicIDs            []string
	Types               []string
	LargeRelic          string
	LargeMovedPart      string
	BodyRelic           string
	LargeBodyPart       string
	SmallBodyPart       string
	HistoryRelic        string
	HistoryPart         string
	HistoryFirstVersion string
	HistoryLastVersion  string
	HotTask             string
	TypicalTask         string
	HotTaskLinks        int
	TypicalTaskLinks    int
	// HotRelicPart is the address of the part every other fixture relic links
	// to, <relic>#<part>: the first part of the first fixture relic.
	HotRelicPart      string
	HotRelicPartLinks int
}

// relicFixtureBuilder writes the relic set in two stages, so a caller can
// measure the repository between them: createRelics writes every relic, and
// linkAndMove adds the links and the large relic's moves.
type relicFixtureBuilder struct {
	fixture    RelicFixture
	repository *gitstore.Repository
	service    core.RelicService
	words      *rand.Rand
	// fixtureParts holds each fixture relic's part IDs, in relic order.
	fixtureParts [][]string
}

// AddRelicFixture adds the relic benchmark set to a task fixture through one
// in-process core.RelicService: the fixture relics, the large relic, the body
// relic and the history relic with its saves, then each fixture relic's links, then the large relic's moves.
// It checks the relic ref count at the end, as BuildFixture checks task refs.
func AddRelicFixture(ctx context.Context, tasks Fixture, spec RelicFixtureSpec) (RelicFixture, error) {
	builder, err := newRelicFixtureBuilder(ctx, tasks, spec)
	if err != nil {
		return RelicFixture{}, err
	}
	if err := builder.createRelics(ctx); err != nil {
		return RelicFixture{}, err
	}
	if err := builder.linkAndMove(ctx); err != nil {
		return RelicFixture{}, err
	}
	return builder.finish(ctx)
}

func newRelicFixtureBuilder(ctx context.Context, tasks Fixture, spec RelicFixtureSpec) (*relicFixtureBuilder, error) {
	if err := validateRelicFixtureSpec(spec, tasks); err != nil {
		return nil, err
	}
	repository, err := gitstore.Open(ctx, tasks.Root)
	if err != nil {
		return nil, err
	}
	ids := &fixtureIDs{entropy: rand.New(rand.NewSource(spec.Seed)), nextAt: relicFixtureOrigin}
	return &relicFixtureBuilder{
		fixture: RelicFixture{
			Fixture: tasks,
			Spec:    spec,
			Types:   append([]string(nil), relicFixtureTypes...),
			// The last active task, so that the task comparison's cli-*
			// scenarios, which act on the first ones, measure a task with a
			// typical number of links. task-show-linked-hot measures this one.
			HotTask: tasks.ActiveTaskIDs[len(tasks.ActiveTaskIDs)-1],
		},
		repository: repository,
		service: core.RelicService{
			Config: tasks.Config,
			Reader: repository,
			Writer: repository,
			Tasks:  repository,
			IDs:    core.IDSourceFunc(ids.next),
			Now:    func() time.Time { return relicFixtureOrigin },
			Actor:  benchmarkActorID,
		},
		words: rand.New(rand.NewSource(spec.Seed)),
	}, nil
}

// createRelics writes the fixture relics, then the large relic, then the body
// relic, one commit each, and then the history relic and its saves.
func (b *relicFixtureBuilder) createRelics(ctx context.Context) error {
	spec := b.fixture.Spec
	for index := range spec.Relics {
		input := core.RelicCreateInput{
			Title: fmt.Sprintf("Benchmark relic %04d", index),
			Type:  relicFixtureTypes[index%len(relicFixtureTypes)],
			Parts: make([]core.RelicPartInput, spec.PartsPerRelic),
		}
		for part := range input.Parts {
			input.Parts[part] = b.part(part, spec.BodyBytes)
		}
		created, err := b.service.Create(ctx, input)
		if err != nil {
			return fmt.Errorf("create benchmark relic %d: %w", index, err)
		}
		b.fixture.RelicIDs = append(b.fixture.RelicIDs, created.Relic.ID)
		b.fixtureParts = append(b.fixtureParts, partIDsOf(created))
	}

	document, err := b.largeRelicDocument()
	if err != nil {
		return err
	}
	input, err := core.RelicCreateInputFromDocument(document)
	if err != nil {
		return fmt.Errorf("read the large relic document: %w", err)
	}
	large, err := b.service.Create(ctx, input)
	if err != nil {
		return fmt.Errorf("create the large relic: %w", err)
	}
	b.fixture.LargeRelic = large.Relic.ID
	b.fixture.LargeMovedPart = large.Parts[0].ID

	body, err := b.service.Create(ctx, core.RelicCreateInput{
		Title: "Benchmark body relic",
		Type:  "note",
		Parts: []core.RelicPartInput{b.part(0, spec.LargeBodyBytes), b.part(1, spec.SmallBodyBytes)},
	})
	if err != nil {
		return fmt.Errorf("create the body relic: %w", err)
	}
	b.fixture.BodyRelic = body.Relic.ID
	b.fixture.LargeBodyPart = body.Parts[0].ID
	b.fixture.SmallBodyPart = body.Parts[1].ID
	return b.createHistoryRelic(ctx)
}

// createHistoryRelic creates the relic the history and diff scenarios read,
// one part of HistoryBodyBytes, and saves that body HistoryEdits times, each
// save rewriting relicHistoryLinesPerEdit lines of the version before it.
func (b *relicFixtureBuilder) createHistoryRelic(ctx context.Context) error {
	spec := b.fixture.Spec
	part := b.part(0, spec.HistoryBodyBytes)
	created, err := b.service.Create(ctx, core.RelicCreateInput{
		Title: "Benchmark history relic",
		Type:  "note",
		Parts: []core.RelicPartInput{part},
	})
	if err != nil {
		return fmt.Errorf("create the history relic: %w", err)
	}
	relicID, partID := created.Relic.ID, created.Parts[0].ID
	version := created.Parts[0].Body.Version
	b.fixture.HistoryRelic, b.fixture.HistoryPart = relicID, partID
	b.fixture.HistoryFirstVersion = version
	text := part.Body.Content
	for edit := range spec.HistoryEdits {
		text = rewriteLines(b.words, text, relicHistoryLinesPerEdit)
		saved, err := b.service.Update(ctx, relicID, []core.RelicEdit{{
			Kind: core.RelicEditPart, Part: partID, Base: version,
			Body: &core.RelicBodyInput{Format: relicFixtureBodyFormat, Content: text},
		}})
		if err != nil {
			return fmt.Errorf("save %d of the history relic: %w", edit+1, err)
		}
		version = saved.Parts[0].Body.Version
	}
	b.fixture.HistoryLastVersion = version
	return nil
}

// rewriteLines returns text with count of its lines, drawn from random,
// replaced by other words of the same length. The size and the line count stay
// as they are. A text of fewer lines has every line rewritten.
func rewriteLines(random *rand.Rand, text []byte, count int) []byte {
	lines := bytes.Split(slices.Clone(text), []byte{'\n'})
	for _, index := range random.Perm(len(lines))[:min(count, len(lines))] {
		words := seededWords(random, len(lines[index]))
		lines[index] = bytes.ReplaceAll(words, []byte{'\n'}, []byte{' '})
	}
	return bytes.Join(lines, []byte{'\n'})
}

// linkAndMove gives every fixture relic its links, one update each, and then
// moves the large relic's parts in updates of MovesPerUpdate moves.
//
// Link 0 of every relic points at the hot task; links 1 and on are spread in
// turn over the other active tasks, and link k is from part k. The typical
// task is the first of those others, which is the first active task: the task
// the cli-* scenarios of the task comparison show and edit.
//
// The links to relics come after those, in the same update, each from the next
// part. The first of them points at the hot part, in every relic but the one
// that holds it; the rest are spread over other relics' parts.
func (b *relicFixtureBuilder) linkAndMove(ctx context.Context) error {
	spec := b.fixture.Spec
	others := b.fixture.ActiveTaskIDs[:len(b.fixture.ActiveTaskIDs)-1]
	b.fixture.TypicalTask = others[0]
	b.fixture.HotRelicPart = b.fixture.RelicIDs[0] + "#" + b.fixtureParts[0][0]
	spread := 0
	for index, relicID := range b.fixture.RelicIDs {
		edits := make([]core.RelicEdit, spec.LinksPerRelic)
		for link := range edits {
			edit := core.RelicEdit{Kind: core.RelicEditAddLink, From: b.fixtureParts[index][link]}
			if link == 0 {
				edit.Target, edit.Rel = b.fixture.HotTask, relicHotRelation
				b.fixture.HotTaskLinks++
			} else {
				edit.Target, edit.Rel = others[spread%len(others)], relicSpreadRelation
				if spread%len(others) == 0 {
					b.fixture.TypicalTaskLinks++
				}
				spread++
			}
			edits[link] = edit
		}
		edits = append(edits, b.relicLinks(index)...)
		if _, err := b.service.Update(ctx, relicID, edits); err != nil {
			return fmt.Errorf("link benchmark relic %s: %w", relicID, err)
		}
	}
	return b.moveLargeRelicParts(ctx)
}

// relicLinks are the links fixture relic index has to other relics' parts,
// link k from part LinksPerRelic+k. In every relic but the first, link 0 points
// at the hot part, the first relic's part 0. Every other link points at part 1
// or on of the k+1th relic after this one, so none reaches the hot part or the
// relic it starts from.
func (b *relicFixtureBuilder) relicLinks(index int) []core.RelicEdit {
	spec := b.fixture.Spec
	edits := make([]core.RelicEdit, spec.RelicLinksPerRelic)
	for link := range edits {
		edit := core.RelicEdit{Kind: core.RelicEditAddLink, From: b.fixtureParts[index][spec.LinksPerRelic+link]}
		if link == 0 && index != 0 {
			edit.Target, edit.Rel = b.fixture.HotRelicPart, relicHotRelation
			edit.Pointer = json.RawMessage(relicHotPartPointer)
			b.fixture.HotRelicPartLinks++
		} else {
			other := (index + link + 1) % spec.Relics
			part := 1 + (index+link)%(spec.PartsPerRelic-1)
			edit.Target, edit.Rel = b.fixture.RelicIDs[other]+"#"+b.fixtureParts[other][part], relicSpreadRelation
		}
		edits[link] = edit
	}
	return edits
}

// moveLargeRelicParts makes spec.Moves seeded random moves of the large
// relic's parts: a uniform part, placed before or after a uniform other one.
func (b *relicFixtureBuilder) moveLargeRelicParts(ctx context.Context) error {
	spec := b.fixture.Spec
	if spec.Moves == 0 {
		return nil
	}
	document, err := b.service.Show(ctx, b.fixture.LargeRelic, core.RelicShowOptions{})
	if err != nil {
		return err
	}
	partIDs := make([]string, len(document.Parts))
	for index, part := range document.Parts {
		partIDs[index] = part.ID
	}
	moves := rand.New(rand.NewSource(spec.Seed))
	for done := 0; done < spec.Moves; {
		count := min(spec.MovesPerUpdate, spec.Moves-done)
		edits := make([]core.RelicEdit, count)
		for index := range edits {
			moved, anchor := randomPartPair(moves, len(partIDs))
			edit := core.RelicEdit{Kind: core.RelicEditMovePart, Part: partIDs[moved]}
			if moves.Intn(2) == 0 {
				edit.Before = partIDs[anchor]
			} else {
				edit.After = partIDs[anchor]
			}
			edits[index] = edit
		}
		if _, err := b.service.Update(ctx, b.fixture.LargeRelic, edits); err != nil {
			return fmt.Errorf("move parts of the large relic after %d moves: %w", done, err)
		}
		done += count
	}
	return nil
}

// randomPartPair draws a part to move and a different part to place it by.
func randomPartPair(random *rand.Rand, parts int) (moved, anchor int) {
	moved = random.Intn(parts)
	anchor = random.Intn(parts - 1)
	if anchor >= moved {
		anchor++
	}
	return moved, anchor
}

// finish packs the relic refs as a relic command does and checks that every
// relic has its ref.
func (b *relicFixtureBuilder) finish(ctx context.Context) (RelicFixture, error) {
	// The builder writes through the service in this process, so nothing has
	// run the pack every relic command ends with. Run it here: the fixture's
	// relic refs are then as a checkout that made them with Workbook has them.
	b.repository.PackMovedRelicRefs(ctx)
	refs, err := b.repository.Git(ctx, nil, "for-each-ref", "--format=%(refname)", relicRefPrefix)
	if err != nil {
		return RelicFixture{}, err
	}
	want := b.fixture.Spec.Relics + relicFixtureExtraRelics
	if got := countRefLines(refs); got != want {
		return RelicFixture{}, fmt.Errorf("relic refs = %d, want %d", got, want)
	}
	return b.fixture, nil
}

// part is one fixture part with a heading field and a body of size seeded
// words.
func (b *relicFixtureBuilder) part(index, size int) core.RelicPartInput {
	heading, _ := json.Marshal(fmt.Sprintf("Section %d", index+1))
	return core.RelicPartInput{
		Type:   relicFixturePartType,
		Fields: map[string]json.RawMessage{"heading": heading},
		Body:   &core.RelicBodyInput{Format: relicFixtureBodyFormat, Content: seededWords(b.words, size)},
	}
}

// largeRelicDocument is the large relic as a workbook.relic document, with
// bodies drawn from the builder's words.
func (b *relicFixtureBuilder) largeRelicDocument() (core.RelicDocument, error) {
	spec := b.fixture.Spec
	document := core.RelicDocument{
		Format:     core.RelicDocumentFormat,
		Version:    relicDocumentVersion,
		Title:      "Benchmark large relic",
		Type:       "paper",
		Properties: map[string]json.RawMessage{},
		Parts:      make([]core.RelicDocumentPart, spec.LargeParts),
		Links:      []core.RelicDocumentLink{},
	}
	for index := range document.Parts {
		part := b.part(index, spec.LargePartBodyBytes)
		text := string(part.Body.Content)
		document.Parts[index] = core.RelicDocumentPart{
			Type:   part.Type,
			Fields: part.Fields,
			Body:   &core.RelicDocumentBody{Format: part.Body.Format, Bytes: int64(len(text)), Text: &text},
		}
	}
	return document, nil
}

// LargeRelicDocument is the workbook.relic document relic-create-large reads:
// a relic of LargeParts parts of LargePartBodyBytes each, as one JSON line.
func LargeRelicDocument(spec RelicFixtureSpec) ([]byte, error) {
	builder := &relicFixtureBuilder{
		fixture: RelicFixture{Spec: spec},
		words:   rand.New(rand.NewSource(spec.Seed)),
	}
	document, err := builder.largeRelicDocument()
	if err != nil {
		return nil, err
	}
	return encodeRelicDocument(document)
}

// fullBodiesSizes are the body sizes of the relic relic-create-16mib writes:
// LargeBodyBytes each until FullBodiesBytes less SmallBodyBytes is reached,
// the last of those holding what is left, then SmallBodyBytes.
func fullBodiesSizes(spec RelicFixtureSpec) []int {
	var sizes []int
	for left := spec.FullBodiesBytes - spec.SmallBodyBytes; left > 0; left -= spec.LargeBodyBytes {
		sizes = append(sizes, min(left, spec.LargeBodyBytes))
	}
	return append(sizes, spec.SmallBodyBytes)
}

// FullBodiesRelicParts is how many parts the FullBodiesRelicDocument relic has.
func FullBodiesRelicParts(spec RelicFixtureSpec) int { return len(fullBodiesSizes(spec)) }

// FullBodiesRelicDocument is the workbook.relic document relic-create-16mib
// reads: a relic whose live bodies total FullBodiesBytes, as one JSON line.
// Its last part is the small one relic-edit-part-16mib replaces.
func FullBodiesRelicDocument(spec RelicFixtureSpec) ([]byte, error) {
	builder := &relicFixtureBuilder{words: rand.New(rand.NewSource(spec.Seed + 16))}
	sizes := fullBodiesSizes(spec)
	document := core.RelicDocument{
		Format:     core.RelicDocumentFormat,
		Version:    relicDocumentVersion,
		Title:      "Benchmark relic at the live-body limit",
		Type:       "note",
		Properties: map[string]json.RawMessage{},
		Parts:      make([]core.RelicDocumentPart, len(sizes)),
		Links:      []core.RelicDocumentLink{},
	}
	for index, size := range sizes {
		part := builder.part(index, size)
		text := string(part.Body.Content)
		document.Parts[index] = core.RelicDocumentPart{
			Type:   part.Type,
			Fields: part.Fields,
			Body:   &core.RelicDocumentBody{Format: part.Body.Format, Bytes: int64(len(text)), Text: &text},
		}
	}
	return encodeRelicDocument(document)
}

// encodeRelicDocument writes a relic document as one JSON line.
func encodeRelicDocument(document core.RelicDocument) ([]byte, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode relic document: %w", err)
	}
	return append(encoded, '\n'), nil
}

func partIDsOf(result core.RelicMutationResult) []string {
	ids := make([]string, len(result.Parts))
	for index, part := range result.Parts {
		ids[index] = part.ID
	}
	return ids
}

var relicFixtureWords = strings.Fields(`
	relic part body index link task commit history order position merge
	branch review design plan decision note spec transcript limit change
	read write fetch push clone cache query field value type format title`)

// seededWords returns exactly size bytes of words drawn from random, with a
// line break after every twelfth word.
func seededWords(random *rand.Rand, size int) []byte {
	var text strings.Builder
	text.Grow(size + 16)
	for count := 1; text.Len() < size; count++ {
		text.WriteString(relicFixtureWords[random.Intn(len(relicFixtureWords))])
		if count%12 == 0 {
			text.WriteByte('\n')
		} else {
			text.WriteByte(' ')
		}
	}
	return []byte(text.String()[:size])
}

// mergeEditLines are the lines, counted from 1, the two saves of a merge relic
// of lines lines change: line 10 on the first clone and the line four fifths
// of the way down on the second, line 2,000 of 2,500.
func mergeEditLines(lines int) (first, second int) {
	return relicMergeFirstLine, lines * 4 / 5
}

// mergeBody is a body of exactly size bytes in lines lines of seeded words,
// each ending in a line break.
func mergeBody(random *rand.Rand, size, lines int) []byte {
	var text bytes.Buffer
	text.Grow(size)
	for line := range lines {
		width := size / lines
		if line < size%lines {
			width++
		}
		words := bytes.ReplaceAll(seededWords(random, width-1), []byte{'\n'}, []byte{' '})
		text.Write(words)
		text.WriteByte('\n')
	}
	return text.Bytes()
}

// mergeMarker starts the line side's save of sample sample writes.
func mergeMarker(side string, sample int) string {
	return fmt.Sprintf("%s %d ", side, sample)
}

// markLine returns text with the start of line number, counted from 1,
// overwritten by marker. The line keeps its length, so the body keeps its size.
func markLine(text []byte, number int, marker string) []byte {
	lines := bytes.Split(slices.Clone(text), []byte{'\n'})
	copy(lines[number-1], marker)
	return bytes.Join(lines, []byte{'\n'})
}

// repetitiveBody is size bytes of the line "x", the most repetitive text a
// body can hold.
func repetitiveBody(size int) []byte {
	return bytes.Repeat([]byte("x\n"), size/2)
}

// changeRepetitiveLines returns a repetitive body with each of the lines,
// counted from 0, replaced by letter, so the body keeps its size.
func changeRepetitiveLines(text []byte, letter byte, lines ...int) []byte {
	changed := slices.Clone(text)
	for _, line := range lines {
		changed[2*line] = letter
	}
	return changed
}

// mergeRelic is a relic of one part that the merge scenarios have two clones
// save.
type mergeRelic struct{ id, part string }

func (m mergeRelic) address() string { return m.id + "#" + m.part }

// createMergeRelics writes the merge relics and the repetitive one through
// service, one create each.
func createMergeRelics(ctx context.Context, service core.RelicService, spec RelicFixtureSpec) (relics []mergeRelic, repetitive mergeRelic, err error) {
	words := rand.New(rand.NewSource(spec.Seed + 32))
	create := func(title, relicType string, body []byte) (mergeRelic, error) {
		created, err := service.Create(ctx, core.RelicCreateInput{
			Title: title,
			Type:  relicType,
			Parts: []core.RelicPartInput{{
				Type: relicFixturePartType,
				Body: &core.RelicBodyInput{Format: relicFixtureBodyFormat, Content: body},
			}},
		})
		if err != nil {
			return mergeRelic{}, fmt.Errorf("create %s: %w", title, err)
		}
		return mergeRelic{id: created.Relic.ID, part: created.Parts[0].ID}, nil
	}
	for index := range spec.MergeRelics {
		relic, err := create(fmt.Sprintf("Benchmark merge relic %03d", index),
			relicFixtureTypes[index%len(relicFixtureTypes)], mergeBody(words, spec.MergeBodyBytes, spec.MergeBodyLines))
		if err != nil {
			return nil, mergeRelic{}, err
		}
		relics = append(relics, relic)
	}
	repetitive, err = create("Benchmark repetitive relic", "note", repetitiveBody(spec.RepetitiveBodyBytes))
	return relics, repetitive, err
}
