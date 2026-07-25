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

type TaskStatusUpdater func(context.Context, string, core.Status) (core.Task, error)

type TaskCreator func(context.Context, core.CreateInput) (core.Task, error)

type TaskUpdater func(context.Context, string, core.UpdateInput) (core.Task, error)

type TasksDocument struct {
	Format       string             `json:"format"`
	Version      int                `json:"version"`
	Tasks        []core.Task        `json:"tasks"`
	Presentation []TaskPresentation `json:"presentation"`
}

type TaskMutationDocument struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Task    core.Task `json:"task"`
}

type TaskPresentation struct {
	TaskID   string `json:"taskId"`
	IDPrefix string `json:"idPrefix"`
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
	page         *template.Template
	mux          *http.ServeMux
}

type pageData struct {
	Board presentation.Board
}

type updateStatusRequest struct {
	Status core.Status `json:"status"`
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

// NewHandler accepts either the existing list/status callbacks or the expanded
// list/create/update/status callback set while command wiring migrates.
func NewHandler(list TaskLister, callbacks ...any) http.Handler {
	var create TaskCreator
	var update TaskUpdater
	var updateStatus TaskStatusUpdater
	switch len(callbacks) {
	case 1:
		updateStatus = taskStatusUpdater(callbacks[0])
	case 3:
		create = taskCreator(callbacks[0])
		update = taskUpdater(callbacks[1])
		updateStatus = taskStatusUpdater(callbacks[2])
	default:
		panic("webui.NewHandler requires list/status or list/create/update/status callbacks")
	}
	page := template.Must(template.New("index.html").ParseFS(assets, "assets/index.html"))
	handler := &handler{list: list, create: create, update: update, updateStatus: updateStatus, page: page, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /{$}", handler.serveBoard)
	handler.mux.HandleFunc("GET /api/tasks", handler.serveTasks)
	handler.mux.HandleFunc("POST /api/tasks", handler.createTask)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}", handler.updateTask)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}/status", handler.updateTaskStatus)
	handler.mux.HandleFunc("GET /healthz", handler.serveHealth)
	return http.HandlerFunc(handler.serveHTTP)
}

func taskCreator(callback any) TaskCreator {
	switch callback := callback.(type) {
	case TaskCreator:
		return callback
	case func(context.Context, core.CreateInput) (core.Task, error):
		return TaskCreator(callback)
	default:
		panic("webui.NewHandler create callback has the wrong type")
	}
}

func taskUpdater(callback any) TaskUpdater {
	switch callback := callback.(type) {
	case TaskUpdater:
		return callback
	case func(context.Context, string, core.UpdateInput) (core.Task, error):
		return TaskUpdater(callback)
	default:
		panic("webui.NewHandler update callback has the wrong type")
	}
}

func taskStatusUpdater(callback any) TaskStatusUpdater {
	switch callback := callback.(type) {
	case TaskStatusUpdater:
		return callback
	case func(context.Context, string, core.Status) (core.Task, error):
		return TaskStatusUpdater(callback)
	default:
		panic("webui.NewHandler status callback has the wrong type")
	}
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
	case "/", "/healthz":
		return http.MethodGet, true
	case "/api/tasks":
		return http.MethodGet + ", " + http.MethodPost, true
	default:
		if taskStatusPathID(path) != "" {
			return http.MethodPatch, true
		}
		if taskPathID(path) != "" {
			return http.MethodPatch, true
		}
		return "", false
	}
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

func (handler *handler) serveBoard(writer http.ResponseWriter, request *http.Request) {
	tasks, err := handler.list(request.Context())
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := handler.page.Execute(writer, pageData{Board: presentation.NewBoard(tasks)}); err != nil {
		return
	}
}

func (handler *handler) serveTasks(writer http.ResponseWriter, request *http.Request) {
	tasks, err := handler.list(request.Context())
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, TasksDocument{
		Format:       "workbook.tasks",
		Version:      1,
		Tasks:        tasks,
		Presentation: taskPresentation(tasks),
	})
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
	task, err := handler.updateStatus(request.Context(), id, input.Status)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, task)
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
	task, err := handler.create(request.Context(), core.CreateInput(body))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, task)
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
	task, err := handler.update(request.Context(), id, core.UpdateInput(body))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, task)
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

func (handler *handler) writeTaskMutation(writer http.ResponseWriter, task core.Task) {
	writeJSON(writer, http.StatusOK, TaskMutationDocument{
		Format:  "workbook.task-mutation",
		Version: 1,
		Task:    task,
	})
}

func taskPresentation(tasks []core.Task) []TaskPresentation {
	views := presentation.TaskViews(tasks)
	result := make([]TaskPresentation, len(views))
	for index, view := range views {
		result[index] = TaskPresentation{TaskID: view.Task.ID, IDPrefix: view.IDPrefix}
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
