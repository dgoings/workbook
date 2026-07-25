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

type StatusDefinition struct {
	Status Status
	Label  string
}

var workflowStatuses = [...]StatusDefinition{
	{Status: StatusBacklog, Label: "Backlog"},
	{Status: StatusReady, Label: "Ready"},
	{Status: StatusBlocked, Label: "Blocked"},
	{Status: StatusInProgress, Label: "In Progress"},
	{Status: StatusInReview, Label: "In Review"},
	{Status: StatusDone, Label: "Done"},
}

func WorkflowStatuses() []StatusDefinition {
	return append([]StatusDefinition(nil), workflowStatuses[:]...)
}

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
)

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

type Task struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	TaskData
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
	if !isValidStatus(task.Status) {
		return TaskData{}, Errorf(CategoryValidation, "invalid task status %q", task.Status)
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
	return task, nil
}

func parseRank(rank string) (*big.Rat, error) {
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

func isValidStatus(status Status) bool {
	for _, definition := range workflowStatuses {
		if status == definition.Status {
			return true
		}
	}
	return false
}

func isValidPriority(priority Priority) bool {
	switch priority {
	case PriorityLow, PriorityMedium, PriorityHigh:
		return true
	default:
		return false
	}
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
