package core

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

var rankPattern = regexp.MustCompile(`^[1-9][0-9]*/1$`)

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
	StatusInProgress Status = "in-progress"
	StatusBlocked    Status = "blocked"
	StatusDone       Status = "done"
)

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
	if !rankPattern.MatchString(task.Rank) {
		return TaskData{}, Errorf(CategoryValidation, "rank %q must be a positive integer over 1", task.Rank)
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

func isValidStatus(status Status) bool {
	switch status {
	case StatusBacklog, StatusReady, StatusInProgress, StatusBlocked, StatusDone:
		return true
	default:
		return false
	}
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
