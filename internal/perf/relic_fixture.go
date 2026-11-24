package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
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
	relicRefPrefix         = "refs/workbook/relics/"
	// relicDocumentVersion is the version of the workbook.relic document this
	// build reads. core.RelicCreateInputFromDocument refuses any other, so a
	// change there fails TestLargeRelicDocumentIsTheLargeRelic.
	relicDocumentVersion = 1
)

// relicFixtureTypes are the relic types the fixture relics take in turn.
var relicFixtureTypes = []string{"spec", "plan", "decision", "transcript", "note"}

// relicFixtureOrigin is where the relic fixture's IDs start and the instant
// every change it writes is recorded at: one year after the task fixture's
// origin, so no relic ID repeats a task fixture ID.
var relicFixtureOrigin = benchmarkOrigin.AddDate(1, 0, 0)

// RelicFixtureSpec is one scale of the relic benchmark set. See the story's
// table: FullRelicFixtureSpec is what scripts/benchmark.sh --relics measures,
// and SmokeRelicFixtureSpec is what the tests build.
type RelicFixtureSpec struct {
	Relics             int   `json:"relics"`
	PartsPerRelic      int   `json:"partsPerRelic"`
	BodyBytes          int   `json:"bodyBytes"`
	LinksPerRelic      int   `json:"linksPerRelic"`
	LargeParts         int   `json:"largeParts"`
	LargePartBodyBytes int   `json:"largePartBodyBytes"`
	LargeBodyBytes     int   `json:"largeBodyBytes"`
	SmallBodyBytes     int   `json:"smallBodyBytes"`
	Moves              int   `json:"moves"`
	MovesPerUpdate     int   `json:"movesPerUpdate"`
	ChangedRelics      int   `json:"changedRelics"`
	Seed               int64 `json:"seed"`
}

// FullRelicFixtureSpec is the story's scale.
func FullRelicFixtureSpec() RelicFixtureSpec {
	return RelicFixtureSpec{
		Relics: 1000, PartsPerRelic: 20, BodyBytes: 1024, LinksPerRelic: 10,
		LargeParts: 2000, LargePartBodyBytes: 524,
		LargeBodyBytes: 1024 * 1024, SmallBodyBytes: 2 * 1024,
		Moves: 10000, MovesPerUpdate: 100, ChangedRelics: 100, Seed: 1,
	}
}

// SmokeRelicFixtureSpec is the scale the tests build.
func SmokeRelicFixtureSpec() RelicFixtureSpec {
	return RelicFixtureSpec{
		Relics: 20, PartsPerRelic: 4, BodyBytes: 1024, LinksPerRelic: 2,
		LargeParts: 50, LargePartBodyBytes: 524,
		LargeBodyBytes: 64 * 1024, SmallBodyBytes: 2 * 1024,
		Moves: 200, MovesPerUpdate: 50, ChangedRelics: 5, Seed: 1,
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
		{"links per relic", spec.LinksPerRelic}, {"large parts", spec.LargeParts},
		{"large part body bytes", spec.LargePartBodyBytes}, {"large body bytes", spec.LargeBodyBytes},
		{"small body bytes", spec.SmallBodyBytes}, {"moves per update", spec.MovesPerUpdate},
	} {
		if count.value < 1 {
			return fmt.Errorf("relic fixture %s must be positive", count.name)
		}
	}
	if spec.Moves < 0 || spec.ChangedRelics < 0 {
		return fmt.Errorf("relic fixture moves and changed relics must not be negative")
	}
	if spec.LinksPerRelic > spec.PartsPerRelic {
		return fmt.Errorf("relic fixture links per relic (%d) must not exceed parts per relic (%d): link k is from part k",
			spec.LinksPerRelic, spec.PartsPerRelic)
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
// are the Relics fixture relics in creation order; LargeRelic and BodyRelic
// are the two relics added beside them.
type RelicFixture struct {
	Fixture
	Spec             RelicFixtureSpec
	RelicIDs         []string
	Types            []string
	LargeRelic       string
	LargeMovedPart   string
	BodyRelic        string
	LargeBodyPart    string
	SmallBodyPart    string
	HotTask          string
	TypicalTask      string
	HotTaskLinks     int
	TypicalTaskLinks int
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
// in-process core.RelicService: the fixture relics, the large relic and the
// body relic, then each fixture relic's links, then the large relic's moves.
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
			HotTask: tasks.ActiveTaskIDs[0],
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
// relic, one commit each.
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
	return nil
}

// linkAndMove gives every fixture relic its links, one update each, and then
// moves the large relic's parts in updates of MovesPerUpdate moves.
//
// Link 0 of every relic points at the hot task; links 1 and on are spread in
// turn over the other active tasks, and link k is from part k. The typical
// task is the first of those others.
func (b *relicFixtureBuilder) linkAndMove(ctx context.Context) error {
	spec := b.fixture.Spec
	others := b.fixture.ActiveTaskIDs[1:]
	b.fixture.TypicalTask = others[0]
	spread := 0
	for index, relicID := range b.fixture.RelicIDs {
		edits := make([]core.RelicEdit, spec.LinksPerRelic)
		for link := range edits {
			edit := core.RelicEdit{Kind: core.RelicEditAddLink, From: b.fixtureParts[index][link]}
			if link == 0 {
				edit.Task, edit.Rel = b.fixture.HotTask, relicHotRelation
				b.fixture.HotTaskLinks++
			} else {
				edit.Task, edit.Rel = others[spread%len(others)], relicSpreadRelation
				if spread%len(others) == 0 {
					b.fixture.TypicalTaskLinks++
				}
				spread++
			}
			edits[link] = edit
		}
		if _, err := b.service.Update(ctx, relicID, edits); err != nil {
			return fmt.Errorf("link benchmark relic %s: %w", relicID, err)
		}
	}
	return b.moveLargeRelicParts(ctx)
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

// finish checks that every relic has its ref.
func (b *relicFixtureBuilder) finish(ctx context.Context) (RelicFixture, error) {
	refs, err := b.repository.Git(ctx, nil, "for-each-ref", "--format=%(refname)", relicRefPrefix)
	if err != nil {
		return RelicFixture{}, err
	}
	want := b.fixture.Spec.Relics + 2
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
		Type:       "spec",
		Schema:     json.RawMessage("null"),
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
