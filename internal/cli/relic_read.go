package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// openRelicReadService opens the repository for a command that only reads, so
// a checkout with no acting person can still read its relics. Its Reader is
// the local index, which brings itself up to the relic refs before it answers
// and reads from Git any relic it holds no usable row for. The release closes
// the index.
//
// Only reads go through the index. A command that changes a relic opens a
// task session and reads the relic from Git (see mutateRelic), so a write
// never folds onto a snapshot the index built.
func openRelicReadService(ctx context.Context, cwd string, stderr io.Writer) (core.RelicService, func(), error) {
	service, repository, err := openRelicServiceFor(ctx, cwd, stderr, false)
	if err != nil {
		return core.RelicService{}, nil, err
	}
	store, release, err := openProjection(ctx, repository, service.Config)
	if err != nil {
		return core.RelicService{}, nil, err
	}
	index := store.RelicReader()
	service.Reader = index
	service.Links = index
	service.Tasks = indexedTaskReader{index: store, repository: repository}
	return service, release, nil
}

// indexedTaskReader is the task reader a relic read marks removed link
// targets through: the index, or the repository when the index cannot be
// built or read, as the relic reads themselves fall back.
type indexedTaskReader struct {
	index      core.TaskReader
	repository core.TaskReader
}

func (r indexedTaskReader) List(ctx context.Context, config core.ProjectConfig) ([]core.Snapshot, error) {
	snapshots, err := r.index.List(ctx, config)
	if err != nil && ctx.Err() == nil {
		return r.repository.List(ctx, config)
	}
	return snapshots, err
}

func (r indexedTaskReader) Get(ctx context.Context, config core.ProjectConfig, taskID string) (core.Snapshot, error) {
	snapshot, err := r.index.Get(ctx, config, taskID)
	if err != nil && ctx.Err() == nil && core.CategoryOf(err) != core.CategoryNotFound {
		return r.repository.Get(ctx, config, taskID)
	}
	return snapshot, err
}

func (r indexedTaskReader) Resolve(ctx context.Context, config core.ProjectConfig, prefix string) (string, error) {
	taskID, err := r.index.Resolve(ctx, config, prefix)
	if err != nil && ctx.Err() == nil && core.CategoryOf(err) != core.CategoryNotFound && core.CategoryOf(err) != core.CategoryValidation {
		return r.repository.Resolve(ctx, config, prefix)
	}
	return taskID, err
}
