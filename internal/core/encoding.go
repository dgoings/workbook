package core

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// EncodeDocument serializes a known durable document as one canonical JSON line.
func EncodeDocument(value any) ([]byte, error) {
	var document any
	switch typed := value.(type) {
	case OperationPack:
		normalized, err := normalizeOperationPackDocument(typed)
		if err != nil {
			return nil, err
		}
		document = normalized
	case StateDocument:
		normalized, err := normalizeStateDocument(typed)
		if err != nil {
			return nil, err
		}
		document = normalized
	default:
		return nil, Errorf(CategoryValidation, "cannot encode unsupported document type %T", value)
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, Wrap(CategoryValidation, "cannot encode document", err)
	}
	return append(encoded, '\n'), nil
}

// DecodeOperationPack strictly decodes a persisted operation pack.
func DecodeOperationPack(data []byte) (OperationPack, error) {
	var pack OperationPack
	if err := decodeOneJSON(data, &pack); err != nil {
		return OperationPack{}, err
	}
	projectKey, err := projectKeyFromTaskID(pack.TaskID)
	if err != nil {
		return OperationPack{}, Wrap(CategoryCorruptData, "operation pack task ID is invalid", err)
	}
	if err := validateOperationPackEnvelope(pack, projectKey); err != nil {
		return OperationPack{}, err
	}
	return pack, nil
}

// DecodeStateDocument strictly decodes a persisted task state document.
func DecodeStateDocument(data []byte) (StateDocument, error) {
	var state StateDocument
	if err := decodeOneJSON(data, &state); err != nil {
		return StateDocument{}, err
	}
	projectKey, err := projectKeyFromTaskID(state.TaskID)
	if err != nil {
		return StateDocument{}, Wrap(CategoryCorruptData, "task state task ID is invalid", err)
	}
	if err := validateStateDocument(state, projectKey); err != nil {
		return StateDocument{}, err
	}
	return state, nil
}

func normalizeOperationPackDocument(pack OperationPack) (OperationPack, error) {
	projectKey, err := projectKeyFromTaskID(pack.TaskID)
	if err != nil {
		return OperationPack{}, err
	}
	if err := validateOperationPackEnvelope(pack, projectKey); err != nil {
		return OperationPack{}, err
	}

	normalized := pack
	normalized.Operations = make([]Operation, len(pack.Operations))
	for i, operation := range pack.Operations {
		normalizedOperation := operation
		if operation.Task != nil {
			task, err := normalizeCanonicalTask(projectKey, copyTaskData(*operation.Task))
			if err != nil {
				return OperationPack{}, Wrap(CategoryValidation, "operation task is invalid", err)
			}
			normalizedOperation.Task = &task
		}
		normalized.Operations[i] = normalizedOperation
	}
	return normalized, nil
}

func normalizeStateDocument(state StateDocument) (StateDocument, error) {
	projectKey, err := projectKeyFromTaskID(state.TaskID)
	if err != nil {
		return StateDocument{}, err
	}
	if err := validateStateEnvelope(state, projectKey); err != nil {
		return StateDocument{}, err
	}

	normalized := state
	normalized.Task, err = normalizeCanonicalTask(projectKey, copyTaskData(state.Task))
	if err != nil {
		return StateDocument{}, Wrap(CategoryValidation, "state task is invalid", err)
	}
	if state.History.CompactedFrom != nil {
		compactedFrom := *state.History.CompactedFrom
		normalized.History.CompactedFrom = &compactedFrom
	}
	return normalized, nil
}

func decodeOneJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return Wrap(CategoryCorruptData, "cannot decode document", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return corrupt("document contains more than one JSON value")
		}
		return Wrap(CategoryCorruptData, "cannot decode document suffix", err)
	}
	return nil
}

func projectKeyFromTaskID(taskID string) (string, error) {
	projectKey, _, found := strings.Cut(taskID, "-")
	if !found {
		return "", Errorf(CategoryValidation, "task ID %q is missing a project key", taskID)
	}
	if err := ValidateTaskID(projectKey, taskID); err != nil {
		return "", err
	}
	return projectKey, nil
}
