//go:build windows

package pipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/etwids"
	"golang.org/x/sys/windows"
)

// PipeName is the named pipe path the service listens on. Declared as a var
// rather than a const so tests can substitute a unique pipe name per run via
// t.Cleanup, isolating the test pipe from any existing service instance on
// the host.
var PipeName = `\\.\pipe\drainctl`

// PipeRequest is the JSON request sent by clients.
type PipeRequest struct {
	Cmd         string `json:"cmd"`                    // "status", "history", "servers", "remove-server", "register", "baseline-reset"
	Limit       int    `json:"limit,omitempty"`        // for history
	ChangesOnly bool   `json:"changes_only,omitempty"` // for history
	Hostname    string `json:"hostname,omitempty"`     // for remove-server
	URL         string `json:"url,omitempty"`          // for register
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
	HandleServers() json.RawMessage                              // nil if dashboard not enabled
	HandleRemoveServer(hostname string) error                    // ErrNotFound or nil
	HandleRegister(dashboardURL string) (json.RawMessage, error) // SSPI call made under service identity
	HandleBaselineReset() error                                  // wipes evtspike baselines; nil if evtspike not running
}

// registerDeadline is the per-connection deadline applied when handling a
// "register" command. Registration calls out to the dashboard over HTTPS with
// SSPI Negotiate, so the default 5s pipe deadline is too tight.
const registerDeadline = 30 * time.Second

const pipeMessageCap = 1024 * 1024

var callerIsPrivilegedFunc = callerIsPrivileged

// ErrPipeUnavailable signals that the pipe could not be dialled at all — the
// service is not installed, not running, or the pipe is otherwise unreachable.
// Callers use this to distinguish bootstrap / service-down conditions (where a
// direct-HTTP fallback is appropriate) from service-reported errors surfaced
// over a working pipe (where retrying under a different identity would either
// duplicate the operation or hide the real failure).
var ErrPipeUnavailable = errors.New("pipe unavailable")

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
	buf, err := readPipeMessage(conn, 4096)
	if err != nil || len(buf) == 0 {
		return
	}

	var req PipeRequest
	if err := json.Unmarshal(buf, &req); err != nil {
		resp := PipeResponse{OK: false, Error: "invalid request"}
		data, _ := json.Marshal(resp)
		_, _ = conn.Write(data)
		return
	}

	if requiresPrivilege(req.Cmd) {
		allowed, sidStr, err := callerIsPrivilegedFunc(conn)
		if err != nil || !allowed {
			args := []any{
				"cmd", req.Cmd,
				"sid", sidStr,
				slog.Int("event_id", etwids.EvtAccessDenied),
			}
			if err != nil {
				args = append(args, "error", err)
			}
			slog.Warn("pipe=access_denied", args...)
			resp := PipeResponse{OK: false, Error: "access denied"}
			data, _ := json.Marshal(resp)
			_, _ = conn.Write(data)
			return
		}
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

	case "register":
		if req.URL == "" {
			resp = PipeResponse{OK: false, Error: "url required"}
			break
		}
		// Extend the connection deadline: register makes an outbound HTTPS
		// call under the service's machine-account identity, which takes
		// longer than the default 5 s pipe deadline.
		_ = conn.SetDeadline(time.Now().Add(registerDeadline))
		raw, err := handler.HandleRegister(req.URL)
		if err != nil {
			resp = PipeResponse{OK: false, Error: err.Error()}
		} else {
			resp = PipeResponse{OK: true, Data: raw}
		}

	case "baseline-reset":
		if err := handler.HandleBaselineReset(); err != nil {
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

func requiresPrivilege(cmd string) bool {
	switch cmd {
	case "register", "remove-server", "baseline-reset":
		return true
	default:
		return false
	}
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

// RegisterViaPipe asks the service to register this host with the dashboard.
// The service performs the SSPI Negotiate + HTTPS call under its own
// machine-account identity, which is required by the dashboard's
// requireMachineAccount middleware. Returns the raw JSON response so callers
// can decode it to dashboard.RegisterResult without this package importing the
// dashboard package.
func RegisterViaPipe(dashboardURL string) (json.RawMessage, error) {
	resp, err := pipeRPCTimeout(PipeRequest{Cmd: "register", URL: dashboardURL}, registerDeadline)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// BaselineResetViaPipe asks the running service to wipe its evtspike
// baseline — both the in-memory per-channel detectors and the persisted
// baseline.json file. On-disk deletion alone achieves nothing because the
// service rewrites the file from memory on its next 15-minute persistence
// tick; this RPC synchronises the in-memory wipe with the on-disk remove.
func BaselineResetViaPipe() error {
	_, err := pipeRPC(PipeRequest{Cmd: "baseline-reset"})
	return err
}

// pipeRPC connects to the service pipe, sends a request, and reads the response.
func pipeRPC(req PipeRequest) (*PipeResponse, error) {
	return pipeRPCTimeout(req, 5*time.Second)
}

// pipeRPCTimeout is pipeRPC with a caller-supplied deadline for commands that
// may take longer than the 5 s default (e.g. "register", which performs an
// outbound SSPI + HTTPS call on the service side).
func pipeRPCTimeout(req PipeRequest, timeout time.Duration) (*PipeResponse, error) {
	conn, err := dialPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPipeUnavailable, err)
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(timeout))

	data, _ := json.Marshal(req)
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	buf, err := readPipeResponse(io.LimitReader(conn, 4*1024*1024))
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

func readPipeMessage(r io.Reader, initialBufSize int) ([]byte, error) {
	if initialBufSize <= 0 {
		initialBufSize = 4096
	}
	if initialBufSize > pipeMessageCap {
		initialBufSize = pipeMessageCap
	}

	buf := make([]byte, initialBufSize)
	used := 0
	for {
		if used == len(buf) {
			if used >= pipeMessageCap {
				return nil, fmt.Errorf("pipe: message exceeds 1 MiB cap")
			}
			growBy := len(buf)
			if growBy > 32*1024 {
				growBy = 32 * 1024
			}
			if growBy > pipeMessageCap-len(buf) {
				growBy = pipeMessageCap - len(buf)
			}
			buf = append(buf, make([]byte, growBy)...)
		}

		n, err := r.Read(buf[used:])
		used += n
		switch {
		case err == nil:
			return buf[:used], nil
		case errors.Is(err, io.EOF):
			return buf[:used], nil
		case errors.Is(err, windows.ERROR_MORE_DATA):
			if used >= pipeMessageCap {
				return nil, fmt.Errorf("pipe: message exceeds 1 MiB cap")
			}
			continue
		default:
			return nil, err
		}
	}
}

// readPipeResponse reads a full message-mode pipe response. Windows named pipes
// can return ERROR_MORE_DATA when the current read buffer is smaller than the
// pending message; that signals "continue reading" rather than a fatal error.
func readPipeResponse(r io.Reader) ([]byte, error) {
	return readPipeMessage(r, 32*1024)
}
