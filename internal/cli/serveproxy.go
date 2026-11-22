package cli

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"strings"
	"sync"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/webui"
)

// The environment variables that set serve's two hosting options, for a
// service manager that sets a process's environment more easily than its
// arguments. A flag given on the command line wins over its variable.
const (
	servePublicURLVariable   = "WORKBOOK_SERVE_PUBLIC_URL"
	serveActorHeaderVariable = "WORKBOOK_SERVE_ACTOR_HEADER"
)

// serveHosting is what `workbook serve` was told about the proxy in front of
// it. The zero value is a board with no proxy, which is every board started
// with neither option: it behaves exactly as a board did before the options
// existed.
type serveHosting struct {
	// public is the address people open the board at, or nil.
	public *webui.PublicAddress
	// actorHeader is the request header that names the person each request
	// acts for, in canonical form, or empty when no header is honored.
	actorHeader string
}

// readServeHosting settles the two options from their flags and variables.
//
// A flag that was given wins over its variable, whatever either says. A value
// that is given and unusable — blank included — stops serve before it binds
// anything, with the source named: a board that quietly started without the
// header its deployer asked for would record every signed-in person's edit
// under the server's own name, and one that started without its public
// address would refuse every request the proxy sent, and both look like
// something else from the outside.
func readServeHosting(flags *commandFlagSet, publicFlag, headerFlag string, lookupEnv func(string) (string, bool)) (serveHosting, error) {
	given := map[string]bool{}
	flags.Visit(func(visited *flag.Flag) { given[visited.Name] = true })
	var hosting serveHosting

	publicValue, publicSource, publicSet := publicFlag, "--public-url", given["public-url"]
	if !publicSet {
		publicValue, publicSet = lookupEnv(servePublicURLVariable)
		publicSource = servePublicURLVariable
	}
	if publicSet {
		public, err := webui.ParsePublicURL(publicValue)
		if err != nil {
			return serveHosting{}, core.Errorf(core.CategoryInvocation, "%s: %s", publicSource, err.Error())
		}
		hosting.public = &public
	}

	headerValue, headerSource, headerSet := headerFlag, "--actor-header", given["actor-header"]
	if !headerSet {
		headerValue, headerSet = lookupEnv(serveActorHeaderVariable)
		headerSource = serveActorHeaderVariable
	}
	if headerSet {
		name, err := validActorHeader(headerValue)
		if err != nil {
			return serveHosting{}, core.Errorf(core.CategoryInvocation, "%s: %s", headerSource, err.Error())
		}
		hosting.actorHeader = name
	}
	return hosting, nil
}

// reservedActorHeaders are request headers that already mean something to the
// board or to HTTP itself, so none of them can also carry a person's name. Go
// takes Host out of a request's headers altogether, so a board told to read it
// would refuse every request as nameless.
var reservedActorHeaders = map[string]bool{
	"Accept": true, "Connection": true, "Content-Length": true, "Content-Type": true,
	"Host": true, "If-None-Match": true, "Origin": true, "Transfer-Encoding": true, "Upgrade": true,
}

// validActorHeader checks a header name given to --actor-header and returns it
// in the canonical form Go files a request's headers under. The name is
// matched without regard to case, as HTTP matches header names; the value it
// carries is never changed.
func validActorHeader(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("the header name must not be blank; give the header your sign-in proxy sets, such as Cf-Access-Authenticated-User-Email")
	}
	for index := 0; index < len(name); index++ {
		if !headerTokenByte(name[index]) {
			return "", fmt.Errorf("%q is not a header name: use letters, digits and - only, such as Cf-Access-Authenticated-User-Email", name)
		}
	}
	canonical := textproto.CanonicalMIMEHeaderKey(name)
	if reservedActorHeaders[canonical] {
		return "", fmt.Errorf("%s already means something to the board and cannot carry a person's name; give the header your sign-in proxy sets", canonical)
	}
	return canonical, nil
}

// headerTokenByte is RFC 9110's tchar: what a header field name is made of.
func headerTokenByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", b) >= 0
}

// actorFromHeader names the person each request acts for from one header.
//
// With the header honored, a request must name somebody: a board that fell
// back to the server's own name for a request that came without one would
// let anyone who reached its port past the proxy act as the server, and would
// record a signed-in person's edit under the wrong name the day a proxy
// stopped sending it. So a request with no value, reads and the page
// included, is refused 401. The health check is the one exception, because a
// hosting platform calls it directly rather than through the proxy, and it
// reads and changes nothing.
//
// The value is the person exactly as sent, held to ValidateActor's rule, and
// it must be one value: a header sent twice, or one carrying a list, is
// refused, because a proxy that appends its value to one the visitor sent
// rather than replacing it would otherwise let the visitor choose their name.
func actorFromHeader(header string) requestActor {
	return func(request *http.Request) (string, error) {
		if request.URL.Path == "/healthz" {
			return "", nil
		}
		values := request.Header.Values(header)
		switch {
		case len(values) == 0 || (len(values) == 1 && values[0] == ""):
			return "", &requestRefusal{
				status:   http.StatusUnauthorized,
				category: core.CategoryInvocation,
				message:  "This board takes each person's name from the " + header + " header its sign-in proxy sets, and this request came without one; open the board through the sign-in proxy.",
			}
		case len(values) > 1:
			return "", &requestRefusal{
				status:   http.StatusBadRequest,
				category: core.CategoryValidation,
				message:  "The " + header + " header was sent more than once; it must name exactly one person.",
			}
		case strings.Contains(values[0], ","):
			return "", &requestRefusal{
				status:   http.StatusBadRequest,
				category: core.CategoryValidation,
				message:  "The " + header + " header carries several values; it must name exactly one person.",
			}
		}
		return values[0], nil
	}
}

// requestRefusal is a request the board will not serve because of whom it
// names, with the answer it gets instead.
type requestRefusal struct {
	status   int
	category core.Category
	message  string
}

func (refusal *requestRefusal) Error() string { return refusal.message }

// boardActorHeaderWarning names what honoring the header gives away on a board
// that listens beyond this machine.
//
// The board does not check that a request came through the proxy — sign-in
// proxies have no common way to prove it — so whoever reaches the port
// directly sends the header themselves and is whoever they say. Listening on
// the local machine only, with the proxy beside it, is what closes that, and
// the warning says so. It does not stop the board: a deployment may close the
// port by other means, such as a firewall, and serve cannot see them.
func boardActorHeaderWarning(address, header string) string {
	if header == "" || webui.BoundToLoopback(address) {
		return ""
	}
	return fmt.Sprintf(
		"Warning: the board at %s takes each person's name from the %s header and is listening beyond this machine. Anyone who can reach that address without going through the sign-in proxy can send the header and act as anyone. Listen on 127.0.0.1 with the proxy on the same machine, or make sure nothing but the proxy can reach the port.",
		address, header,
	)
}

// syncWriter serializes writes to serve's standard error, which the board's
// server log, its sync loop and its startup lines all share. Each Write is one
// whole line from one writer, so lines from requests answered at once never
// interleave.
type syncWriter struct {
	mutex  sync.Mutex
	writer io.Writer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.writer.Write(p)
}
