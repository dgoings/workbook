package webui

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// hostedCacheControl is what every answer of a hosted board carries. Private
// because the answer is for the person signed in and nobody else, and
// no-store because a page of tasks or an attachment left on a shared
// machine's disk, or in a cache between the proxy and the person, outlives the
// person's access to it.
const hostedCacheControl = "private, no-store"

// answerPrivately sets hostedCacheControl before anything inside it runs, so
// the answers given before the board's own handler — the same-origin guard's
// refusals, and a refusal of a request that names no usable person — carry it
// as well as the board's pages and data do.
func answerPrivately(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", hostedCacheControl)
		inner.ServeHTTP(writer, request)
	})
}

// serverWords reports whether an error of this category is worded by the
// server rather than by Workbook's reading of the request.
//
// An operational failure keeps everything its wrapping said, which for a Git
// command is the command, its exit status and Git's own output — the
// server's folder and the remote's address among them. Corrupt data names the
// objects and files it could not read. An error with no category is treated
// as operational everywhere else, so it is here too. Every other category is
// a sentence Workbook wrote about the request itself — a title too long, a
// head that moved, a task that is not there — which is the person's to read.
func serverWords(category core.Category) bool {
	switch category {
	case core.CategoryOperational, core.CategoryCorruptData, "":
		return true
	default:
		return false
	}
}

// disclosedWarnings are the warning codes whose message carries the server's
// own words, and the sentence a hosted board answers with instead. Each says
// what happened to the change, because a warning sits beside a change that
// was recorded and the person needs to know whether it was.
var disclosedWarnings = map[string]string{
	core.WarningAutoSync: "The change was recorded on the server, but publishing it to the shared repository did not finish.",
	core.WarningTasksMoved: "The change was recorded on the server, but this project's tasks have moved to another repository, " +
		"so it was not published there; whoever runs this board has to set it up again from the new repository.",
	core.WarningProjectionUpdate: "The change was recorded, but the server's local index of tasks could not be updated.",
	core.WarningDocsRefresh:      "The change was recorded, but the project's generated guidelines could not be refreshed.",
}

// disclosed is warnings as this board may show them. A board that is not
// hosted shows them as they are. A hosted one replaces the message of each
// warning whose words are the server's with a short sentence and a reference,
// and writes the original to the server's log; every other warning is
// Workbook's own sentence about the request and is kept.
func (handler *handler) disclosed(warnings []core.Warning) []core.Warning {
	if !handler.Hosted || len(warnings) == 0 {
		return warnings
	}
	shown := make([]core.Warning, len(warnings))
	for index, warning := range warnings {
		shown[index] = warning
		if sentence, server := disclosedWarnings[warning.Code]; server {
			reference := handler.logDetail(warning.Code + ": " + warning.Message)
			shown[index].Message = strings.TrimSuffix(sentence, ".") + ". The server's log has the detail under reference " + reference + "."
		}
	}
	return shown
}

// logDetail writes detail to the server's log under a new reference, and
// returns the reference. The line is whole, so lines from requests answered at
// once do not interleave, and it begins with the reference, so whoever reads
// the log can find it from the one a person reports.
func (handler *handler) logDetail(detail string) string {
	reference := newReference()
	if handler.ServerLog != nil {
		detail = strings.ReplaceAll(detail, "\n", "\n    ")
		_, _ = fmt.Fprintf(handler.ServerLog, "workbook serve: reference %s: %s\n", reference, detail)
	}
	return reference
}

// newReference is twelve hexadecimal digits drawn at random: enough that two
// answers on one board never share one in practice, and short enough for a
// person to read out to whoever runs the server.
func newReference() string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand does not fail on any platform this builds for; a
		// reference that is not unique still keeps the detail out of the answer.
		return "000000000000"
	}
	return hex.EncodeToString(raw[:])
}
