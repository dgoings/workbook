package historyvalidation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// configValidatorSource is a validator source that also answers for the
// configuration ledger. Splitting it from validatorSource keeps every existing
// test proving what it already proved: a source that does not implement the
// configuration reader must still validate tasks, and reports no ledger.
type configValidatorSource struct {
	validatorSource
	present bool
	commits []gitstore.ConfigHistoryCommit
	failure *gitstore.ConfigHistoryFailure
}

func (s *configValidatorSource) ReadConfigHistoryStream(
	_ context.Context,
	_ core.ProjectConfig,
	stream gitstore.ConfigHistoryStream,
) (bool, error) {
	if !s.present {
		return false, nil
	}
	head := ""
	if len(s.commits) > 0 {
		head = s.commits[len(s.commits)-1].ObjectID
	}
	if err := stream.Begin(gitstore.ConfigHistoryStart{Head: head}); err != nil {
		return true, err
	}
	for _, commit := range s.commits {
		if err := stream.Commit(commit); err != nil {
			return true, err
		}
	}
	return true, stream.End(gitstore.ConfigHistoryResult{
		Head:           head,
		CheckedCommits: len(s.commits),
		Failure:        s.failure,
	})
}

// TestValidateReportsNoConfigSectionWithoutALedger pins the byte compatibility
// the whole section depends on: most projects have no ledger, and their audit
// output must be exactly what it was before this section existed.
func TestValidateReportsNoConfigSectionWithoutALedger(t *testing.T) {
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	source := &configValidatorSource{present: false}

	result, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if result.Config != nil {
		t.Fatalf("Config = %#v, want nothing reported for a project with no ledger", result.Config)
	}
	if result.Advisories != nil {
		t.Fatalf("Advisories = %#v, want none", result.Advisories)
	}
}

func TestValidateFoldsTheConfigurationLedgerAndReportsItValid(t *testing.T) {
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	source := &configValidatorSource{present: true, commits: configLedger(t, core.LegacyVocabulary().Document())}

	result, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if result.Config == nil || !result.Config.Valid {
		t.Fatalf("Config = %#v, want a valid ledger", result.Config)
	}
	if result.Config.CommitsChecked != len(source.commits) {
		t.Fatalf("commits checked = %d, want %d", result.Config.CommitsChecked, len(source.commits))
	}
	if len(result.Advisories) != 0 {
		t.Fatalf("Advisories = %#v, want none for a vocabulary inside every ceiling", result.Advisories)
	}
}

func TestValidateReportsATamperedConfigurationCheckpoint(t *testing.T) {
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	ledger := configLedger(t, core.LegacyVocabulary().Document())
	tampered := ledger[len(ledger)-1].State.Config.Vocabulary
	tampered.Statuses[0].Label = "Tampered"
	ledger[len(ledger)-1].State.Config.Vocabulary = tampered
	source := &configValidatorSource{present: true, commits: ledger}

	result, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Validate() category = %q, want %q; error = %v", got, want, err)
	}
	if result.Config == nil || result.Config.Valid || result.Config.Failure == nil {
		t.Fatalf("Config = %#v, want the tampered checkpoint reported", result.Config)
	}
	if result.Config.Failure.Commit != ledger[len(ledger)-1].ObjectID {
		t.Fatalf("failure commit = %q, want the tampered one", result.Config.Failure.Commit)
	}
}

// TestValidateCarriesAResourceRefusalsOwnCategory is the resource-bound
// contract's other half: a pack this clone declined to fold is not corruption,
// so the audit must not restate it as corruption and hand the caller exit 7.
func TestValidateCarriesAResourceRefusalsOwnCategory(t *testing.T) {
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	refusal := core.Errorf(core.CategoryOperational,
		"a configuration pack carries 65 operations, over this clone's fold budget of %d (MaxConfigOperationsPerPack)",
		core.MaxConfigOperationsPerPack)
	source := &configValidatorSource{
		present: true,
		commits: configLedger(t, core.LegacyVocabulary().Document()),
		failure: &gitstore.ConfigHistoryFailure{Commit: "0f0f0f0f", Err: refusal},
	}

	result, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if got, want := core.CategoryOf(err), core.CategoryOperational; got != want {
		t.Fatalf("Validate() category = %q, want %q; error = %v", got, want, err)
	}
	if result.Config == nil || result.Config.Failure == nil {
		t.Fatalf("Config = %#v, want the refusal reported", result.Config)
	}
	if got := result.Config.Failure.Category; got != string(core.CategoryOperational) {
		t.Fatalf("failure category = %q, want %q", got, core.CategoryOperational)
	}
	if !strings.Contains(result.Config.Failure.Message, "MaxConfigOperationsPerPack") {
		t.Fatalf("failure message = %q, want it to name the bound", result.Config.Failure.Message)
	}
}

// TestValidateReportsTheOverCeilingAdvisoryAndStaysValid is the seam PR-A left:
// a folded state over a status ceiling is nobody's mistake, so it is an
// advisory and never a verdict.
func TestValidateReportsTheOverCeilingAdvisoryAndStaysValid(t *testing.T) {
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	source := &configValidatorSource{present: true, commits: configLedger(t, oversizedVocabulary(t))}

	result, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if err != nil {
		t.Fatalf("Validate() error = %v, want an over-ceiling state to stay valid", err)
	}
	if result.Config == nil || !result.Config.Valid {
		t.Fatalf("Config = %#v, want the ledger reported valid", result.Config)
	}
	if len(result.Advisories) != 1 {
		t.Fatalf("Advisories = %#v, want exactly one", result.Advisories)
	}
	advisory := result.Advisories[0]
	if advisory.Code != AdvisoryStatusCeiling {
		t.Fatalf("advisory code = %q, want %q", advisory.Code, AdvisoryStatusCeiling)
	}
	if !strings.Contains(advisory.Message, fmt.Sprintf("%d statuses", core.MaxStatusCount+1)) {
		t.Fatalf("advisory message = %q, want it to state the observed count", advisory.Message)
	}
	if !strings.Contains(advisory.Message, "removing one brings it back under") {
		t.Fatalf("advisory message = %q, want it to name the way out", advisory.Message)
	}
}

// configLedger builds a real two-commit ledger — a genesis carrying the given
// vocabulary, then one relabel — through core's own fold, so the checkpoints the
// audit recomputes are the ones a clone would have written.
func configLedger(t *testing.T, vocabulary core.VocabularyDocument) []gitstore.ConfigHistoryCommit {
	t.Helper()
	const projectID = "01K0M6B8A4FTT8C39MXXYTW7C1"
	const generation = "01K0M6B8A4FTT8C39MXXYTW7C3"
	wallTime := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)

	genesis, err := core.NewConfigOperationPack(projectID, generation, "author@example.test", 1, wallTime,
		[]core.ConfigOperation{{
			ID:     "01K0M6B8A4FTT8C39MXXYTW7C4",
			Type:   core.ConfigGenesis,
			Config: &core.ConfigData{Vocabulary: vocabulary},
		}})
	if err != nil {
		t.Fatalf("NewConfigOperationPack(genesis) error = %v", err)
	}
	genesisState, err := core.ApplyConfig(nil, genesis)
	if err != nil {
		t.Fatalf("ApplyConfig(genesis) error = %v", err)
	}
	subject := vocabulary.Statuses[0].Status
	next, err := core.NewConfigOperationPack(projectID, generation, "author@example.test", 2, wallTime,
		[]core.ConfigOperation{{
			ID:     "01K0M6B8A4FTT8C39MXXYTW7C5",
			Type:   core.ConfigStatusRelabel,
			Status: subject,
			Label:  "Renamed heading",
		}})
	if err != nil {
		t.Fatalf("NewConfigOperationPack(relabel) error = %v", err)
	}
	nextState, err := core.ApplyConfig(&genesisState, next)
	if err != nil {
		t.Fatalf("ApplyConfig(relabel) error = %v", err)
	}
	return []gitstore.ConfigHistoryCommit{
		{ObjectID: "aaaaaaaa", Operation: genesis, State: genesisState},
		{ObjectID: "bbbbbbbb", Operation: next, State: nextState},
	}
}

// oversizedVocabulary is a vocabulary one status past the ceiling. It is
// reachable without anybody breaking a rule: two clones adding a status
// concurrently is enough, and the fold accepts it precisely so that pair cannot
// produce a history no clone can read.
func oversizedVocabulary(t *testing.T) core.VocabularyDocument {
	t.Helper()
	definitions := make([]core.StatusDefinition, 0, core.MaxStatusCount+1)
	for index := 0; index <= core.MaxStatusCount; index++ {
		tags := []core.StatusTag{}
		switch index {
		case 0:
			tags = []core.StatusTag{core.StatusTagDefault, core.StatusTagNext}
		case core.MaxStatusCount:
			tags = []core.StatusTag{core.StatusTagDone}
		}
		definitions = append(definitions, core.StatusDefinition{
			Status: core.Status(fmt.Sprintf("status-%02d", index)),
			Label:  fmt.Sprintf("Status %02d", index),
			Rank:   fmt.Sprintf("%d/1", index+1),
			Tags:   tags,
		})
	}
	vocabulary, err := core.NewVocabulary(definitions, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v; an over-ceiling vocabulary must still be representable", err)
	}
	return vocabulary.Document()
}
