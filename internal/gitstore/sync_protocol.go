package gitstore

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// parseRemoteTaskHeads accepts only the flat Workbook task refs emitted by a
// wildcard `git ls-remote --refs` query. Object IDs are checked against the
// repository format previously observed from local Git output.
//
// Every name here comes from origin, which anyone with push access can write
// to, so a name this version does not recognize as exactly one task is
// returned as an IgnoredRef rather than failing the query. Refusing the whole
// listing would let one stray ref stop every clone from publishing. Records
// that describe Git itself misbehaving — an unterminated or malformed line, a
// bad object ID, a ref outside the namespace, a duplicate — still fail.
func (r *Repository) parseRemoteTaskHeads(
	config core.ProjectConfig,
	output []byte,
) (map[string]string, []IgnoredRef, error) {
	heads := make(map[string]string)
	var ignored []IgnoredRef
	if len(output) == 0 {
		return heads, nil, nil
	}
	if output[len(output)-1] != '\n' {
		return nil, nil, core.Errorf(core.CategoryOperational, "Git returned unterminated remote task heads")
	}
	for _, line := range bytes.Split(output[:len(output)-1], []byte{'\n'}) {
		parts := bytes.Split(line, []byte{'\t'})
		if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) == 0 {
			return nil, nil, core.Errorf(core.CategoryOperational, "Git returned an invalid remote task-head record")
		}
		objectID := string(parts[0])
		if err := r.validateFullObjectID(objectID); err != nil {
			return nil, nil, core.Wrap(core.CategoryOperational, "Git returned an invalid remote task object ID", err)
		}
		refName := string(parts[1])
		if !strings.HasPrefix(refName, taskRefPrefix) {
			return nil, nil, core.Errorf(core.CategoryOperational, "Git returned remote ref outside %q", taskRefPrefix)
		}
		taskID := strings.TrimPrefix(refName, taskRefPrefix)
		if taskID == "" || strings.Contains(taskID, "/") || strings.HasSuffix(taskID, "^{}") {
			ignored = append(ignored, IgnoredRef{Ref: refName, Reason: "ref does not name exactly one task"})
			continue
		}
		if err := core.ValidateTaskID(config.Key, taskID); err != nil {
			ignored = append(ignored, IgnoredRef{Ref: refName, Reason: err.Error()})
			continue
		}
		if _, duplicate := heads[taskID]; duplicate {
			return nil, nil, core.Errorf(core.CategoryOperational, "Git returned duplicate remote task ref %q", refName)
		}
		heads[taskID] = objectID
	}
	return heads, ignored, nil
}

// parsePushPorcelain produces one exact task outcome for each expected
// destination. Git can return useful porcelain records for partial rejections
// even when the overall push command exits nonzero.
func parsePushPorcelain(output []byte, expected map[string]string, commandErr error) (map[string]SyncTaskResult, error) {
	results := make(map[string]SyncTaskResult, len(expected))
	if len(output) > 0 && output[len(output)-1] != '\n' {
		return nil, core.Errorf(core.CategoryOperational, "Git returned unterminated push porcelain")
	}
	hasRejected := false
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "To ") || line == "Done" {
			continue
		}
		if len(line) < 2 || line[1] != '\t' {
			return nil, core.Errorf(core.CategoryOperational, "Git returned malformed push porcelain record %q", line)
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || len(fields[0]) != 1 || fields[1] == "" || fields[2] == "" {
			return nil, core.Errorf(core.CategoryOperational, "Git returned malformed push porcelain record %q", line)
		}
		from, destination, found := strings.Cut(fields[1], ":")
		if !found || from == "" || destination == "" {
			return nil, core.Errorf(core.CategoryOperational, "Git returned malformed push porcelain refspec %q", fields[1])
		}
		taskID, found := expected[destination]
		if !found {
			return nil, core.Errorf(core.CategoryOperational, "Git returned unexpected push destination %q", destination)
		}
		if _, duplicate := results[taskID]; duplicate {
			return nil, core.Errorf(core.CategoryOperational, "Git returned duplicate push destination %q", destination)
		}

		result := SyncTaskResult{TaskID: taskID}
		switch fields[0][0] {
		case '*', ' ':
			result.Status = SyncPublished
		case '=':
			result.Status = SyncUpToDate
		case '!':
			result.Status = SyncRejected
			result.Detail = fields[2]
			hasRejected = true
		case '+', '-':
			return nil, core.Errorf(core.CategoryOperational, "Git returned forbidden push porcelain flag %q", fields[0])
		default:
			return nil, core.Errorf(core.CategoryOperational, "Git returned unknown push porcelain flag %q", fields[0])
		}
		results[taskID] = result
	}
	if len(results) != len(expected) {
		return nil, core.Errorf(core.CategoryOperational, "Git returned %d push outcomes, want %d", len(results), len(expected))
	}
	if commandErr != nil && !hasRejected {
		return nil, fmt.Errorf("git push failed without a rejected porcelain outcome: %w", commandErr)
	}
	return results, nil
}
