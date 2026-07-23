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
		if err := validateOperationPackDurableDocument(typed); err != nil {
			return nil, err
		}
		document = typed
	case StateDocument:
		if err := validateStateDurableDocument(typed); err != nil {
			return nil, err
		}
		document = typed
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
	if err := validateOperationPackDurableDocument(pack); err != nil {
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
	if err := validateStateDurableDocument(state); err != nil {
		return StateDocument{}, err
	}
	return state, nil
}

func validateOperationPackDurableDocument(pack OperationPack) error {
	projectKey, err := projectKeyFromTaskID(pack.TaskID)
	if err != nil {
		return Wrap(CategoryCorruptData, "operation pack task ID is invalid", err)
	}
	return validateOperationPackDocument(pack, projectKey)
}

func validateStateDurableDocument(state StateDocument) error {
	projectKey, err := projectKeyFromTaskID(state.TaskID)
	if err != nil {
		return Wrap(CategoryCorruptData, "task state task ID is invalid", err)
	}
	return validateStateDocument(state, projectKey)
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
