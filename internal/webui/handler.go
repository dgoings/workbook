package webui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/presentation"
)

const securityPolicy = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

//go:embed assets/index.html
var assets embed.FS

type TaskLister func(context.Context) ([]core.Task, error)

type TasksDocument struct {
	Format       string             `json:"format"`
	Version      int                `json:"version"`
	Tasks        []core.Task        `json:"tasks"`
	Presentation []TaskPresentation `json:"presentation"`
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
	list TaskLister
	page *template.Template
	mux  *http.ServeMux
}

type pageData struct {
	Board presentation.Board
}

func NewHandler(list TaskLister) http.Handler {
	page := template.Must(template.New("index.html").ParseFS(assets, "assets/index.html"))
	handler := &handler{list: list, page: page, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /{$}", handler.serveBoard)
	handler.mux.HandleFunc("GET /api/tasks", handler.serveTasks)
	handler.mux.HandleFunc("GET /healthz", handler.serveHealth)
	return http.HandlerFunc(handler.serveHTTP)
}

func (handler *handler) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Security-Policy", securityPolicy)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodGet && isKnownPath(request.URL.Path) {
		writer.Header().Set("Allow", http.MethodGet)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	handler.mux.ServeHTTP(writer, request)
}

func isKnownPath(path string) bool {
	switch path {
	case "/", "/api/tasks", "/healthz":
		return true
	default:
		return false
	}
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
