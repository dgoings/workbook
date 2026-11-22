package core

import "testing"

// A conflict names the remote it was met against. One met against origin
// carries no remote and reads exactly as it always did, so a project that
// shares its tasks through origin prints the same words.
func TestConflictWordingNamesTheRemoteInvolved(t *testing.T) {
	t.Parallel()
	tombstone := &TombstoneConflict{OperationID: "01M", Operation: OperationFieldSet, Field: "title"}
	for _, test := range []struct {
		conflict Conflict
		want     string
	}{
		{Conflict{Type: ConflictDescription}, "the description changed here and on origin; reapply the wanted text"},
		{Conflict{Type: ConflictDescription, Remote: "tasks"}, "the description changed here and on tasks; reapply the wanted text"},
		{Conflict{Type: ConflictTombstone, Tombstone: tombstone}, "origin tombstoned this task, so the local field.set operation cannot apply"},
		{Conflict{Type: ConflictTombstone, Tombstone: tombstone, Remote: "tasks"}, "tasks tombstoned this task, so the local field.set operation cannot apply"},
	} {
		if got := ConflictDetail(test.conflict); got != test.want {
			t.Errorf("ConflictDetail(%+v) = %q, want %q", test.conflict, got, test.want)
		}
	}
	for _, test := range []struct {
		conflict ConfigConflict
		want     string
	}{
		{ConfigConflict{Type: ConfigConflictStatusRename, Ours: "a", Theirs: "b"}, "this status was renamed to a here and to b on origin"},
		{ConfigConflict{Type: ConfigConflictStatusRename, Ours: "a", Theirs: "b", Remote: "tasks"}, "this status was renamed to a here and to b on tasks"},
		{ConfigConflict{Type: ConfigConflictDisplaySetting, Remote: "tasks"}, "this display setting was changed here and on tasks"},
	} {
		if got := ConfigConflictDetail(test.conflict); got != test.want {
			t.Errorf("ConfigConflictDetail(%+v) = %q, want %q", test.conflict, got, test.want)
		}
	}
	if ConflictRemote(DefaultTasksRemote) != "" || ConflictRemote("tasks") != "tasks" {
		t.Fatal("ConflictRemote must leave origin out and name any other remote")
	}
}
