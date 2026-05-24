package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cybernagle/memory-cli/internal/processor"
)

func (s *Server) handleProcessStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	status := processor.GlobalTracker.Get()
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleProcessEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send initial state
	status := processor.GlobalTracker.Get()
	data, _ := json.Marshal(map[string]any{
		"type":   "status",
		"status": status,
	})
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()

	// Subscribe to events
	ch := processor.GlobalTracker.Subscribe()
	defer processor.GlobalTracker.Unsubscribe(ch)

	// Stream events or heartbeat
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", event.Message)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
