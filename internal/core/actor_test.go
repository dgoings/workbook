package core

import (
	"context"
	"strings"
	"testing"
)

func TestValidateActorAcceptsAnAssignablePerson(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"alice@example.com",
		"a.b+tag@sub.example.co",
		"ä@example.com",
		"alice@example.com.",
	} {
		if err := ValidateActor(value); err != nil {
			t.Errorf("ValidateActor(%q) = %v, want accepted", value, err)
		}
		// The actor rule is the assignment rule narrowed, never widened: a name
		// this accepts is one --assign accepts as a person.
		if err := ValidateAssigneeAuthoring(value); err != nil {
			t.Errorf("ValidateAssigneeAuthoring(%q) = %v, but ValidateActor accepted it", value, err)
		}
	}
}

func TestValidateActorRefusesAnythingButAnEmailAddress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		value string
		want  string
	}{
		{"", "must not be blank"},
		{"alice", "must be an email address"},
		{"alice@example", "must be an email address"},
		{"@example.com", "must be an email address"},
		{"alice@example.com/impl-1", "with no /label"},
		{"alice@example.com/", "with no /label"},
		{" alice@example.com", "must be an email address"},
		{"alice@example.com ", "must be an email address"},
		{"alice@exa mple.com", "must be an email address"},
		{"alice@example.com\n", "must be an email address"},
		{"al\x7fice@example.com", "must be an email address"},
		{"a@b@example.com", "must be an email address"},
		{"a<b@example.com", "cannot be recorded as a Git author"},
		{"a>b@example.com", "cannot be recorded as a Git author"},
		{`"alice@example.com`, "cannot be recorded as a Git author"},
		{"alice@example.com'", "cannot be recorded as a Git author"},
		{"alice@example.com;", "cannot be recorded as a Git author"},
		{",alice@example.com", "cannot be recorded as a Git author"},
		{`\alice@example.com`, "cannot be recorded as a Git author"},
		{strings.Repeat("a", MaxAssignmentPrincipalBytes) + "@example.com", "must not exceed"},
		{"s\xffam@example.com", "must be valid UTF-8"},
		{"sam@example.com\xc3", "must be valid UTF-8"},
	}
	for _, tc := range cases {
		err := ValidateActor(tc.value)
		if err == nil {
			t.Errorf("ValidateActor(%q) accepted it", tc.value)
			continue
		}
		if CategoryOf(err) != CategoryValidation {
			t.Errorf("ValidateActor(%q) category = %q, want validation", tc.value, CategoryOf(err))
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ValidateActor(%q) = %q, want it to mention %q", tc.value, err, tc.want)
		}
	}
}

func TestValidateActorDoesNotQuoteAnOversizedValue(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("x", 1<<20) + "@example.com"
	err := ValidateActor(value)
	if err == nil {
		t.Fatal("ValidateActor accepted a megabyte name")
	}
	if len(err.Error()) > 200 {
		t.Fatalf("error is %d bytes; it quoted the value", len(err.Error()))
	}
}

func TestWithActorCarriesEachContextsOwnPerson(t *testing.T) {
	t.Parallel()
	base := context.Background()
	if _, ok := ActorFrom(base); ok {
		t.Fatal("a plain context carries an actor")
	}
	alice, err := WithActor(base, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := WithActor(base, "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := ActorFrom(alice); !ok || got != "alice@example.com" {
		t.Fatalf("ActorFrom(alice) = %q, %v", got, ok)
	}
	if got, ok := ActorFrom(bob); !ok || got != "bob@example.com" {
		t.Fatalf("ActorFrom(bob) = %q, %v", got, ok)
	}
	if _, ok := ActorFrom(base); ok {
		t.Fatal("deriving a context gave its parent an actor")
	}
}

func TestWithActorRefusesAnUnusableName(t *testing.T) {
	t.Parallel()
	ctx, err := WithActor(context.Background(), "alice@example.com/impl-1")
	if err == nil {
		t.Fatal("WithActor accepted a labelled name")
	}
	if _, ok := ActorFrom(ctx); ok {
		t.Fatal("a refused name still reached the context")
	}
}

func TestWithEnvironmentActor(t *testing.T) {
	t.Parallel()
	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) {
			value, ok := values[name]
			return value, ok
		}
	}

	unset, err := WithEnvironmentActor(context.Background(), lookup(nil))
	if err != nil {
		t.Fatalf("unset: %v", err)
	}
	if _, ok := ActorFrom(unset); ok {
		t.Fatal("an unset variable named an actor")
	}

	set, err := WithEnvironmentActor(context.Background(), lookup(map[string]string{ActorVariable: "alice@example.com"}))
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if got, ok := ActorFrom(set); !ok || got != "alice@example.com" {
		t.Fatalf("ActorFrom = %q, %v", got, ok)
	}

	for _, value := range []string{"", "   ", "alice", "alice@example.com/impl-1"} {
		ctx, err := WithEnvironmentActor(context.Background(), lookup(map[string]string{ActorVariable: value}))
		if err == nil {
			t.Errorf("%s=%q was accepted", ActorVariable, value)
			continue
		}
		if CategoryOf(err) != CategoryValidation {
			t.Errorf("%s=%q category = %q, want validation", ActorVariable, value, CategoryOf(err))
		}
		if !strings.Contains(err.Error(), ActorVariable) {
			t.Errorf("%s=%q error %q does not name the variable", ActorVariable, value, err)
		}
		if _, ok := ActorFrom(ctx); ok {
			t.Errorf("%s=%q reached the context", ActorVariable, value)
		}
	}
}

// An operation is stored as JSON, whose encoder turns each byte that is not
// UTF-8 into U+FFFD, so a value that is not would be recorded as somebody else.
// The assignment rule's patterns cannot see it — Go's regexp reads a bad byte as
// U+FFFD, which they accept — so it is checked on its own, and nothing the rule
// accepted before is refused now.
func TestValidateAssigneeAuthoringRefusesInvalidUTF8Only(t *testing.T) {
	t.Parallel()
	if !assignmentPrincipalPattern.MatchString("s\xffam@example.com") {
		t.Fatal("the principal pattern refuses invalid UTF-8 by itself; this test no longer covers a hole")
	}
	for _, value := range []string{"s\xffam@example.com", "sam@example.com/impl\xff", "sam@exa\xc3mple.com"} {
		err := ValidateAssigneeAuthoring(value)
		if err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
			t.Errorf("ValidateAssigneeAuthoring(%q) = %v, want it refused as invalid UTF-8", value, err)
		}
	}
	for _, value := range []string{"sam@example.com", "ä@example.com", "sam@example.com/ünï code", "josé@exämple.com/impl-1"} {
		if err := ValidateAssigneeAuthoring(value); err != nil && strings.Contains(err.Error(), "UTF-8") {
			t.Errorf("ValidateAssigneeAuthoring(%q) = %v; a valid UTF-8 value was refused for its encoding", value, err)
		}
	}
	for _, value := range []string{"sam@example.com", "ä@example.com", "josé@exämple.com/impl-1"} {
		if err := ValidateAssigneeAuthoring(value); err != nil {
			t.Errorf("ValidateAssigneeAuthoring(%q) = %v, want accepted", value, err)
		}
	}
}

func TestValidateGitIdentityRefusesWhatGitWouldNotRecordFaithfully(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"sam@example.com", "Sam@Example.com", "ä@example.com", ".sam@example.com."} {
		if err := ValidateGitIdentity(value); err != nil {
			t.Errorf("ValidateGitIdentity(%q) = %v, want accepted", value, err)
		}
	}
	for _, value := range []string{"", "s\xffam@example.com", "a<b@example.com", "a>b", " sam@example.com", "sam@example.com ",
		"sam\n@example.com", "sam\x00@example.com", "sam\x7f@example.com", ",sam@example.com", "sam@example.com;", `"sam`, "sam'", `\sam`, "sam:"} {
		if err := ValidateGitIdentity(value); err == nil {
			t.Errorf("ValidateGitIdentity(%q) accepted it", value)
		}
	}
}
