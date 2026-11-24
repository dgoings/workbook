package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
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
	service, err := openRelicServiceFor(ctx, cwd, stderr, false)
	if err != nil {
		return core.RelicService{}, nil, err
	}
	repository, ok := service.Reader.(*gitstore.Repository)
	if !ok {
		return core.RelicService{}, nil, core.Errorf(core.CategoryOperational, "relic reads need the repository")
	}
	store, release, err := openProjection(ctx, repository, service.Config)
	if err != nil {
		return core.RelicService{}, nil, err
	}
	service.Reader = store.RelicReader()
	return service, release, nil
}
