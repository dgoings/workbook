package projection

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// A relic row keeps how the relic stands against the schema version it names,
// judged when a pass indexes it: conformance holds open, conforms,
// not-conforming or unknown-schema, and schema_problem the first problem for a
// relic that does not fit. A relic that names no version is open, which no
// listing is read for, so a project with no schema reads none. A pass for a
// task command judges nothing and stores a relic that names a version with an
// empty conformance, not yet judged.
//
// A version, once listed, never changes, so a judgment against a listed
// version stands. A row judged unknown-schema stands only while the listing
// lacks its version: once a sync brings it, the next judging pass indexes the
// relic again and judges it. A row judged against a version the listing no
// longer has is indexed again the same way, so the stored judgment always
// equals the one Git gives.

// relicSchemaCache holds compiled schema versions by blob ID.
type relicSchemaCache struct {
	mu      sync.Mutex
	schemas map[string]*core.RelicSchema
}

func (c *relicSchemaCache) get(blob string) (*core.RelicSchema, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	schema, ok := c.schemas[blob]
	return schema, ok
}

func (c *relicSchemaCache) put(blob string, schema *core.RelicSchema) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.schemas == nil {
		c.schemas = map[string]*core.RelicSchema{}
	}
	c.schemas[blob] = schema
}

// relicJudge judges the relics one pass indexes. It reads the schema listing
// once, and only when a relic or row names a version.
type relicJudge struct {
	store   *Store
	judging bool
	listing *core.RelicSchemaListing
}

func (j *relicJudge) schemas(ctx context.Context) (core.RelicSchemaListing, error) {
	if j.listing == nil {
		listing, err := j.store.relics.RelicSchemaListing(ctx, j.store.config)
		if err != nil {
			return core.RelicSchemaListing{}, err
		}
		j.listing = &listing
	}
	return *j.listing, nil
}

// rowStands reports whether an unchanged row's judgment stands. A pass that
// does not judge leaves every judgment as it is.
func (j *relicJudge) rowStands(ctx context.Context, row cachedRelic) (bool, error) {
	if !j.judging || row.schema == "" {
		return true, nil
	}
	listing, err := j.schemas(ctx)
	if err != nil {
		return false, err
	}
	return row.judgedStands(listing), nil
}

// judge judges a relic Git just returned, every part's fields in hand. It
// returns the zero judgment, not yet judged, for a relic naming a version on
// a pass that does not judge.
func (j *relicJudge) judge(ctx context.Context, relic gitstore.IndexedRelic) (core.RelicConformance, error) {
	data := relic.Snapshot.Record.State.Relic
	if data.Schema == "" {
		return core.RelicConformance{State: core.RelicConformanceOpen}, nil
	}
	if !j.judging {
		return core.RelicConformance{}, nil
	}
	listing, err := j.schemas(ctx)
	if err != nil {
		return core.RelicConformance{}, err
	}
	if _, known := listing.ByID(data.Schema); !known {
		return core.JudgeRelic(data, nil, listing, nil)
	}
	fields := make(map[string]map[string]json.RawMessage, len(data.Parts))
	for _, part := range data.LiveParts() {
		document, err := core.DecodeRelicFieldsDocument(relic.Fields[part.ID])
		if err != nil {
			return core.RelicConformance{}, core.Wrap(core.CategoryCorruptData,
				"relic "+relic.Snapshot.Record.State.RelicID+": the fields of part "+part.ID+" cannot be judged", err)
		}
		fields[part.ID] = document.Fields
	}
	return core.JudgeRelic(data, fields, listing, func(version core.RelicSchemaVersion) (*core.RelicSchema, error) {
		if schema, ok := j.store.relicSchemas.get(version.Blob); ok {
			return schema, nil
		}
		_, schema, err := j.store.relics.ReadRelicSchema(ctx, j.store.config, version)
		if err != nil {
			return nil, err
		}
		j.store.relicSchemas.put(version.Blob, schema)
		return schema, nil
	})
}
