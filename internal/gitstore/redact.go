package gitstore

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// urlUserinfo matches the userinfo of a URL with an authority: a scheme, `://`,
// and everything up to the last `@` before a slash, whitespace or a quote. A
// password holding an unencoded `@` is matched whole, and the match cannot run
// past the URL it started in. An email address has no scheme in front of it and
// an scp-style remote such as git@host:path has no `://`, so neither is
// touched.
//
// The pattern alone cannot remove a userinfo that holds a quote or a slash: it
// cannot tell where such a userinfo ends. Those are removed by their exact
// text, read from Git's configuration; see readCredentialRedactor.
var urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s'"]*@`)

// credentialPartMinimum is the shortest user name or password removed on its
// own, wherever it appears, rather than only as part of its URL. Anything
// shorter, such as the user name "git", is an ordinary word in Git's messages
// and replacing it would mangle them.
//
// A user name is removed on its own only from an address with no password,
// where it is often the token itself (`https://ghp_…@github.com/…`). Beside a
// password it is usually an ordinary name such as `workbook`, which Git's
// messages also spell as a ref prefix (`refs/workbook/tasks/*`) or a path
// segment (`/workbook/repo.git`), so there only the password is removed on its
// own and the user name goes only with the rest of its address's userinfo.
//
// A password too short to be removed on its own cannot be the secret either,
// so its user name is treated as one; see passwordIsNotTheSecret.
const credentialPartMinimum = 8

// passwordIsNotTheSecret reports whether the password beside a user name is a
// placeholder: empty (`https://<token>:@host/…`), shorter than anything
// removed on its own, or the fixed word hosts ask for when the token travels
// as the user name (`https://<token>:x-oauth-basic@github.com/…`). The user
// name is then the credential and is removed wherever it appears.
func passwordIsNotTheSecret(password string) bool {
	return len(password) < credentialPartMinimum || password == "x-oauth-basic"
}

// credentialRedactionMark stands in for a credential found outside its URL.
const credentialRedactionMark = "***"

// gitCredentialConfig selects every configured address Git may connect to and
// so print: remotes' fetch and push URLs, and the url.<base>.insteadOf and
// pushInsteadOf rewrites, whose base is the address Git actually uses.
const gitCredentialConfig = `^(remote\..+\.(url|pushurl)|url\..+\.(insteadof|pushinsteadof))$`

// gitCredentialConfigTimeout bounds the configuration read that runs after a
// failure. The failed command's own context may already be spent — a timeout
// is a common failure — and the read must still happen.
const gitCredentialConfigTimeout = 5 * time.Second

type redaction struct {
	text        string
	replacement string
}

// credentialRedactor removes credentials from text Git printed.
//
// Its zero value removes what the userinfo pattern finds. One built by
// readCredentialRedactor or argumentCredentialRedactor also removes every
// configured or named credential by its exact text: the whole userinfo of each
// address where it follows `://`, and on its own wherever it is long enough not
// to be an ordinary word, the whole userinfo, the password, and — only for an
// address that has no password — the user name.
type credentialRedactor struct {
	redactions []redaction
}

func (c credentialRedactor) redact(text string) string {
	for _, entry := range c.redactions {
		text = strings.ReplaceAll(text, entry.text, entry.replacement)
	}
	return urlUserinfo.ReplaceAllString(text, "$1")
}

// redactURLCredentials removes the userinfo the pattern finds, without reading
// any configuration.
func redactURLCredentials(text string) string {
	return credentialRedactor{}.redact(text)
}

// argumentCredentialRedactor knows the credentials in the addresses a command
// names on its own command line, such as `git push https://u:p@host/r.git`.
// It costs no process.
func argumentCredentialRedactor(args []string) credentialRedactor {
	return newCredentialRedactor(args)
}

// readCredentialRedactor knows the credentials in every address Git is
// configured with for directory, and in the addresses args name.
//
// It is called only after a command fails with something on stderr that holds
// an `@`, which every URL with a userinfo does: that output is what an error,
// a refusal reason or a board warning carries, and Git leaves the userinfo in
// some of its messages ("no DAV locking support on <url>", and every message
// naming a URL whose userinfo holds a slash, which Git's own anonymizer gives
// up on). A successful command, or a failure that prints no `@`, costs no
// extra process. When the configuration cannot be read, the addresses args
// name and the pattern are what is left.
func readCredentialRedactor(ctx context.Context, gitPath, directory string, extraEnv, args []string) credentialRedactor {
	addresses := append([]string(nil), args...)
	readContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitCredentialConfigTimeout)
	defer cancel()
	command := exec.CommandContext(readContext, gitPath, "-C", directory, "config", "-z", "--get-regexp", gitCredentialConfig)
	command.Env = gitEnvironment(os.Environ(), extraEnv)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if command.Run() == nil {
		addresses = append(addresses, configuredAddresses(stdout.String())...)
	}
	return newCredentialRedactor(addresses)
}

// configuredAddresses reads `git config -z --get-regexp` output for
// gitCredentialConfig into every address Git may use: each remote URL as
// configured, each rewrite's base and prefix, and each remote URL as a
// rewrite turns it into.
func configuredAddresses(listing string) []string {
	type rewrite struct{ base, prefix string }
	var remotes []string
	var rewrites []rewrite
	for _, entry := range strings.Split(listing, "\x00") {
		key, value, _ := strings.Cut(entry, "\n")
		switch {
		case strings.HasPrefix(key, "remote."):
			remotes = append(remotes, value)
		case strings.HasPrefix(key, "url."):
			base := strings.TrimPrefix(key, "url.")
			if trimmed, found := strings.CutSuffix(base, ".pushinsteadof"); found {
				base = trimmed
			} else {
				base = strings.TrimSuffix(base, ".insteadof")
			}
			rewrites = append(rewrites, rewrite{base: base, prefix: value})
		}
	}
	addresses := append([]string(nil), remotes...)
	for _, rule := range rewrites {
		addresses = append(addresses, rule.base, rule.prefix)
		for _, remote := range remotes {
			if rule.prefix != "" && strings.HasPrefix(remote, rule.prefix) {
				addresses = append(addresses, rule.base+remote[len(rule.prefix):])
			}
		}
	}
	return addresses
}

func newCredentialRedactor(addresses []string) credentialRedactor {
	seen := map[string]bool{}
	var redactions []redaction
	add := func(text, replacement string) {
		if text == "" || seen[text] {
			return
		}
		seen[text] = true
		redactions = append(redactions, redaction{text: text, replacement: replacement})
	}
	addPart := func(part string) {
		if len(part) < credentialPartMinimum {
			return
		}
		add(part, credentialRedactionMark)
	}
	for _, address := range addresses {
		userinfo := addressUserinfo(address)
		if userinfo == "" {
			continue
		}
		add("://"+userinfo+"@", "://")
		// Git's own anonymizer cuts a userinfo at its first `@`, so a password
		// holding one is printed as `scheme://<rest of the password>@host`.
		for tail := userinfo; strings.Contains(tail, "@"); {
			_, tail, _ = strings.Cut(tail, "@")
			add("://"+tail+"@", "://")
			addPart(tail)
		}
		addPart(userinfo)
		user, password, hasPassword := strings.Cut(userinfo, ":")
		parts := []string{user}
		if hasPassword {
			// The user name still goes wherever it is printed as the userinfo
			// of an address, which is where Git puts it once it has dropped the
			// password; on its own it stays, see credentialPartMinimum.
			add("://"+user+"@", "://")
			parts = []string{password}
			if passwordIsNotTheSecret(password) {
				parts = []string{user, password}
			}
		}
		for _, part := range parts {
			addPart(part)
			if decoded, err := url.PathUnescape(part); err == nil {
				addPart(decoded)
			}
		}
	}
	// Longest first, so a whole userinfo goes before the user name or
	// password inside it, and one credential that contains another is removed
	// whole.
	sort.SliceStable(redactions, func(i, j int) bool {
		return len(redactions[i].text) > len(redactions[j].text)
	})
	return credentialRedactor{redactions: redactions}
}

// addressUserinfo returns the userinfo of an address with a scheme: the text
// between `://` and the last `@` before the host.
//
// When an `@` comes before the first `/`, `?` or `#`, the userinfo ends at the
// last such `@`, so a password holding an unencoded `@` or a quote is taken
// whole. When the only `@` comes after a slash, it belongs to the path —
// `https://host/team/@scope/repo.git` — unless what comes before that slash
// cannot be a host: a colon followed by something that is not a port, which is
// how a password holding a slash reads (`https://bot:a/b@host/repo.git`). Git's
// own anonymizer gives up on that form and prints it whole, so it is the form
// that most needs removing.
func addressUserinfo(address string) string {
	_, rest, found := strings.Cut(address, "://")
	if !found {
		return ""
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
		return rest[:at]
	}
	at := strings.Index(rest, "@")
	if at < 0 || strings.HasPrefix(rest, "[") {
		return ""
	}
	_, port, hasColon := strings.Cut(rest[:end], ":")
	if !hasColon || isPort(port) {
		return ""
	}
	// The userinfo runs on through any further `@` that comes before the next
	// slash, as it does in the authority.
	for {
		next := strings.IndexAny(rest[at+1:], "/@")
		if next < 0 || rest[at+1+next] != '@' {
			break
		}
		at += 1 + next
	}
	return rest[:at]
}

func isPort(text string) bool {
	for _, character := range text {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
