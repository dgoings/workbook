package core

import (
	"context"
	"strings"
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
// name becomes the commit's author and committer, and Git silently drops `<`
// and `>` from an identity and trims a few punctuation characters from its
// ends, which would leave the commit naming somebody the operation does not.
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
	if strings.Contains(value, assignmentLabelSeparator) {
		return Errorf(CategoryValidation,
			"the acting person %q must be an email address with no /label", value)
	}
	if err := ValidateAssigneeAuthoring(value); err != nil {
		return Errorf(CategoryValidation, "the acting person %q must be an email address", value)
	}
	if strings.ContainsAny(value, "<>") ||
		strings.ContainsAny(value[:1], actorIdentCrud) ||
		strings.ContainsAny(value[len(value)-1:], actorIdentCrud) {
		return Errorf(CategoryValidation,
			"the acting person %q cannot be recorded as a Git author: it must not contain < or > or begin or end with any of %s",
			value, actorIdentCrud)
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
