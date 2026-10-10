package cli

import (
	"fmt"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// Some tests need a configuration ledger this build cannot write: a project
// from a build ahead of this one, or from a build behind it that predates a
// section every genesis now carries. Those tests forge the ledger with Git
// plumbing, and each forgery is the same construction — the four objects
// gitstore's writeConfigObjects writes (operation.json and state.json blobs,
// the tree holding them side by side, the commit naming that tree), then
// refs/workbook/config pointed at the result.
//
// internal/cli's forgeries share that construction here, once. Each call site
// keeps only what it records and why no ordinary write path reaches it. When
// the shape of a configuration commit changes, this file is one of two edits
// that follow it: the other is forgeConfigTree in internal/gitstore's
// treeshape_test.go, which rewrites a ledger tip in place, keeping its parents
// and adding stray entries, and lives in a package this helper cannot reach.

// writeConfigLedgerCommit writes one configuration-ledger commit carrying the
// two stored documents exactly as given and returns its object ID, moving no
// ref. An empty parent writes a genesis.
//
// The documents are bytes rather than values because a forgery from a newer
// build carries members and operations this build's encoder refuses.
func writeConfigLedgerCommit(t *testing.T, repository, parent, operation, state, subject string) string {
	t.Helper()
	operationBlob := gitWithInput(t, repository, operation, "hash-object", "-w", "--stdin")
	stateBlob := gitWithInput(t, repository, state, "hash-object", "-w", "--stdin")
	tree := gitWithInput(t, repository, fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n",
		operationBlob, stateBlob), "mktree")
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return gitWithInput(t, repository, "", append(args, "-m", subject)...)
}

// writeConfigLedgerPack folds pack onto prior (nil for a genesis), encodes the
// pack and the checkpoint it produced with the encoder gitstore calls, and
// writes both as one commit on parent. It returns the commit and the
// checkpoint, so a later pack can build on either.
func writeConfigLedgerPack(
	t *testing.T,
	repository, parent string,
	prior *core.ConfigStateDocument,
	pack core.ConfigOperationPack,
	subject string,
) (string, core.ConfigStateDocument) {
	t.Helper()
	state, err := core.ApplyConfig(prior, pack)
	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	packBytes, err := core.EncodeDocument(pack)
	if err != nil {
		t.Fatalf("encode configuration operation pack: %v", err)
	}
	stateBytes, err := core.EncodeDocument(state)
	if err != nil {
		t.Fatalf("encode configuration state document: %v", err)
	}
	return writeConfigLedgerCommit(t, repository, parent, string(packBytes), string(stateBytes), subject), state
}

// writeLegacyConfigGenesis writes, under a fresh history generation, the
// genesis a build that predates the priorities section left behind: a status
// vocabulary and no priorities section at all. It moves no ref.
func writeLegacyConfigGenesis(
	t *testing.T,
	repository, projectID, actor string,
	wallTime time.Time,
	subject string,
) (string, core.ConfigStateDocument) {
	t.Helper()
	ids := core.CryptoULIDSource{}
	generation, err := ids.New()
	if err != nil {
		t.Fatalf("history generation ID: %v", err)
	}
	genesisID, err := ids.New()
	if err != nil {
		t.Fatalf("genesis operation ID: %v", err)
	}
	pack, err := core.NewConfigOperationPack(projectID, generation, actor, 1, wallTime,
		[]core.ConfigOperation{{
			ID:     genesisID,
			Type:   core.ConfigGenesis,
			Config: &core.ConfigData{Vocabulary: core.LegacyVocabulary().Document()},
		}})
	if err != nil {
		t.Fatalf("legacy genesis pack: %v", err)
	}
	commit, state := writeConfigLedgerPack(t, repository, "", nil, pack, subject)
	if state.Config.Priorities != nil {
		t.Fatalf("legacy genesis state carries a priorities section: %#v", state.Config.Priorities)
	}
	return commit, state
}

// moveConfigLedger points the configuration ref at head. A non-empty expected
// makes the move a compare-and-swap against it; an empty one replaces whatever
// the ref holds, or creates it.
func moveConfigLedger(t *testing.T, repository, head, expected string) {
	t.Helper()
	args := []string{"update-ref", configLedgerRefName, head}
	if expected != "" {
		args = append(args, expected)
	}
	cliGit(t, repository, args...)
}
