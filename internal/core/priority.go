package core

// PriorityTag marks a priority's role. There is one today: where a task lands
// when nobody names a priority. It is a tag rather than a boolean so it shares
// the normalization statuses use, and so a second role can be added without
// changing the durable shape.
type PriorityTag string

const PriorityTagDefault PriorityTag = "default"

// PriorityDefinition is one priority as the ledger stores it.
type PriorityDefinition struct {
	Priority Priority `json:"priority"`
	Label    string   `json:"label"`
	// Rank orders the priority among its peers, most urgent first. It is the
	// same reduced-rational string a status rank uses, and for the same reason:
	// two clones must be able to insert a priority between the same two
	// neighbours without coordinating.
	Rank string        `json:"rank"`
	Tags []PriorityTag `json:"tags"`
	// Color is the board's ink for this priority, `#rrggbb`. Unset means the
	// board derives one from the priority's position, which is why it is
	// omitempty rather than a stored default: nothing stores a default.
	Color string `json:"color,omitempty"`
}

func (definition PriorityDefinition) HasTag(tag PriorityTag) bool {
	for _, candidate := range definition.Tags {
		if candidate == tag {
			return true
		}
	}
	return false
}

func (definition PriorityDefinition) key() Priority { return definition.Priority }
func (definition PriorityDefinition) rank() string  { return definition.Rank }

// PriorityAlias forwards a priority name a rename retired to the name that
// replaced it. A clone that has not fetched the rename keeps writing the old
// name; this is what lets the clone that has fetched it read those tasks into
// the right group instead of stranding them.
type PriorityAlias struct {
	From Priority `json:"from"`
	To   Priority `json:"to"`
}

// RetiredPriority forwards a removed priority to the one its tasks belong in.
// A removal never rewrites stored task documents.
type RetiredPriority struct {
	Priority    Priority `json:"priority"`
	Destination Priority `json:"destination"`
}

// PriorityDocument is the stored form of a priority vocabulary. Every member is
// a sorted array for the reason VocabularyDocument's are: the checkpoint's
// bytes are compared for equality, so the sort has to be something this package
// decides rather than a property of the encoder.
type PriorityDocument struct {
	Priorities []PriorityDefinition `json:"priorities"`
	Aliases    []PriorityAlias      `json:"aliases"`
	Retired    []RetiredPriority    `json:"retired"`
}

// builtInPriorityDefinitions is the set a project that configured none is read
// as having: today's three, most urgent first, with medium carrying the default
// the service used to hardcode.
func builtInPriorityDefinitions() []PriorityDefinition {
	return []PriorityDefinition{
		{Priority: PriorityHigh, Label: "High", Rank: "1/1"},
		{Priority: PriorityMedium, Label: "Medium", Rank: "2/1", Tags: []PriorityTag{PriorityTagDefault}},
		{Priority: PriorityLow, Label: "Low", Rank: "3/1"},
	}
}
