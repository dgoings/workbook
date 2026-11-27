package core

import (
	"context"
	"strings"
	"time"
)

// RelicChainCommit is one commit of a relic's first-parent chain: its object
// ID, its first parent's (empty for the root) and its operation pack.
type RelicChainCommit struct {
	Commit, Parent string
	Operation      RelicOperationPack
}

// RelicChain is a relic's first-parent chain ending at Head, oldest first.
// Truncated names the commit a read stopped at; the commits below it are kept.
type RelicChain struct {
	RelicID, Head string
	Commits       []RelicChainCommit
	Truncated     *HistoryTruncation
}

// RelicPartObjectIDs names the Git objects one part's content is stored in at
// one commit: its fields.json blob, and its body blob, empty when it has none.
// Two equal IDs mean equal content, so a comparison opens only the objects
// that differ.
type RelicPartObjectIDs struct {
	Fields, Body string
}

// RelicVersion is one relic commit read back for a comparison: its snapshot and
// the objects each of its parts, live or removed, is stored in.
type RelicVersion struct {
	Snapshot RelicSnapshot
	Parts    map[string]RelicPartObjectIDs
}

// RelicHistoryReader reads what relic history and diffs need: the chain ending
// at a head, from the index when it holds the whole chain and from Git
// otherwise, and the versions at given commits, always from Git.
type RelicHistoryReader interface {
	RelicChain(context.Context, ProjectConfig, RelicHead) (RelicChain, error)
	ReadRelicVersions(context.Context, ProjectConfig, string, []string) ([]RelicVersion, error)
}

// RelicChangeOperation is one stored operation as history lists it. Applied is
// set on every operation that saves a body: whether the fold took the save.
type RelicChangeOperation struct {
	RelicOperation
	Applied *bool `json:"applied,omitempty"`
}

// RelicChange is one relic commit rendered as one entry, its operations in pack
// order. Summary is the operations' distinct types in that order.
type RelicChange struct {
	Commit       string                 `json:"commit"`
	Actor        string                 `json:"actor"`
	WallTime     time.Time              `json:"wallTime"`
	LogicalClock uint64                 `json:"logicalClock"`
	Summary      string                 `json:"summary"`
	Operations   []RelicChangeOperation `json:"operations"`
}

// RelicHistoryBodyVersion is one saved text of a part's body as history lists
// it, named by the operation that saved it. Base is the version the save
// started from, empty for the first body. (RelicBodyVersion is the conflict
// report's body, with its content.)
type RelicHistoryBodyVersion struct {
	Version  string    `json:"version"`
	Base     string    `json:"base,omitempty"`
	Format   string    `json:"format,omitempty"`
	Bytes    int64     `json:"bytes"`
	Actor    string    `json:"actor"`
	WallTime time.Time `json:"wallTime"`
	Commit   string    `json:"commit"`
	Current  bool      `json:"current,omitempty"`
}

// RelicHistory is the rendered history of a relic, or of one part of it: a
// window onto its changes, oldest first, and, for a part, every body version.
// BodyVersions is never windowed.
type RelicHistory struct {
	Relic        RelicSummary              `json:"relic"`
	Part         string                    `json:"part,omitempty"`
	Total        int                       `json:"total"`
	Showing      int                       `json:"showing"`
	Changes      []RelicChange             `json:"changes"`
	BodyVersions []RelicHistoryBodyVersion `json:"bodyVersions,omitempty"`
	Truncated    *HistoryTruncation        `json:"truncated,omitempty"`
}

// RelicPoint is one point of a relic's history: the state after a whole commit.
// Index is the commit's place in the chain, and Version the operation ID that
// names the point. The empty side, before the relic, has Index -1, a zero
// Commit and no Version.
type RelicPoint struct {
	Index   int
	Commit  RelicChainCommit
	Version string
}

// emptyRelicPoint is the side before a relic's root commit.
var emptyRelicPoint = RelicPoint{Index: -1}

// relicReadableCommits returns the chain's commits up to the first pack this
// build cannot read, and the truncation that ends them: the chain's own when
// every commit is readable.
func relicReadableCommits(chain RelicChain) ([]RelicChainCommit, *HistoryTruncation) {
	for index, commit := range chain.Commits {
		if commit.Operation.RequiresNewerReader() {
			return chain.Commits[:index], &HistoryTruncation{
				Commit:   commit.Commit,
				Message:  "this change was written by a newer workbook; upgrade workbook to read it",
				Category: CategoryNewerWriter,
			}
		}
	}
	return chain.Commits, chain.Truncated
}

// savesRelicBody reports whether the operation saves a body, and for which
// part.
func savesRelicBody(operation RelicOperation) (partID string, saves bool) {
	switch {
	case operation.Type == RelicOperationPartAdd && operation.Body != nil:
		return operation.ID, true
	case operation.Type == RelicOperationPartBodySet && operation.Body != nil:
		return operation.Part, true
	}
	return "", false
}

// RelicBodySavesApplied returns, for every operation of the chain that saves a
// body, whether the fold took it. It replays body versions alone, part by part
// in chain and pack order, and folds no state: a part exists from its part.add,
// whose body is always taken, and a part.body.set is taken exactly when the
// part exists, live or removed, and its base is the part's version at that
// point, empty when it has no body. These are ApplyRelic's rules, so index and
// Git chains give the same answer. The replay stops where the history does.
func RelicBodySavesApplied(chain RelicChain) map[string]bool {
	commits, _ := relicReadableCommits(chain)
	versions := map[string]string{}
	applied := map[string]bool{}
	for _, commit := range commits {
		for _, operation := range commit.Operation.Operations {
			partID, saves := savesRelicBody(operation)
			switch operation.Type {
			case RelicOperationPartAdd:
				if _, exists := versions[operation.ID]; exists {
					if saves {
						applied[operation.ID] = false
					}
					continue
				}
				versions[operation.ID] = ""
				if saves {
					versions[operation.ID] = operation.ID
					applied[operation.ID] = true
				}
			case RelicOperationPartBodySet:
				if !saves {
					continue
				}
				current, exists := versions[partID]
				took := exists && operation.Body.Base == current
				applied[operation.ID] = took
				if took {
					versions[partID] = operation.ID
				}
			}
		}
	}
	return applied
}

// relicOperationNamesPart reports whether an operation belongs to a part's
// history: it adds or names the part, as RelicPartsNamedBy counts, or it links
// from the part.
func relicOperationNamesPart(operation RelicOperation, partID string) bool {
	if operation.Type == RelicOperationPartAdd && operation.ID == partID {
		return true
	}
	if operation.Part == partID {
		return true
	}
	return operation.Type == RelicOperationLinkAdd && operation.Link != nil && operation.Link.From == partID
}

// relicChangeOperations lists the pack's operations, or only those naming the
// part when partID is set, each with its applied mark. It is never nil.
func relicChangeOperations(operations []RelicOperation, applied map[string]bool, partID string) []RelicChangeOperation {
	listed := make([]RelicChangeOperation, 0, len(operations))
	for _, operation := range operations {
		if partID != "" && !relicOperationNamesPart(operation, partID) {
			continue
		}
		entry := RelicChangeOperation{RelicOperation: operation}
		if _, saves := savesRelicBody(operation); saves {
			took := applied[operation.ID]
			entry.Applied = &took
		}
		listed = append(listed, entry)
	}
	return listed
}

// relicChangeSummary joins the operations' distinct types in pack order. A type
// this build does not know is named like any other.
func relicChangeSummary(operations []RelicChangeOperation) string {
	seen := map[RelicOperationType]bool{}
	types := make([]string, 0, len(operations))
	for _, operation := range operations {
		if seen[operation.Type] {
			continue
		}
		seen[operation.Type] = true
		types = append(types, string(operation.Type))
	}
	return strings.Join(types, ", ")
}

// BuildRelicHistory renders a relic's chain, or one part's history when partID
// is set, keeping the last limit changes (DefaultChangeLimit when limit is not
// positive) unless all is set. A part's history lists only the changes with an
// operation naming the part, each with only those operations, and every body
// version of the part. The listing ends at the first pack written by a newer
// Workbook, which becomes the truncation.
func BuildRelicHistory(chain RelicChain, summary RelicSummary, partID string, limit int, all bool) RelicHistory {
	commits, truncation := relicReadableCommits(chain)
	applied := RelicBodySavesApplied(chain)
	changes := make([]RelicChange, 0, len(commits))
	var versions []RelicHistoryBodyVersion
	for _, commit := range commits {
		pack := commit.Operation
		operations := relicChangeOperations(pack.Operations, applied, partID)
		if partID != "" && len(operations) == 0 {
			continue
		}
		changes = append(changes, RelicChange{
			Commit:       commit.Commit,
			Actor:        pack.Actor.ID,
			WallTime:     pack.WallTime,
			LogicalClock: pack.LogicalClock,
			Summary:      relicChangeSummary(operations),
			Operations:   operations,
		})
		if partID == "" {
			continue
		}
		for _, operation := range operations {
			if saved, saves := savesRelicBody(operation.RelicOperation); !saves || saved != partID || !applied[operation.ID] {
				continue
			}
			versions = append(versions, RelicHistoryBodyVersion{
				Version:  operation.ID,
				Base:     operation.Body.Base,
				Format:   operation.Body.Format,
				Bytes:    operation.Body.Bytes,
				Actor:    pack.Actor.ID,
				WallTime: pack.WallTime,
				Commit:   commit.Commit,
			})
		}
	}
	if len(versions) > 0 {
		versions[len(versions)-1].Current = true
	}

	history := RelicHistory{Relic: summary, Part: partID, Total: len(changes), BodyVersions: versions, Truncated: truncation}
	if !all {
		if limit <= 0 {
			limit = DefaultChangeLimit
		}
		if len(changes) > limit {
			changes = changes[len(changes)-limit:]
		}
	}
	history.Changes = changes
	history.Showing = len(changes)
	return history
}

// relicPointAt names the point after the chain's commit at index by the last
// operation ID of that commit, the name Workbook gives a point it chose.
func relicPointAt(chain RelicChain, index int) RelicPoint {
	if index < 0 {
		return emptyRelicPoint
	}
	commit := chain.Commits[index]
	version := ""
	if operations := commit.Operation.Operations; len(operations) > 0 {
		version = operations[len(operations)-1].ID
	}
	return RelicPoint{Index: index, Commit: commit, Version: version}
}

// ResolveRelicVersion finds the point a version names: an operation ID of the
// chain, or an unambiguous, case-insensitive prefix of one. Every operation of
// a commit names the same point, so a prefix is ambiguous only when it matches
// operations of more than one commit. The point carries the full ID matched, or
// the commit's last operation ID when a prefix matched several of its
// operations. A Git commit ID is never a version.
func ResolveRelicVersion(chain RelicChain, relicID, prefix string) (RelicPoint, error) {
	if strings.TrimSpace(prefix) == "" {
		return RelicPoint{}, Errorf(CategoryValidation, "give a change ID, or a prefix of one")
	}
	wanted := strings.ToUpper(prefix)
	var (
		matched []int
		version string
	)
	for index, commit := range chain.Commits {
		hits := 0
		for _, operation := range commit.Operation.Operations {
			id := strings.ToUpper(operation.ID)
			if id == wanted {
				return RelicPoint{Index: index, Commit: commit, Version: operation.ID}, nil
			}
			if strings.HasPrefix(id, wanted) {
				hits++
				version = operation.ID
			}
		}
		if hits > 0 {
			matched = append(matched, index)
			if hits > 1 {
				version = relicPointAt(chain, index).Version
			}
		}
	}
	switch len(matched) {
	case 0:
		return RelicPoint{}, Errorf(CategoryNotFound,
			"relic %s has no change %q in its history; a version is a change ID from \"workbook relic show %s --history\", not a Git commit",
			relicID, prefix, relicID)
	case 1:
		return RelicPoint{Index: matched[0], Commit: chain.Commits[matched[0]], Version: version}, nil
	default:
		return RelicPoint{}, Errorf(CategoryValidation,
			"change prefix %q matches %d changes of %s; use more characters", prefix, len(matched), relicID)
	}
}

// LatestRelicPoints returns the points a diff with no version compares: for
// the whole relic, the head commit against its parent; for a part, the newest
// commit naming the part against that commit's parent. A root commit's parent
// is the empty side.
func LatestRelicPoints(chain RelicChain, partID string) (from, to RelicPoint, err error) {
	if len(chain.Commits) == 0 {
		return RelicPoint{}, RelicPoint{}, Errorf(CategoryCorruptData, "relic %s has no history", chain.RelicID)
	}
	latest := len(chain.Commits) - 1
	if partID != "" {
		latest = -1
		for index := len(chain.Commits) - 1; index >= 0 && latest < 0; index-- {
			for _, operation := range chain.Commits[index].Operation.Operations {
				if relicOperationNamesPart(operation, partID) {
					latest = index
					break
				}
			}
		}
		if latest < 0 {
			return RelicPoint{}, RelicPoint{}, Errorf(CategoryNotFound, "relic %s has no change to part %s", chain.RelicID, partID)
		}
	}
	return relicPointAt(chain, latest-1), relicPointAt(chain, latest), nil
}
