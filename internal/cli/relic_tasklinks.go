package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/projection"
)

// runRelicListLinkedTo is relic list --linked-to: the relics that link to a
// task, each with its links to it. A relic the index cannot read is a
// warning, on stderr or in the envelope.
func runRelicListLinkedTo(
	ctx context.Context,
	service core.RelicService,
	task string,
	filter core.RelicListFilter,
	jsonMode bool,
	stdout, stderr io.Writer,
) error {
	if err := checkRelicUpdateID("--linked-to", task); err != nil {
		return err
	}
	summaries, warnings, err := service.LinkedTo(ctx, task, filter)
	if err != nil {
		return err
	}
	if jsonMode {
		writeResultWithWarnings(stdout, "relic list", summaries, warnings)
		return nil
	}
	writeRelicList(stdout, summaries)
	writeWarnings(stderr, warnings)
	return nil
}

// writeRelicLinkMatches prints, under a relic's line, each of its links to
// the task relic list --linked-to asked about.
func writeRelicLinkMatches(stdout io.Writer, matches []core.RelicLinkMatch) {
	for _, match := range matches {
		from := match.Part
		if from == "" {
			from = "relic"
		}
		fmt.Fprintf(stdout, "\t%s\t%s\t%s\n", singleLine(match.Link), singleLine(from), singleLine(match.Rel))
	}
}

// taskRelicLinks reads the links relics have to a task, for workbook show,
// through the relic index on the store the task was read from. It never
// fails the show: a relic, or every relic, that cannot be read is a warning.
// Only the caller's own cancellation is returned.
func taskRelicLinks(ctx context.Context, service core.Service, taskID string) ([]core.TaskRelicLink, []core.Warning, error) {
	store, ok := service.Reader.(*projection.Store)
	if !ok {
		return nil, nil, nil
	}
	index := store.RelicReader()
	relics := core.RelicService{Config: service.Config, Keys: service.Keys, Reader: index, Links: index}
	summaries, warnings, err := relics.LinkedToTask(ctx, taskID, core.RelicListFilter{})
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, []core.Warning{{
			Code:    core.WarningRelicLinksUnread,
			Message: "relics cannot be read, so their links are not listed: " + singleLine(err.Error()) + "; run `workbook validate`",
		}}, nil
	}
	return core.TaskRelicLinks(summaries), warnings, nil
}

// writeShowRelics prints the links relics have to the task, after its thread,
// and nothing when there are none: a blank line, Relics:, and one line per
// link with the relic, or the relic and its part, the relationship and the
// relic's title.
func writeShowRelics(output io.Writer, links []core.TaskRelicLink) {
	if len(links) == 0 {
		return
	}
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Relics:")
	for _, link := range links {
		address := link.Relic
		if link.Part != "" {
			address += "#" + link.Part
		}
		fmt.Fprintf(output, "\t%s\t%s\t%s\n", singleLine(address), singleLine(link.Rel), singleLine(link.Title))
	}
}
