package webui

import (
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// jsonMediaType is the only media type a mutation may declare. The three types
// a cross-site form can send — text/plain, application/x-www-form-urlencoded,
// and multipart/form-data — are CORS-simple, so a POST carrying one needs no
// preflight and reaches the board from any page on the web. Requiring a type
// that is not simple puts every mutation behind a preflight the board never
// answers.
const jsonMediaType = "application/json"

// defaultHTTPPort is what a browser means by an authority with no port. The
// board speaks plain HTTP, so an absent port is 80 and not the bound port.
const defaultHTTPPort = "80"

// GuardSameOrigin refuses requests a browser on another site can make.
//
// The board has no authentication: it answers whoever opens the port with
// every task in the project, and its mutations publish to the project's origin
// where teammates and coding agents later read them. Two browser-level
// assumptions are all that stand between that and a drive-by page, so both are
// checked here rather than trusted:
//
//   - The Host header must name the address the board is bound to. A hostile
//     page that rebinds its own DNS name to the board's address reaches the
//     listener with a foreign Host, and without this check the browser treats
//     the answer as same-origin with the attacker. A loopback bind is named by
//     any loopback host and an explicit bind by that address alone; a wildcard
//     bind is the one case with no host to pin, and pins only the port.
//   - An Origin header, when the browser sends one, must name the board itself.
//     Cross-site requests that carry one are refused whatever their method.
//   - A mutating request must declare application/json, which no cross-site
//     form can send without a preflight.
//
// boundAddr is the listener's own address, so a board on an ephemeral port
// guards the port it actually got.
func GuardSameOrigin(inner http.Handler, boundAddr string) http.Handler {
	guard := newOriginGuard(boundAddr)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if status, message := guard.reject(request); status != 0 {
			writeSecurityHeaders(writer)
			writeJSON(writer, status, ErrorDocument{
				Format:  "workbook.error",
				Version: 1,
				Error:   ErrorBody{Category: core.CategoryValidation, Message: message},
			})
			return
		}
		inner.ServeHTTP(writer, request)
	})
}

// BoundToLoopback reports whether a listener address accepts connections from
// this machine only. A board bound anywhere else is reachable by whoever shares
// the network and still has nothing to authenticate them.
func BoundToLoopback(address string) bool {
	host, _, ok := splitAuthority(address)
	if !ok {
		return false
	}
	return loopbackHost(host)
}

// BoundToWildcard reports whether a listener address is the one bind whose Host
// header the guard cannot pin. A wildcard listener answers every address this
// machine has, under every name that resolves to one of them, so a page that
// points its own name here is addressing the board as legitimately as a
// teammate is and the two are indistinguishable from the header.
func BoundToWildcard(address string) bool {
	host, _, ok := splitAuthority(address)
	if !ok {
		return false
	}
	return wildcardHost(host)
}

// originGuard holds the decision the bound address settles once, so no request
// re-parses it.
type originGuard struct {
	// address is the listener address as given, used only in messages.
	address string
	// port every acceptable Host and Origin must name.
	port string
	// host is the single address the listener answers on, which every Host and
	// Origin must then name. It is empty for the two binds that answer more than
	// one address, distinguished by the fields below.
	host string
	// loopback is true when the board is reachable from this machine only, in
	// which case any loopback name is the board and a name that is not loopback
	// cannot be.
	loopback bool
	// wildcard is true when the board answers every address this machine has. It
	// cannot know which of them, or which name for one, a browser used, so it
	// pins the port and requires an Origin to repeat the authority the browser
	// addressed. That is the guard's one remaining gap and is documented as such
	// in README.md and named by the warning serve prints.
	wildcard bool
	// unusable is set when the bound address could not be parsed at all. The
	// guard then refuses everything rather than guess which requests are safe.
	unusable bool
}

func newOriginGuard(boundAddr string) *originGuard {
	host, port, ok := splitAuthority(boundAddr)
	if !ok {
		return &originGuard{address: boundAddr, unusable: true}
	}
	guard := &originGuard{address: boundAddr, port: port}
	switch {
	case loopbackHost(host):
		guard.loopback = true
	case wildcardHost(host):
		guard.wildcard = true
	default:
		guard.host = host
	}
	return guard
}

// reject answers with the HTTP status and message a refused request deserves,
// or 0 and an empty message when the request may proceed.
func (guard *originGuard) reject(request *http.Request) (int, string) {
	if guard.unusable {
		return http.StatusForbidden, fmt.Sprintf("the board could not read its own address %q, so it refuses every request", guard.address)
	}
	host, port, ok := splitAuthority(request.Host)
	if !ok || port != guard.port || !guard.hostIsBoard(host) {
		return http.StatusForbidden, fmt.Sprintf("Host %q is not this board at %q", request.Host, guard.address)
	}
	if origin := request.Header.Get("Origin"); origin != "" && !guard.originIsBoard(origin, host) {
		return http.StatusForbidden, fmt.Sprintf("Origin %q is not this board at %q", origin, guard.address)
	}
	if mutatingMethod(request.Method) && !jsonRequest(request.Header.Get("Content-Type")) {
		return http.StatusUnsupportedMediaType, fmt.Sprintf("%s requests must declare Content-Type: %s", request.Method, jsonMediaType)
	}
	return 0, ""
}

// hostIsBoard reports whether the host half of an authority names this board.
// Callers check the port themselves, having split it off already.
func (guard *originGuard) hostIsBoard(host string) bool {
	switch {
	case guard.loopback:
		return loopbackHost(host)
	case guard.wildcard:
		// Every address this machine has is the board, and every name that
		// resolves to one of them addresses it, so there is nothing to compare
		// the host with. The port is all this bind can pin.
		return true
	default:
		return sameHost(guard.host, host)
	}
}

// originIsBoard reports whether an Origin header names the board the browser
// just addressed. requestHost is the already-accepted host from the Host
// header, which is what a wildcard bind — the one with no address of its own to
// compare against — falls back to.
func (guard *originGuard) originIsBoard(origin, requestHost string) bool {
	parsed, err := url.Parse(origin)
	// An origin is a scheme, a host, and a port; anything else in the header —
	// a path, credentials, the opaque "null" of a sandboxed frame — means it is
	// not this board.
	if err != nil || parsed.Scheme != "http" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host, port, ok := splitAuthority(parsed.Host)
	if !ok || port != guard.port {
		return false
	}
	if guard.wildcard {
		// No name belongs to a wildcard bind, so the strongest test left is that
		// the Origin repeats the authority the browser addressed. A rebound name
		// satisfies it by matching itself; nothing else here can tell them apart.
		return sameHost(host, requestHost)
	}
	return guard.hostIsBoard(host)
}

// splitAuthority separates an authority into a host and the port it means. An
// authority with no port means the scheme's default port, which is what a
// browser sends when a board is bound to port 80.
func splitAuthority(authority string) (string, string, bool) {
	if authority == "" {
		return "", "", false
	}
	if host, port, err := net.SplitHostPort(authority); err == nil {
		if host == "" || port == "" {
			return "", "", false
		}
		return host, port, true
	}
	if strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]") {
		host := authority[1 : len(authority)-1]
		if host == "" {
			return "", "", false
		}
		return host, defaultHTTPPort, true
	}
	if strings.ContainsAny(authority, ":[]") {
		return "", "", false
	}
	return authority, defaultHTTPPort, true
}

// sameHost reports whether two hosts name the same address. Two IP literals are
// compared as addresses, so 192.168.1.5 and ::ffff:192.168.1.5 are one host and
// an IPv6 address keeps its meaning however it is abbreviated. Anything else is
// a name, compared case-insensitively and without the trailing dot a browser may
// send. An IP and a name are never the same host: the guard pins the address a
// listener reports, and a name that resolves to it is exactly what a rebinding
// page sends.
func sameHost(left, right string) bool {
	left, right = trimRootDot(left), trimRootDot(right)
	if leftIP, rightIP := net.ParseIP(left), net.ParseIP(right); leftIP != nil && rightIP != nil {
		return leftIP.Equal(rightIP)
	}
	return strings.EqualFold(left, right)
}

// wildcardHost reports whether a bind address names every address this machine
// has, which 0.0.0.0 and :: do and a routable address does not.
func wildcardHost(host string) bool {
	ip := net.ParseIP(trimRootDot(host))
	return ip != nil && ip.IsUnspecified()
}

// loopbackHost reports whether a host names this machine. A name that merely
// begins with "localhost", such as localhost.attacker.example, does not.
func loopbackHost(host string) bool {
	host = trimRootDot(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// trimRootDot removes the trailing dot of a fully qualified name, which a
// browser may send and which names the same host without it.
func trimRootDot(host string) string {
	if len(host) > 1 && strings.HasSuffix(host, ".") {
		return host[:len(host)-1]
	}
	return host
}

func mutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// jsonRequest reports whether a Content-Type header declares JSON. Parameters
// such as charset are allowed; an absent or unparseable header is not, because
// a form POST is exactly the request that omits one.
func jsonRequest(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == jsonMediaType
}
