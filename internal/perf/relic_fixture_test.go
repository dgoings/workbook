package perf

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
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
			if len(summaries) != 22 {
				t.Fatalf("relics = %d, want 22", len(summaries))
			}
			if len(fixture.RelicIDs) != 20 {
				t.Fatalf("fixture relic IDs = %d, want 20", len(fixture.RelicIDs))
			}
			wantTypes := []string{"spec", "plan", "decision", "transcript", "note"}
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
				if len(document.Links) != 2 {
					t.Fatalf("relic %s has %d links, want 2", relicID, len(document.Links))
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
			if !found || large.Type != "spec" || large.Parts != 50 {
				t.Fatalf("large relic = %+v (listed %v), want a spec with 50 live parts", large, found)
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
	if got, want := countRefLines(refs), fixture.Spec.Relics+2; got != want {
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
	if !reflect.DeepEqual(first.RelicIDs, second.RelicIDs) || first.LargeRelic != second.LargeRelic || first.BodyRelic != second.BodyRelic {
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
		Relics: 1000, PartsPerRelic: 20, BodyBytes: 1024, LinksPerRelic: 10,
		LargeParts: 2000, LargePartBodyBytes: 524, LargeBodyBytes: 1 << 20, SmallBodyBytes: 2048,
		Moves: 10000, MovesPerUpdate: 100, ChangedRelics: 100, Seed: full.Seed,
		FullBodiesBytes: core.MaxRelicLiveBodyBytes,
	}
	if full != wantFull {
		t.Fatalf("full spec = %+v, want %+v", full, wantFull)
	}
	smoke := SmokeRelicFixtureSpec()
	wantSmoke := RelicFixtureSpec{
		Relics: 20, PartsPerRelic: 4, BodyBytes: 1024, LinksPerRelic: 2,
		LargeParts: 50, LargePartBodyBytes: 524, LargeBodyBytes: 64 << 10, SmallBodyBytes: 2048,
		Moves: 200, MovesPerUpdate: 50, ChangedRelics: 5, Seed: smoke.Seed,
		FullBodiesBytes: 256 << 10,
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

// Mutation witness: a packFixtureObjects that leaves an object loose keeps the
// task comparison charging the relic set for how its objects are stored, and
// one that packs or moves a ref measures a checkout Workbook does not leave.
func TestPackFixtureObjectsLeavesNoLooseObjectAndNoRefChanged(t *testing.T) {
	t.Parallel()
	fixture := sharedSmokeRelicFixtureFor(t)
	root := filepath.Join(t.TempDir(), "copy")
	if err := os.CopyFS(root, os.DirFS(fixture.Root)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	git := func(args ...string) string {
		t.Helper()
		output, _, err := runRepositoryGit(ctx, time.Minute, root, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return string(output)
	}
	refs := git("for-each-ref")
	if loose := git("count-objects"); strings.HasPrefix(loose, "0 objects") {
		t.Fatalf("the fixture has no loose object to pack: %q", loose)
	}
	packedRefs, err := os.ReadFile(filepath.Join(root, ".git", "packed-refs"))
	if err != nil {
		t.Fatal(err)
	}

	if err := packFixtureObjects(ctx, time.Minute, root); err != nil {
		t.Fatal(err)
	}
	if loose := git("count-objects"); !strings.HasPrefix(loose, "0 objects") {
		t.Fatalf("loose objects after the pack = %q, want none", loose)
	}
	if after := git("for-each-ref"); after != refs {
		t.Fatalf("refs changed:\nbefore:\n%s\nafter:\n%s", refs, after)
	}
	afterPackedRefs, err := os.ReadFile(filepath.Join(root, ".git", "packed-refs"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterPackedRefs, packedRefs) {
		t.Fatal("the pack of objects changed which refs are packed")
	}
	git("fsck", "--connectivity-only")
}
