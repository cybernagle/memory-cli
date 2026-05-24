package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cybernagle/memory-cli/internal/agent"
	"github.com/cybernagle/memory-cli/internal/store"
)

type HTTPServer struct {
	agent *agent.Agent
	keys  map[string]bool
	mux   *http.ServeMux
}

func NewHTTPServer(keys []string, s store.Store) *HTTPServer {
	a := agent.New(s)
	agent.RegisterAll(a, s)

	keySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}

	srv := &HTTPServer{
		agent: a,
		keys:  keySet,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /a2a", srv.authMiddleware(srv.handleRPC))
	mux.HandleFunc("GET /.well-known/agent-card.json", srv.handleAgentCard)
	srv.mux = mux

	return srv
}

func (srv *HTTPServer) Handler() http.Handler {
	return srv.mux
}

func (srv *HTTPServer) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(srv.keys) == 0 {
			http.Error(w, "no API keys configured", http.StatusForbidden)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" {
			http.Error(w, "missing Authorization header", http.StatusUnauthorized)
			return
		}

		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok {
			http.Error(w, "invalid Authorization scheme, use Bearer", http.StatusUnauthorized)
			return
		}

		if !srv.keys[token] {
			http.Error(w, "invalid API key", http.StatusForbidden)
			return
		}

		next(w, r)
	}
}

func (srv *HTTPServer) handleRPC(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, Response{Error: fmt.Sprintf("parse error: %v", err)})
		return
	}

	resp := srv.executeRequest(r.Context(), &req)
	writeJSON(w, http.StatusOK, resp)
}

func (srv *HTTPServer) executeRequest(ctx context.Context, req *Request) Response {
	switch req.Method {
	case "tools/list":
		return Response{ID: req.ID, Result: srv.agent.ListTools(), Status: "completed"}
	case "tools/call":
		toolName, _ := req.Params["name"].(string)
		params, _ := req.Params["params"].(map[string]any)
		if params == nil {
			params = req.Params
		}
		result, err := srv.agent.Execute(ctx, toolName, params)
		if err != nil {
			return Response{ID: req.ID, Error: err.Error()}
		}
		return resultToResponse(req.ID, result)
	default:
		result, err := srv.agent.Execute(ctx, req.Method, req.Params)
		if err != nil {
			return Response{ID: req.ID, Error: err.Error()}
		}
		return resultToResponse(req.ID, result)
	}
}

func (srv *HTTPServer) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	tools := srv.agent.ListTools()
	skills := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		skills = append(skills, map[string]any{
			"id":          t.Name,
			"name":        t.Name,
			"description": t.Description,
		})
	}

	card := map[string]any{
		"name":        "memory-cli",
		"description": "Unified memory management for AI agents",
		"url":         fmt.Sprintf("http://%s/a2a", r.Host),
		"skills":      skills,
		"protocols":   []string{"json-rpc-2.0"},
		"auth": map[string]any{
			"schemes": []string{"bearer"},
		},
	}

	writeJSON(w, http.StatusOK, card)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
