package core

import (
	"fmt"
	"strconv"
	"strings"
)

// RelicConflictType discriminates the situations that stop a relic's replay.
// It is a union of its own, beside the task and configuration unions, because
// a relic conflict is reported against a relic and a part.
type RelicConflictType string

const (
	// RelicConflictBody reports a part whose body was saved here and on the
	// remote from the same version, to different bytes. Neither text is
	// dropped: the remote's stands and this clone's is parked.
	RelicConflictBody RelicConflictType = "body"
	// RelicConflictTombstone reports local changes to a relic the remote had
	// already deleted.
	RelicConflictTombstone RelicConflictType = "tombstone"
)

// RelicBodyVersion is one version of a part's body in a conflict report: its
// version, label, size and blob, and its content as exactly one of Text and
// Base64 when the content was read. See SetRelicBodyVersionContent.
type RelicBodyVersion struct {
	Version string  `json:"version"`
	Format  string  `json:"format,omitempty"`
	Bytes   int64   `json:"bytes"`
	Object  string  `json:"object"`
	Text    *string `json:"text,omitempty"`
	Base64  *string `json:"base64,omitempty"`
}

// RelicBodyConflict carries the three bodies of a body conflict. Base is the
// part's body at the commit both histories share, nil when the part had none
// there; Ours is the body at the parked local tip; Theirs is the body at the
// fetched tip, which is the one that stands.
type RelicBodyConflict struct {
	Base   *RelicBodyVersion `json:"base,omitempty"`
	Ours   RelicBodyVersion  `json:"ours"`
	Theirs RelicBodyVersion  `json:"theirs"`
}

// RelicTombstoneConflict names the local operation that could not apply to a
// relic the fetched history had already deleted.
type RelicTombstoneConflict struct {
	OperationID string             `json:"operationId"`
	Operation   RelicOperationType `json:"operation"`
}

// RelicUnappliedOperation is a local operation a stopped replay did not
// apply, other than the one its conflict names. Commit is the local commit
// whose operation.json holds it.
type RelicUnappliedOperation struct {
	OperationID string             `json:"operationId"`
	Operation   RelicOperationType `json:"operation"`
	Commit      string             `json:"commit"`
}

// RelicConflict is one relic whose replay stopped. ParkedRef names the ref
// that keeps the local tip, and Unreplayed counts the local packs that were
// not replayed. Unapplied lists, oldest first, every operation of those packs
// other than the one the conflict names and those the remote already holds.
// Remote names the remote when it is not origin.
type RelicConflict struct {
	RelicID    string                    `json:"relicId"`
	Type       RelicConflictType         `json:"type"`
	Part       string                    `json:"part,omitempty"`
	Body       *RelicBodyConflict        `json:"body,omitempty"`
	Tombstone  *RelicTombstoneConflict   `json:"tombstone,omitempty"`
	ParkedRef  string                    `json:"parkedRef,omitempty"`
	Unreplayed int                       `json:"unreplayed"`
	Unapplied  []RelicUnappliedOperation `json:"unapplied,omitempty"`
	Remote     string                    `json:"remote,omitempty"`
}

// SetRelicBodyVersionContent fills in a version's content as a relic document
// carries a body: the text when the bytes are valid UTF-8, their base64 when
// they are not. It replaces whatever content the version held.
func SetRelicBodyVersionContent(version *RelicBodyVersion, content []byte) {
	version.Text, version.Base64 = relicBodyTextOrBase64(content)
}

// RelicConflictError summarizes a relic conflict list as the command's
// failure. The envelope carries each conflict in full.
func RelicConflictError(conflicts []RelicConflict) error {
	if len(conflicts) == 1 {
		return Errorf(CategoryConflict, "%s", RelicConflictDetail(conflicts[0]))
	}
	return Errorf(CategoryConflict,
		"%d relic(s) need a decision before their local changes can be replayed", len(conflicts))
}

// RelicConflictDetail renders one relic conflict as a single line that names
// the relic, says what happened and what to do. It never holds body text.
func RelicConflictDetail(conflict RelicConflict) string {
	relic := "relic " + conflict.RelicID + ": "
	remote := conflictRemote(conflict.Remote)
	switch conflict.Type {
	case RelicConflictBody:
		return relic + relicBodyConflictDetail(conflict, remote) + relicUnappliedDetail(conflict, "saving the body")
	case RelicConflictTombstone:
		detail := remote + " deleted this relic, so the local changes cannot apply"
		if conflict.Tombstone != nil {
			detail = fmt.Sprintf("%s deleted this relic, so the local %s operation %s cannot apply",
				remote, conflict.Tombstone.Operation, conflict.Tombstone.OperationID)
		}
		if conflict.ParkedRef != "" {
			detail += "; the local changes are kept at " + conflict.ParkedRef
		}
		return relic + detail + "; to keep them, run `workbook relic restore " + conflict.RelicID +
			"` and make the changes again" + relicUnappliedDetail(conflict, "the restore")
	default:
		return relic + string(conflict.Type)
	}
}

// relicBodyConflictDetail is a body conflict's detail after the relic: the
// three commands of the recovery when the local tip is parked, and the two
// that still apply when it is not.
func relicBodyConflictDetail(conflict RelicConflict, remote string) string {
	part := conflict.Part
	detail := "part " + part + ": the body was saved here and on " + remote +
		" from the same version; the text from " + remote + " stands"
	show := "`workbook relic show " + conflict.RelicID + "#" + part + " --body > current.md`"
	if conflict.ParkedRef == "" {
		return detail + "; run " + show + " to read it, combine it with the text saved here in mine.md, then run " +
			"`workbook relic update " + conflict.RelicID + " --part " + part + " --body-file mine.md --base <version>`" +
			" with the version it shows"
	}
	theirs := "<version>"
	if conflict.Body != nil && conflict.Body.Theirs.Version != "" {
		theirs = conflict.Body.Theirs.Version
	}
	return detail + "; the text saved here is kept at " + conflict.ParkedRef +
		"; run `git cat-file blob " + conflict.ParkedRef + ":parts/" + part + "/body > mine.md` to write it, " +
		show + " to write the text that stands, combine the two in mine.md, then run " +
		"`workbook relic update " + conflict.RelicID + " --part " + part + " --body-file mine.md --base " + theirs + "`"
}

// relicUnappliedNamed bounds how many unapplied operations a detail names.
const relicUnappliedNamed = 5

// relicUnappliedDetail is the end of a detail that names the other local
// changes the replay did not apply, or "" when there are none. It names each
// by its operation type and ID, never by anything the relic holds, and says
// how to see them and to make them again after the recovery.
func relicUnappliedDetail(conflict RelicConflict, after string) string {
	unapplied := conflict.Unapplied
	if len(unapplied) == 0 {
		return ""
	}
	named := make([]string, 0, relicUnappliedNamed)
	for index, operation := range unapplied {
		if index == relicUnappliedNamed {
			break
		}
		named = append(named, string(operation.Operation)+" "+operation.OperationID)
	}
	list := strings.Join(named, ", ")
	if more := len(unapplied) - len(named); more > 0 {
		list += " and " + strconv.Itoa(more) + " more"
	}
	detail := "; " + strconv.Itoa(len(unapplied)) + " other local change(s) were not applied either: " + list
	if conflict.ParkedRef != "" {
		detail += "; they are kept at " + conflict.ParkedRef
	}
	return detail + "; `git cat-file blob " + unapplied[0].Commit + ":operation.json` shows the first, " +
		"and the report's `unapplied` list under --json gives the commit of each; make them again after " + after
}
