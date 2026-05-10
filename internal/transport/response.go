package transport

import "github.com/cybernagle/memory-cli/internal/agent"

func resultToResponse(id int, result agent.ToolResult) Response {
	status := "completed"
	if result.InputRequired {
		status = "input-required"
	}
	return Response{ID: id, Result: result, Status: status}
}
