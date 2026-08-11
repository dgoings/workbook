package core

import (
	"bytes"
	"testing"
)

func validProjectIdentity() ProjectIdentity {
	return ProjectIdentity{
		Format:    ProjectIdentityFormat,
		Version:   ProjectIdentityVersion,
		ProjectID: projectID,
		Key:       "WB",
	}
}

// The identity document's bytes are a durable interface: two clones publish the
// same Git object only if they encode the same bytes, so this pins them.
func TestEncodeProjectIdentityUsesCanonicalJSON(t *testing.T) {
	got, err := EncodeDocument(validProjectIdentity())
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	want := []byte(`{"format":"workbook.project-identity","version":1,"projectId":"01K0M6B8A4FTT8C39MXXYTW7C1","key":"WB"}` + "\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeDocument() = %s, want %s", got, want)
	}
	if got[len(got)-1] != '\n' || bytes.Count(got, []byte{'\n'}) != 1 {
		t.Fatalf("EncodeDocument() must contain one trailing LF, got %q", got)
	}
}

// The identity format string is deliberately not the tracked configuration's.
// They carry different fields, are written by different authorities, and have
// different lifecycles, so neither may decode as the other.
func TestProjectIdentityFormatIsDistinctFromTheTrackedConfiguration(t *testing.T) {
	if ProjectIdentityFormat == "workbook.project" {
		t.Fatal("the identity document must not reuse the tracked configuration's format string")
	}
	tracked := []byte(`{"format":"workbook.project","version":2,"projectId":"` + projectID + `","key":"WB"}` + "\n")
	if _, err := DecodeProjectIdentity(tracked); err == nil {
		t.Fatal("DecodeProjectIdentity() accepted a tracked configuration document")
	}
}

func TestDecodeProjectIdentityRoundTrips(t *testing.T) {
	want := validProjectIdentity()
	encoded, err := EncodeDocument(want)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}

	got, err := DecodeProjectIdentity(encoded)
	if err != nil {
		t.Fatalf("DecodeProjectIdentity() error = %v", err)
	}
	if got != want {
		t.Fatalf("DecodeProjectIdentity() = %#v, want %#v", got, want)
	}
}

func TestDecodeProjectIdentityRejectsMalformedDocuments(t *testing.T) {
	for name, contents := range map[string]string{
		"malformed":          `{"format":`,
		"foreign format":     `{"format":"other.identity","version":1,"projectId":"` + projectID + `","key":"WB"}`,
		"future version":     `{"format":"workbook.project-identity","version":2,"projectId":"` + projectID + `","key":"WB"}`,
		"unknown field":      `{"format":"workbook.project-identity","version":1,"projectId":"` + projectID + `","key":"WB","autoSync":true}`,
		"invalid project ID": `{"format":"workbook.project-identity","version":1,"projectId":"not-a-ulid","key":"WB"}`,
		"lowercase ULID":     `{"format":"workbook.project-identity","version":1,"projectId":"01k0m6b8a4ftt8c39mxxytw7c1","key":"WB"}`,
		"invalid key":        `{"format":"workbook.project-identity","version":1,"projectId":"` + projectID + `","key":"wb"}`,
		"two values":         `{"format":"workbook.project-identity","version":1,"projectId":"` + projectID + `","key":"WB"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeProjectIdentity([]byte(contents)); err == nil {
				t.Fatalf("DecodeProjectIdentity(%q) error = nil, want a corrupt-data failure", contents)
			} else if got := CategoryOf(err); got != CategoryCorruptData {
				t.Fatalf("DecodeProjectIdentity(%q) category = %q, want %q", contents, got, CategoryCorruptData)
			}
		})
	}
}

func TestEncodeDocumentRejectsInvalidProjectIdentity(t *testing.T) {
	invalid := validProjectIdentity()
	invalid.ProjectID = "not-a-ulid"

	if _, err := EncodeDocument(invalid); err == nil {
		t.Fatal("EncodeDocument() error = nil, want a rejection of an invalid identity")
	}
}
