package webui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	pathpkg "path"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/presentation"
)

const securityPolicy = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

//go:embed assets/index.html
var assets embed.FS

type TaskLister func(context.Context) ([]core.Task, error)

type TaskStatusUpdater func(context.Context, string, core.Status, string) (core.MutationResult, error)

type TaskPositionUpdater func(context.Context, string, core.PlaceInput) (core.MutationResult, error)

type TaskCreator func(context.Context, core.CreateInput) (core.MutationResult, error)

type TaskUpdater func(context.Context, string, core.UpdateInput) (core.MutationResult, error)

type TaskDeleter func(context.Context, string) (core.MutationResult, error)

type TaskRestorer func(context.Context, string) (core.MutationResult, error)

type TaskDependencyAdder func(context.Context, string, string) (core.MutationResult, error)

type TaskDependencyRemover func(context.Context, string, string) (core.MutationResult, error)

// TaskHistoryReader reads one task together with its complete change log. The
// detail view shows history by default rather than behind an opt-in flag, and
// the status lifecycle lane it derives has to reach back to the task's
// creation, so the board asks for the whole chain rather than the CLI's
// ten-change default window.
type TaskHistoryReader func(context.Context, string) (core.TaskDetail, error)

// SyncStateReporter answers what the board will do with the next mutation.
type SyncStateReporter func(context.Context) SyncState

// SyncModeSetter shifts between handing publication to a watcher and waiting
// for the push. It rejects a mode it does not recognize.
type SyncModeSetter func(context.Context, string) (SyncState, error)

// SyncModeDeferred hands publication to a running watcher; SyncModeInline waits
// for the push so a successful response means origin has the change.
const (
	SyncModeDeferred = "deferred"
	SyncModeInline   = "inline"
)

// SyncState is what the board reports about publication. Watcher is false when
// no trustworthy watcher answers, in which case a deferred board still falls
// back to publishing inline and the indicator says so.
type SyncState struct {
	Mode    string `json:"mode"`
	Watcher bool   `json:"watcher"`
	Detail  string `json:"detail,omitempty"`
}

type SyncDocument struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Sync    SyncState `json:"sync"`
}

type syncModeRequest struct {
	Mode string `json:"mode"`
}

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

// LifecycleStage is one stop on a task's status lane. WallTime, Commit, and
// Actor are absent for a stop no recorded change entered, which is how a task
// that never changed status and a history a read could not walk in full both
// render honestly.
type LifecycleStage struct {
	Status   core.Status `json:"status"`
	Label    string      `json:"label"`
	Commit   string      `json:"commit,omitempty"`
	Actor    string      `json:"actor,omitempty"`
	WallTime *time.Time  `json:"wallTime,omitempty"`
	Current  bool        `json:"current"`
}

// TaskHistoryDocument carries one task's change log and the status lane derived
// from it. The lane is derived on the server from the whole chain, so a client
// that renders only part of the log still shows every status the task stood in.
type TaskHistoryDocument struct {
	Format    string           `json:"format"`
	Version   int              `json:"version"`
	TaskID    string           `json:"taskId"`
	Lifecycle []LifecycleStage `json:"lifecycle"`
	History   core.ChangeLog   `json:"history"`
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

// Options names every capability a board can be built with. A nil field is a
// capability this board does not have, and its route says so rather than
// pretending: that is what lets a read-only board and the full one come from
// the same constructor.
//
// The list is named rather than positional because Delete and Restore share a
// signature, as do Depend and Free. Passed positionally, a transposed pair
// compiles and silently inverts the semantics; named, the same mistake is
// visible in the call site itself.
type Options struct {
	List         TaskLister
	Create       TaskCreator
	Update       TaskUpdater
	UpdateStatus TaskStatusUpdater
	Position     TaskPositionUpdater
	Delete       TaskDeleter
	Restore      TaskRestorer
	Depend       TaskDependencyAdder
	Free         TaskDependencyRemover
	History      TaskHistoryReader
	SyncState    SyncStateReporter
	SetSyncMode  SyncModeSetter
}

// handler embeds Options rather than copying it field by field, so there is no
// second list to keep in step and no assignment that could cross two
// capabilities on the way in.
type handler struct {
	Options
	page *template.Template
	mux  *http.ServeMux
}

type pageData struct {
	Board presentation.Board
}

// expectedHead is the task tip the browser rendered before proposing a change.
// It is optional on every request that carries it: a client that omits it keeps
// the behavior these routes had before the field existed, which is what lets
// the server half land before any client sends one.
type updateStatusRequest struct {
	Status       core.Status `json:"status"`
	ExpectedHead string      `json:"expectedHead"`
}

type positionTaskRequest struct {
	Status       core.Status `json:"status"`
	Before       string      `json:"before"`
	After        string      `json:"after"`
	ExpectedHead string      `json:"expectedHead"`
}

type createTaskRequest struct {
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      core.Status   `json:"status"`
	Priority    core.Priority `json:"priority"`
	Labels      []string      `json:"labels"`
}

// updateTaskRequest is converted directly to core.UpdateInput, so its fields
// must stay identical in name, type, and order.
type updateTaskRequest struct {
	Title        *string        `json:"title"`
	Description  *string        `json:"description"`
	Status       *core.Status   `json:"status"`
	Priority     *core.Priority `json:"priority"`
	Labels       *[]string      `json:"labels"`
	ExpectedHead string         `json:"expectedHead"`
}

// pageFuncs give the page template the one fact a presentation.TaskView does
// not carry: whether this build has a column for the status the task holds. A
// card in the unknown-status region cannot be dragged anywhere, so it must not
// announce itself as movable.
//
// The client script answers the same question on every poll, and answers it
// from the columns this function rendered — it reads the emitted [data-status]
// nodes rather than a status list of its own — so the two cannot disagree about
// a card even while the page is being served by a build the script does not
// match. Neither side reads it off the containing list, so a card that changes
// status carries the right answer with it as it moves.
var pageFuncs = template.FuncMap{"knownStatus": knownStatus}

func knownStatus(status core.Status) bool {
	for _, definition := range core.WorkflowStatuses() {
		if definition.Status == status {
			return true
		}
	}
	return false
}

// NewHandler builds the board from the capabilities it is given. It is the only
// constructor: the tiered NewHandlerWithTaskMutations and
// NewHandlerWithSyncControl existed to append capabilities to a positional list
// without renaming every earlier call, and a named field expresses the same
// tier by being set or left nil.
func NewHandler(options Options) http.Handler {
	page := template.Must(template.New("index.html").Funcs(pageFuncs).ParseFS(assets, "assets/index.html"))
	handler := &handler{Options: options, page: page, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /{$}", handler.serveBoard)
	handler.mux.HandleFunc("GET /deleted", handler.serveBoard)
	handler.mux.HandleFunc("GET /tasks/new", handler.serveBoard)
	handler.mux.HandleFunc("GET /tasks/{id}", handler.serveBoard)
	handler.mux.HandleFunc("GET /api/tasks", handler.serveTasks)
	handler.mux.HandleFunc("GET /api/tasks/{id}/history", handler.serveTaskHistory)
	handler.mux.HandleFunc("POST /api/tasks", handler.createTask)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}", handler.updateTask)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}/status", handler.updateTaskStatus)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}/position", handler.positionTask)
	handler.mux.HandleFunc("DELETE /api/tasks/{id}", handler.deleteTask)
	handler.mux.HandleFunc("POST /api/tasks/{id}/restore", handler.restoreTask)
	handler.mux.HandleFunc("PUT /api/tasks/{id}/dependencies/{dependency}", handler.addTaskDependency)
	handler.mux.HandleFunc("DELETE /api/tasks/{id}/dependencies/{dependency}", handler.removeTaskDependency)
	handler.mux.HandleFunc("GET /api/sync", handler.serveSyncState)
	handler.mux.HandleFunc("PUT /api/sync", handler.updateSyncMode)
	handler.mux.HandleFunc("GET /healthz", handler.serveHealth)
	return http.HandlerFunc(handler.serveHTTP)
}

// writeSecurityHeaders states the page's own restrictions on every response,
// including the ones the same-origin guard refuses before this handler runs.
func writeSecurityHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Security-Policy", securityPolicy)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
}

func (handler *handler) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	writeSecurityHeaders(writer)
	// Bounded here rather than at each route so no handler, present or future,
	// can read an unbounded body by forgetting to ask for a limit. A route that
	// never reads its body is covered too, and http.MaxBytesReader is given the
	// ResponseWriter so a sender that ignores the limit loses its connection
	// rather than keeping it to try again.
	request.Body = http.MaxBytesReader(writer, request.Body, MaxRequestBodyBytes)
	if malformedTaskDependencyRequestPath(request.URL.Path, request.URL.EscapedPath()) {
		http.NotFound(writer, request)
		return
	}
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

func malformedTaskDependencyRequestPath(decodedPath, escapedPath string) bool {
	if escapedPath != decodedPath &&
		(taskDependencyRouteShaped(decodedPath) || malformedTaskDependencyPath(decodedPath)) {
		return true
	}
	return malformedTaskDependencyPath(escapedPath)
}

func taskDependencyRouteShaped(requestPath string) bool {
	_, _, valid := taskDependencyPathIDs(requestPath)
	return valid
}

func malformedTaskDependencyPath(requestPath string) bool {
	if _, _, valid := taskDependencyPathIDs(requestPath); valid {
		return false
	}
	cleanedPath := pathpkg.Clean(requestPath)
	if _, _, cleanedDependency := taskDependencyPathIDs(cleanedPath); cleanedDependency {
		return true
	}
	if !taskAPIPath(requestPath) && !taskAPIPath(cleanedPath) {
		return false
	}
	return hasPathSegment(requestPath, "dependencies") ||
		hasPathSegment(cleanedPath, "dependencies")
}

func taskAPIPath(path string) bool {
	return path == "/api/tasks" || strings.HasPrefix(path, "/api/tasks/")
}

func hasPathSegment(path, marker string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == marker {
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
	case "/api/sync":
		return http.MethodGet + ", " + http.MethodPut, true
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
		if taskHistoryPathID(path) != "" {
			return http.MethodGet, true
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
		parts[0] == "." || parts[0] == ".." ||
		parts[1] != "dependencies" || parts[2] == "" ||
		parts[2] == "." || parts[2] == ".." {
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

func taskHistoryPathID(path string) string {
	const prefix = "/api/tasks/"
	const suffix = "/history"
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
	tasks, err := handler.listTasks(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := handler.page.Execute(writer, pageData{Board: presentation.NewBoard(activeTasks(tasks))}); err != nil {
		return
	}
}

// listTasks reads the board's tasks, reporting a board built without a lister
// the way every other route reports a capability it was not given. Listing was
// mandatory by signature while the constructor was positional; a named field
// can be left out, so the check has to exist.
func (handler *handler) listTasks(request *http.Request) ([]core.Task, error) {
	if handler.List == nil {
		return nil, core.Errorf(core.CategoryOperational, "task listing is not configured")
	}
	return handler.List(request.Context())
}

func (handler *handler) serveTasks(writer http.ResponseWriter, request *http.Request) {
	tasks, err := handler.listTasks(request)
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

func (handler *handler) serveTaskHistory(writer http.ResponseWriter, request *http.Request) {
	if handler.History == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task history is not configured"))
		return
	}
	id := request.PathValue("id")
	if id == "" {
		id = taskHistoryPathID(request.URL.Path)
	}
	detail, err := handler.History(request.Context(), id)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	if detail.History == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task history reader returned no change log"))
		return
	}
	writeJSON(writer, http.StatusOK, TaskHistoryDocument{
		Format:    "workbook.task-history",
		Version:   1,
		TaskID:    detail.ID,
		Lifecycle: lifecycleStages(*detail.History, detail.Status),
		History:   *detail.History,
	})
}

func lifecycleStages(log core.ChangeLog, current core.Status) []LifecycleStage {
	stops := presentation.Lifecycle(log, current)
	stages := make([]LifecycleStage, len(stops))
	for index, stop := range stops {
		stages[index] = LifecycleStage{
			Status:   stop.Status,
			Label:    stop.Label,
			Commit:   stop.Commit,
			Actor:    stop.Actor,
			WallTime: stop.WallTime,
			Current:  stop.Current,
		}
	}
	return stages
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
		handler.writeError(writer, decodeRequestError("decode status update", err))
		return
	}
	if handler.UpdateStatus == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task status updating is not configured"))
		return
	}
	result, err := handler.UpdateStatus(request.Context(), id, input.Status, input.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) positionTask(writer http.ResponseWriter, request *http.Request) {
	if handler.Position == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task positioning is not configured"))
		return
	}
	var body positionTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode task position", err))
		return
	}
	result, err := handler.Position(
		request.Context(),
		request.PathValue("id"),
		core.PlaceInput{Status: body.Status, Before: body.Before, After: body.After, ExpectedHead: body.ExpectedHead},
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
		handler.writeError(writer, decodeRequestError("decode task create", err))
		return
	}
	if handler.Create == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task creation is not configured"))
		return
	}
	result, err := handler.Create(request.Context(), core.CreateInput(body))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) updateTask(writer http.ResponseWriter, request *http.Request) {
	var body updateTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode task update", err))
		return
	}
	if handler.Update == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task updating is not configured"))
		return
	}
	id := request.PathValue("id")
	if id == "" {
		id = taskPathID(request.URL.Path)
	}
	result, err := handler.Update(request.Context(), id, core.UpdateInput(body))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) deleteTask(writer http.ResponseWriter, request *http.Request) {
	if handler.Delete == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task deletion is not configured"))
		return
	}
	result, err := handler.Delete(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) restoreTask(writer http.ResponseWriter, request *http.Request) {
	if handler.Restore == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task restoration is not configured"))
		return
	}
	result, err := handler.Restore(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) addTaskDependency(writer http.ResponseWriter, request *http.Request) {
	if err := requireEmptyRequestBody(request.Body); err != nil {
		handler.writeError(writer, core.Wrap(core.CategoryInvocation, "validate dependency request", err))
		return
	}
	if handler.Depend == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task dependency addition is not configured"))
		return
	}
	result, err := handler.Depend(request.Context(), request.PathValue("id"), request.PathValue("dependency"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) removeTaskDependency(writer http.ResponseWriter, request *http.Request) {
	if err := requireEmptyRequestBody(request.Body); err != nil {
		handler.writeError(writer, core.Wrap(core.CategoryInvocation, "validate dependency request", err))
		return
	}
	if handler.Free == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task dependency removal is not configured"))
		return
	}
	result, err := handler.Free(request.Context(), request.PathValue("id"), request.PathValue("dependency"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func requireEmptyRequestBody(body io.Reader) error {
	read, err := io.CopyN(io.Discard, body, 1)
	if read > 0 {
		return errors.New("request body must be empty")
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// MaxRequestBodyBytes bounds one request body.
//
// The largest task this version can store is a title, a description, and a
// label set at core's ceilings — under 70 KiB of task text. JSON escaping can
// expand that severalfold in the worst case, so this sits an order of magnitude
// above the largest body the board could honestly send while still refusing a
// client that means to stream indefinitely. A request over it is the sender's
// mistake, so it reads as an invocation failure rather than a validation one.
const MaxRequestBodyBytes = 1 << 20

// decodeRequest reads exactly one JSON value from the request body, which
// serveHTTP has already bounded.
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

// decodeRequestError categorizes a body-decoding failure for a client.
//
// Only the outermost message reaches the response, so a body stopped by the
// ceiling is reported as itself rather than wrapped in the route's context: the
// route's "decode task create" alone would leave the sender to guess whether
// its JSON was malformed or merely too large, which is the one decode failure a
// client can act on without seeing the body.
func decodeRequestError(context string, err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return core.Errorf(
			core.CategoryInvocation,
			"request body must not exceed %d bytes",
			MaxRequestBodyBytes,
		)
	}
	return core.Wrap(core.CategoryInvocation, context, err)
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

func (handler *handler) serveSyncState(writer http.ResponseWriter, request *http.Request) {
	if handler.SyncState == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "publication state is not configured"))
		return
	}
	writeJSON(writer, http.StatusOK, SyncDocument{Format: "workbook.sync", Version: 1, Sync: handler.SyncState(request.Context())})
}

func (handler *handler) updateSyncMode(writer http.ResponseWriter, request *http.Request) {
	if handler.SetSyncMode == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "publication mode is not configured"))
		return
	}
	var body syncModeRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode publication mode", err))
		return
	}
	state, err := handler.SetSyncMode(request.Context(), body.Mode)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, SyncDocument{Format: "workbook.sync", Version: 1, Sync: state})
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
	case core.CategoryNotInitialized, core.CategoryStaleWrite, core.CategoryConflict:
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
