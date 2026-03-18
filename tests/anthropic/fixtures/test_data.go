// Package fixtures provides test data and helpers for tool_output tests.
package fixtures

// SmallToolOutputConst is below typical minTokens threshold (< 256 bytes)
const SmallToolOutputConst = `{"status": "ok", "count": 42}`

// MediumToolOutputConst triggers compression (~500 bytes)
const MediumToolOutputConst = `{
	"files": [
		{"name": "main.go", "size": 1234, "modified": "2024-01-15T10:30:00Z"},
		{"name": "config.go", "size": 567, "modified": "2024-01-14T09:20:00Z"},
		{"name": "handler.go", "size": 2345, "modified": "2024-01-13T08:10:00Z"},
		{"name": "models.go", "size": 890, "modified": "2024-01-12T07:00:00Z"},
		{"name": "utils.go", "size": 456, "modified": "2024-01-11T06:50:00Z"}
	],
	"total_size": 5492,
	"directory": "/workspace/project"
}`

// CompressedSummary is a sample compressed output
const CompressedSummary = `Go HTTP server with Config, Routes, CRUD for users.`

// LargeToolOutputConst is a large tool output for compression testing (~2KB)
var LargeToolOutputConst = generateLargeOutput()

func generateLargeOutput() string {
	return `package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Server struct {
	router     *http.ServeMux
	httpServer *http.Server
	config     *Config
}

type Config struct {
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

func NewServer(cfg *Config) *Server {
	s := &Server{router: http.NewServeMux(), config: cfg}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	s.router.HandleFunc("/health", s.healthHandler)
	s.router.HandleFunc("/api/v1/users", s.usersHandler)
}

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

func (s *Server) usersHandler(w http.ResponseWriter, r *http.Request) {
	users := []map[string]interface{}{
		{"id": 1, "name": "Alice", "email": "alice@example.com"},
		{"id": 2, "name": "Bob", "email": "bob@example.com"},
	}
	json.NewEncoder(w).Encode(users)
}

func (s *Server) Start() error {
	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.config.Port),
		Handler: s.router,
	}
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func main() {
	cfg := &Config{Port: 18080, ReadTimeout: 15 * time.Second}
	server := NewServer(cfg)
	server.Start()
}`
}
