package cli

import (
	"strings"
	"testing"
)

// forgeOriginRelicChild puts on origin, from repository, a child of the
// relic's tip there whose operation document is not JSON, so every fetch
// reports the relic invalid and withholds it.
func forgeOriginRelicChild(t *testing.T, repository, relicID string) string {
	t.Helper()
	if code, stdout, stderr := run(t, repository, "fetch"); code != 0 {
		t.Fatalf("fetch = (%d, %q, %q)", code, stdout, stderr)
	}
	parent := relicHeadOf(t, repository, relicID)
	blob := hashObject(t, repository, "not json")
	tree := gitWithInput(t, repository, "100644 blob "+blob+"\toperation.json\n", "mktree")
	forged := cliGitOutput(t, repository, "commit-tree", tree, "-p", parent, "-m", "forged")
	cliGit(t, repository, "push", "--no-verify", "origin", forged+":"+relicRef(relicID))
	return forged
}

// A relic command whose fetch withheld the relic it changes records the change
// and does not push it: origin would refuse it, and the next sync withholds
// the relic again rather than replaying the change. The report says why.
func TestRelicCommandDoesNotPushARelicTheFetchWithheld(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	relicID := mustCreateRelic(t, first, "Withheld plan", "--type", "agenda").Relic.ID
	forged := forgeOriginRelicChild(t, second, relicID)

	envelope := runRelicJSON(t, first, 0, "update", relicID, "--title", "Renamed here")
	if envelope.Sync == nil || envelope.Sync.PushRelic == nil || envelope.Sync.PushRelic.Status != "invalid" {
		t.Fatalf("sync = %+v, want the relic reported invalid and not pushed", envelope.Sync)
	}
	if envelope.Sync.Status != "failed" || !strings.Contains(envelope.Sync.Detail, "not pushed") ||
		strings.Contains(envelope.Sync.Detail, "replays") {
		t.Fatalf("sync = %+v, want a detail saying the relic was not pushed and why", envelope.Sync)
	}
	var warned bool
	for _, warning := range envelope.Warnings {
		if strings.Contains(warning.Message, "replays") {
			t.Fatalf("warning %q promises a replay the next sync does not make", warning.Message)
		}
		if strings.Contains(warning.Message, "relic "+relicID+" was not pushed") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("warnings = %+v, want one saying the relic was not pushed", envelope.Warnings)
	}
	if got := originRelicHead(t, first, relicID); got != forged {
		t.Fatalf("origin holds %s, want the forged tip %s left alone", got, forged)
	}

	code, stdout, stderr := run(t, first, "relic", "update", relicID, "--title", "Renamed again")
	if code != 0 || strings.Contains(stdout+stderr, "replays") ||
		!strings.Contains(stderr, "relic "+relicID+" was not pushed") {
		t.Fatalf("relic update = (%d, %q, %q), want exit 0 saying the relic was not pushed", code, stdout, stderr)
	}
}

// A plain push publishes nothing for a relic whose only difference from
// origin is that origin is ahead: it has no change of its own to publish, so
// it is neither refused nor told the next sync replays local changes.
func TestPushSkipsARelicThatIsOnlyBehindOrigin(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	relicID := mustCreateRelic(t, first, "Behind plan", "--type", "agenda").Relic.ID
	if code, stdout, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = (%d, %q, %q)", code, stdout, stderr)
	}
	mustUpdateRelic(t, first, relicID, "--title", "Moved on")
	if code, stdout, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = (%d, %q, %q)", code, stdout, stderr)
	}
	// The fetch fast-forwarded the relic; put it back on the older tip, as a
	// clone whose fetch had not moved it yet would hold.
	older := cliGitOutput(t, second, "rev-parse", relicRef(relicID)+"^")
	cliGit(t, second, "update-ref", relicRef(relicID), older)

	code, stdout, stderr := run(t, second, "push")
	if code != 0 || strings.Contains(stdout+stderr, "rejected") || strings.Contains(stdout+stderr, "replays") {
		t.Fatalf("push = (%d, %q, %q), want exit 0 and nothing refused", code, stdout, stderr)
	}
}

// A plain push after a fetch withheld a relic, because origin's tip of it
// fails its check, does not push it: it names the relic and the reason, and
// does not exit as if origin had refused a publication.
func TestPushDoesNotPushARelicWhoseOriginTipFailsItsCheck(t *testing.T) {
	t.Parallel()
	for _, local := range []string{"no local change", "a local change"} {
		t.Run(local, func(t *testing.T) {
			t.Parallel()
			first, second := cliSyncRepositories(t)
			relicID := mustCreateRelic(t, first, "Forged plan", "--type", "agenda").Relic.ID
			forged := forgeOriginRelicChild(t, second, relicID)
			if local == "a local change" {
				mustUpdateRelic(t, first, relicID, "--title", "Mine", "--no-sync")
			}
			if code, stdout, stderr := run(t, first, "fetch"); code != 7 {
				t.Fatalf("fetch = (%d, %q, %q), want exit 7", code, stdout, stderr)
			}

			code, stdout, stderr := run(t, first, "push")
			output := stdout + stderr
			if strings.Contains(output, "rejected") || strings.Contains(output, "replays") {
				t.Fatalf("push = (%d, %q, %q), want no refusal and no promise of a replay", code, stdout, stderr)
			}
			switch local {
			case "no local change":
				if code != 0 {
					t.Fatalf("push = (%d, %q, %q), want exit 0: there is nothing to publish", code, stdout, stderr)
				}
			default:
				if code != 7 || !strings.Contains(output, relicID) || !strings.Contains(output, "not pushed") {
					t.Fatalf("push = (%d, %q, %q), want exit 7 naming the relic as not pushed", code, stdout, stderr)
				}
			}
			if got := originRelicHead(t, first, relicID); got != forged {
				t.Fatalf("origin holds %s, want the forged tip %s left alone", got, forged)
			}
		})
	}
}
