package processor

import (
	"encoding/json"
	"sync"
)

// StatusTracker holds real-time processor state for SSE streaming.
type StatusTracker struct {
	mu     sync.RWMutex
	status ProcessStatus
	subs   []chan ProcessEvent
	subMu  sync.Mutex
}

type ProcessStatus struct {
	Running  bool   `json:"running"`
	Phase    string `json:"phase"`   // "idle", "extracting", "merging", "writing"
	Message  string `json:"message"` // human-readable progress
	Session  string `json:"session"` // current session being processed
	Progress struct {
		Layer1Input  int `json:"layer1_input"`
		Layer1Output int `json:"layer1_output"`
		Layer2Input  int `json:"layer2_input"`
		Layer2Output int `json:"layer2_output"`
		Organized    int `json:"organized"`
		Processed     int `json:"processed"`
		TotalInbox   int `json:"total_inbox"`
	} `json:"progress"`
	LastError string `json:"last_error,omitempty"`
}

type ProcessEvent struct {
	Type    string         `json:"type"` // "status", "log", "done", "error"
	Message string         `json:"message"`
	Status  *ProcessStatus `json:"status,omitempty"`
}

func NewStatusTracker() *StatusTracker {
	return &StatusTracker{
		status: ProcessStatus{Phase: "idle"},
	}
}

func EventFromStatus(typ string) ProcessEvent {
	s := GlobalTracker.Get()
	return ProcessEvent{Type: typ, Status: &s}
}

// GlobalTracker is the default global instance.
var GlobalTracker = NewStatusTracker()

func (t *StatusTracker) Get() ProcessStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.status
}

func (t *StatusTracker) Update(fn func(*ProcessStatus)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn(&t.status)
}

func (t *StatusTracker) Emit(event ProcessEvent) {
	if event.Status == nil {
		s := t.Get()
		event.Status = &s
	}
	t.subMu.Lock()
	defer t.subMu.Unlock()
	data, _ := json.Marshal(event)
	for _, ch := range t.subs {
		select {
		case ch <- ProcessEvent{Message: string(data)}:
		default:
		}
	}
}

func (t *StatusTracker) Subscribe() chan ProcessEvent {
	ch := make(chan ProcessEvent, 64)
	t.subMu.Lock()
	t.subs = append(t.subs, ch)
	t.subMu.Unlock()
	return ch
}

func (t *StatusTracker) Unsubscribe(ch chan ProcessEvent) {
	t.subMu.Lock()
	defer t.subMu.Unlock()
	for i, s := range t.subs {
		if s == ch {
			t.subs = append(t.subs[:i], t.subs[i+1:]...)
			close(ch)
			return
		}
	}
}
