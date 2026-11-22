package core

import "testing"

func TestValidateTasksRemoteName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"tasks", "Tasks_2", "team.tasks", "_t", "a-b"} {
		if err := ValidateTasksRemoteName(name); err != nil {
			t.Errorf("ValidateTasksRemoteName(%q) = %v, want accepted", name, err)
		}
	}
	for _, name := range []string{"", "-tasks", ".tasks", "a/b", "a..b", "tasks.lock", "tasks.", "a b", "a:b", "a*b"} {
		if err := ValidateTasksRemoteName(name); CategoryOf(err) != CategoryValidation {
			t.Errorf("ValidateTasksRemoteName(%q) = %v, want a validation error", name, err)
		}
	}
}

func TestValidateTasksRemoteURLRefusesCredentials(t *testing.T) {
	t.Parallel()
	for _, address := range []string{
		"git@github.com:team/tasks.git",
		"ssh://git@github.com/team/tasks.git",
		"https://github.com/team/tasks.git",
		"/srv/git/tasks.git",
		"file:///srv/git/tasks.git",
	} {
		if err := ValidateTasksRemoteURL(address); err != nil {
			t.Errorf("ValidateTasksRemoteURL(%q) = %v, want accepted", address, err)
		}
	}
	for _, address := range []string{
		"",
		" https://github.com/team/tasks.git",
		"https://github.com/team/tasks.git\n",
		"https://token@github.com/team/tasks.git",
		"https://user:secret@github.com/team/tasks.git",
		"ssh://git:secret@github.com/team/tasks.git",
		"--upload-pack=touch /tmp/x",
		"https://github.com/team/ta sks.git",
	} {
		if err := ValidateTasksRemoteURL(address); CategoryOf(err) != CategoryValidation {
			t.Errorf("ValidateTasksRemoteURL(%q) = %v, want a validation error", address, err)
		}
	}
}
