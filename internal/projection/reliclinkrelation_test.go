package projection

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// linkIndexRelicDirectly records a link.add from the relic to the target with
// the relationship, written as a teammate's build would have, with nothing
// checked. It returns the link's ID.
func linkIndexRelicDirectly(t *testing.T, repository *gitstore.Repository, config core.ProjectConfig, relicID, target string, number int, rel string) string {
	t.Helper()
	id := relicIndexTestID(number)
	changeIndexRelic(t, repository, config, relicID, core.RelicOperation{
		ID: id, Type: core.RelicOperationLinkAdd,
		Link: &core.RelicLinkChange{To: core.RelicLinkTarget{Relic: target}, Rel: rel, Seen: relicIndexTestID(number + 1)},
	})
	return id
}

// The index judges a relic's links as Git does, after its properties and its
// parts: a spec on the built-in with a blocks link does not conform, naming
// the link, and one that also lacks its status names the property.
func TestConformanceJudgesLinks(t *testing.T) {
	t.Parallel()
	repository, config := initializeRelicWorkbook(t, "sha1")
	ctx := context.Background()
	service := relicServiceForIndex(repository, config, 7000)
	service.Schemas = repository
	target := createIndexRelic(t, service, "Target", 0)
	spec, err := service.Create(ctx, core.RelicCreateInput{Title: "Spec", Type: "spec",
		Properties: map[string]json.RawMessage{"status": relicIndexJSON(t, "draft")}})
	if err != nil {
		t.Fatalf("Create(spec) error = %v", err)
	}
	unchecked := service
	unchecked.Schemas = nil
	statusless, err := unchecked.Create(ctx, core.RelicCreateInput{Title: "No status", Type: "spec"})
	if err != nil {
		t.Fatalf("Create(spec with no status) error = %v", err)
	}
	migrateIndexRelic(t, repository, config, statusless.Relic.ID, 7900, "spec", "workbook:spec:1")
	note, err := service.Create(ctx, core.RelicCreateInput{Title: "Note", Type: "note"})
	if err != nil {
		t.Fatalf("Create(note) error = %v", err)
	}
	linkID := linkIndexRelicDirectly(t, repository, config, spec.Relic.ID, target, 7910, "blocks")
	linkIndexRelicDirectly(t, repository, config, statusless.Relic.ID, target, 7920, "blocks")
	linkIndexRelicDirectly(t, repository, config, note.Relic.ID, target, 7930, "blocks")

	store := openRelicStore(t, repositorySource{repository: repository}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	runRelicPass(t, store)
	rows := relicConformanceRows(t, store)
	if row := rows[spec.Relic.ID]; row[1] != core.RelicConformanceNotConforming ||
		row[2] != `link `+linkID+` relationship "blocks": the relationship must be one of `+
			strings.Join(core.DefaultRelicRelationNames(), ", ")+` (relationships)` {
		t.Errorf("spec row = %q, want not-conforming naming the link", row)
	}
	if row := rows[statusless.Relic.ID]; row[1] != core.RelicConformanceNotConforming || !strings.HasPrefix(row[2], `property "status": `) {
		t.Errorf("statusless row = %q, want the property named before the link", row)
	}
	if row := rows[note.Relic.ID]; row[1] != core.RelicConformanceOpen {
		t.Errorf("note row = %q, want open", row)
	}

	gitList := listedJSON(t, repository, config, repository)
	if got := listedJSON(t, repository, config, store.RelicReader()); got != gitList {
		t.Errorf("index list =\n%s\nGit list =\n%s", got, gitList)
	}
	if !strings.Contains(gitList, `"schemaProblem":"link `+linkID+` relationship \"blocks\": `) {
		t.Errorf("Git list does not name the link:\n%s", gitList)
	}

	// A version 13 cache judged links not at all, so its row says conforms;
	// the stamp alone makes it discarded and the relic judged again.
	if _, err := store.db.ExecContext(ctx, `UPDATE relics SET conformance = 'conforms', schema_problem = '' WHERE relic_id = ?`, spec.Relic.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE projection_meta SET value = '13' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	reopened := openRelicStore(t, repositorySource{repository: repository}, config, store.CachePath())
	if got := listedJSON(t, repository, config, reopened.RelicReader()); got != gitList {
		t.Errorf("list from a version 13 cache =\n%s\nGit list =\n%s", got, gitList)
	}
}
