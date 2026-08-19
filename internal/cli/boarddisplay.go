package cli

import (
	"context"
	"strings"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/webui"
)

// boardDisplay is the display settings' second surface, the sibling of
// boardVocabulary.
//
// It authors nothing `workbook config set` could not have authored, in the same
// operations and against the same rules — values are canonicalized at this
// boundary exactly as they are at the command line, and the ledger's own
// compare-and-swap settles two savers. What differs is only what a server can
// honestly do around the write, which is what boardVocabulary.apply already
// says.
type boardDisplay struct {
	repository *gitstore.Repository
	config     core.ProjectConfig
	publisher  *boardPublisher
}

// set records the settings a save proposes, and records only what it changes.
//
// The change is a full replacement — three values and one Save — so working out
// what actually moved is this function's job rather than the form's. It is not
// bookkeeping: a display operation stamps generation two into this project's
// configuration checkpoint, and from that moment every clone running an older
// Workbook can read the project and can no longer change its configuration. That
// cost is permanent and it is paid per project, so it must only ever be paid for
// a change somebody actually made. A Save pressed twice writes once; a Save
// pressed with nothing edited writes nothing at all, answers success, and leaves
// the ledger tip exactly where it was.
//
// `config set` refuses the same no-op outright, with exit 5, because a person
// typing a command meant to change something and deserves to be told they did
// not. A form has no such intent to report: the reader may have edited one field
// of three, and refusing the whole save would be refusing the two that did move.
// So the two surfaces share the invariant — no operation without a change — and
// differ in what they say about the empty case, which is what they should differ
// about.
func (board *boardDisplay) set(
	ctx context.Context,
	change webui.DisplayChange,
) (webui.DisplayMutation, error) {
	fetchConfigBefore(ctx, board.repository, board.config, board.publisher)
	state, err := board.repository.LoadVocabularyState(ctx, board.config)
	if err != nil {
		return webui.DisplayMutation{}, err
	}
	// Cheap and early, so a save nobody could land does not first canonicalize
	// and diff. The enforcement is at the write.
	if state.Head != change.ExpectedHead {
		return webui.DisplayMutation{}, staleDisplayWrite(change.ExpectedHead)
	}
	operations, err := displayOperations(state.Display, change)
	if err != nil {
		return webui.DisplayMutation{}, err
	}
	if len(operations) == 0 {
		// Nothing moved, so nothing is recorded and the answer is the
		// configuration as it already stands. This is the whole of the no-op
		// rule: no pack, no commit, no ref update, no generation marker.
		return webui.DisplayMutation{
			State: webui.VocabularyState{Head: state.Head, Display: state.Display},
		}, nil
	}
	written, err := board.repository.WriteConfigOperationOnto(
		ctx, board.config, core.CryptoULIDSource{}, operations,
		displayOperationsSubject(operations), change.ExpectedHead)
	if err != nil {
		if core.CategoryOf(err) == core.CategoryStaleWrite {
			// Whether the ledger moved while the save was being composed or
			// while it was being written, the reader's situation is the same one
			// and reads the same way.
			return webui.DisplayMutation{}, staleDisplayWrite(change.ExpectedHead)
		}
		return webui.DisplayMutation{}, err
	}
	return webui.DisplayMutation{
		State:    webui.VocabularyState{Head: written.Head, Display: written.State.Display()},
		Warnings: board.publisher.publishConfig(ctx),
	}, nil
}

// displayOperations is the diff a save turns into: one operation for each
// setting that actually changes, in the order every surface presents them.
//
// Values are canonicalized before they are compared, not after, and that is the
// load-bearing half. The ledger stores a trimmed name and a lowercase colour, so
// `#1A7F4B` typed over a stored `#1a7f4b` is the same colour and must record
// nothing; comparing what was typed against what is stored would make it a
// change and stamp the generation marker for a keystroke that meant nothing.
//
// A value that is not a value core would store is refused here, where the reader
// can still be told the rule, rather than at the operation document, which
// reports the same thing as corrupt data.
func displayOperations(
	current core.DisplaySettings,
	change webui.DisplayChange,
) ([]core.ConfigOperation, error) {
	proposed := map[string]string{
		core.DisplayProjectName:  change.Name,
		core.DisplayPrimaryColor: change.PrimaryColor,
		core.DisplayTextColor:    change.TextColor,
	}
	operations := make([]core.ConfigOperation, 0, len(core.DisplaySettingNames))
	for _, setting := range core.DisplaySettingNames {
		before, _ := current.Value(setting)
		// Trimmed before it is asked whether it says anything, so a field
		// holding a space is the empty field it looks like. The alternative is
		// refusing a save because a name ends in one, which is a rule about
		// typing rather than about configuration — and the form trims what it
		// sends anyway, so refusing here would only ever answer a caller that is
		// not the form.
		wanted := strings.TrimSpace(proposed[setting])
		if wanted == "" {
			if before != "" {
				operations = append(operations,
					core.ConfigOperation{Type: core.ConfigDisplayUnset, Setting: setting})
			}
			continue
		}
		canonical, err := core.CanonicalDisplayValue(setting, wanted)
		if err != nil {
			return nil, err
		}
		if canonical == before {
			continue
		}
		operations = append(operations,
			core.ConfigOperation{Type: core.ConfigDisplaySet, Setting: setting, Value: canonical})
	}
	return operations, nil
}

// displayOperationsSubject writes what the ledger's `git log` says about a save.
// One change reads exactly as the same change made from the command line would;
// several are one decision the reader made in one form, and the commit says so
// rather than listing three settings in a subject line.
func displayOperationsSubject(operations []core.ConfigOperation) string {
	if len(operations) == 1 {
		operation := operations[0]
		change := configDisplayChange{Operation: "set", Setting: operation.Setting, Value: operation.Value}
		if operation.Type == core.ConfigDisplayUnset {
			change.Operation = "unset"
		}
		return displayCommitSubject(change)
	}
	return "workbook: change the board's display settings"
}

// staleDisplayWrite refuses a save composed against a configuration that has
// since moved.
//
// It names the configuration rather than the statuses, because either half of it
// may be what moved: the two live in one ledger under one tip, so a teammate's
// `workbook status add` is exactly as much a reason for this refusal as their
// `workbook config set primary-color` is.
func staleDisplayWrite(expected string) error {
	if expected == "" {
		return core.Errorf(core.CategoryStaleWrite,
			"this project's configuration has been recorded since these settings were composed; reload and try again")
	}
	return core.Errorf(core.CategoryStaleWrite,
		"this project's configuration has changed since %s; reload and try again", expected)
}
