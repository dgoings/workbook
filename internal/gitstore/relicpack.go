package gitstore

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// A checkout can hold thousands of relic refs and as many tracking copies of
// them. Git writes each new or moved ref as a file of its own until something
// packs it, and every fetch, a task command's included, reads every ref the
// checkout holds: with a thousand relics loose, that reading was most of what
// a relic-heavy checkout added to `workbook next`. So a run that moved a relic
// ref packs Workbook's relic namespaces, and nothing else, once at its end:
//
//	git pack-refs --include refs/workbook/relics/*
//	              --include refs/workbook/remotes/*/relics/*
//	              --include refs/workbook/reconciled-relics/*
//
// --include keeps every other ref as it is: the person's branches and tags,
// and the rest of refs/workbook/, are not packed (a ref already packed stays
// packed, as Git always leaves it). Only the files ref backend has loose refs;
// on any other backend pack-refs would compact every ref the repository
// holds, so nothing is run there.
//
// Packing is maintenance. It changes no ref's value, so a failure is ignored:
// the command's result, exit code and output are what they would have been
// without it. A Git that refuses --include (it arrived after Git 2.29, the
// oldest Workbook supports) is remembered for the rest of the handle's life,
// so a watcher does not ask again every round.

// relicPackPatterns are the --include patterns of the pack. pack-refs matches
// them as wildmatch globs without pathname semantics, so * crosses slashes and
// the tracking pattern covers every remote the checkout fetched relics from.
var relicPackPatterns = []string{
	relicRefPrefix + "*",
	remoteTrackingRoot + "*/relics/*",
	relicParkedRefPrefix + "*",
}

// gitUsageExitCode is the exit status Git's option parser gives a command line
// it does not accept, an unknown option among them.
const gitUsageExitCode = 129

// noteRelicRefsMoved records that this handle moved a relic ref, so the next
// PackMovedRelicRefs packs.
func (r *Repository) noteRelicRefsMoved() {
	r.relicPackMu.Lock()
	r.relicRefsMoved = true
	r.relicPackMu.Unlock()
}

// PackMovedRelicRefs packs the relic namespaces when this handle moved a relic
// ref since it last packed, and otherwise runs nothing. It returns nothing:
// see the comment at the top of this file.
func (r *Repository) PackMovedRelicRefs(ctx context.Context) {
	r.relicPackMu.Lock()
	defer r.relicPackMu.Unlock()
	if !r.relicRefsMoved || r.relicPackRefused || ctx.Err() != nil {
		return
	}
	r.relicRefsMoved = false
	if !r.relicPackFormatKnown {
		// Git before 2.45 knows one backend, files, and prints the unknown
		// flag back instead of a format name.
		output, err := r.Git(ctx, nil, "rev-parse", "--show-ref-format")
		if err != nil {
			return
		}
		format := strings.TrimSpace(string(output))
		r.relicPackFormatKnown = true
		r.relicPackRefused = format != "files" && format != "--show-ref-format"
		if r.relicPackRefused {
			return
		}
	}
	args := []string{"pack-refs"}
	for _, pattern := range relicPackPatterns {
		args = append(args, "--include", pattern)
	}
	result := r.gitWithEnvResult(ctx, nil, nil, args...)
	var exit *exec.ExitError
	if errors.As(result.err, &exit) && exit.ExitCode() == gitUsageExitCode {
		r.relicPackRefused = true
	}
}
