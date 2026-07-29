package webui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strings"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/presentation"
)

const securityPolicy = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

//go:embed assets/index.html
var assets embed.FS

type TaskLister func(context.Context) ([]core.Task, error)

type TaskStatusUpdater func(context.Context, string, core.Status) (core.MutationResult, error)

type TaskPositionUpdater func(context.Context, string, core.PlaceInput) (core.MutationResult, error)

type TaskCreator func(context.Context, core.CreateInput) (core.MutationResult, error)

type TaskUpdater func(context.Context, string, core.UpdateInput) (core.MutationResult, error)

type TaskDeleter func(context.Context, string) (core.MutationResult, error)

type TaskRestorer func(context.Context, string) (core.MutationResult, error)

type TaskDependencyAdder func(context.Context, string, string) (core.MutationResult, error)

type TaskDependencyRemover func(context.Context, string, string) (core.MutationResult, error)

type TasksDocument struct {
	Format       string             `json:"format"`
	Version      int                `json:"version"`
	Tasks        []core.Task        `json:"tasks"`
	Presentation []TaskPresentation `json:"presentation"`
}

type TaskMutationDocument struct {
	Format   string         `json:"format"`
	Version  int            `json:"version"`
	Task     core.Task      `json:"task"`
	Warnings []core.Warning `json:"warnings,omitempty"`
}

type TaskPresentation struct {
	TaskID                string `json:"taskId"`
	IDPrefix              string `json:"idPrefix"`
	DependenciesComplete  int    `json:"dependenciesComplete"`
	DependenciesTotal     int    `json:"dependenciesTotal"`
	WaitingOnDependencies bool   `json:"waitingOnDependencies"`
}

type HealthDocument struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Status  string `json:"status"`
}

type ErrorBody struct {
	Category core.Category `json:"category"`
	Message  string        `json:"message"`
}

type ErrorDocument struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Error   ErrorBody `json:"error"`
}

type handler struct {
	list         TaskLister
	create       TaskCreator
	update       TaskUpdater
	updateStatus TaskStatusUpdater
	position     TaskPositionUpdater
	delete       TaskDeleter
	restore      TaskRestorer
	depend       TaskDependencyAdder
	free         TaskDependencyRemover
	page         *template.Template
	mux          *http.ServeMux
}

type pageData struct {
	Board presentation.Board
}

type updateStatusRequest struct {
	Status core.Status `json:"status"`
}

type positionTaskRequest struct {
	Status core.Status `json:"status"`
	Before string      `json:"before"`
	After  string      `json:"after"`
}

type createTaskRequest struct {
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      core.Status   `json:"status"`
	Priority    core.Priority `json:"priority"`
	Labels      []string      `json:"labels"`
}

type updateTaskRequest struct {
	Title       *string        `json:"title"`
	Description *string        `json:"description"`
	Status      *core.Status   `json:"status"`
	Priority    *core.Priority `json:"priority"`
	Labels      *[]string      `json:"labels"`
}

func NewHandler(list TaskLister, create TaskCreator, update TaskUpdater, updateStatus TaskStatusUpdater) http.Handler {
	return newHandler(list, create, update, updateStatus, nil, nil, nil, nil, nil)
}

func NewHandlerWithTaskMutations(list TaskLister, create TaskCreator, update TaskUpdater, updateStatus TaskStatusUpdater, position TaskPositionUpdater, delete TaskDeleter, restore TaskRestorer, depend TaskDependencyAdder, free TaskDependencyRemover) http.Handler {
	return newHandler(list, create, update, updateStatus, position, delete, restore, depend, free)
}

func newHandler(list TaskLister, create TaskCreator, update TaskUpdater, updateStatus TaskStatusUpdater, position TaskPositionUpdater, delete TaskDeleter, restore TaskRestorer, depend TaskDependencyAdder, free TaskDependencyRemover) http.Handler {
	page := template.Must(template.New("index.html").ParseFS(assets, "assets/index.html"))
	handler := &handler{list: list, create: create, update: update, updateStatus: updateStatus, position: position, delete: delete, restore: restore, depend: depend, free: free, page: page, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /{$}", handler.serveBoard)
	handler.mux.HandleFunc("GET /deleted", handler.serveBoard)
	handler.mux.HandleFunc("GET /tasks/new", handler.serveBoard)
	handler.mux.HandleFunc("GET /tasks/{id}", handler.serveBoard)
	handler.mux.HandleFunc("GET /api/tasks", handler.serveTasks)
	handler.mux.HandleFunc("POST /api/tasks", handler.createTask)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}", handler.updateTask)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}/status", handler.updateTaskStatus)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}/position", handler.positionTask)
	handler.mux.HandleFunc("DELETE /api/tasks/{id}", handler.deleteTask)
	handler.mux.HandleFunc("POST /api/tasks/{id}/restore", handler.restoreTask)
	handler.mux.HandleFunc("PUT /api/tasks/{id}/dependencies/{dependency}", handler.addTaskDependency)
	handler.mux.HandleFunc("DELETE /api/tasks/{id}/dependencies/{dependency}", handler.removeTaskDependency)
	handler.mux.HandleFunc("GET /healthz", handler.serveHealth)
	return http.HandlerFunc(handler.serveHTTP)
}

func (handler *handler) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Security-Policy", securityPolicy)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if method, known := allowedMethod(request.URL.Path); known && !methodAllowed(request.Method, method) {
		writer.Header().Set("Allow", method)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	handler.mux.ServeHTTP(writer, request)
}

func methodAllowed(requestMethod, allowed string) bool {
	for _, method := range strings.Split(allowed, ", ") {
		if requestMethod == method {
			return true
		}
	}
	return false
}

func allowedMethod(path string) (string, bool) {
	switch path {
	case "/", "/deleted", "/healthz", "/tasks/new":
		return http.MethodGet, true
	case "/api/tasks":
		return http.MethodGet + ", " + http.MethodPost, true
	default:
		if _, _, ok := taskDependencyPathIDs(path); ok {
			return http.MethodPut + ", " + http.MethodDelete, true
		}
		if taskPositionPathID(path) != "" {
			return http.MethodPatch, true
		}
		if taskStatusPathID(path) != "" {
			return http.MethodPatch, true
		}
		if taskRestorePathID(path) != "" {
			return http.MethodPost, true
		}
		if taskPathID(path) != "" {
			return http.MethodPatch + ", " + http.MethodDelete, true
		}
		if taskPagePathID(path) != "" {
			return http.MethodGet, true
		}
		return "", false
	}
}

func taskDependencyPathIDs(path string) (string, string, bool) {
	const prefix = "/api/tasks/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 3 || parts[0] == "" ||
		parts[1] != "dependencies" || parts[2] == "" {
		return "", "", false
	}
	return parts[0], parts[2], true
}

func taskPagePathID(path string) string {
	const prefix = "/tasks/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	id := strings.TrimPrefix(path, prefix)
	if id == "" || id == "new" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func taskPathID(path string) string {
	const prefix = "/api/tasks/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	id := strings.TrimPrefix(path, prefix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func taskStatusPathID(path string) string {
	const prefix = "/api/tasks/"
	const suffix = "/status"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func taskPositionPathID(path string) string {
	const prefix = "/api/tasks/"
	const suffix = "/position"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func taskRestorePathID(path string) string {
	const prefix = "/api/tasks/"
	const suffix = "/restore"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func (handler *handler) serveBoard(writer http.ResponseWriter, request *http.Request) {
	tasks, err := handler.list(request.Context())
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := handler.page.Execute(writer, pageData{Board: presentation.NewBoard(activeTasks(tasks))}); err != nil {
		return
	}
}

func (handler *handler) serveTasks(writer http.ResponseWriter, request *http.Request) {
	tasks, err := handler.list(request.Context())
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	if request.URL.Query().Get("deleted") == "true" {
		tasks = deletedTasks(tasks)
	} else {
		tasks = activeTasks(tasks)
	}
	writeJSON(writer, http.StatusOK, TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: taskPresentation(tasks),
	})
}

func activeTasks(tasks []core.Task) []core.Task {
	active := make([]core.Task, 0, len(tasks))
	for _, task := range tasks {
		if !task.Deleted {
			active = append(active, task)
		}
	}
	return active
}

func deletedTasks(tasks []core.Task) []core.Task {
	deleted := make([]core.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Deleted {
			deleted = append(deleted, task)
		}
	}
	return deleted
}

func (handler *handler) updateTaskStatus(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		id = taskStatusPathID(request.URL.Path)
	}
	var input updateStatusRequest
	if err := decodeRequest(request.Body, &input); err != nil {
		handler.writeError(writer, core.Wrap(core.CategoryInvocation, "decode status update", err))
		return
	}
	result, err := handler.updateStatus(request.Context(), id, input.Status)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) positionTask(writer http.ResponseWriter, request *http.Request) {
	if handler.position == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task positioning is not configured"))
		return
	}
	var body positionTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, core.Wrap(core.CategoryInvocation, "decode task position", err))
		return
	}
	result, err := handler.position(
		request.Context(),
		request.PathValue("id"),
		core.PlaceInput{Status: body.Status, Before: body.Before, After: body.After},
	)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) createTask(writer http.ResponseWriter, request *http.Request) {
	var body createTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, core.Wrap(core.CategoryInvocation, "decode task create", err))
		return
	}
	if handler.create == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task creation is not configured"))
		return
	}
	result, err := handler.create(request.Context(), core.CreateInput(body))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) updateTask(writer http.ResponseWriter, request *http.Request) {
	var body updateTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, core.Wrap(core.CategoryInvocation, "decode task update", err))
		return
	}
	if handler.update == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task updating is not configured"))
		return
	}
	id := request.PathValue("id")
	if id == "" {
		id = taskPathID(request.URL.Path)
	}
	result, err := handler.update(request.Context(), id, core.UpdateInput(body))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) deleteTask(writer http.ResponseWriter, request *http.Request) {
	if handler.delete == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task deletion is not configured"))
		return
	}
	result, err := handler.delete(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) restoreTask(writer http.ResponseWriter, request *http.Request) {
	if handler.restore == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task restoration is not configured"))
		return
	}
	result, err := handler.restore(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) addTaskDependency(writer http.ResponseWriter, request *http.Request) {
	if handler.depend == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task dependency addition is not configured"))
		return
	}
	result, err := handler.depend(request.Context(), request.PathValue("id"), request.PathValue("dependency"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) removeTaskDependency(writer http.ResponseWriter, request *http.Request) {
	if handler.free == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task dependency removal is not configured"))
		return
	}
	result, err := handler.free(request.Context(), request.PathValue("id"), request.PathValue("dependency"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func decodeRequest(body io.Reader, value any) error {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (handler *handler) writeTaskMutation(writer http.ResponseWriter, result core.MutationResult) {
	writeJSON(writer, http.StatusOK, TaskMutationDocument{
		Format:   "workbook.task-mutation",
		Version:  1,
		Task:     result.Task,
		Warnings: result.Warnings,
	})
}

func taskPresentation(tasks []core.Task) []TaskPresentation {
	views := presentation.TaskViews(tasks)
	result := make([]TaskPresentation, len(views))
	for index, view := range views {
		result[index] = TaskPresentation{
			TaskID:                view.Task.ID,
			IDPrefix:              view.IDPrefix,
			DependenciesComplete:  view.DependenciesComplete,
			DependenciesTotal:     view.DependenciesTotal,
			WaitingOnDependencies: view.WaitingOnDependencies,
		}
	}
	return result
}

func (handler *handler) serveHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, HealthDocument{
		Format:  "workbook.health",
		Version: 1,
		Status:  "ok",
	})
}

func (handler *handler) writeError(writer http.ResponseWriter, err error) {
	category := core.CategoryOf(err)
	if category == "" {
		category = core.CategoryOperational
	}
	message := err.Error()
	var typed *core.Error
	if errors.As(err, &typed) && category != core.CategoryOperational {
		message = typed.Message
	}
	writeJSON(writer, statusForError(category), ErrorDocument{
		Format:  "workbook.error",
		Version: 1,
		Error: ErrorBody{
			Category: category,
			Message:  message,
		},
	})
}

func statusForError(category core.Category) int {
	switch category {
	case core.CategoryInvocation, core.CategoryValidation:
		return http.StatusBadRequest
	case core.CategoryNotFound:
		return http.StatusNotFound
	case core.CategoryNotInitialized, core.CategoryStaleWrite:
		return http.StatusConflict
	case core.CategoryCorruptData, core.CategoryOperational:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
