package core

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeDocumentUsesCanonicalJSON(t *testing.T) {
	state := StateDocument{
		Format:       "workbook.task-state",
		Version:      1,
		ProjectID:    projectID,
		TaskID:       taskID,
		History:      History{Generation: generationID, CompactedFrom: nil},
		LogicalClock: 1,
		Task: TaskData{
			Title: "Build Git store", Description: "",
			Status: StatusBacklog, Priority: PriorityMedium,
			Labels: []string{"poc", "git"}, Rank: "1/1",
			Dependencies: []string{}, CreatedAt: createdAt,
			UpdatedAt: createdAt, Deleted: false,
		},
	}

	got, err := EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	want := []byte(`{"format":"workbook.task-state","version":1,"projectId":"01K0M6B8A4FTT8C39MXXYTW7C1","taskId":"WB-01K0M6B8A4FTT8C39MXXYTW7C2","history":{"generation":"01K0M6B8A4FTT8C39MXXYTW7C3","compactedFrom":null},"logicalClock":1,"task":{"title":"Build Git store","description":"","status":"backlog","priority":"medium","labels":["git","poc"],"rank":"1/1","dependencies":[],"createdAt":"2026-07-23T12:00:00Z","updatedAt":"2026-07-23T12:00:00Z","deleted":false}}` + "\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeDocument() = %s, want %s", got, want)
	}
	if got[len(got)-1] != '\n' || bytes.Count(got, []byte{'\n'}) != 1 {
		t.Fatalf("EncodeDocument() must contain one trailing LF, got %q", got)
	}

	again, err := EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument() repeat error = %v", err)
	}
	if !bytes.Equal(got, again) {
		t.Fatalf("EncodeDocument() is not deterministic: %q then %q", got, again)
	}
}

func TestDecodeDocumentsRejectUnknownFieldsAndTrailingData(t *testing.T) {
	packBytes, err := json.Marshal(createPack())
	if err != nil {
		t.Fatalf("json.Marshal(operation pack) error = %v", err)
	}
	state := StateDocument{
		Format:       stateDocumentFormat,
		Version:      documentVersion,
		ProjectID:    projectID,
		TaskID:       taskID,
		History:      History{Generation: generationID},
		LogicalClock: 1,
		Task: TaskData{
			Title: "Build Git store", Status: StatusBacklog, Priority: PriorityMedium,
			Labels: []string{}, Rank: "1/1", Dependencies: []string{},
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal(state document) error = %v", err)
	}

	decodedPack, err := DecodeOperationPack(packBytes)
	if err != nil {
		t.Fatalf("DecodeOperationPack() error = %v", err)
	}
	if got, want := decodedPack.TaskID, taskID; got != want {
		t.Fatalf("DecodeOperationPack() task ID = %q, want %q", got, want)
	}
	decodedState, err := DecodeStateDocument(stateBytes)
	if err != nil {
		t.Fatalf("DecodeStateDocument() error = %v", err)
	}
	if got, want := decodedState.History.Generation, generationID; got != want {
		t.Fatalf("DecodeStateDocument() generation = %q, want %q", got, want)
	}

	tests := []struct {
		name   string
		decode func([]byte) error
		bytes  []byte
	}{
		{
			name:   "operation pack unknown field",
			decode: func(bytes []byte) error { _, err := DecodeOperationPack(bytes); return err },
			bytes:  appendUnknownJSONField(packBytes),
		},
		{
			name:   "state document unknown field",
			decode: func(bytes []byte) error { _, err := DecodeStateDocument(bytes); return err },
			bytes:  appendUnknownJSONField(stateBytes),
		},
		{
			name:   "operation pack trailing data",
			decode: func(bytes []byte) error { _, err := DecodeOperationPack(bytes); return err },
			bytes:  append(append([]byte(nil), packBytes...), []byte(" {}")...),
		},
		{
			name:   "state document trailing data",
			decode: func(bytes []byte) error { _, err := DecodeStateDocument(bytes); return err },
			bytes:  append(append([]byte(nil), stateBytes...), []byte(" {}")...),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertCorrupt(t, test.decode(test.bytes))
		})
	}
}

func TestDecodeOperationPackRejectsInvalidOperations(t *testing.T) {
	tests := map[string]func(*OperationPack){
		"unknown type": func(pack *OperationPack) {
			pack.Operations[0].Type = "future.operation"
			pack.Operations[0].Task = nil
		},
		"unsupported field": func(pack *OperationPack) {
			pack.Operations[0] = Operation{ID: operationID1, Type: OperationFieldSet, Field: "labels", Value: "git"}
		},
		"invalid status value": func(pack *OperationPack) {
			pack.Operations[0] = Operation{ID: operationID1, Type: OperationFieldSet, Field: "status", Value: "later"}
		},
		"empty set label": func(pack *OperationPack) {
			pack.Operations[0] = Operation{ID: operationID1, Type: OperationSetAdd, Field: "labels", Value: ""}
		},
		"invalid set dependency": func(pack *OperationPack) {
			pack.Operations[0] = Operation{ID: operationID1, Type: OperationSetRemove, Field: "dependencies", Value: "WB-not-a-ulid"}
		},
		"payload-bearing tombstone": func(pack *OperationPack) {
			pack.Operations[0].Type = OperationTaskTombstone
			pack.Operations[0].Field = "status"
		},
		"field set task payload": func(pack *OperationPack) {
			pack.Operations[0].Type = OperationFieldSet
			pack.Operations[0].Field = "title"
			pack.Operations[0].Value = "Build Git store"
		},
		"invalid operation ULID": func(pack *OperationPack) {
			pack.Operations[0].ID = "not-a-ulid"
		},
		"noncanonical operation ULID": func(pack *OperationPack) {
			pack.Operations[0].ID = strings.ToLower(operationID1)
		},
		"duplicate operation ID": func(pack *OperationPack) {
			pack.Operations = append(pack.Operations, Operation{ID: operationID1, Type: OperationFieldSet, Field: "status", Value: "ready"})
		},
		"task create payload shape": func(pack *OperationPack) {
			pack.Operations[0].Field = "title"
		},
		"task create invalid task": func(pack *OperationPack) {
			pack.Operations[0].Task.Title = " "
		},
		"task create noncanonical task": func(pack *OperationPack) {
			pack.Operations[0].Task.Labels = []string{"poc", "git"}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			pack := createPack()
			mutate(&pack)
			encoded, err := json.Marshal(pack)
			if err != nil {
				t.Fatalf("json.Marshal(operation pack) error = %v", err)
			}
			_, err = DecodeOperationPack(encoded)
			assertCorrupt(t, err)
		})
	}
}

func appendUnknownJSONField(document []byte) []byte {
	result := append([]byte(nil), document[:len(document)-1]...)
	return append(result, []byte(`,"unknown":true}`)...)
}
