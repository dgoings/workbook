package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// Text output for the relic commands. Every stored string passes through
// singleLine, so nothing a relic holds can move the cursor, color the terminal
// or forge a line of its own; --json is the interface for exact values. A
// body's text passes through core.DisplayText instead, which removes the same
// characters but keeps its spaces, tabs and line breaks. The one exception is a single part's body under `relic show <relic>#<part>
// --body` when standard output is not a terminal, which is written byte for
// byte so it can be redirected (see writeRelicPartBody).

// writeRelicMutation reports a command that changed a relic, or did not.
func writeRelicMutation(stdout io.Writer, command string, result core.RelicMutationResult, jsonMode bool) {
	if jsonMode {
		writeResult(stdout, command, result)
		return
	}
	if !result.Changed {
		fmt.Fprintf(stdout, "No change: %s\n", result.Relic.ID)
		return
	}
	fmt.Fprintf(stdout, "%s\t%s\t%s\n", result.Relic.ID, singleLine(result.Relic.Type), singleLine(result.Relic.Title))
	for _, part := range result.Parts {
		version := "-"
		if part.Body != nil {
			version = part.Body.Version
		}
		fmt.Fprintf(stdout, "part\t%s\t%s\t%s\n", part.ID, singleLine(part.Type), version)
	}
}

// writeRelicList prints one line per relic. A retired relic, which only relic
// list --retired and --all list, and a deleted one, which only --all lists,
// get a last column each saying so, retired first, as a removed part does in
// writeRelicDocument; a live relic's line is the same with or without them.
// toRelic says that the links listed under each line point at a relic.
func writeRelicList(stdout io.Writer, summaries []core.RelicSummary, toRelic bool) {
	for _, summary := range summaries {
		parts := "parts"
		if summary.Parts == 1 {
			parts = "part"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%d %s\t%s",
			summary.ID, singleLine(summary.Type), summary.Parts, parts, singleLine(summary.Title))
		if summary.Retired {
			fmt.Fprint(stdout, "\tretired")
		}
		if summary.Deleted {
			fmt.Fprint(stdout, "\tdeleted")
		}
		fmt.Fprintln(stdout)
		writeRelicLinkMatches(stdout, summary, toRelic)
	}
}

// writeRelicDocument prints a relic as relic show presents it: the relic, its
// properties, then each part with its fields and, with bodies, its text.
func writeRelicDocument(stdout io.Writer, document core.RelicDocument, bodies bool) {
	fmt.Fprintf(stdout, "%s\t%s\t%s\n", document.ID, singleLine(document.Type), singleLine(document.Title))
	fmt.Fprintf(stdout, "Author:\t%s\n", singleLine(document.Author))
	if document.UpdatedAt != nil {
		fmt.Fprintf(stdout, "Updated:\t%s\n", document.UpdatedAt.UTC().Format(time.RFC3339))
	}
	if document.Retired {
		fmt.Fprintln(stdout, "Retired:\tyes")
	}
	if document.Deleted {
		fmt.Fprintln(stdout, "Deleted:\tyes")
	}
	if compacted := document.Compacted; compacted != nil {
		fmt.Fprintf(stdout, "Compacted:\t%s (%s)\n", relicDay(compacted.At), singleLine(compacted.Boundary))
	}
	writeRelicSchema(stdout, document)
	if len(document.Properties) > 0 {
		fmt.Fprintln(stdout, "Properties:")
		writeRelicValues(stdout, "  ", document.Properties)
	}
	for _, part := range document.Parts {
		fmt.Fprintf(stdout, "#%s\t%s\t", part.ID, singleLine(part.Type))
		switch {
		case part.Dropped != nil:
			fmt.Fprintf(stdout, "removed on %s, content dropped %s",
				relicDay(part.Dropped.RemovedAt), relicDay(part.Dropped.DroppedAt))
		case part.Removed:
			fmt.Fprintf(stdout, "%s\tremoved", relicBodySummary(part.Body))
		default:
			fmt.Fprint(stdout, relicBodySummary(part.Body))
		}
		fmt.Fprintln(stdout)
		writeRelicValues(stdout, "  ", part.Fields)
		if bodies && part.Body != nil {
			writeRelicBodyText(stdout, *part.Body)
		}
	}
	writeRelicLinks(stdout, document.Links)
	writeRelicIncomingLinks(stdout, document.LinkedFrom)
}

// writeRelicSchema prints the schema version the relic names, or that it names
// none, and for a relic that does not fit that version the first thing that
// does not.
func writeRelicSchema(stdout io.Writer, document core.RelicDocument) {
	switch schema := document.Schema; {
	case schema == nil:
		fmt.Fprintln(stdout, "Schema:\topen")
	case schema.Version == 0:
		fmt.Fprintf(stdout, "Schema:\tunknown version %s\n", singleLine(schema.ID))
	default:
		fmt.Fprintf(stdout, "Schema:\t%s version %d\n", singleLine(schema.Type), schema.Version)
	}
	if document.State == core.RelicConformanceNotConforming {
		fmt.Fprintf(stdout, "Does not fit:\t%s\n", singleLine(document.Problem))
	}
}

// writeRelicLinks prints a relic's links after its parts, one line each: the
// link's ID, the part it is from or "relic", its relationship and its target,
// with "(removed)" after a target that is deleted or removed, "(removed on
// <date>)" when a compaction dropped it, "(not in this clone)" after one this
// clone does not have and "(retired)" after a retired relic, then its pointer
// when it has one.
func writeRelicLinks(stdout io.Writer, links []core.RelicDocumentLink) {
	if len(links) == 0 {
		return
	}
	fmt.Fprintln(stdout, "Links:")
	for _, link := range links {
		from := link.From
		if from == "" {
			from = "relic"
		}
		fmt.Fprintf(stdout, "\t%s\t%s\t%s\t%s", singleLine(link.ID), singleLine(from), singleLine(link.Rel), singleLine(link.To.Address()))
		switch {
		case link.RemovedAt != nil:
			fmt.Fprintf(stdout, " (removed on %s)", relicDay(*link.RemovedAt))
		case link.TargetState == core.RelicLinkTargetMissing:
			fmt.Fprint(stdout, " (not in this clone)")
		case link.TargetState == core.RelicLinkTargetRetired:
			fmt.Fprint(stdout, " (retired)")
		case link.TargetRemoved:
			fmt.Fprint(stdout, " (removed)")
		}
		if link.Pointer != nil {
			fmt.Fprintf(stdout, "\t%s", singleLine(string(link.Pointer)))
		}
		fmt.Fprintln(stdout)
	}
}

// writeRelicIncomingLinks prints the links other relics have to the relic
// shown, after its own: one line each with the link's ID, the relic it is on
// with the part it is from, its relationship and the part of the shown relic
// it points at or "relic", then its pointer when it has one.
func writeRelicIncomingLinks(stdout io.Writer, links []core.RelicIncomingLink) {
	if len(links) == 0 {
		return
	}
	fmt.Fprintln(stdout, "Linked from:")
	for _, link := range links {
		from := link.Relic
		if link.From != "" {
			from += "#" + link.From
		}
		to := link.ToPart
		if to == "" {
			to = "relic"
		}
		fmt.Fprintf(stdout, "\t%s\t%s\t%s\t%s", singleLine(link.Link), singleLine(from), singleLine(link.Rel), singleLine(to))
		if link.Pointer != nil {
			fmt.Fprintf(stdout, "\t%s", singleLine(string(link.Pointer)))
		}
		fmt.Fprintln(stdout)
	}
}

// relicDay is a time as relic text output dates a removal or a compaction.
func relicDay(when time.Time) string {
	return when.UTC().Format("2006-01-02")
}

// relicMark is what follows a link of a relic that is no longer in use:
// when its content was removed, if a compaction dropped it, or else that it is
// deleted or retired; nothing otherwise.
func relicMark(retired, deleted bool, removedAt *time.Time) string {
	switch {
	case removedAt != nil:
		return " (removed on " + relicDay(*removedAt) + ")"
	case deleted:
		return " (deleted)"
	case retired:
		return " (retired)"
	default:
		return ""
	}
}

// relicBodySummary describes a part's body without its text.
func relicBodySummary(body *core.RelicDocumentBody) string {
	if body == nil {
		return "no body"
	}
	format := body.Format
	if format == "" {
		format = "txt"
	}
	return fmt.Sprintf("%s, %d bytes, version %s", singleLine(format), body.Bytes, body.Version)
}

// writeRelicBodyText prints a body under its part, one sanitized line per
// line. A body that is not UTF-8 is not printed; its part address with --body
// writes its bytes into a file or a pipe.
func writeRelicBodyText(stdout io.Writer, body core.RelicDocumentBody) {
	if body.Text == nil {
		if body.Base64 != nil {
			fmt.Fprintln(stdout, "  Body:\tnot text; show the part alone with --body, into a file or a pipe, to write its bytes")
		}
		return
	}
	fmt.Fprintln(stdout, "  Body:")
	// The body's own layout is kept, as relic show <relic>#<part> --body
	// shows it on a terminal, under an indent of four spaces.
	for _, line := range strings.Split(strings.TrimSuffix(core.DisplayText(*body.Text), "\n"), "\n") {
		if line == "" {
			fmt.Fprintln(stdout)
			continue
		}
		fmt.Fprintf(stdout, "    %s\n", line)
	}
}

// writeRelicValues prints properties or fields in name order, one
// `name: value` line each. A JSON string prints as its text, anything else as
// compact JSON: a null prints as null.
func writeRelicValues(stdout io.Writer, indent string, values map[string]json.RawMessage) {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(stdout, "%s%s: %s\n", indent, singleLine(name), singleLine(relicValueText(values[name])))
	}
}

// relicValueText is a value as relic show prints it. Only a JSON string is
// decoded to its text. A null also decodes into a string without an error,
// as the empty one, so the first byte decides, not the decoder.
func relicValueText(value json.RawMessage) string {
	if trimmed := bytes.TrimSpace(value); len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err == nil {
			return text
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return string(value)
	}
	return compact.String()
}

// writeRelicExport writes the bare workbook.relic document, indented by two
// spaces, and nothing else.
//
// It writes '<', '>' and '&' as themselves rather than as the six-byte escapes
// json.Marshal uses for HTML, so a Markdown or HTML body reads as it was
// written and is no larger than its text. A property or field value is
// already canonical JSON and is written exactly as it is stored.
func writeRelicExport(stdout io.Writer, document core.RelicDocument) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return core.Wrap(core.CategoryOperational, "the relic document cannot be encoded", err)
	}
	_, err := stdout.Write(encoded.Bytes())
	return err
}
