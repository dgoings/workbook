package webui

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// PublicAddress is the address people open a hosted board at, which is not
// the one it listens on: a sign-in proxy in front of the board answers
// https://tasks.example.com and forwards each request to the board's local
// bind address.
//
// It is an origin and nothing more — a scheme, a host and a port — because an
// origin is all the board's protection against other websites compares with.
// The board answers at the root of its address, so a path would name a board
// this one is not, and a query or a user name has no meaning for an origin at
// all. ParsePublicURL refuses each rather than drop it.
type PublicAddress struct {
	scheme string
	// host is lowercased and without a root dot, ready to compare.
	host string
	// port is explicit, the scheme's default when the address gave none.
	port string
}

// publicURLRule is the sentence every refusal of a public address carries, so
// whoever typed one learns what is accepted rather than only what was wrong.
const publicURLRule = "the public address must be an https:// address such as https://tasks.example.com, or http:// only for this machine (localhost or a loopback address), with no path beyond /, no query and no user name; " +
	"its host is an IP address or a name of letters, digits and hyphens between dots (the xn-- form for other scripts), " +
	"and a port, when given, is a number from 1 to 65535 with no leading zero"

// ParsePublicURL reads the address a hosted board is opened at.
//
// It accepts an absolute https:// address, and http:// only for a host that
// names this machine, because a browser treats an http:// page on any other
// host as reachable by whoever is on the network path. Anything beyond the
// origin — a path other than /, a query, a fragment, a user name or password —
// is refused, and so is a host that is neither an IP address nor a name a
// browser can send, and a port that is not a number in range written without
// a leading zero: a board started with any of them would refuse every request. The error is an
// invocation error: the value was given to `workbook serve` at startup, and
// nothing about the repository is wrong.
func ParsePublicURL(raw string) (PublicAddress, error) {
	refuse := func(reason string) (PublicAddress, error) {
		return PublicAddress{}, core.Errorf(core.CategoryInvocation, "%q is not usable: %s; %s", raw, reason, publicURLRule)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return refuse("it is not an address")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return refuse("it does not begin with https://")
	}
	if parsed.Opaque != "" || parsed.Host == "" {
		return refuse("it names no host")
	}
	if parsed.User != nil {
		return refuse("it carries a user name")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return refuse("it has a path")
	}
	if parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.ContainsAny(raw, "?#") {
		return refuse("it has a query or a fragment")
	}
	host := trimRootDot(strings.ToLower(parsed.Hostname()))
	if host == "" {
		return refuse("it names no host")
	}
	if net.ParseIP(host) == nil && !validHostName(host) {
		return refuse("its host is not a host name a browser can send")
	}
	port := parsed.Port()
	if port == "" {
		if strings.HasSuffix(parsed.Host, ":") {
			return refuse("its port is empty")
		}
		port = defaultPort(parsed.Scheme)
	} else if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 || port[0] == '0' {
		// A browser writes a port without leading zeros, so 0443 would
		// never match the Host and Origin a request carries.
		return refuse("its port is not a number from 1 to 65535 with no leading zero")
	}
	if parsed.Scheme == "http" && !loopbackHost(host) {
		return refuse("http:// is accepted only for this machine")
	}
	return PublicAddress{scheme: parsed.Scheme, host: host, port: port}, nil
}

// validHostName reports whether host is a name a browser sends in a Host or
// Origin header as it is: dot-separated labels of 1 to 63 letters, digits and
// hyphens, no label beginning or ending with a hyphen, 253 characters at most,
// and a last label that is not all digits (a browser reads such a name as an
// IPv4 address and rewrites it). A name in another script reaches the board in
// its xn-- form, so that is the form accepted. host is already lowercased and
// has no root dot.
func validHostName(host string) bool {
	if len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	last := labels[len(labels)-1]
	return strings.Trim(last, "0123456789") != ""
}

// Origin is the address as a browser names it in an Origin header: the
// scheme, the host, and the port only when it is not the scheme's default.
func (public PublicAddress) Origin() string {
	authority := public.host
	if strings.Contains(authority, ":") {
		authority = "[" + authority + "]"
	}
	if public.port != defaultPort(public.scheme) {
		authority += ":" + public.port
	}
	return public.scheme + "://" + authority
}

// namesHost reports whether a Host header addresses this public address. A
// Host with no port means the scheme's default port, because that is what a
// browser leaves out.
func (public PublicAddress) namesHost(authority string) bool {
	host, port, ok := splitAuthorityFor(authority, defaultPort(public.scheme))
	return ok && port == public.port && sameHost(public.host, host)
}

// namesOrigin reports whether an Origin header is this public address's.
func (public PublicAddress) namesOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != public.scheme || parsed.Opaque != "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return public.namesHost(parsed.Host)
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return defaultHTTPPort
}
