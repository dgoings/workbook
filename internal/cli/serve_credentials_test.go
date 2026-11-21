package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A hosted board is read by people the deployment never gave origin's
// credential to, and the warning a configuration change answers with when
// publishing fails carries Git's own words about the failure. Git leaves the
// userinfo of the remote URL in some of those words — a remote that answers a
// push with something other than Git's protocol gets "no DAV locking support on
// <url>" — so a token kept in origin's URL, which is where a server deployment
// usually keeps one, reached the browser of whoever changed a status.
//
// The remote here is that kind of server: it answers every request with the
// same short line of plain text. origin starts out as a real remote and is repointed only once the board
// has published to it, which is how a deployment meets this: the board has
// already confirmed which project origin holds, so the next change goes
// straight to the push. The warning must still say that publishing failed and
// name the remote, and must not carry the token.
func TestRunServeKeepsOriginCredentialsOutOfPublishingWarnings(t *testing.T) {
	t.Parallel()
	notGit := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		// Plain text that is not a ref listing. Git reads it as a dumb HTTP
		// remote with no refs, falls back to pushing over WebDAV, and fails
		// there with the message that names the URL credentials and all.
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("not a Git server"))
	}))
	t.Cleanup(notGit.Close)
	host := strings.TrimPrefix(notGit.URL, "http://")
	address := func(credential string) string { return "http://" + credential + "@" + host + "/team/repo.git" }

	// Each way of keeping a credential where Git prints it, including the
	// passwords the userinfo pattern alone cannot bound: a quote, a slash, an
	// unencoded @.
	cases := []struct {
		name, secret string
		repoint      func(t *testing.T, repository string)
	}{
		{name: "token", secret: "s3cr3t-token", repoint: func(t *testing.T, repository string) {
			cliGit(t, repository, "remote", "set-url", "origin", address("reviewer:s3cr3t-token"))
		}},
		{name: "apostrophe", secret: "SECRETTOKEN", repoint: func(t *testing.T, repository string) {
			cliGit(t, repository, "remote", "set-url", "origin", address("deploy-bot:it's-SECRETTOKEN"))
		}},
		{name: "double quote", secret: "SECRETD", repoint: func(t *testing.T, repository string) {
			cliGit(t, repository, "remote", "set-url", "origin", address(`reviewer:a"SECRETD`))
		}},
		{name: "slash", secret: "SECRETE", repoint: func(t *testing.T, repository string) {
			cliGit(t, repository, "remote", "set-url", "origin", address("reviewer:a/SECRETE"))
		}},
		{name: "at sign", secret: "SECRETG", repoint: func(t *testing.T, repository string) {
			cliGit(t, repository, "remote", "set-url", "origin", address("reviewer:p@ss'SECRETG"))
		}},
		{name: "pushurl", secret: "SECRETTOKEN", repoint: func(t *testing.T, repository string) {
			cliGit(t, repository, "config", "remote.origin.pushurl", address("deploy-bot:it's-SECRETTOKEN"))
		}},
		{name: "insteadOf", secret: "SECRETTOKEN", repoint: func(t *testing.T, repository string) {
			cliGit(t, repository, "remote", "set-url", "origin", "http://"+host+"/team/repo.git")
			cliGit(t, repository, "config", "url."+address("deploy-bot:it's-SECRETTOKEN")+".insteadOf", "http://"+host+"/team/repo.git")
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, _ := cliSetupPublishedProject(t)
			addr := startServeBoard(t, repository)
			board := "http://" + addr

			// Inline, so each change is published by its own request and its
			// warning is the one that request's answer carries, whether or
			// not a watcher answers.
			if body, status := boardRequest(t, http.MethodPut, board+"/api/sync", `{"mode":"inline"}`); status != http.StatusOK {
				t.Fatalf("PUT /api/sync = %d: %s", status, body)
			}
			if warnings, body := addBoardStatus(t, board, "triage"); warnings["auto-sync-incomplete"] != "" {
				t.Fatalf("the first status change, against a working origin, did not publish: %s", body)
			}

			test.repoint(t, repository)
			warnings, body := addBoardStatus(t, board, "parked")
			publishing := warnings["auto-sync-incomplete"]
			if publishing == "" {
				t.Fatalf("the status change answered %s, want a warning that publishing it failed", body)
			}
			for _, secret := range []string{test.secret, "reviewer:", "deploy-bot"} {
				if strings.Contains(string(body), secret) {
					t.Fatalf("the status change's answer carries origin's credential %q: %s", secret, body)
				}
			}
			if !strings.Contains(publishing, host) {
				t.Fatalf("publishing warning = %q, want the remote at %s still named", publishing, host)
			}
		})
	}
}

// `workbook sync --json` reports Git's words about a failed fetch and a failed
// push, and a credential configured in any of the addresses Git uses must not
// survive into either.
func TestSyncKeepsConfiguredCredentialsOutOfItsReport(t *testing.T) {
	t.Parallel()
	notGit := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("not a Git server"))
	}))
	t.Cleanup(notGit.Close)
	host := strings.TrimPrefix(notGit.URL, "http://")
	address := func(credential string) string { return "http://" + credential + "@" + host + "/team/repo.git" }
	cases := []struct {
		name, secret string
		configure    [][]string
	}{
		{name: "apostrophe", secret: "SECRETTOKEN", configure: [][]string{{"remote", "set-url", "origin", address("deploy-bot:it's-SECRETTOKEN")}}},
		{name: "double quote", secret: "SECRETD", configure: [][]string{{"remote", "set-url", "origin", address(`reviewer:a"SECRETD`)}}},
		{name: "slash on fetch", secret: "SECRETE", configure: [][]string{{"remote", "set-url", "origin", address("reviewer:a/SECRETE")}}},
		{name: "at sign", secret: "SECRETG", configure: [][]string{{"remote", "set-url", "origin", address("reviewer:p@ss'SECRETG")}}},
		{name: "pushurl", secret: "SECRETTOKEN", configure: [][]string{{"config", "remote.origin.pushurl", address("deploy-bot:it's-SECRETTOKEN")}}},
		{name: "insteadOf", secret: "SECRETTOKEN", configure: [][]string{
			{"remote", "set-url", "origin", "http://" + host + "/team/repo.git"},
			{"config", "url." + address("deploy-bot:it's-SECRETTOKEN") + ".insteadOf", "http://" + host + "/team/repo.git"},
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository, _ := cliSetupPublishedProject(t)
			for _, args := range test.configure {
				cliGit(t, repository, args...)
			}
			// The change publishes automatically, and fails to.
			_, createdOut, createdErr := run(t, repository, "create", "Unpublished change")
			for _, secret := range []string{test.secret, "reviewer:", "deploy-bot"} {
				if strings.Contains(createdOut+createdErr, secret) {
					t.Fatalf("create's report carries the credential %q:\nstdout: %s\nstderr: %s", secret, createdOut, createdErr)
				}
			}
			// A failed task push says only how many outcomes Git returned, so
			// not every case names the remote; none may carry its credential.
			code, stdout, stderr := run(t, repository, "sync", "--json")
			if code == 0 {
				t.Fatalf("sync against a remote that is not Git succeeded: %s", stdout)
			}
			for _, secret := range []string{test.secret, "reviewer:", "deploy-bot"} {
				if strings.Contains(stdout+stderr, secret) {
					t.Fatalf("sync's report carries the credential %q:\nstdout: %s\nstderr: %s", secret, stdout, stderr)
				}
			}
		})
	}
}

// addBoardStatus adds a status through the board against the head it reads
// first, and returns the answer's warnings by code along with the answer.
func addBoardStatus(t *testing.T, board, status string) (map[string]string, []byte) {
	t.Helper()
	body, code := boardRequest(t, http.MethodGet, board+"/api/vocabulary", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/vocabulary = %d: %s", code, body)
	}
	var vocabulary struct {
		Head string `json:"head"`
	}
	if err := json.Unmarshal(body, &vocabulary); err != nil {
		t.Fatalf("decode the vocabulary: %v\n%s", err, body)
	}
	addition, err := json.Marshal(map[string]any{
		"status": status, "label": status, "tags": []string{}, "expectedHead": vocabulary.Head,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, code = boardRequest(t, http.MethodPost, board+"/api/vocabulary/statuses", string(addition))
	if code != http.StatusOK {
		t.Fatalf("POST /api/vocabulary/statuses = %d: %s", code, body)
	}
	var answer struct {
		Warnings []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("decode the status change: %v\n%s", err, body)
	}
	warnings := make(map[string]string, len(answer.Warnings))
	for _, warning := range answer.Warnings {
		warnings[warning.Code] = warning.Message
	}
	return warnings, body
}
