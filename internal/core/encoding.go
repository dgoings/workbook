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
	case ProjectIdentity:
		if err := validateProjectIdentityDocument(typed); err != nil {
			return nil, err
		}
		document = typed
	case ConfigOperationPack:
		if err := validateConfigOperationPackDocument(typed); err != nil {
			return nil, err
		}
		document = typed
	case ConfigStateDocument:
		if err := validateConfigStateDocument(typed); err != nil {
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

// newerGeneration reports the writer-format generation a document that failed a
// strict decode was claiming, and whether it was claiming one this build cannot
// meet.
//
// It runs only after a strict decode has already failed, which is deliberate on
// two counts. Every stored document is decoded on every read, so a marker
// pre-pass ahead of the strict decode would parse the whole corpus twice to
// learn something that is absent from every document any build has written. And
// it is not needed earlier: a newer document either carries members this build
// does not know — in which case the strict decode rejects it and this runs — or
// it does not, in which case the strict decode succeeds and the marker is
// simply one of the members it read.
//
// A document that does not parse at all cannot claim anything, so its original
// failure stands. That is what keeps "corrupt" available as an answer: the
// marker is a claim inside a well-formed document, not a way to opt out of
// being one.
func newerGeneration(data []byte) (int, bool) {
	var marker struct {
		MinReader int `json:"minReader"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&marker); err != nil {
		return 0, false
	}
	return marker.MinReader, marker.MinReader > SupportedFormatGeneration
}

// DecodeOperationPack strictly decodes a persisted operation pack, unless the
// pack declares a generation this build cannot fold — in which case it is read
// leniently and returned marked, so that a reader can still say which task it
// belongs to and every fold refuses it by name.
func DecodeOperationPack(data []byte) (OperationPack, error) {
	var pack OperationPack
	if err := decodeOneJSON(data, &pack); err != nil {
		generation, newer := newerGeneration(data)
		if !newer {
			return OperationPack{}, err
		}
		if lenientErr := decodeOneLenientJSON(data, &pack); lenientErr != nil {
			return OperationPack{}, newerWriter(
				"an operation pack written by a newer workbook could not be read; upgrade workbook")
		}
		pack.MinReader = generation
	}
	if pack.RequiresNewerReader() {
		if err := validateNewerOperationPack(pack); err != nil {
			return OperationPack{}, err
		}
		return pack, nil
	}
	if err := validateOperationPackDurableDocument(pack); err != nil {
		return OperationPack{}, err
	}
	return pack, nil
}

// DecodeStateDocument strictly decodes a persisted task state document, with
// the same exception the operation pack makes for a newer generation.
//
// A newer checkpoint is what makes reads keep working. The whole point of the
// contract is that list, board, show and next serve the task from the stored
// state rather than from a fold, so this must hand back a usable task even
// though it cannot vouch for the document's every member.
func DecodeStateDocument(data []byte) (StateDocument, error) {
	var state StateDocument
	if err := decodeOneJSON(data, &state); err != nil {
		generation, newer := newerGeneration(data)
		if !newer {
			return StateDocument{}, err
		}
		if lenientErr := decodeOneLenientJSON(data, &state); lenientErr != nil {
			return StateDocument{}, newerWriter(
				"a task state written by a newer workbook could not be read; upgrade workbook")
		}
		state.MinReader = generation
	}
	if state.RequiresNewerReader() {
		return validateNewerStateDocument(state)
	}
	if err := validateStateDurableDocument(state); err != nil {
		return StateDocument{}, err
	}
	return state, nil
}

// validateNewerOperationPack checks what a newer pack still has to answer for,
// and judges nothing else.
//
// What it checks is what stays true across every generation of this format: the
// document is the kind of document the tree said it was, it names a project and
// a task, and it carries operations. Version, identifier shapes, clocks and the
// operations themselves are deliberately not checked. A build that declared a
// generation this one does not have has told us its rules are not our rules,
// and applying ours would turn "written by a newer workbook" back into
// "corrupt" one field at a time.
func validateNewerOperationPack(pack OperationPack) error {
	if pack.Format != operationPackFormat {
		return corrupt("unsupported operation pack format %q", pack.Format)
	}
	if strings.TrimSpace(pack.ProjectID) == "" || strings.TrimSpace(pack.TaskID) == "" {
		return newerWriter("an operation pack written by a newer workbook names no task; upgrade workbook")
	}
	if len(pack.Operations) == 0 {
		return newerWriterTask(pack.TaskID)
	}
	return nil
}

// validateNewerStateDocument makes a newer checkpoint usable enough to show the
// task, and no further.
//
// The stored task is normalized rather than compared against its normal form.
// Comparing is how this build proves a checkpoint is canonical, and it cannot
// prove that about a document whose members it did not all decode; normalizing
// is how every downstream reader gets the total value it expects — a task with
// arrays rather than nulls. A task that will not normalize at all is reported
// as newer-writer rather than corrupt, for the same reason everything else here
// is: under a marker this build cannot meet, every failure to make sense of the
// document is explained by the marker.
func validateNewerStateDocument(state StateDocument) (StateDocument, error) {
	if state.Format != stateDocumentFormat {
		return StateDocument{}, corrupt("unsupported task state format %q", state.Format)
	}
	if strings.TrimSpace(state.ProjectID) == "" || strings.TrimSpace(state.TaskID) == "" {
		return StateDocument{}, newerWriter(
			"a task state written by a newer workbook names no task; upgrade workbook")
	}
	projectKey, _, found := strings.Cut(state.TaskID, "-")
	if !found {
		return StateDocument{}, newerWriterTask(state.TaskID)
	}
	normalized, err := normalizeCanonicalTask(projectKey, state.Task)
	if err != nil {
		return StateDocument{}, newerWriterTask(state.TaskID)
	}
	state.Task = normalized
	return state, nil
}

// DecodeProjectIdentity strictly decodes a persisted project identity.
func DecodeProjectIdentity(data []byte) (ProjectIdentity, error) {
	var identity ProjectIdentity
	if err := decodeOneJSON(data, &identity); err != nil {
		return ProjectIdentity{}, err
	}
	if err := validateProjectIdentityDocument(identity); err != nil {
		return ProjectIdentity{}, err
	}
	return identity, nil
}

// DecodeConfigOperationPack strictly decodes a persisted configuration
// operation pack.
func DecodeConfigOperationPack(data []byte) (ConfigOperationPack, error) {
	var pack ConfigOperationPack
	if err := decodeOneJSON(data, &pack); err != nil {
		generation, newer := newerGeneration(data)
		if !newer {
			return ConfigOperationPack{}, err
		}
		if lenientErr := decodeOneLenientJSON(data, &pack); lenientErr != nil {
			return ConfigOperationPack{}, newerWriterConfig()
		}
		pack.MinReader = generation
	}
	if pack.RequiresNewerReader() {
		if pack.Format != configOperationPackFormat {
			return ConfigOperationPack{}, corrupt("unsupported configuration operation pack format %q", pack.Format)
		}
		if len(pack.Operations) == 0 {
			return ConfigOperationPack{}, newerWriterConfig()
		}
		return pack, nil
	}
	if err := validateConfigOperationPackDocument(pack); err != nil {
		return ConfigOperationPack{}, err
	}
	return pack, nil
}

// DecodeConfigStateDocument strictly decodes a persisted configuration
// checkpoint. A document that decodes here is canonical, which is what lets
// every Vocabulary accessor built from one be total.
// A checkpoint carrying a newer writer-format generation is the one exception,
// and it is read leniently for the reason the whole contract exists: resolving
// a status has to keep working while the ledger is unfoldable, or a clone that
// is merely out of date cannot render a board. Its vocabulary is normalized
// rather than compared against its normal form — this build cannot prove a
// document canonical when it did not decode all of it, but it can still make
// the value total, which is what the accessors need.
func DecodeConfigStateDocument(data []byte) (ConfigStateDocument, error) {
	var state ConfigStateDocument
	if err := decodeOneJSON(data, &state); err != nil {
		generation, newer := newerGeneration(data)
		if !newer {
			return ConfigStateDocument{}, err
		}
		if lenientErr := decodeOneLenientJSON(data, &state); lenientErr != nil {
			return ConfigStateDocument{}, newerWriterConfig()
		}
		state.MinReader = generation
	}
	if state.RequiresNewerReader() {
		if state.Format != configStateDocumentFormat {
			return ConfigStateDocument{}, corrupt("unsupported configuration state format %q", state.Format)
		}
		normalized, err := normalizeVocabularyDocument(state.Config.Vocabulary)
		if err != nil {
			return ConfigStateDocument{}, newerWriterConfig()
		}
		state.Config.Vocabulary = normalized
		return state, nil
	}
	if err := validateConfigStateDocument(state); err != nil {
		return ConfigStateDocument{}, err
	}
	return state, nil
}

func validateProjectIdentityDocument(identity ProjectIdentity) error {
	if identity.Format != ProjectIdentityFormat {
		return Errorf(CategoryCorruptData, "unsupported Workbook project identity format %q", identity.Format)
	}
	if identity.Version != ProjectIdentityVersion {
		return Errorf(CategoryCorruptData, "unsupported Workbook project identity version %d", identity.Version)
	}
	if err := ValidateProjectID(identity.ProjectID); err != nil {
		return Wrap(CategoryCorruptData, "Workbook project identity project ID is invalid", err)
	}
	if err := ValidateProjectKey(identity.Key); err != nil {
		return Wrap(CategoryCorruptData, "Workbook project identity project key is invalid", err)
	}
	return nil
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
	return decodeOne(decoder, destination)
}

// decodeOneLenientJSON reads a document whose member set this build does not
// know all of, keeping the one-value-per-object rule and dropping only the
// unknown-member rejection. It is used exclusively for documents that declared
// a newer writer-format generation, where an unrecognized member is the
// expected consequence of the marker rather than evidence of corruption.
func decodeOneLenientJSON(data []byte, destination any) error {
	return decodeOne(json.NewDecoder(bytes.NewReader(data)), destination)
}

func decodeOne(decoder *json.Decoder, destination any) error {
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
