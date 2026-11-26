package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// failingRelicReader is a relic reader whose every read fails.
type failingRelicReader struct{ err error }

func (r failingRelicReader) ListRelicHeads(context.Context, core.ProjectConfig) ([]core.RelicHead, error) {
	return nil, r.err
}

func (r failingRelicReader) ReadRelicTips(context.Context, core.ProjectConfig, []core.RelicHead) ([]core.RelicSnapshot, error) {
	return nil, r.err
}

func (r failingRelicReader) GetRelic(context.Context, core.ProjectConfig, string) (core.RelicSnapshot, error) {
	return core.RelicSnapshot{}, r.err
}

func (r failingRelicReader) ReadRelicParts(context.Context, core.ProjectConfig, core.RelicSnapshot, []string, bool) (map[string]core.RelicPartContent, error) {
	return nil, r.err
}

// A relic lookup that fails while the fetch reported relic conflicts stops
// the command with that failure: it cannot tell whether the relic it names is
// one of them, so it must not go on as if it were not.
func TestRelicConflictForReturnsAFailedLookup(t *testing.T) {
	t.Parallel()
	failure := core.Errorf(core.CategoryOperational, "cannot list relic refs")
	session := &taskSession{relicConflicts: []core.RelicConflict{{RelicID: "WB-R-01M8C9ZXDWW3JVW2CPRETKSKC1"}}}
	conflict, err := session.relicConflictFor(context.Background(),
		core.RelicService{Reader: failingRelicReader{err: failure}}, "WB-R-01M8C9ZXDWW3JVW2CPRETKSKC1")
	if conflict != nil || !errors.Is(err, failure) {
		t.Fatalf("relicConflictFor = (%+v, %v), want the lookup's failure", conflict, err)
	}
}
