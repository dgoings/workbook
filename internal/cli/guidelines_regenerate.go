package cli

import (
	"context"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/release"
)

// regenerateGuidelines rewrites the generated guidelines against the statuses,
// priorities, and keys this change produced.
//
// The guidelines state a project's statuses, priorities, and keys, so every
// status change, every priority change, and every key change makes them
// stale, and a generated file that has to be refreshed by hand is a generated
// file that is wrong most of the time. It goes through the same Reconcile the
// documentation commands use, which is what keeps the one promise that
// matters about a generated file: Workbook rewrites what it wrote, and never
// overwrites what somebody edited.
//
// This is called from status mutations, priority mutations, and key
// mutations, so all three parameters are required at every call site
// regardless of which section the caller's own change touched: a status
// rename that passed only the statuses and let priorities default to the zero
// value would silently overwrite a project's configured priorities with the
// built-in three the moment somebody renamed a column, and the same is true of
// a key change that let the statuses or priorities default. Passing the
// sections a caller did not itself change is exactly what keeps the rest of
// the document accurate; each caller passes its own freshly-written section
// and reads the other two off the session's own service, which is what it
// opened the fetch-then-refresh with.
//
// The relic types are read here, after the change, because no caller changes
// them without this being the first to see the result: schema put adds a
// version, and publication can renumber it.
//
// It returns its failure rather than raising it. The configuration change is
// already recorded and published by the time this runs, so a documentation
// refresh that could not finish is news to report beside a success, not a
// reason to exit non-zero on a durable write — the same trade `sync` makes.
func regenerateGuidelines(
	ctx context.Context,
	session *taskSession,
	vocabulary core.Vocabulary,
	priorities core.PriorityVocabulary,
	keys core.KeySet,
	noDocs bool,
) (*agentdocs.Report, error) {
	if noDocs {
		return nil, nil
	}
	relicTypes, err := currentRelicTypes(ctx, session.repository, session.config)
	if err != nil {
		return &agentdocs.Report{}, err
	}
	report, err := agentdocs.ApplyGuidelines(agentdocs.Options{
		Root:       session.repository.Root,
		Project:    session.config,
		Vocabulary: vocabulary,
		Priorities: priorities,
		Keys:       keys,
		RelicTypes: relicTypes,
		Generator:  release.Version,
	})
	return &report, err
}

// guidelinesRelicTypes describes the newest schema of each relic type in the
// listing, for the guidelines. A type whose schema cannot be read is described
// as unreadable, so that one bad version does not keep the rest of the file
// from being written. Only a canceled command is an error: its reads fail for a
// reason that says nothing about the schemas.
func guidelinesRelicTypes(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	listing core.RelicSchemaListing,
) ([]agentdocs.RelicTypeRules, error) {
	var types []agentdocs.RelicTypeRules
	for _, relicType := range listing.Types() {
		newest, _ := listing.Newest(relicType)
		rules := agentdocs.RelicTypeRules{Type: relicType, Version: newest.Version}
		if _, schema, err := repository.ReadRelicSchema(ctx, config, newest); err != nil {
			rules.Unreadable = true
		} else {
			rules.Summary = schema.Summary()
		}
		if err := ctx.Err(); err != nil {
			return nil, core.Wrap(core.CategoryOperational, "read the relic schemas", err)
		}
		types = append(types, rules)
	}
	return types, nil
}

// currentRelicTypes is guidelinesRelicTypes over the settings the repository
// holds now.
func currentRelicTypes(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
) ([]agentdocs.RelicTypeRules, error) {
	listing, err := repository.RelicSchemaListing(ctx, config)
	if err != nil {
		return nil, err
	}
	return guidelinesRelicTypes(ctx, repository, config, listing)
}
