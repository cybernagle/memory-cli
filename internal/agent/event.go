package agent

import "time"

type EventType string

const (
	EventAgentStart EventType = "agent_start"
	EventAgentEnd   EventType = "agent_end"
	EventToolStart  EventType = "tool_start"
	EventToolEnd    EventType = "tool_end"
	EventError      EventType = "error"
)

type Event struct {
	Type      EventType `json:"type"`
	ToolName  string    `json:"tool_name,omitempty"`
	Data      any       `json:"data"`
	Timestamp time.Time `json:"timestamp"`
	Error     string    `json:"error,omitempty"`
}

func newEvent(t EventType) Event {
	return Event{Type: t, Timestamp: time.Now()}
}

func toolEvent(t EventType, name string, data any) Event {
	return Event{Type: t, ToolName: name, Data: data, Timestamp: time.Now()}
}

func errorEvent(name, msg string) Event {
	return Event{Type: EventError, ToolName: name, Error: msg, Timestamp: time.Now()}
}
