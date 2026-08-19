package core

import "strings"

// The display settings a project may record, named exactly as they are typed.
//
// They are untyped string constants rather than a named type, because a setting
// name is a word somebody types at a command line and looks up in a table. It
// is never a member of a struct the way a Status is, so a type would buy one
// conversion at every boundary and nothing at all inside the fold.
const (
	// DisplayProjectName is what the board calls this project, replacing the
	// generic heading every Workbook board otherwise carries.
	DisplayProjectName = "project-name"
	// DisplayPrimaryColor is the accent the board draws its own furniture in.
	DisplayPrimaryColor = "primary-color"
	// DisplayTextColor is the ink the board sets its prose in.
	DisplayTextColor = "text-color"
)

// DefaultProjectName is what a board calls itself when nobody has named the
// project.
//
// It is stated here rather than in the surface that renders it because two
// surfaces read it — the board's heading and `workbook config show`, which has
// to be able to say what "default" resolves to — and a fallback with two
// authors eventually has two values. Nothing stores it: an unconfigured name is
// an absent value, and this is what an absent value looks like when it is read.
const DefaultProjectName = "Workbook board"

// DisplaySettingNames lists the settings in the order every surface presents
// them: the name first, because it is what distinguishes two boards at a
// glance, and then the two colors.
var DisplaySettingNames = []string{DisplayProjectName, DisplayPrimaryColor, DisplayTextColor}

// DisplayDocument is the stored form of a project's display settings.
//
// Every member is omitempty and the whole section is carried by pointer, and
// both are the same decision: the canonical form of "nothing is configured" is
// the absent member rather than an empty object. A configuration checkpoint is
// compared by bytes — ValidateConfigCheckpoint re-encodes and diffs — so a
// section that serialized as `"display":{}` for a project nobody has configured
// would make every checkpoint written before this section existed disagree with
// what this build recomputes for it. normalizeDisplayDocument is what enforces
// that, and validateConfigStateDocument is what asks.
type DisplayDocument struct {
	Name         string `json:"name,omitempty"`
	PrimaryColor string `json:"primaryColor,omitempty"`
	TextColor    string `json:"textColor,omitempty"`
}

// DisplaySettings is a resolved read of the same three values, and the zero
// value means a project that has configured none of them.
//
// It is a second struct rather than the stored one handed out, for the reason
// Vocabulary is a second type beside VocabularyDocument: one is the durable
// format, which may not gain a member without a format decision and a
// generation to carry it, and the other is what callers read, which may. It is
// also a value rather than a pointer, so a caller asking for a name it has not
// configured reads an empty string instead of dereferencing nothing.
//
// It carries no serialization tags, and that is the distinction holding: the
// durable form is DisplayDocument, and every surface that publishes these
// values builds its own view of them — `workbook config show` reports each
// setting beside the source it came from, which is a shape this struct does not
// have. Tags here would be a second wire format for one section, and the first
// caller to marshal it would have made it real.
type DisplaySettings struct {
	Name         string
	PrimaryColor string
	TextColor    string
}

// Configured reports that this project recorded at least one display setting,
// which is the difference between a board that looks like every other Workbook
// board on purpose and one nobody has decided about.
//
// Nothing calls it yet. It belongs to the web board — the surface that has to
// choose between the legacy palette and a computed one, and between the generic
// heading and a project's own — which is PR 2 of this story. It is here rather
// than there because the question is about these three values and the answer is
// one expression over all of them; a caller writing that expression at the call
// site is a caller deciding what "configured" means.
func (settings DisplaySettings) Configured() bool {
	return settings != DisplaySettings{}
}

// Value reads the setting a name refers to, and reports whether the name refers
// to one at all.
//
// It exists because two callers hold a setting name rather than a member: the
// reconcile classifier, comparing what origin says a setting is against what a
// local operation would make it, and `workbook config show`, printing all three
// in order. Both would otherwise write this switch, and a switch with three
// authors eventually disagrees about one arm. The fold does not use it — that
// path needs a pointer to write through, which is a different question.
func (settings DisplaySettings) Value(setting string) (string, bool) {
	switch setting {
	case DisplayProjectName:
		return settings.Name, true
	case DisplayPrimaryColor:
		return settings.PrimaryColor, true
	case DisplayTextColor:
		return settings.TextColor, true
	default:
		return "", false
	}
}

// ValidateDisplaySetting reports whether a word names a display setting.
//
// It is exported for the same reason ValidateStatusToken is: the command line
// builds a configuration operation out of a word somebody typed, every check
// inside the operation document reports a bad member as corrupt data, and a typo
// deserves a validation failure that lists the settings instead.
func ValidateDisplaySetting(setting string) error {
	for _, known := range DisplaySettingNames {
		if setting == known {
			return nil
		}
	}
	return Errorf(
		CategoryValidation,
		"unknown display setting %q; the display settings are %s",
		setting, strings.Join(DisplaySettingNames, ", "),
	)
}

// CanonicalDisplayValue validates one setting's value and returns the form the
// ledger stores.
//
// Canonicalizing here rather than after the fact is what keeps the durable
// format single-valued: a checkpoint is compared by bytes, so `#ABC123` and
// `#abc123` stored as written would be two different configurations that mean
// the same color, and a name typed with a stray space would be a third name. So
// the boundary folds and trims before anything is authored, and
// validateConfigOperationDocument refuses anything that is not already in this
// form as corrupt data.
func CanonicalDisplayValue(setting, value string) (string, error) {
	switch setting {
	case DisplayProjectName:
		name := strings.TrimSpace(value)
		if err := ValidateProjectName(name); err != nil {
			return "", err
		}
		return name, nil
	case DisplayPrimaryColor, DisplayTextColor:
		return ValidateThemeColor(strings.TrimSpace(value))
	default:
		return "", ValidateDisplaySetting(setting)
	}
}

// normalizeDisplayDocument returns the canonical stored form of a display
// section, and refuses one carrying a value no author could have written.
//
// Nil in, nil out; a section whose every member is empty normalizes to nil,
// which is the rule that keeps a project's checkpoint bytes unchanged through
// the whole life of the section — before anybody configures anything, and again
// after they clear the last setting.
//
// The walk follows DisplaySettingNames rather than a map of the three, because
// a section with more than one thing wrong has to be refused with the same
// message on every clone. Go randomizes map iteration per run, so the map form
// picked which of two bad values to name by coin toss, and a refusal somebody
// is meant to act on cannot be one of two sentences.
func normalizeDisplayDocument(document *DisplayDocument) (*DisplayDocument, error) {
	if document == nil {
		return nil, nil
	}
	normalized := *document
	stored := ResolveDisplaySettings(&normalized)
	for _, setting := range DisplaySettingNames {
		value, _ := stored.Value(setting)
		if value == "" {
			continue
		}
		canonical, err := CanonicalDisplayValue(setting, value)
		if err != nil {
			return nil, err
		}
		if canonical != value {
			return nil, Errorf(CategoryValidation, "display setting %s is not stored canonically", setting)
		}
	}
	if normalized == (DisplayDocument{}) {
		return nil, nil
	}
	return &normalized, nil
}

// ResolveDisplaySettings resolves a stored section for reading. An absent
// section and a section this build could not have written are the same answer
// to the caller — nothing is configured — because a decoded checkpoint has
// already been normalized and this cannot be reached with the second.
//
// It is exported for the reconcile path, which classifies a local operation
// against the configuration one fetched commit carried and therefore holds a
// ConfigData rather than a checkpoint.
func ResolveDisplaySettings(document *DisplayDocument) DisplaySettings {
	if document == nil {
		return DisplaySettings{}
	}
	return DisplaySettings{
		Name:         document.Name,
		PrimaryColor: document.PrimaryColor,
		TextColor:    document.TextColor,
	}
}

// configDisplay is the mutable working form of the display section during a
// fold, the sibling of configVocabulary.
type configDisplay struct {
	document DisplayDocument
}

func newConfigDisplay(stored *DisplayDocument) (*configDisplay, error) {
	normalized, err := normalizeDisplayDocument(stored)
	if err != nil {
		return nil, Wrap(CategoryCorruptData, "configuration contains invalid display settings", err)
	}
	display := &configDisplay{}
	if normalized != nil {
		display.document = *normalized
	}
	return display, nil
}

// member points at the stored value a setting names. The bool is false only for
// a name validateConfigOperationDocument already refused, which is why the fold
// can treat it as structure rather than as a value to tolerate.
func (display *configDisplay) member(setting string) (*string, bool) {
	switch setting {
	case DisplayProjectName:
		return &display.document.Name, true
	case DisplayPrimaryColor:
		return &display.document.PrimaryColor, true
	case DisplayTextColor:
		return &display.document.TextColor, true
	default:
		return nil, false
	}
}

// apply records one display operation.
//
// Like the vocabulary's fold it never judges a value: a display.set carrying a
// color this build would refuse to author still folds, because by the time it
// reaches here somebody has already recorded it and refusing would strand the
// clone that fetched it rather than the person who wrote it. Only the shape is
// structure, and the shape was settled by the operation document check.
func (display *configDisplay) apply(operation ConfigOperation) error {
	member, known := display.member(operation.Setting)
	if !known {
		return corrupt("%s names unknown display setting %q", operation.Type, operation.Setting)
	}
	switch operation.Type {
	case ConfigDisplaySet:
		*member = operation.Value
	case ConfigDisplayUnset:
		*member = ""
	default:
		return corrupt("unsupported display operation type %q", operation.Type)
	}
	return nil
}

// canonical returns the section as it is stored, which is nothing at all when
// every setting is empty.
func (display *configDisplay) canonical() *DisplayDocument {
	if display.document == (DisplayDocument{}) {
		return nil
	}
	stored := display.document
	return &stored
}
