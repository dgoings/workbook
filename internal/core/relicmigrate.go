package core

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

// relicMigrationTarget is what relic migrate --to asks for: a type, and either
// a version number of it or, with number zero, its newest version.
type relicMigrationTarget struct {
	relicType string
	number    int
}

// parseRelicMigrationTarget reads --to: N, a version of the relic's own type;
// <type>, that type's newest version; or <type>@N.
func parseRelicMigrationTarget(target, relicType string) (relicMigrationTarget, error) {
	invalid := func() error {
		return Errorf(CategoryInvocation,
			"--to %q is not a version: give N for version N of the relic's type, <type> for that type's newest version, or <type>@N",
			target)
	}
	// Digits alone are a version number, never a type's name.
	if target != "" && strings.Trim(target, "0123456789") == "" {
		number, ok := parseRelicVersionNumber(target)
		if !ok {
			return relicMigrationTarget{}, invalid()
		}
		return relicMigrationTarget{relicType: relicType, number: number}, nil
	}
	name, numberText, versioned := strings.Cut(target, "@")
	if ValidateRelicType(name) != nil {
		return relicMigrationTarget{}, invalid()
	}
	if !versioned {
		return relicMigrationTarget{relicType: name}, nil
	}
	number, ok := parseRelicVersionNumber(numberText)
	if !ok {
		return relicMigrationTarget{}, invalid()
	}
	return relicMigrationTarget{relicType: name, number: number}, nil
}

// parseRelicVersionNumber reads a version number: decimal digits only, and at
// least 1.
func parseRelicVersionNumber(text string) (int, bool) {
	if text == "" || strings.Trim(text, "0123456789") != "" {
		return 0, false
	}
	number, err := strconv.Atoi(text)
	if err != nil || number < 1 {
		return 0, false
	}
	return number, true
}

// Migrate moves a relic to another schema version, of its own type or of
// another, recorded as one relic.migrate. Any version this clone lists may be
// the target, an older one too, and a type with no schema makes the relic
// open. The whole relic, its live parts only, must fit the version it moves
// to. Migrating to what the relic already names writes nothing. The version
// the relic names now is not read, so a relic naming one this clone has not
// synced can still be moved.
func (s RelicService) Migrate(ctx context.Context, idOrPrefix, target string) (RelicMutationResult, error) {
	snapshot, err := s.relicToChange(ctx, idOrPrefix)
	if err != nil {
		return RelicMutationResult{}, err
	}
	relicID := snapshot.Record.State.RelicID
	relic := snapshot.Record.State.Relic
	wanted, err := parseRelicMigrationTarget(target, relic.Type)
	if err != nil {
		return RelicMutationResult{}, err
	}
	if relic.Deleted {
		return RelicMutationResult{}, Errorf(CategoryValidation,
			"relic %s is deleted; restore it first with \"workbook relic restore %s\"", relicID, relicID)
	}
	listing, err := s.schemaListing(ctx)
	if err != nil {
		return RelicMutationResult{}, err
	}
	var version RelicSchemaVersion
	var found bool
	if wanted.number == 0 {
		version, found = listing.Newest(wanted.relicType)
	} else if version, found = listing.Number(wanted.relicType, wanted.number); !found {
		return RelicMutationResult{}, Errorf(CategoryNotFound, "type %s has no schema version %d", wanted.relicType, wanted.number)
	}
	unchanged := relic.Type == wanted.relicType && relic.Schema == version.ID
	if unchanged && !found {
		return relicMigrationResult(snapshot, false, listing, judgedAs(false)), nil
	}

	if found {
		_, schema, err := s.Schemas.ReadRelicSchema(ctx, s.Config, version)
		if err != nil {
			return RelicMutationResult{}, err
		}
		live := relic.LiveParts()
		partIDs := make([]string, len(live))
		for index, part := range live {
			partIDs[index] = part.ID
		}
		content, err := s.Reader.ReadRelicParts(ctx, s.Config, snapshot, partIDs, false)
		if err != nil {
			return RelicMutationResult{}, err
		}
		fields := make(map[string]map[string]json.RawMessage, len(content))
		for partID, read := range content {
			fields[partID] = read.Fields
		}
		problem := schema.CheckRelic(relic, fields)
		// A relic that already names the version is not refused for not
		// fitting it; it is reported as it stands.
		if unchanged {
			conformance := judgedAs(true)
			if problem != nil {
				conformance = &RelicConformance{State: RelicConformanceNotConforming, Problem: problem.String()}
			}
			return relicMigrationResult(snapshot, false, listing, conformance), nil
		}
		if problem != nil {
			return RelicMutationResult{}, RelicSchemaRefusal(version, *problem)
		}
	}

	ids, err := s.newIDs(1)
	if err != nil {
		return RelicMutationResult{}, err
	}
	operation := RelicOperation{ID: ids[0], Type: RelicOperationMigrate, RelicType: wanted.relicType, Schema: version.ID}
	written, err := s.writeRelicChange(ctx, snapshot, []RelicOperation{operation}, nil, "migrate relic "+relicID)
	if err != nil {
		return RelicMutationResult{}, err
	}
	return relicMigrationResult(written, true, listing, judgedAs(found)), nil
}

// relicMigrationResult is a migration's result, which carries the
// conformance its check of the whole relic found.
func relicMigrationResult(snapshot RelicSnapshot, changed bool, listing RelicSchemaListing, conformance *RelicConformance) RelicMutationResult {
	result := relicMutationUnder(snapshot, changed, listing)
	result.Relic.RelicConformance = conformance
	return result
}
