package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/app"
)

type Server struct {
	App  *app.App
	HTTP *http.Server
}

func New(application *app.App, listen string) *Server {
	server := &Server{App: application}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /readyz", server.ready)
	mux.HandleFunc("GET /v1/status", server.status)
	mux.HandleFunc("GET /v1/projects", server.projects)
	mux.HandleFunc("GET /v1/tasks", server.tasks)
	mux.HandleFunc("POST /v1/tasks/", server.taskAction)
	server.HTTP = &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return server
}

func (s *Server) ListenAndServe() error {
	err := s.HTTP.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.HTTP.Shutdown(ctx)
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("ok\n"))
}

func (s *Server) ready(writer http.ResponseWriter, _ *http.Request) {
	snapshot := s.App.Snapshot()
	if !snapshot.Ready {
		http.Error(writer, "not ready", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("ready\n"))
}

func (s *Server) status(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, s.App.Snapshot())
}

func (s *Server) projects(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, s.App.Snapshot().Projects)
}

func (s *Server) tasks(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, s.App.ListTasks())
}

func (s *Server) taskAction(writer http.ResponseWriter, request *http.Request) {
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/v1/tasks/"), "/")
	if len(parts) != 3 {
		http.Error(writer, "expected /v1/tasks/<project>/<task>/<action>", http.StatusNotFound)
		return
	}
	projectID, taskID, action := parts[0], parts[1], parts[2]
	var err error
	switch action {
	case "approve":
		err = s.App.Approve(projectID, taskID)
	case "reject":
		err = s.App.Reject(projectID, taskID, "rejected through local API")
	case "rollback":
		err = s.App.Rollback(request.Context(), projectID, taskID)
	default:
		http.Error(writer, "unsupported task action", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(writer, map[string]string{"status": "ok", "project_id": projectID, "task_id": taskID, "action": action})
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		http.Error(writer, fmt.Sprintf("encode response: %v", err), http.StatusInternalServerError)
	}
}
