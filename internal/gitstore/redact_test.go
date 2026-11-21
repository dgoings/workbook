package gitstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testenv"
)

func TestRedactURLCredentialsDropsUserinfoFromEveryURL(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, input, want string }{
		{
			name:  "user and password",
			input: "error: no DAV locking support on http://reviewer:s3cr3t@git.example/team/repo.git/",
			want:  "error: no DAV locking support on http://git.example/team/repo.git/",
		},
		{
			// GitHub and others accept a token as the user name alone, so a
			// userinfo with no password is as much a secret as one with.
			name:  "token as the user name",
			input: "fatal: unable to access 'https://ghp_abc123@github.com/o/r.git/'",
			want:  "fatal: unable to access 'https://github.com/o/r.git/'",
		},
		{
			name:  "several URLs in one message",
			input: "a https://u:p@one.example/x and ssh://git@two.example:22/y",
			want:  "a https://one.example/x and ssh://two.example:22/y",
		},
		{
			name:  "a password holding punctuation",
			input: "push to https://bot:p%40ss:w;rd!@host.example/r.git failed",
			want:  "push to https://host.example/r.git failed",
		},
		{
			name:  "no credentials",
			input: "fatal: '/srv/git/repo.git' does not appear to be a git repository; scp-style git@host.example:o/r.git",
			want:  "fatal: '/srv/git/repo.git' does not appear to be a git repository; scp-style git@host.example:o/r.git",
		},
		{
			name:  "an email address is not a URL",
			input: "Author: sam@example.com",
			want:  "Author: sam@example.com",
		},
	}
	for _, test := range cases {
		if got := redactURLCredentials(test.input); got != test.want {
			t.Errorf("%s: redactURLCredentials(%q) = %q, want %q", test.name, test.input, got, test.want)
		}
	}
}

// Git anonymizes the remote URL in most of what it prints, but not all of it:
// a remote that answers a push with something other than Git's protocol — a
// misconfigured proxy, a login page — gets "no DAV locking support on <url>"
// with the userinfo left in. Every Git failure this package reports carries
// Git's stderr, and the board shows those reports to whoever made the change,
// so a token in origin's URL must not survive into one.
func TestGitFailureNeverCarriesURLCredentials(t *testing.T) {
	t.Parallel()
	repository, _ := writeRepository(t)
	fakeGit := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\n" +
		"printf 'XML error: syntax error\\n' >&2\n" +
		"printf 'error: no DAV locking support on http://reviewer:s3cr3t-token@127.0.0.1:9/team/repo.git/\\n' >&2\n" +
		"printf \"error: failed to push some refs to 'http://127.0.0.1:9/team/repo.git'\\n\" >&2\n" +
		"exit 1\n"
	testenv.WriteExecutable(t, fakeGit, []byte(script))
	repository.gitPath = fakeGit

	_, err := repository.Git(context.Background(), nil, "push", "http://reviewer:s3cr3t-token@127.0.0.1:9/team/repo.git", "HEAD")
	if err == nil {
		t.Fatal("Git() error = nil, want the fake push's failure")
	}
	assertNoURLCredentials(t, "Git() error", err.Error())
	if !strings.Contains(err.Error(), "no DAV locking support on http://127.0.0.1:9/team/repo.git/") {
		t.Fatalf("Git() error = %q, want Git's stderr kept with only the credentials removed", err)
	}

	// The configuration ledger's refusal reads stderr itself rather than
	// through the error, so it is asked separately.
	result := repository.gitWithEnvResult(context.Background(), nil, nil, "push", "--porcelain", "origin", "HEAD")
	reason := pushRefusalReason(result)
	assertNoURLCredentials(t, "pushRefusalReason()", reason)
	if !strings.Contains(reason, "127.0.0.1:9/team/repo.git") {
		t.Fatalf("pushRefusalReason() = %q, want the remote still named", reason)
	}
}

func assertNoURLCredentials(t *testing.T, what, text string) {
	t.Helper()
	for _, secret := range []string{"s3cr3t-token", "reviewer:"} {
		if strings.Contains(text, secret) {
			t.Fatalf("%s = %q, which still carries %q", what, text, secret)
		}
	}
}
