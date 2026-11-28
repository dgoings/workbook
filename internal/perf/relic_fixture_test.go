package perf

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/projection"
	"github.com/dgoings/workbook/internal/testenv"
)

// buildSmokeRelicFixture builds the smoke task fixture in a fresh temporary
// directory and adds the smoke relic set to it.
func buildSmokeRelicFixture(t *testing.T, objectFormat string) RelicFixture {
	t.Helper()
	spec := SmokeRelicFixtureSpec()
	ctx := context.Background()
	tasks, err := BuildFixture(ctx, filepath.Join(t.TempDir(), "fixture"), spec.TaskFixture(objectFormat))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := AddRelicFixture(ctx, tasks, spec)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

var sharedSmokeRelicFixture struct {
	once    sync.Once
	root    string
	fixture RelicFixture
	err     error
}

// sharedSmokeRelicFixtureFor builds the SHA-1 smoke relic fixture once for
// every test that only reads it. It lives under the isolated home TestMain
// sets up, which TestMain removes when the package's tests end.
func sharedSmokeRelicFixtureFor(t *testing.T) RelicFixture {
	t.Helper()
	shared := &sharedSmokeRelicFixture
	shared.once.Do(func() {
		shared.root, shared.err = os.MkdirTemp(os.Getenv("HOME"), "relic-fixture-")
		if shared.err != nil {
			return
		}
		spec := SmokeRelicFixtureSpec()
		ctx := context.Background()
		var tasks Fixture
		tasks, shared.err = BuildFixture(ctx, filepath.Join(shared.root, "fixture"), spec.TaskFixture("sha1"))
		if shared.err != nil {
			return
		}
		shared.fixture, shared.err = AddRelicFixture(ctx, tasks, spec)
	})
	if shared.err != nil {
		t.Fatal(shared.err)
	}
	return shared.fixture
}

// relicFixtureService reads the fixture's relics straight from Git.
func relicFixtureService(t *testing.T, fixture RelicFixture) (core.RelicService, *gitstore.Repository) {
	t.Helper()
	repository, err := gitstore.Open(context.Background(), fixture.Root)
	if err != nil {
		t.Fatal(err)
	}
	return core.RelicService{
		Config: fixture.Config,
		Reader: repository,
		Writer: repository,
		Tasks:  repository,
	}, repository
}

// Mutation witness: dropping a relic, a type, a part, a link, the large
// relic's parts or the body relic's sizes, or pointing link 0 anywhere but the
// hot task, leaves the smoke set short of the population the scenarios read.
func TestAddRelicFixtureBuildsTheSmokeSet(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			if objectFormat == "sha256" && testing.Short() {
				t.Skip("builds a SHA-256 relic fixture of its own; the run without -short covers it")
			}
			if objectFormat == "sha256" && !supportsObjectFormat(t, objectFormat) {
				testenv.MissingCapability(t, "Git does not support SHA-256 repositories")
			}
			var fixture RelicFixture
			if objectFormat == "sha1" {
				fixture = sharedSmokeRelicFixtureFor(t)
			} else {
				fixture = buildSmokeRelicFixture(t, objectFormat)
			}
			ctx := context.Background()
			service, repository := relicFixtureService(t, fixture)

			summaries, err := service.List(ctx, core.RelicListFilter{IncludeDeleted: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(summaries) != 23 {
				t.Fatalf("relics = %d, want 23", len(summaries))
			}
			if len(fixture.RelicIDs) != 20 {
				t.Fatalf("fixture relic IDs = %d, want 20", len(fixture.RelicIDs))
			}
			wantTypes := []string{"paper", "agenda", "decision", "transcript", "note"}
			if !reflect.DeepEqual(fixture.Types, wantTypes) {
				t.Fatalf("types = %v, want %v", fixture.Types, wantTypes)
			}
			byID := map[string]core.RelicSummary{}
			for _, summary := range summaries {
				byID[summary.ID] = summary
			}
			perType := map[string]int{}
			for _, relicID := range fixture.RelicIDs {
				summary, found := byID[relicID]
				if !found {
					t.Fatalf("fixture relic %s is not listed", relicID)
				}
				perType[summary.Type]++
				if summary.Parts != 4 {
					t.Fatalf("relic %s has %d parts, want 4", relicID, summary.Parts)
				}
				document, err := service.Export(ctx, relicID)
				if err != nil {
					t.Fatal(err)
				}
				if len(document.Links) != 3 {
					t.Fatalf("relic %s has %d links, want 2 to tasks and 1 to a relic", relicID, len(document.Links))
				}
				for _, part := range document.Parts {
					if part.Body == nil || part.Body.Bytes != 1024 || part.Body.Format != "md" {
						t.Fatalf("relic %s part %s body = %+v, want 1024 bytes of md", relicID, part.ID, part.Body)
					}
				}
				if document.Links[0].To.Task != fixture.HotTask && document.Links[1].To.Task != fixture.HotTask {
					t.Fatalf("relic %s links %+v, want one to the hot task %s", relicID, document.Links, fixture.HotTask)
				}
			}
			for _, relicType := range wantTypes {
				if perType[relicType] != 4 {
					t.Fatalf("relics of type %s = %d, want 4", relicType, perType[relicType])
				}
			}

			large, found := byID[fixture.LargeRelic]
			if !found || large.Type != "paper" || large.Parts != 50 {
				t.Fatalf("large relic = %+v (listed %v), want a paper with 50 live parts", large, found)
			}
			body, found := byID[fixture.BodyRelic]
			if !found || body.Type != "note" {
				t.Fatalf("body relic = %+v (listed %v), want a note", body, found)
			}
			bodies, err := service.Export(ctx, fixture.BodyRelic)
			if err != nil {
				t.Fatal(err)
			}
			sizes := map[string]int64{}
			for _, part := range bodies.Parts {
				sizes[part.ID] = part.Body.Bytes
			}
			if sizes[fixture.LargeBodyPart] != 65536 || sizes[fixture.SmallBodyPart] != 2048 || len(sizes) != 2 {
				t.Fatalf("body relic part sizes = %v, want %s: 65536 and %s: 2048", sizes, fixture.LargeBodyPart, fixture.SmallBodyPart)
			}

			requireHistoryRelic(t, fixture, service, byID[fixture.HistoryRelic])

			if fixture.HotTaskLinks != 20 {
				t.Fatalf("hot task links = %d, want 20", fixture.HotTaskLinks)
			}
			store, err := projection.Open(ctx, repository, fixture.Config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			lookup, err := store.RelicReader().RelicLinksTo(ctx, fixture.Config, core.RelicLinkTarget{Task: fixture.HotTask})
			if err != nil {
				t.Fatal(err)
			}
			if len(lookup.Sources) != 20 || len(lookup.Unread) != 0 {
				t.Fatalf("hot task lookup = %d sources, %d unread; want 20 and 0", len(lookup.Sources), len(lookup.Unread))
			}
			typical, err := store.RelicReader().RelicLinksTo(ctx, fixture.Config, core.RelicLinkTarget{Task: fixture.TypicalTask})
			if err != nil {
				t.Fatal(err)
			}
			if fixture.TypicalTaskLinks < 1 || len(typical.Sources) != fixture.TypicalTaskLinks {
				t.Fatalf("typical task lookup = %d sources, fixture says %d", len(typical.Sources), fixture.TypicalTaskLinks)
			}
			if fixture.TypicalTask == fixture.HotTask {
				t.Fatal("the typical task is the hot task")
			}
		})
	}
}

// Mutation witness: a fixture whose relic links miss the hot part, point a
// relic at itself, take the task links' parts or leave the hot task's relics
// without relic links would time the part lookups on the wrong population, or
// time task-show-linked-hot where no relic link could slow it.
func TestRelicFixtureLinksRelicsToTheHotPart(t *testing.T) {
	t.Parallel()
	fixture := sharedSmokeRelicFixtureFor(t)
	ctx := context.Background()
	service, repository := relicFixtureService(t, fixture)
	hotRelic, hotPart, found := strings.Cut(fixture.HotRelicPart, "#")
	if !found || hotRelic != fixture.RelicIDs[0] {
		t.Fatalf("hot relic part = %q, want a part of the first fixture relic %s", fixture.HotRelicPart, fixture.RelicIDs[0])
	}
	if want := fixture.Spec.Relics - 1; fixture.HotRelicPartLinks != want {
		t.Fatalf("hot part links = %d, want %d", fixture.HotRelicPartLinks, want)
	}

	toHotPart := 0
	for index, relicID := range fixture.RelicIDs {
		document, err := service.Export(ctx, relicID)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 && document.Parts[0].ID != hotPart {
			t.Fatalf("hot part = %s, want the first relic's first part %s", hotPart, document.Parts[0].ID)
		}
		tasks, relics := 0, 0
		for _, link := range document.Links {
			if link.To.Task != "" {
				tasks++
				continue
			}
			relics++
			if link.To.Relic == relicID {
				t.Fatalf("relic %s links to itself: %+v", relicID, link)
			}
			// Relic link k is from the part after the task links' parts.
			if from := document.Parts[fixture.Spec.LinksPerRelic+relics-1].ID; link.From != from {
				t.Fatalf("relic %s relic link %d is from %s, want part %s", relicID, relics, link.From, from)
			}
			if link.To.Relic == hotRelic && link.To.Part == hotPart {
				toHotPart++
				if link.Rel != relicHotRelation || string(link.Pointer) != `{"at":"00:00:01"}` {
					t.Fatalf("relic %s hot part link = %+v, pointer %s", relicID, link, link.Pointer)
				}
			} else if link.Rel != relicSpreadRelation || link.To.Part == "" {
				t.Fatalf("relic %s spread link = %+v, want %s to a part", relicID, link, relicSpreadRelation)
			}
		}
		if tasks != fixture.Spec.LinksPerRelic || relics != fixture.Spec.RelicLinksPerRelic {
			t.Fatalf("relic %s has %d task and %d relic links, want %d and %d",
				relicID, tasks, relics, fixture.Spec.LinksPerRelic, fixture.Spec.RelicLinksPerRelic)
		}
	}
	if toHotPart != fixture.HotRelicPartLinks {
		t.Fatalf("links to the hot part = %d, fixture says %d", toHotPart, fixture.HotRelicPartLinks)
	}

	store, err := projection.Open(ctx, repository, fixture.Config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service.Reader, service.Links = store.RelicReader(), store.RelicReader()
	listed, warnings, err := service.LinkedTo(ctx, fixture.HotRelicPart, core.RelicListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != fixture.HotRelicPartLinks || len(warnings) != 0 {
		t.Fatalf("linked to the hot part = %d relics, %v; want %d", len(listed), warnings, fixture.HotRelicPartLinks)
	}

	// task-show-linked-hot times the task every relic links to: all of those
	// relics hold relic links too.
	lookup, err := store.RelicReader().RelicLinksTo(ctx, fixture.Config, core.RelicLinkTarget{Task: fixture.HotTask})
	if err != nil {
		t.Fatal(err)
	}
	if len(lookup.Sources) != fixture.Spec.Relics {
		t.Fatalf("relics linking to the hot task = %d, want %d", len(lookup.Sources), fixture.Spec.Relics)
	}
	for _, source := range lookup.Sources {
		document, err := service.Export(ctx, source.Relic.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(document.Links, func(link core.RelicDocumentLink) bool { return link.To.Relic != "" }) {
			t.Fatalf("relic %s links to the hot task and holds no relic link", source.Relic.ID)
		}
	}
}

// requireHistoryRelic checks the relic the history and diff scenarios read:
// one part of HistoryBodyBytes saved HistoryEdits times, each save rewriting
// five lines, so that its first and last versions still compare line by line.
//
// Mutation witness: a save that skips, resizes the body or rewrites most of it
// would make relic-history-100 read a shorter chain, or the first-to-last diff
// time the approximate fallback instead of a line diff.
func requireHistoryRelic(t *testing.T, fixture RelicFixture, service core.RelicService, summary core.RelicSummary) {
	t.Helper()
	ctx := context.Background()
	if summary.ID == "" || summary.Parts != 1 || summary.Title != "Benchmark history relic" {
		t.Fatalf("history relic = %+v, want one part", summary)
	}
	spec := fixture.Spec
	_, repository := relicFixtureService(t, fixture)
	service.HistoryReader = repository
	address := fixture.HistoryRelic + "#" + fixture.HistoryPart
	history, err := service.History(ctx, address, core.RelicHistoryOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	versions := history.BodyVersions
	if history.Total != spec.HistoryEdits+1 || len(versions) != spec.HistoryEdits+1 {
		t.Fatalf("history relic = %d changes, %d body versions; want %d of each", history.Total, len(versions), spec.HistoryEdits+1)
	}
	if versions[0].Version != fixture.HistoryFirstVersion || versions[len(versions)-1].Version != fixture.HistoryLastVersion {
		t.Fatalf("history relic versions run %s to %s, fixture says %s to %s", versions[0].Version,
			versions[len(versions)-1].Version, fixture.HistoryFirstVersion, fixture.HistoryLastVersion)
	}
	for _, version := range versions {
		if version.Bytes != int64(spec.HistoryBodyBytes) {
			t.Fatalf("version %s is %d bytes, want %d", version.Version, version.Bytes, spec.HistoryBodyBytes)
		}
	}
	changedLines := func(from, to string) int {
		t.Helper()
		diff, err := service.Diff(ctx, address, []string{from, to}, core.RelicDiffOptions{Context: 0})
		if err != nil {
			t.Fatal(err)
		}
		if len(diff.Parts) != 1 || diff.Parts[0].Body == nil || !diff.Parts[0].Body.Text || diff.Parts[0].Body.Approximate {
			t.Fatalf("diff %s to %s = %+v, want one exact text body diff", from, to, diff.Parts)
		}
		deleted, inserted := 0, 0
		for _, hunk := range diff.Parts[0].Body.Hunks {
			deleted += hunk.FromLines
			inserted += hunk.ToLines
		}
		if deleted != inserted {
			t.Fatalf("diff %s to %s deletes %d lines and inserts %d; a rewrite keeps the line count", from, to, deleted, inserted)
		}
		// 2000 is the line diff's bound on inserted and deleted lines
		// together.
		if deleted+inserted >= 2000 {
			t.Fatalf("diff %s to %s changes %d lines, past the line diff's bound", from, to, deleted+inserted)
		}
		return deleted
	}
	if got := changedLines(versions[0].Version, versions[1].Version); got != 5 {
		t.Fatalf("one save rewrote %d lines, want 5", got)
	}
	if got := changedLines(fixture.HistoryFirstVersion, fixture.HistoryLastVersion); got <= 5 || got > 5*spec.HistoryEdits {
		t.Fatalf("the first and last versions differ in %d lines, want over 5 and at most %d", got, 5*spec.HistoryEdits)
	}
}

// Mutation witness: a fixture that ends without the pack a relic command runs
// leaves every relic ref a loose file. No checkout that made its relics with
// Workbook is in that state, and the task comparison would then charge the
// relic set for refs the product packs. The test looks at the repository's
// files, which only a test may do, to tell a loose ref from a packed one.
func TestAddRelicFixtureLeavesNoRelicRefLoose(t *testing.T) {
	t.Parallel()
	fixture := sharedSmokeRelicFixtureFor(t)
	_, repository := relicFixtureService(t, fixture)
	root := filepath.Join(repository.CommonGitDir, filepath.FromSlash(relicRefPrefix))
	var loose []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			loose = append(loose, entry.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(loose) != 0 {
		t.Fatalf("%d relic refs are loose files after the fixture is built, want none: %v", len(loose), loose)
	}
	refs, err := repository.Git(context.Background(), nil, "for-each-ref", "--format=%(refname)", relicRefPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := countRefLines(refs), fixture.Spec.Relics+3; got != want {
		t.Fatalf("relic refs = %d, want %d", got, want)
	}
}

// Mutation witness: an ID source seeded from the clock, a body drawn from an
// unseeded generator, or a link or move order taken from map iteration makes
// two builds of one spec disagree. Git dates each commit from the clock, so the
// heads export reports are the one thing two builds may not share; they are
// left out of the comparison.
func TestAddRelicFixtureIsDeterministic(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds a second smoke relic fixture; the run without -short covers it")
	}
	// The second build first, so it runs beside the shared one rather than
	// after it.
	second := buildSmokeRelicFixture(t, "sha1")
	first := sharedSmokeRelicFixtureFor(t)
	if !reflect.DeepEqual(first.RelicIDs, second.RelicIDs) || first.LargeRelic != second.LargeRelic || first.BodyRelic != second.BodyRelic ||
		first.HistoryRelic != second.HistoryRelic || first.HistoryFirstVersion != second.HistoryFirstVersion ||
		first.HistoryLastVersion != second.HistoryLastVersion {
		t.Fatalf("relic IDs differ between builds")
	}
	ctx := context.Background()
	firstService, _ := relicFixtureService(t, first)
	secondService, _ := relicFixtureService(t, second)
	summaries, err := firstService.List(ctx, core.RelicListFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range summaries {
		left, err := firstService.Export(ctx, summary.ID)
		if err != nil {
			t.Fatal(err)
		}
		right, err := secondService.Export(ctx, summary.ID)
		if err != nil {
			t.Fatal(err)
		}
		left.Head, right.Head = "", ""
		leftBytes, err := encodeRelicDocument(left)
		if err != nil {
			t.Fatal(err)
		}
		rightBytes, err := encodeRelicDocument(right)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(leftBytes, rightBytes) {
			t.Fatalf("relic %s exports differ between builds", summary.ID)
		}
	}
}

// Mutation witness: a document that the reader refuses, or one whose part
// count or body sizes drift from the spec, would make relic-create-large
// measure a refusal or a different relic.
func TestLargeRelicDocumentIsTheLargeRelic(t *testing.T) {
	t.Parallel()
	spec := SmokeRelicFixtureSpec()
	encoded, err := LargeRelicDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	document, err := core.DecodeRelicDocument(bytes.TrimSuffix(encoded, []byte("\n")))
	if err != nil {
		t.Fatal(err)
	}
	input, err := core.RelicCreateInputFromDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Parts) != spec.LargeParts {
		t.Fatalf("parts = %d, want %d", len(input.Parts), spec.LargeParts)
	}
	for index, part := range input.Parts {
		if part.Body == nil || len(part.Body.Content) != spec.LargePartBodyBytes {
			t.Fatalf("part %d body is not %d bytes", index, spec.LargePartBodyBytes)
		}
	}
	again, err := LargeRelicDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, again) {
		t.Fatal("LargeRelicDocument is not deterministic")
	}
	if !strings.HasSuffix(string(encoded), "\n") {
		t.Fatal("document does not end with a newline")
	}
}

// Mutation witness: a document whose bodies fall short of the spec's total, a
// part over the one-body limit, or a last part that is not the small one the
// edit replaces would make relic-create-16mib, relic-show-16mib and
// relic-edit-part-16mib measure some other relic than one at the live-body
// limit.
func TestFullBodiesRelicDocumentHoldsTheLiveBodyLimit(t *testing.T) {
	t.Parallel()
	for _, spec := range []RelicFixtureSpec{SmokeRelicFixtureSpec(), FullRelicFixtureSpec()} {
		encoded, err := FullBodiesRelicDocument(spec)
		if err != nil {
			t.Fatal(err)
		}
		document, err := core.DecodeRelicDocument(bytes.TrimSuffix(encoded, []byte("\n")))
		if err != nil {
			t.Fatal(err)
		}
		input, err := core.RelicCreateInputFromDocument(document)
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for index, part := range input.Parts {
			if part.Body == nil || len(part.Body.Content) > spec.LargeBodyBytes || len(part.Body.Content) > core.MaxRelicBodyBytes {
				t.Fatalf("part %d body is missing or over %d bytes", index, spec.LargeBodyBytes)
			}
			total += len(part.Body.Content)
		}
		if total != spec.FullBodiesBytes {
			t.Fatalf("live bodies = %d bytes, want %d", total, spec.FullBodiesBytes)
		}
		if last := input.Parts[len(input.Parts)-1]; len(last.Body.Content) != spec.SmallBodyBytes {
			t.Fatalf("last part body = %d bytes, want the small %d", len(last.Body.Content), spec.SmallBodyBytes)
		}
		if got := FullBodiesRelicParts(spec); got != len(input.Parts) {
			t.Fatalf("FullBodiesRelicParts = %d, document has %d", got, len(input.Parts))
		}
		again, err := FullBodiesRelicDocument(spec)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, again) {
			t.Fatal("FullBodiesRelicDocument is not deterministic")
		}
	}
	if full := FullRelicFixtureSpec(); full.FullBodiesBytes != core.MaxRelicLiveBodyBytes || full.LargeBodyBytes != core.MaxRelicBodyBytes {
		t.Fatalf("full spec bodies = %d live, %d per body; want the limits %d and %d",
			full.FullBodiesBytes, full.LargeBodyBytes, core.MaxRelicLiveBodyBytes, core.MaxRelicBodyBytes)
	}
}

// Mutation witness: a full spec that drifts from the story's table makes the
// benchmark measure a different population than the one the budgets name.
func TestRelicFixtureSpecsMatchTheStoryTable(t *testing.T) {
	t.Parallel()
	full := FullRelicFixtureSpec()
	wantFull := RelicFixtureSpec{
		Relics: 1000, PartsPerRelic: 20, BodyBytes: 1024, LinksPerRelic: 10, RelicLinksPerRelic: 5,
		LargeParts: 2000, LargePartBodyBytes: 524, LargeBodyBytes: 1 << 20, SmallBodyBytes: 2048,
		Moves: 10000, MovesPerUpdate: 100, ChangedRelics: 100, Seed: full.Seed,
		FullBodiesBytes:  core.MaxRelicLiveBodyBytes,
		HistoryBodyBytes: 100 << 10, HistoryEdits: 100,
	}
	if full != wantFull {
		t.Fatalf("full spec = %+v, want %+v", full, wantFull)
	}
	smoke := SmokeRelicFixtureSpec()
	wantSmoke := RelicFixtureSpec{
		Relics: 20, PartsPerRelic: 4, BodyBytes: 1024, LinksPerRelic: 2, RelicLinksPerRelic: 1,
		LargeParts: 50, LargePartBodyBytes: 524, LargeBodyBytes: 64 << 10, SmallBodyBytes: 2048,
		Moves: 200, MovesPerUpdate: 50, ChangedRelics: 5, Seed: smoke.Seed,
		FullBodiesBytes:  256 << 10,
		HistoryBodyBytes: 16 << 10, HistoryEdits: 10,
	}
	if smoke != wantSmoke {
		t.Fatalf("smoke spec = %+v, want %+v", smoke, wantSmoke)
	}
	if got := full.TaskFixture("sha1"); got != (FixtureSpec{TotalTasks: 500, ActiveTasks: 475, TombstonedTasks: 25, OperationsPerTask: 20, ObjectFormat: "sha1"}) {
		t.Fatalf("full task fixture = %+v", got)
	}
	if got := smoke.TaskFixture("sha256"); got != (FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: "sha256"}) {
		t.Fatalf("smoke task fixture = %+v", got)
	}
}

// Mutation witness: classifying relic bodies, fields or links as other-blob
// hides what a relic stores; leaving one unclassified breaks the account.
func TestStorageClassifiesRelicObjects(t *testing.T) {
	t.Parallel()
	fixture := sharedSmokeRelicFixtureFor(t)
	account, err := measureGitStorage(context.Background(), 60*time.Second, fixture.Root)
	if err != nil {
		t.Fatal(err)
	}
	classes := classAccountsByName(account)
	for _, class := range []string{ObjectClassRelicBody, ObjectClassRelicFields, ObjectClassRelicLinks} {
		if classes[class].Objects == 0 {
			t.Fatalf("class %s has no objects in %+v", class, account.Classes)
		}
	}
	if classes[ObjectClassOtherBlob].Objects != 0 {
		t.Fatalf("other-blob holds %d objects of a relic fixture", classes[ObjectClassOtherBlob].Objects)
	}
	if account.UnclassifiedObjects != 0 {
		t.Fatalf("unclassified objects = %d", account.UnclassifiedObjects)
	}
}
