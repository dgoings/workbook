package gitstore

import (
	"bytes"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The tasks remote is an optional member of the version 2 document: a file
// that does not name one is exactly what it was before the member existed,
// and a file that names one round-trips byte for byte.
func TestTasksRemoteMemberRoundTripsAndIsAbsentWhenUnset(t *testing.T) {
	t.Parallel()
	unset := []byte(`{"format":"workbook.project","version":2,"projectId":"` + fixedProjectID + `","key":"WB"}` + "\n")
	config, err := decodeConfig(unset)
	if err != nil {
		t.Fatal(err)
	}
	if config.TasksRemote.Chosen() || config.TasksRemote.RemoteName() != "origin" {
		t.Fatalf("TasksRemote = %+v, want origin by default", config.TasksRemote)
	}
	if encoded, err := encodeConfig(config); err != nil || !bytes.Equal(encoded, unset) {
		t.Fatalf("encodeConfig() = %q, %v; want %q", encoded, err, unset)
	}

	set := []byte(`{"format":"workbook.project","version":2,"projectId":"` + fixedProjectID +
		`","key":"WB","tasksRemote":{"name":"tasks","url":"git@example.test:team/tasks.git"}}` + "\n")
	config, err = decodeConfig(set)
	if err != nil {
		t.Fatal(err)
	}
	if config.TasksRemote != (core.TasksRemoteSetting{Name: "tasks", URL: "git@example.test:team/tasks.git"}) {
		t.Fatalf("TasksRemote = %+v", config.TasksRemote)
	}
	if encoded, err := encodeConfig(config); err != nil || !bytes.Equal(encoded, set) {
		t.Fatalf("encodeConfig() = %q, %v; want %q", encoded, err, set)
	}
}

// A document that carries a tasks remote no Workbook may use is refused as
// corrupt rather than read as origin.
func TestDecodeConfigRefusesAnUnusableTasksRemote(t *testing.T) {
	t.Parallel()
	for name, member := range map[string]string{
		"version one":         `"version":1,"projectId":"` + fixedProjectID + `","key":"WB","tasksRemote":{"name":"tasks","url":"/srv/tasks.git"}`,
		"origin with address": `"version":2,"projectId":"` + fixedProjectID + `","key":"WB","tasksRemote":{"name":"origin","url":"/srv/tasks.git"}`,
		"bad name":            `"version":2,"projectId":"` + fixedProjectID + `","key":"WB","tasksRemote":{"name":"a/b","url":"/srv/tasks.git"}`,
		"credential":          `"version":2,"projectId":"` + fixedProjectID + `","key":"WB","tasksRemote":{"name":"tasks","url":"https://x:y@example.test/t.git"}`,
		"unknown member":      `"version":2,"projectId":"` + fixedProjectID + `","key":"WB","tasksRemote":{"name":"tasks","url":"/srv/tasks.git","token":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeConfig([]byte(`{"format":"workbook.project",` + member + "}\n"))
			if core.CategoryOf(err) != core.CategoryCorruptData {
				t.Fatalf("decodeConfig() = %v, want corrupt-data", err)
			}
		})
	}
}
