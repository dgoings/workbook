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

func NewHandler(list TaskLister, updateStatus TaskStatusUpdater) http.Handler {
	page := template.Must(template.New("index.html").ParseFS(assets, "assets/index.html"))
	handler := &handler{list: list, updateStatus: updateStatus, page: page, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /{$}", handler.serveBoard)
	handler.mux.HandleFunc("GET /api/tasks", handler.serveTasks)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}/status", handler.updateTaskStatus)
	handler.mux.HandleFunc("GET /healthz", handler.serveHealth)
	return http.HandlerFunc(handler.serveHTTP)
}

func (handler *handler) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Security-Policy", securityPolicy)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if method, known := allowedMethod(request.URL.Path); known && request.Method != method {
		writer.Header().Set("Allow", method)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	handler.mux.ServeHTTP(writer, request)
}

func allowedMethod(path string) (string, bool) {
	switch path {
	case "/", "/api/tasks", "/healthz":
		return http.MethodGet, true
	default:
		if taskStatusPathID(path) != "" {
			return http.MethodPatch, true
		}
		return "", false
	}
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
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		handler.writeError(writer, core.Wrap(core.CategoryInvocation, "decode status update", err))
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		handler.writeError(writer, core.Wrap(core.CategoryInvocation, "decode status update", err))
		return
	}
	task, err := handler.updateStatus(request.Context(), id, input.Status)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
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
