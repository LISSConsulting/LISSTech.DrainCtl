//go:build windows

package pipe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// PipeName is the named pipe path the service listens on.
const PipeName = `\\.\pipe\drainctl`

// PipeRequest is the JSON request sent by clients.
type PipeRequest struct {
	Cmd         string `json:"cmd"`                    // "status", "history", "servers", "remove-server"
	Limit       int    `json:"limit,omitempty"`        // for history
	ChangesOnly bool   `json:"changes_only,omitempty"` // for history
	Hostname    string `json:"hostname,omitempty"`     // for remove-server
}

// PipeResponse is the JSON response returned by the service.
type PipeResponse struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// PipeHandler provides the data the pipe server needs to answer requests.
type PipeHandler interface {
	HandleStatus() *dc.CheckResult
	HandleHistory(limit int, changesOnly bool) []dc.AuditRecord
	HandleServers() json.RawMessage           // nil if dashboard not enabled
	HandleRemoveServer(hostname string) error // ErrNotFound or nil
}

// ServePipe runs the named pipe server using a simple goroutine-per-connection
// model with the Windows named pipe API.
func ServePipe(ctx context.Context, handler PipeHandler) {
	slog.Info("pipe_server=starting", "pipe", PipeName)

	for {
		// Check for cancellation before creating a new pipe instance.
		select {
		case <-ctx.Done():
			slog.Info("pipe_server=stopped")
			return
		default:
		}

		// Listen for one connection at a time using a helper that wraps
		// CreateNamedPipe + ConnectNamedPipe with cancellation support.
		conn, err := acceptPipeConn(ctx)
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("pipe_server=stopped")
				return
			}
			slog.Error("pipe accept failed", "error", err)
			time.Sleep(time.Second)
			continue
		}

		go handlePipeConn(conn, handler)
	}
}

func handlePipeConn(conn net.Conn, handler PipeHandler) {
	defer func() { _ = conn.Close() }()

	// Set a deadline to prevent slow clients from blocking.
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Read request.
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return
	}

	var req PipeRequest
	if err := json.Unmarshal(buf[:n], &req); err != nil {
		resp := PipeResponse{OK: false, Error: "invalid request"}
		data, _ := json.Marshal(resp)
		_, _ = conn.Write(data)
		return
	}

	var resp PipeResponse
	switch req.Cmd {
	case "status":
		result := handler.HandleStatus()
		if result == nil {
			resp = PipeResponse{OK: false, Error: "no status available"}
		} else if raw, err := json.Marshal(result); err != nil {
			resp = PipeResponse{OK: false, Error: "marshal result: " + err.Error()}
		} else {
			resp = PipeResponse{OK: true, Data: raw}
		}

	case "history":
		limit := req.Limit
		if limit <= 0 {
			limit = 50
		}
		records := handler.HandleHistory(limit, req.ChangesOnly)
		durations := dc.ComputeStateDurations(records)
		out := make([]dc.HistoryRecord, len(records))
		for i, r := range records {
			out[i] = dc.AuditToHistory(r, &durations[i])
		}
		// HistoryRecord contains only primitive-typed fields; Marshal cannot fail.
		raw, _ := json.Marshal(out)
		resp = PipeResponse{OK: true, Data: raw}

	case "servers":
		raw := handler.HandleServers()
		if raw == nil {
			resp = PipeResponse{OK: false, Error: "dashboard not enabled"}
		} else {
			resp = PipeResponse{OK: true, Data: raw}
		}

	case "remove-server":
		if req.Hostname == "" {
			resp = PipeResponse{OK: false, Error: "hostname required"}
		} else if err := handler.HandleRemoveServer(req.Hostname); err != nil {
			resp = PipeResponse{OK: false, Error: err.Error()}
		} else {
			resp = PipeResponse{OK: true}
		}

	default:
		resp = PipeResponse{OK: false, Error: fmt.Sprintf("unknown command: %s", req.Cmd)}
	}

	data, _ := json.Marshal(resp)
	_, _ = conn.Write(data)
}

// CheckViaPipe sends a status request to the service and returns the result.
// Returns an error if the service is not running.
func CheckViaPipe() (*dc.CheckResult, error) {
	resp, err := pipeRPC(PipeRequest{Cmd: "status"})
	if err != nil {
		return nil, err
	}
	var result dc.CheckResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal status: %w", err)
	}
	return &result, nil
}

// HistoryViaPipe sends a history request to the service.
// Returns an error if the service is not running.
func HistoryViaPipe(limit int, changesOnly bool) ([]dc.HistoryRecord, error) {
	resp, err := pipeRPC(PipeRequest{Cmd: "history", Limit: limit, ChangesOnly: changesOnly})
	if err != nil {
		return nil, err
	}
	var records []dc.HistoryRecord
	if err := json.Unmarshal(resp.Data, &records); err != nil {
		return nil, fmt.Errorf("unmarshal history: %w", err)
	}
	return records, nil
}

// ServersViaPipe lists registered dashboard servers via the named pipe.
func ServersViaPipe() (json.RawMessage, error) {
	resp, err := pipeRPC(PipeRequest{Cmd: "servers"})
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// RemoveServerViaPipe removes a server from the dashboard via the named pipe.
func RemoveServerViaPipe(hostname string) error {
	_, err := pipeRPC(PipeRequest{Cmd: "remove-server", Hostname: hostname})
	return err
}

// pipeRPC connects to the service pipe, sends a request, and reads the response.
func pipeRPC(req PipeRequest) (*PipeResponse, error) {
	conn, err := dialPipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	data, _ := json.Marshal(req)
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	buf, err := io.ReadAll(io.LimitReader(conn, 4*1024*1024)) // 4MB cap
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	n := len(buf)

	var resp PipeResponse
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("service error: %s", resp.Error)
	}
	return &resp, nil
}
