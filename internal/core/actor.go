package core

import (
	"context"
	"strings"
	"unicode/utf8"
)

// ActorVariable is the environment variable that names the person a command
// records its edits under, in place of the checkout's `user.email`.
//
// It exists for hosting: one checkout acting for many signed-in people. It is
// an assertion exactly as `user.email` is one — nothing verifies it and there
// is no roster to check it against — so whoever can set it could already push
// under any name they liked.
const ActorVariable = "WORKBOOK_ACTOR"

// actorIdentCrud is the set of characters Git strips from either end of an
// author or committer name and email. An actor that began or ended with one
// would be recorded in the operation under one name and in the commit behind
// it under another, so ValidateActor refuses it rather than let the two
// disagree. Whitespace and control characters are in Git's set as well, and the
// assignment rule already refuses them anywhere.
const actorIdentCrud = `,:;"'\`

// ValidateActor rejects a name somebody is asserting as the acting person.
//
// The rule is the one an assignment applies to its person — an email address,
// in the same plausibility sense — with no `/label`, because a label names one
// of a person's agents and the actor is the person. It is stricter in one
// respect only, and for the commit's sake rather than the operation's: the
// name becomes the commit's author and committer, and ValidateGitIdentity
// refuses what Git would not record byte for byte, which would leave the
// commit naming somebody the operation does not. A value that is not valid
// UTF-8 is refused by both rules, because the operation's JSON would not
// record it byte for byte either.
//
// The value is checked as given and never trimmed. A name with surrounding
// space is refused like any other name with a space in it, rather than quietly
// turned into a different one.
func ValidateActor(value string) error {
	if value == "" {
		return Errorf(CategoryValidation, "the acting person must not be blank; give an email address")
	}
	// Bounded before anything quotes it, for the reason an assignment value is:
	// a request header can carry far more than anybody's name.
	if len(value) > MaxAssignmentPrincipalBytes {
		return Errorf(CategoryValidation,
			"the acting person is %d bytes and must not exceed %d", len(value), MaxAssignmentPrincipalBytes)
	}
	if !utf8.ValidString(value) {
		return Errorf(CategoryValidation, "the acting person %q must be valid UTF-8", value)
	}
	if strings.Contains(value, assignmentLabelSeparator) {
		return Errorf(CategoryValidation,
			"the acting person %q must be an email address with no /label", value)
	}
	if err := ValidateAssigneeAuthoring(value); err != nil {
		return Errorf(CategoryValidation, "the acting person %q must be an email address", value)
	}
	if err := ValidateGitIdentity(value); err != nil {
		return Errorf(CategoryValidation, "the acting person %q cannot be recorded as a Git author: %s", value, err.Error())
	}
	return nil
}

// ValidateGitIdentity refuses a value Git would not record, byte for byte, as a
// commit's author or committer name and email.
//
// Git drops `<` and `>` from an identity wherever they appear, trims control
// characters, spaces and a few punctuation characters (actorIdentCrud) from
// its ends, and stores what it is given as bytes. A value that is not valid
// UTF-8 would be recorded one way in an operation, whose JSON encoding
// replaces each bad byte with U+FFFD, and another way in the commit. Any of
// these would leave a commit naming somebody the operation does not, which is
// worse than writing nothing.
func ValidateGitIdentity(value string) error {
	if value == "" {
		return Errorf(CategoryValidation, "an identity must not be blank")
	}
	if !utf8.ValidString(value) {
		return Errorf(CategoryValidation, "it must be valid UTF-8")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return Errorf(CategoryValidation, "it must not contain control characters")
		}
	}
	first, last := value[0], value[len(value)-1]
	if strings.ContainsAny(value, "<>") ||
		first == ' ' || last == ' ' ||
		strings.IndexByte(actorIdentCrud, first) >= 0 ||
		strings.IndexByte(actorIdentCrud, last) >= 0 {
		return Errorf(CategoryValidation,
			"it must not contain < or > or begin or end with a space or any of %s", actorIdentCrud)
	}
	return nil
}

type actorContextKey struct{}

// WithActor returns a context that records its operations under actor rather
// than under the checkout's `user.email`.
//
// This is the per-operation seam the environment variable is one caller of.
// Everything that decides who is acting reads it from the context it was
// given — gitstore.Repository.Actor, which every Service the command line
// builds takes its Actor from, and the commit writer, which makes the given
// name the commit's author and committer — so a long-running process serving
// several people can give each request its own context and its own person,
// with no state shared between them. The name is checked by ValidateActor
// first, and a context is never returned for one that fails it.
func WithActor(ctx context.Context, actor string) (context.Context, error) {
	if err := ValidateActor(actor); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, actorContextKey{}, actor), nil
}

// ActorFrom returns the actor WithActor recorded on a context, if any.
func ActorFrom(ctx context.Context) (string, bool) {
	actor, ok := ctx.Value(actorContextKey{}).(string)
	return actor, ok
}

// WithEnvironmentActor applies ActorVariable to a context.
//
// Unset leaves the context exactly as it was, which is what keeps every
// command writing what it wrote before the variable existed. Set is set even
// when it is empty: an empty or unusable value is a mistake somebody made
// while trying to name a person, and falling back to the checkout's own name
// would record their edits under the wrong one without saying so.
func WithEnvironmentActor(ctx context.Context, lookup func(string) (string, bool)) (context.Context, error) {
	value, set := lookup(ActorVariable)
	if !set {
		return ctx, nil
	}
	actorCtx, err := WithActor(ctx, value)
	if err != nil {
		// One message rather than a wrapped cause: the command line prints a
		// validation error's own message and not its cause, and the reason is
		// the half somebody needs to fix the value.
		return ctx, Errorf(CategoryValidation, "%s is set but unusable: %s", ActorVariable, err.Error())
	}
	return actorCtx, nil
}

// ActorFor is the person a Service records ctx's edits under: the actor
// WithActor put on ctx when there is one, and the Service's own Actor — the
// checkout's identity, read when the Service was built — only when there is
// not.
//
// It is the one answer every identity question a Service asks goes through:
// the operation's recorded actor, the assignee `self` resolves to, the creator
// an assignment records, which assignments a person may withdraw, and which
// tasks `next` passes over as somebody else's. A Service is built once and may
// serve many requests, so an Actor captured at construction can only ever be
// the fallback; answering from it while the commit writer answered from the
// context is what let an operation and its commit name different people.
func (s Service) ActorFor(ctx context.Context) string {
	if actor, asserted := ActorFrom(ctx); asserted {
		return actor
	}
	return s.Actor
}

// acting returns s with Actor resolved for ctx, so the unexported helpers
// below a public entry point read one settled answer instead of each asking
// the context again.
func (s Service) acting(ctx context.Context) Service {
	s.Actor = s.ActorFor(ctx)
	return s
}
