package gitstore

import "regexp"

// urlUserinfo matches the userinfo of a URL with an authority: a scheme, `://`,
// and everything up to the `@` that ends the userinfo. It stops at a slash, at
// whitespace and at the quotes Git wraps a URL in, so it cannot run past the
// URL it started in and swallow text after it. An email address has no scheme
// in front of it and an scp-style remote such as git@host:path has no `://`,
// so neither is touched.
var urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@'"]+@`)

// redactURLCredentials removes the userinfo from every URL in text.
//
// It is applied to everything Git prints on stderr, because that text is the
// detail of every Git failure this package reports and the board shows those
// reports to whoever made the change. Git anonymizes the remote URL in most of
// its messages but not all of them — a remote that answers a push with
// something other than Git's protocol gets "no DAV locking support on <url>"
// with the userinfo left in — and origin's URL is where a deployment keeps its
// token. The whole userinfo goes, not only a password after a colon, because a
// token is often the user name alone, and the URL is otherwise left as Git
// wrote it, so the remote is still named. That is also what Git's own
// anonymized messages print.
func redactURLCredentials(text string) string {
	return urlUserinfo.ReplaceAllString(text, "$1")
}
