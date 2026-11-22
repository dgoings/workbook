package gitstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
			// Git's own anonymizer cuts at the first @ and leaves the rest of
			// such a password; the pattern takes the userinfo to its last @.
			name:  "a password holding an unencoded @",
			input: "fatal: unable to access 'https://u:p@ss@h.example/r.git/' and https://ss@h.example/r.git",
			want:  "fatal: unable to access 'https://h.example/r.git/' and https://h.example/r.git",
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

// The pattern cannot tell where a userinfo holding a quote or a slash ends, and
// Git prints such userinfo whole: its anonymizer gives up on a slash, and "no
// DAV locking support on <url>" is never anonymized. So the credentials Git is
// configured with are also removed by their exact text — the whole userinfo
// where it follows "://", and on their own when they are long enough not to be
// ordinary words the password, and the user name of an address that has no
// password.
func TestCredentialRedactorRemovesConfiguredCredentialsByTheirText(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, address, input, want string }{
		{
			name:    "a password holding an apostrophe",
			address: "http://deploy-bot:it's-SECRETTOKEN@127.0.0.1:7493/team/repo.git",
			input:   "error: no DAV locking support on http://deploy-bot:it's-SECRETTOKEN@127.0.0.1:7493/team/repo.git/",
			want:    "error: no DAV locking support on http://127.0.0.1:7493/team/repo.git/",
		},
		{
			name:    "a password holding a double quote",
			address: `http://reviewer:a"SECRETD@127.0.0.1:7493/team/repo.git`,
			input:   `error: no DAV locking support on http://reviewer:a"SECRETD@127.0.0.1:7493/team/repo.git/`,
			want:    "error: no DAV locking support on http://127.0.0.1:7493/team/repo.git/",
		},
		{
			name:    "a password holding a slash",
			address: "http://reviewer:a/SECRETE@127.0.0.1:7493/team/repo.git",
			input:   "fatal: unable to access 'http://reviewer:a/SECRETE@127.0.0.1:7493/team/repo.git/': URL rejected: Port number was not a decimal number",
			want:    "fatal: unable to access 'http://127.0.0.1:7493/team/repo.git/': URL rejected: Port number was not a decimal number",
		},
		{
			// Git drops the password and prints the user name alone as the
			// address's userinfo. A quote in the user name is a userinfo the
			// pattern cannot see the end of, so only its configured text
			// removes it.
			name:    "a user name holding an apostrophe, printed without its password",
			address: "http://o'brien:SECRETPASSWORD@127.0.0.1:7493/team/repo.git",
			input:   "fatal: unable to access 'http://o'brien@127.0.0.1:7493/team/repo.git/': The requested URL returned error: 401",
			want:    "fatal: unable to access 'http://127.0.0.1:7493/team/repo.git/': The requested URL returned error: 401",
		},
		{
			name:    "a password holding an @, as Git anonymizes it",
			address: "http://reviewer:p@ss'SECRETG@127.0.0.1:7493/team/repo.git",
			input:   "fatal: unable to access 'http://ss'SECRETG@127.0.0.1:7493/team/repo.git/': URL rejected: Bad hostname",
			want:    "fatal: unable to access 'http://127.0.0.1:7493/team/repo.git/': URL rejected: Bad hostname",
		},
		{
			// A token sent as the user name beside a placeholder password is
			// still the secret, and goes wherever it is printed.
			name:    "a token as the user name beside x-oauth-basic",
			address: "https://ghp_SECRETTOKEN123:x-oauth-basic@github.com/team/repo.git",
			input:   "remote: ghp_SECRETTOKEN123 expired",
			want:    "remote: *** expired",
		},
		{
			name:    "a token as the user name beside an empty password",
			address: "https://ghp_EMPTYPASSTOKEN9:@github.com/team/repo.git",
			input:   "remote: ghp_EMPTYPASSTOKEN9 bad",
			want:    "remote: *** bad",
		},
		{
			name:    "a token as the user name beside a one-letter password",
			address: "https://ghp_SHORTPASSTOKEN9:x@github.com/team/repo.git",
			input:   "remote: ghp_SHORTPASSTOKEN9 bad",
			want:    "remote: *** bad",
		},
		{
			// Beside a password, the user name is a name rather than the
			// secret, and only the password is removed on its own.
			name:    "a password outside its URL",
			address: "https://deploy-bot:SECRETTOKEN@git.example/team/repo.git",
			input:   "remote: token SECRETTOKEN is expired; deploy-bot must sign in again",
			want:    "remote: token *** is expired; deploy-bot must sign in again",
		},
		{
			// The reviewer's case: a user named for the tool beside a password.
			// Every failure names refs/workbook/... and the path holds the word
			// too; neither is a credential and neither may be blanked.
			name:    "a user name that is an ordinary word, beside a password",
			address: "http://workbook:SECRETPASSWORD@127.0.0.1:7493/workbook/repo.git",
			input:   "git ls-remote --refs origin refs/workbook/tasks/* refs/workbook/project failed: exit status 128: fatal: unable to access 'http://workbook:SECRETPASSWORD@127.0.0.1:7493/workbook/repo.git/': Failed to connect; SECRETPASSWORD",
			want:    "git ls-remote --refs origin refs/workbook/tasks/* refs/workbook/project failed: exit status 128: fatal: unable to access 'http://127.0.0.1:7493/workbook/repo.git/': Failed to connect; ***",
		},
		{
			// Git may print the address with the password dropped and the user
			// name kept; that userinfo still goes, inside its address.
			name:    "a user name printed as an address's userinfo without its password",
			address: "http://workbook:SECRETPASSWORD@127.0.0.1:7493/workbook/repo.git",
			input:   "error: no DAV locking support on http://workbook@127.0.0.1:7493/workbook/repo.git/",
			want:    "error: no DAV locking support on http://127.0.0.1:7493/workbook/repo.git/",
		},
		{
			// With no password, the user name is often the token itself, and it
			// is removed wherever it is printed.
			name:    "a token as the whole userinfo",
			address: "https://ghp_SECRETTOKEN123@git.example/team/repo.git",
			input:   "remote: ghp_SECRETTOKEN123 has expired",
			want:    "remote: *** has expired",
		},
		{
			name:    "a percent-encoded password, printed decoded",
			address: "https://bot:SECRET%2FTOKEN@git.example/team/repo.git",
			input:   "remote: SECRET/TOKEN refused",
			want:    "remote: *** refused",
		},
		{
			name:    "an @ in the path is not a userinfo",
			address: "https://git.example/team/@scope/repo.git",
			input:   "fatal: repository 'https://git.example/team/@scope/repo.git/' not found",
			want:    "fatal: repository 'https://git.example/team/@scope/repo.git/' not found",
		},
		{
			name:    "an @ in the path after a port is not a userinfo",
			address: "https://git.example:8443/team/@scope/repo.git",
			input:   "fatal: repository 'https://git.example:8443/team/@scope/repo.git/' not found",
			want:    "fatal: repository 'https://git.example:8443/team/@scope/repo.git/' not found",
		},
		{
			// A short user name is an ordinary word in Git's messages and is
			// left alone outside its URL.
			name:    "ordinary messages",
			address: "ssh://git@git.example/team/repo.git",
			input:   "fatal: git-http-push failed; Author: sam@example.com; scp-style git@host.example:o/r.git",
			want:    "fatal: git-http-push failed; Author: sam@example.com; scp-style git@host.example:o/r.git",
		},
	}
	for _, test := range cases {
		redactor := newCredentialRedactor([]string{test.address})
		if got := redactor.redact(test.input); got != test.want {
			t.Errorf("%s: redact(%q) = %q, want %q", test.name, test.input, got, test.want)
		}
	}
}

// A remote's address is not the only place a credential is configured: a push
// URL, and a url.<base>.insteadOf or pushInsteadOf rewrite whose base carries
// the userinfo, are addresses Git connects to and prints as well.
func TestConfiguredAddressesIncludeRewritesAndPushURLs(t *testing.T) {
	t.Parallel()
	listing := strings.Join([]string{
		"remote.origin.url\nhttps://git.example/team/repo.git",
		"remote.origin.pushurl\nhttps://pusher:PUSHSECRET@git.example/team/repo.git",
		"remote.mirror.url\ngh:team/repo.git",
		"url.https://bot:REWRITESECRET@git.example/.insteadof\nhttps://git.example/",
		"url.https://u:PREFIXSECRET@.pushinsteadof\ngh:",
		"",
	}, "\x00")
	got := configuredAddresses(listing)
	want := []string{
		"https://git.example/team/repo.git",
		"https://pusher:PUSHSECRET@git.example/team/repo.git",
		"gh:team/repo.git",
		"https://bot:REWRITESECRET@git.example/",
		"https://git.example/",
		"https://bot:REWRITESECRET@git.example/team/repo.git",
		"https://u:PREFIXSECRET@",
		"gh:",
		"https://u:PREFIXSECRET@team/repo.git",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configuredAddresses() =\n%q\nwant\n%q", got, want)
	}
}

// Every way of configuring a credential the reviewer leaked through, run
// through real Git against a remote that answers plain text, on the push path
// the board's warning carries and on the fetch path.
func TestGitFailureRedactsConfiguredCredentials(t *testing.T) {
	t.Parallel()
	notGit := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("not a Git server"))
	}))
	t.Cleanup(notGit.Close)
	host := strings.TrimPrefix(notGit.URL, "http://")
	plain := "http://" + host + "/team/repo.git"
	passwords := map[string]string{
		"apostrophe":   "it's-SECRETA",
		"double quote": `a"SECRETD`,
		"slash":        "a/SECRETE",
		"at sign":      "p@ss'SECRETG",
	}
	setups := map[string]func(t *testing.T, repository *Repository, credential string){
		"url": func(t *testing.T, repository *Repository, credential string) {
			gitstoreGit(t, repository, "remote", "add", "origin", "http://"+credential+"@"+host+"/team/repo.git")
		},
		"pushurl": func(t *testing.T, repository *Repository, credential string) {
			gitstoreGit(t, repository, "remote", "add", "origin", plain)
			gitstoreGit(t, repository, "config", "remote.origin.pushurl", "http://"+credential+"@"+host+"/team/repo.git")
		},
		"insteadOf": func(t *testing.T, repository *Repository, credential string) {
			gitstoreGit(t, repository, "remote", "add", "origin", plain)
			gitstoreGit(t, repository, "config", "url.http://"+credential+"@"+host+"/.insteadOf", "http://"+host+"/")
		},
		"pushInsteadOf": func(t *testing.T, repository *Repository, credential string) {
			gitstoreGit(t, repository, "remote", "add", "origin", plain)
			gitstoreGit(t, repository, "config", "url.http://"+credential+"@"+host+"/.pushInsteadOf", "http://"+host+"/")
		},
	}
	for setupName, setup := range setups {
		for passwordName, password := range passwords {
			t.Run(setupName+"/"+passwordName, func(t *testing.T) {
				t.Parallel()
				repository, _ := writeRepository(t)
				// Something to push, so the push reaches the remote and fails
				// there rather than on a missing source ref.
				gitstoreGit(t, repository, "-c", "user.email=a@example.com", "-c", "user.name=A", "commit", "--quiet", "--allow-empty", "-m", "x")
				credential := "deploy-bot:" + password
				setup(t, repository, credential)
				secret := password[strings.LastIndex(password, "SECRET"):]

				push := repository.gitWithEnvResult(context.Background(), nil, nil, "push", "--porcelain", "origin", "HEAD:refs/workbook/config")
				if push.err == nil {
					t.Fatal("push to a remote that is not Git succeeded")
				}
				reason := pushRefusalReason(push)
				if strings.Contains(reason, secret) || strings.Contains(reason, "deploy-bot") {
					t.Fatalf("push refusal = %q, which still carries the credential", reason)
				}
				if !strings.Contains(reason, host) {
					t.Fatalf("push refusal = %q, want the remote at %s still named", reason, host)
				}
				if strings.Contains(reason, "src refspec") {
					t.Fatalf("push refusal = %q, which failed before reaching the remote", reason)
				}

				_, err := repository.Git(context.Background(), nil, "fetch", "origin")
				if err != nil && (strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "deploy-bot")) {
					t.Fatalf("fetch error = %q, which still carries the credential", err)
				}
			})
		}
	}
}

// The fetch path on its own, with the one password Git prints there: a slash
// in the password makes Git's URL unusable and its anonymizer give up, so the
// fetch fails naming the whole address.
func TestGitFetchFailureRedactsAPasswordHoldingASlash(t *testing.T) {
	t.Parallel()
	repository, _ := writeRepository(t)
	gitstoreGit(t, repository, "remote", "add", "origin", "http://reviewer:a/SECRETE@127.0.0.1:9/team/repo.git")
	_, err := repository.Git(context.Background(), nil, "fetch", "origin")
	if err == nil {
		t.Fatal("fetch from an unusable address succeeded")
	}
	if strings.Contains(err.Error(), "SECRETE") {
		t.Fatalf("fetch error = %q, which still carries the password", err)
	}
	if !strings.Contains(err.Error(), "unable to access 'http://127.0.0.1:9/team/repo.git/'") {
		t.Fatalf("fetch error = %q, want Git's message kept with the address named", err)
	}
}

// An object batch reports Git's stderr when it fails, and a partial clone's
// batch fetches what it lacks from its promisor remote, so that stderr can name
// the remote, credential and all.
func TestObjectBatchFailureRedactsConfiguredCredentials(t *testing.T) {
	t.Parallel()
	repository, _ := writeRepository(t)
	directory := t.TempDir()
	fakeGit := filepath.Join(directory, "git")
	// The address holds a quote, so the script reads it from a file rather
	// than from its own text.
	addressFile := filepath.Join(directory, "address")
	if err := os.WriteFile(addressFile, []byte("http://deploy-bot:it's-SECRETTOKEN@127.0.0.1:9/team/repo.git"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"ADDRESS=$(cat '" + addressFile + "')\n" +
		"for argument in \"$@\"; do\n" +
		"  if [ \"$argument\" = config ]; then printf 'remote.origin.url\\n%s\\000' \"$ADDRESS\"; exit 0; fi\n" +
		"done\n" +
		"cat >/dev/null\n" +
		"printf 'error: no DAV locking support on %s/\\n' \"$ADDRESS\" >&2\n" +
		"exit 1\n"
	testenv.WriteExecutable(t, fakeGit, []byte(script))
	repository.gitPath = fakeGit

	batch, err := repository.startObjectBatch(context.Background(), func(writer io.Writer) error {
		_, err := io.WriteString(writer, strings.Repeat("0", 40)+"\n")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Close()
	err = batch.Finish()
	if err == nil {
		t.Fatal("Finish() = nil, want the fake batch's failure")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") || strings.Contains(err.Error(), "deploy-bot") {
		t.Fatalf("batch error = %q, which still carries the credential", err)
	}
	if !strings.Contains(err.Error(), "no DAV locking support on http://127.0.0.1:9/team/repo.git/") {
		t.Fatalf("batch error = %q, want Git's stderr kept with only the credential removed", err)
	}
}

func gitstoreGit(t *testing.T, repository *Repository, args ...string) {
	t.Helper()
	if _, err := repository.Git(context.Background(), nil, args...); err != nil {
		t.Fatal(err)
	}
}
