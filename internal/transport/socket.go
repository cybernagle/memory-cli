package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/cybernagle/memory-cli/internal/agent"
	"github.com/cybernagle/memory-cli/internal/store"
)

type SocketServer struct {
	socketPath string
	agent      *agent.Agent
}

func NewSocketServer(socketPath string, s *store.Store) *SocketServer {
	a := agent.New(s)
	agent.RegisterAll(a, s)
	return &SocketServer{
		socketPath: socketPath,
		agent:      a,
	}
}

type Request struct {
	ID     int            `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

type Response struct {
	ID     int    `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (srv *SocketServer) Listen(ctx context.Context) error {
	// Remove stale socket
	os.Remove(srv.socketPath)

	// Ensure parent directory exists
	dir := strings.LastIndex(srv.socketPath, "/")
	if dir > 0 {
		os.MkdirAll(srv.socketPath[:dir], 0755)
	}

	l, err := net.Listen("unix", srv.socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", srv.socketPath, err)
	}
	defer l.Close()

	log.Printf("Memory socket listening on %s", srv.socketPath)

	go func() {
		<-ctx.Done()
		l.Close()
		os.Remove(srv.socketPath)
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Printf("accept error: %v", err)
				continue
			}
		}
		go srv.handleConn(conn)
	}
}

func (srv *SocketServer) handleConn(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			srv.writeResponse(conn, Response{Error: fmt.Sprintf("parse error: %v", err)})
			continue
		}

		resp := srv.handleRequest(&req)
		srv.writeResponse(conn, resp)
	}
}

func (srv *SocketServer) handleRequest(req *Request) Response {
	switch req.Method {
	case "tools/list":
		return Response{ID: req.ID, Result: srv.agent.ListTools()}
	case "tools/call":
		toolName, _ := req.Params["name"].(string)
		params, _ := req.Params["params"].(map[string]any)
		if params == nil {
			params = req.Params
		}
		result, err := srv.agent.Execute(context.Background(), toolName, params)
		if err != nil {
			return Response{ID: req.ID, Error: err.Error()}
		}
		return Response{ID: req.ID, Result: result}
	default:
		// Try to execute as a tool name directly
		result, err := srv.agent.Execute(context.Background(), req.Method, req.Params)
		if err != nil {
			return Response{ID: req.ID, Error: err.Error()}
		}
		return Response{ID: req.ID, Result: result}
	}
}

func (srv *SocketServer) writeResponse(conn net.Conn, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("marshal response error: %v", err)
		return
	}
	conn.Write(append(data, '\n'))
}
