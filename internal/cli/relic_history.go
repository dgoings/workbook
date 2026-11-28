package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// maxRelicDiffContext is the largest --context relic diff accepts. A body is
// at most 1 MiB, so it has fewer lines than this, and "all" says every line.
const maxRelicDiffContext = 1_000_000

// defaultRelicDiffContext is the context of a unified diff.
const defaultRelicDiffContext = 3

// relicHistoryRequest checks relic show's history flags, before anything is
// read, and returns the window they ask for. A flag that would silently do
// nothing is refused, as show --history refuses it.
func relicHistoryRequest(flags *commandFlagSet, history, bodies, removed, all bool, limit string) (core.RelicHistoryOptions, error) {
	options := core.RelicHistoryOptions{All: all}
	if flagGiven(flags, "limit") {
		parsed, err := strconv.Atoi(limit)
		if err != nil || parsed < 1 {
			return options, core.Errorf(core.CategoryInvocation, "relic show --limit must be a positive whole number")
		}
		options.Limit = parsed
	}
	if !history {
		for _, name := range []string{"limit", "all"} {
			if flagGiven(flags, name) {
				return options, core.Errorf(core.CategoryInvocation, "relic show --%s requires --history", name)
			}
		}
		return options, nil
	}
	if options.Limit > 0 && options.All {
		return options, core.Errorf(core.CategoryInvocation, "cannot use --limit with --all")
	}
	if bodies {
		return options, core.Errorf(core.CategoryInvocation, "relic show --history cannot be combined with --body")
	}
	if removed {
		return options, core.Errorf(core.CategoryInvocation, "relic show --history cannot be combined with --removed")
	}
	return options, nil
}

// runRelicHistory answers relic show --history.
func runRelicHistory(ctx context.Context, address, cwd string, options core.RelicHistoryOptions, jsonMode bool, stdout, stderr io.Writer) error {
	service, release, err := openRelicReadService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	defer release()
	history, err := service.History(ctx, address, options)
	if err != nil {
		return err
	}
	if jsonMode {
		writeResult(stdout, "relic show", history)
		return nil
	}
	writeRelicHistory(stdout, history)
	return nil
}

func runRelicDiff(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("relic diff", []string{"<relic>[#<part>]"}, args)
	if err != nil {
		return err
	}
	var versions []string
	for len(args) > 0 && isRequiredFirstArgument(args[0]) {
		versions, args = append(versions, args[0]), args[1:]
	}
	if len(versions) > 2 {
		return core.Errorf(core.CategoryInvocation, "relic diff takes at most two versions")
	}
	flags := newFlagSet("relic", "diff")
	contextLines := flags.String("context", "", "lines of context around each change, or all")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	options := core.RelicDiffOptions{Context: defaultRelicDiffContext}
	if flagGiven(flags, "context") {
		if options.Context, err = parseRelicDiffContext(*contextLines); err != nil {
			return err
		}
	}
	service, release, err := openRelicReadService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	defer release()
	diff, err := service.Diff(ctx, values[0], versions, options)
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "relic diff", diff)
		return nil
	}
	writeRelicDiff(stdout, diff)
	return nil
}

// parseRelicDiffContext reads --context: a number of lines, or "all", which is
// -1 to the diff.
func parseRelicDiffContext(value string) (int, error) {
	if value == "all" {
		return -1, nil
	}
	lines, err := strconv.Atoi(value)
	if err != nil || lines < 0 || lines > maxRelicDiffContext {
		return 0, core.Errorf(core.CategoryInvocation, `relic diff --context must be a whole number or "all"`)
	}
	return lines, nil
}

// writeRelicHistory prints a history: the relic, the window shown, each change
// with its operations, for a part its body versions, and last, for a compacted
// relic, what the compaction dropped. Every stored string passes through
// singleLine.
func writeRelicHistory(stdout io.Writer, history core.RelicHistory) {
	relic := history.Relic
	fmt.Fprintf(stdout, "%s\t%s\t%s\n", relic.ID, singleLine(relic.Type), singleLine(relic.Title))
	if history.Showing < history.Total {
		fmt.Fprintf(stdout, "Showing %d most recent changes out of %d.\n", history.Showing, history.Total)
	} else {
		fmt.Fprintf(stdout, "Showing all %d change(s).\n", history.Total)
	}
	for _, change := range history.Changes {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n",
			relicTime(change.WallTime), singleLine(change.Actor), singleLine(change.Summary))
		for _, operation := range change.Operations {
			fmt.Fprintf(stdout, "\t%s\t%s", singleLine(operation.ID), singleLine(string(operation.Type)))
			if part := relicHistoryPartColumn(operation.RelicOperation); part != "" {
				fmt.Fprintf(stdout, "\t%s", singleLine(part))
			}
			if operation.Applied != nil && !*operation.Applied {
				fmt.Fprint(stdout, " (not applied)")
			}
			fmt.Fprintln(stdout)
		}
	}
	if len(history.BodyVersions) > 0 {
		fmt.Fprintln(stdout, "Body versions:")
		for _, version := range history.BodyVersions {
			fmt.Fprintf(stdout, "\t%s\t%s\t%s\t%s\t%d bytes",
				singleLine(version.Version), relicTime(version.WallTime), singleLine(version.Actor),
				relicFormatLabel(version.Format), version.Bytes)
			if version.Current {
				fmt.Fprint(stdout, "\tcurrent")
			}
			fmt.Fprintln(stdout)
		}
	}
	if truncated := history.Truncated; truncated != nil {
		shown := *truncated
		shown.Commit, shown.Message = singleLine(shown.Commit), singleLine(shown.Message)
		writeHistoryTruncation(stdout, &shown)
	}
	if compacted := history.Compacted; compacted != nil {
		fmt.Fprintf(stdout, "Compacted on %s: %d earlier changes (%s to %s) were dropped; history starts at change %s.\n",
			relicDay(compacted.At), compacted.Changes, relicDay(compacted.From), relicDay(compacted.To),
			singleLine(compacted.Version))
	}
}

// relicHistoryPartColumn is the part a history line shows for an operation: the
// one it names, the one a part.add makes, which is the operation's own ID, or
// the one a link is added from. It is empty when the operation concerns no part.
func relicHistoryPartColumn(operation core.RelicOperation) string {
	switch {
	case operation.Type == core.RelicOperationPartAdd:
		return operation.ID
	case operation.Type == core.RelicOperationLinkAdd && operation.Link != nil:
		return operation.Link.From
	}
	return operation.Part
}

func relicTime(moment time.Time) string {
	return moment.UTC().Format(time.RFC3339)
}

// relicFormatLabel is a body's format as history and diffs print it, "-" for a
// body with none.
func relicFormatLabel(format string) string {
	if format == "" {
		return "-"
	}
	return singleLine(format)
}

// writeRelicDiff prints a diff: the two points, the relic's own changes and
// properties, each changed part with its fields and body, and the links. Body
// lines pass through core.DisplayText, so indentation is kept; every other
// stored string passes through singleLine.
func writeRelicDiff(stdout io.Writer, diff core.RelicDiff) {
	from := "(before the relic)"
	if diff.From != nil {
		from = singleLine(diff.From.Version)
	}
	fmt.Fprintf(stdout, "Comparing:\t%s\t→\t%s\n", from, singleLine(diff.To.Version))
	if len(diff.Changes) == 0 && len(diff.Properties) == 0 && len(diff.Parts) == 0 && diff.Links == nil {
		compared := "relic"
		if diff.Part != "" {
			compared = "part"
		}
		fmt.Fprintf(stdout, "These two versions hold the same %s.\n", compared)
		return
	}
	for _, change := range diff.Changes {
		name := singleLine(change.Name)
		if name != "" {
			name = strings.ToUpper(name[:1]) + name[1:]
		}
		fmt.Fprintf(stdout, "%s:\t%s\n", name, relicValueChangeText(change))
	}
	for _, change := range diff.Properties {
		fmt.Fprintf(stdout, "Property %s:\t%s\n", singleLine(change.Name), relicValueChangeText(change))
	}
	for _, part := range diff.Parts {
		writeRelicPartDiff(stdout, part)
	}
	if links := diff.Links; links != nil {
		fmt.Fprintln(stdout, "Links:")
		writeRelicDiffLinks(stdout, "+", links.Added)
		writeRelicDiffLinks(stdout, "-", links.Removed)
	}
}

// relicValueChangeText is a changed value as "<from> → <to>", or "+<to>" and
// "-<from>" for one added or removed.
func relicValueChangeText(change core.RelicValueChange) string {
	text := func(value json.RawMessage) string { return singleLine(relicValueText(value)) }
	switch change.Kind {
	case core.ChangeAdded:
		return "+" + text(change.To)
	case core.ChangeRemoved:
		return "-" + text(change.From)
	}
	return text(change.From) + " → " + text(change.To)
}

func writeRelicPartDiff(stdout io.Writer, part core.RelicPartDiff) {
	fmt.Fprintf(stdout, "Part %s\t%s\t%s", singleLine(part.ID), singleLine(part.Type), singleLine(string(part.Kind)))
	if moved := part.Moved; moved != nil && moved.FromIndex != moved.ToIndex {
		fmt.Fprintf(stdout, "\tmoved %d → %d", moved.FromIndex, moved.ToIndex)
	}
	fmt.Fprintln(stdout)
	for _, field := range part.Fields {
		fmt.Fprintf(stdout, "\tField %s:\t%s\n", singleLine(field.Name), relicValueChangeText(field))
	}
	body := part.Body
	if body == nil {
		return
	}
	side := func(body *core.RelicBody) string {
		if body == nil {
			return "no body"
		}
		return fmt.Sprintf("%s %d bytes", relicFormatLabel(body.Format), body.Bytes)
	}
	fmt.Fprintf(stdout, "\tBody:\t%s → %s", side(body.From), side(body.To))
	if !body.Text {
		fmt.Fprintln(stdout, "\tnot text")
		return
	}
	fmt.Fprintln(stdout)
	for _, hunk := range body.Hunks {
		fmt.Fprintf(stdout, "\t@@ -%d,%d +%d,%d @@\n", hunk.FromStart, hunk.FromLines, hunk.ToStart, hunk.ToLines)
		for _, line := range hunk.Lines {
			mark := " "
			switch line.Kind {
			case core.DiffDelete:
				mark = "-"
			case core.DiffInsert:
				mark = "+"
			}
			// A body line keeps its layout, as relic show --body prints it.
			fmt.Fprintf(stdout, "\t%s%s\n", mark, core.DisplayText(line.Text))
			if line.NoNewline {
				fmt.Fprintln(stdout, "\t\\ No newline at end of file")
			}
		}
	}
}

func writeRelicDiffLinks(stdout io.Writer, mark string, links []core.RelicLink) {
	for _, link := range links {
		from := link.From
		if from == "" {
			from = "relic"
		}
		fmt.Fprintf(stdout, "\t%s%s\t%s\t%s\t%s",
			mark, singleLine(link.ID), singleLine(from), singleLine(link.Rel), singleLine(link.To.Address()))
		if link.Pointer != nil {
			fmt.Fprintf(stdout, "\t%s", singleLine(string(link.Pointer)))
		}
		fmt.Fprintln(stdout)
	}
}
