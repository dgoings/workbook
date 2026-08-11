package core

import (
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"
)

var rankPattern = regexp.MustCompile(`^[1-9][0-9]*/[1-9][0-9]*$`)

type ProjectConfig struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	ProjectID string `json:"projectId"`
	Key       string `json:"key"`
	// AutoSync is the project's automatic synchronization policy. An unset
	// policy defers to the user configuration and then to the built-in
	// default.
	AutoSync AutoSyncSetting `json:"autoSync,omitempty"`
}

// SameIdentity reports whether two configurations name the same project.
//
// Identity is the format, the project ID, and the project key. Mutable
// preferences such as the automatic synchronization policy are deliberately
// excluded: the private project guard exists to detect a swapped project, and
// changing a preference must not read as corruption. The document version is
// excluded too, so a version 1 guard still matches a version 2 configuration.
func (config ProjectConfig) SameIdentity(other ProjectConfig) bool {
	return config.Format == other.Format &&
		config.ProjectID == other.ProjectID &&
		config.Key == other.Key
}

// ProjectConfig is compared with == throughout the repository boundary, so it
// must stay comparable. This assertion fails to compile the moment a field
// that cannot be a map key is added, which is the cheapest possible warning
// that roughly a dozen call sites — validateRepositoryConfig above all — would
// otherwise start failing at run time instead.
var _ = map[ProjectConfig]struct{}{}

// ProjectIdentityFormat names the canonical identity document Workbook stores
// in its project identity ref.
//
// It is deliberately distinct from ProjectConfig's "workbook.project": the two
// documents carry different fields, are written by different authorities, and
// have different lifecycles. The identity document is immutable after mint and
// owned by the ref; the tracked configuration is mutable, committed, and from
// v0.5.0 carries identity only as an advisory copy.
const ProjectIdentityFormat = "workbook.project-identity"

// ProjectIdentityVersion is the identity document version Workbook writes.
const ProjectIdentityVersion = 1

// ProjectIdentity is a project's canonical, immutable identity.
//
// It carries no timestamp and no actor: the document has to encode to the same
// bytes in every clone that adopts the same project, because two clones
// publishing it independently must produce the same Git object rather than two
// competing histories.
type ProjectIdentity struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	ProjectID string `json:"projectId"`
	Key       string `json:"key"`
}

// SameIdentity reports whether two identity documents name the same project.
// The document version is excluded so a future version that adds a field still
// compares equal on the identity it carries.
func (identity ProjectIdentity) SameIdentity(other ProjectIdentity) bool {
	return identity.ProjectID == other.ProjectID && identity.Key == other.Key
}

// AutoSyncSetting records whether a configuration layer enables automatic
// synchronization, disables it, or leaves the decision to the next layer.
//
// It is a comparable tri-state rather than a *bool because ProjectConfig is
// compared with == when reconciling the tracked configuration against the
// common project guard; two pointers to equal values would compare unequal and
// report corrupt data.
type AutoSyncSetting uint8

const (
	AutoSyncUnset AutoSyncSetting = iota
	AutoSyncEnabled
	AutoSyncDisabled
)

// Enabled reports the policy, falling back to the supplied value when unset.
func (setting AutoSyncSetting) Enabled(fallback bool) bool {
	switch setting {
	case AutoSyncEnabled:
		return true
	case AutoSyncDisabled:
		return false
	default:
		return fallback
	}
}

func (setting AutoSyncSetting) MarshalJSON() ([]byte, error) {
	switch setting {
	case AutoSyncEnabled:
		return []byte("true"), nil
	case AutoSyncDisabled:
		return []byte("false"), nil
	default:
		return nil, Errorf(CategoryValidation, "cannot encode an unset automatic synchronization policy")
	}
}

func (setting *AutoSyncSetting) UnmarshalJSON(contents []byte) error {
	switch string(contents) {
	case "true":
		*setting = AutoSyncEnabled
	case "false":
		*setting = AutoSyncDisabled
	default:
		return Errorf(CategoryCorruptData, "automatic synchronization policy must be true or false")
	}
	return nil
}

type Status string

const (
	StatusBacklog    Status = "backlog"
	StatusReady      Status = "ready"
	StatusBlocked    Status = "blocked"
	StatusInProgress Status = "in-progress"
	StatusInReview   Status = "in-review"
	StatusDone       Status = "done"
)

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
)

type PriorityDefinition struct {
	Priority Priority
	Label    string
}

var priorities = [...]PriorityDefinition{
	{Priority: PriorityLow, Label: "Low"},
	{Priority: PriorityMedium, Label: "Medium"},
	{Priority: PriorityHigh, Label: "High"},
}

func Priorities() []PriorityDefinition {
	return append([]PriorityDefinition(nil), priorities[:]...)
}

type TaskData struct {
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Status       Status    `json:"status"`
	Priority     Priority  `json:"priority"`
	Labels       []string  `json:"labels"`
	Rank         string    `json:"rank"`
	Dependencies []string  `json:"dependencies"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Deleted      bool      `json:"deleted"`
}

// Task is a projected task: the stored document plus the identifiers a caller
// needs to act on it.
//
// Status is the resolved status — the live status the stored value means under
// the project's vocabulary today, not necessarily the token stored in the ref.
// Resolution happens once, in Project, so that no consumer has to remember to
// do it and none of them can disagree about the answer.
type Task struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	TaskData
	// StoredStatus is the status the task's ref actually holds, populated only
	// when it differs from the resolved Status.
	//
	// It is the visible half of a distributed rename. A clone that renames a
	// status cannot rewrite other clones' task refs — history is append-only,
	// and those clones may be offline — so a task keeps its old token until
	// something writes to it. Reporting both values is what keeps that honest:
	// the board shows the column the task belongs in, and a caller that needs
	// to explain why can say what is actually on disk.
	StoredStatus      Status `json:"storedStatus,omitempty"`
	HistoryGeneration string `json:"historyGeneration"`
	Head              string `json:"head"`
}

func NormalizeTask(projectKey string, task TaskData) (TaskData, error) {
	if err := ValidateProjectKey(projectKey); err != nil {
		return TaskData{}, err
	}

	task.Title = strings.TrimSpace(task.Title)
	if task.Title == "" {
		return TaskData{}, Errorf(CategoryValidation, "task title must not be blank")
	}
	// A stored status is checked for shape, not for membership. NormalizeTask
	// runs on every read of every ref, including refs written by a clone whose
	// project configuration this one has not fetched yet, so asking "is this
	// one of the statuses I know about?" here would make a teammate's task
	// unreadable rather than merely unfamiliar. Membership is asked at the
	// mutation boundary, in Service, where a person is choosing a value.
	if err := ValidateStatusToken(task.Status); err != nil {
		return TaskData{}, err
	}
	if !isValidPriority(task.Priority) {
		return TaskData{}, Errorf(CategoryValidation, "invalid task priority %q", task.Priority)
	}
	if _, err := parseRank(task.Rank); err != nil {
		return TaskData{}, err
	}

	labels, err := normalizeLabels(task.Labels)
	if err != nil {
		return TaskData{}, err
	}
	dependencies, err := normalizeDependencies(projectKey, task.Dependencies)
	if err != nil {
		return TaskData{}, err
	}
	task.Labels = labels
	task.Dependencies = dependencies
	if err := validateTaskFieldSizes(task); err != nil {
		return TaskData{}, err
	}
	return task, nil
}

func parseRank(rank string) (*big.Rat, error) {
	// Checked first, and reported without quoting the value: the rest of this
	// function converts the digit string to arbitrary precision and formats it
	// back, and echoing a rejected megabyte into an error message would spend
	// the same cost the ceiling exists to withhold.
	if err := validateRankSize(rank); err != nil {
		return nil, err
	}
	if !rankPattern.MatchString(rank) {
		return nil, Errorf(CategoryValidation, "rank %q must be a positive reduced rational", rank)
	}
	numerator, denominator, _ := strings.Cut(rank, "/")
	num, ok := new(big.Int).SetString(numerator, 10)
	if !ok {
		return nil, Errorf(CategoryValidation, "rank %q must be a positive reduced rational", rank)
	}
	den, ok := new(big.Int).SetString(denominator, 10)
	if !ok {
		return nil, Errorf(CategoryValidation, "rank %q must be a positive reduced rational", rank)
	}
	parsed := new(big.Rat).SetFrac(num, den)
	if formatRank(parsed) != rank {
		return nil, Errorf(CategoryValidation, "rank %q must be a positive reduced rational", rank)
	}
	return parsed, nil
}

func formatRank(rank *big.Rat) string {
	return rank.Num().String() + "/" + rank.Denom().String()
}

func isValidPriority(priority Priority) bool {
	for _, definition := range priorities {
		if priority == definition.Priority {
			return true
		}
	}
	return false
}

func normalizeLabels(labels []string) ([]string, error) {
	if len(labels) == 0 {
		return nil, nil
	}

	unique := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		if label == "" {
			return nil, Errorf(CategoryValidation, "task labels must not be empty")
		}
		unique[label] = struct{}{}
	}
	return sortedKeys(unique), nil
}

func normalizeDependencies(projectKey string, dependencies []string) ([]string, error) {
	if len(dependencies) == 0 {
		return nil, nil
	}

	unique := make(map[string]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		if err := ValidateTaskID(projectKey, dependency); err != nil {
			return nil, err
		}
		unique[dependency] = struct{}{}
	}
	return sortedKeys(unique), nil
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}
