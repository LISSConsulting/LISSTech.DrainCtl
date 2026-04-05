//go:build windows

package dashboard

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// ServerInfo describes a registered server and its last known state.
type ServerInfo struct {
	Hostname     string          `json:"hostname"`
	RegisteredAt time.Time       `json:"registered_at"`
	LastResult   *dc.CheckResult `json:"last_result,omitempty"`
	LastSeen     time.Time       `json:"last_seen,omitempty"`
}

// historyMax is the maximum number of CheckResult records retained per host.
const historyMax = 100

// ServerState manages the set of registered servers, persisted to servers.json.
type ServerState struct {
	mu      sync.RWMutex
	servers map[string]*ServerInfo
	history map[string][]dc.CheckResult
	path    string
	log     dc.LogFunc
}

// NewServerState creates a ServerState backed by servers.json in dataDir.
// If the file exists it is loaded; otherwise the state starts empty.
func NewServerState(dataDir string, log dc.LogFunc) *ServerState {
	if log == nil {
		log = dc.DiscardLogger()
	}
	s := &ServerState{
		servers: make(map[string]*ServerInfo),
		history: make(map[string][]dc.CheckResult),
		path:    filepath.Join(dataDir, "servers.json"),
		log:     log,
	}
	s.load()
	return s
}

// Register adds (or re-registers) a hostname and persists the change.
func (s *ServerState) Register(hostname string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[hostname]; !ok {
		s.servers[hostname] = &ServerInfo{
			Hostname:     hostname,
			RegisteredAt: time.Now(),
		}
	}
	s.save()
}

// Remove deletes a server by hostname and persists. Returns true if found.
func (s *ServerState) Remove(hostname string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[hostname]; !ok {
		return false
	}
	delete(s.servers, hostname)
	delete(s.history, hostname)
	s.save()
	return true
}

// IsRegistered returns true if the hostname is known.
func (s *ServerState) IsRegistered(hostname string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.servers[hostname]
	return ok
}

// Update sets the last result and last-seen time for a registered host,
// and appends the result to the per-host history ring (capped at historyMax).
func (s *ServerState) Update(hostname string, result *dc.CheckResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if info, ok := s.servers[hostname]; ok {
		info.LastResult = result
		info.LastSeen = time.Now()
		s.save()
	}
	buf := s.history[hostname]
	if len(buf) < historyMax {
		buf = append(buf, *result)
	} else {
		copy(buf, buf[1:])
		buf[historyMax-1] = *result
	}
	s.history[hostname] = buf
}

// HostHistory returns the last n CheckResult records for hostname, newest first.
// If n <= 0 or n > historyMax, up to historyMax records are returned.
func (s *ServerState) HostHistory(hostname string, n int) []dc.CheckResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	buf := s.history[hostname]
	if len(buf) == 0 {
		return nil
	}
	if n <= 0 || n > len(buf) {
		n = len(buf)
	}
	// Return newest-first slice of the ring (buf is oldest-first).
	src := buf[len(buf)-n:]
	out := make([]dc.CheckResult, n)
	for i, r := range src {
		out[n-1-i] = r
	}
	return out
}

// Get returns a snapshot of the named server, or nil if not registered.
func (s *ServerState) Get(hostname string) *ServerInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, ok := s.servers[hostname]
	if !ok {
		return nil
	}
	copy := *info
	return &copy
}

// All returns a snapshot of all servers sorted by hostname.
func (s *ServerState) All() []ServerInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ServerInfo, 0, len(s.servers))
	for _, info := range s.servers {
		out = append(out, *info)
	}
	slices.SortFunc(out, func(a, b ServerInfo) int {
		return cmp.Compare(a.Hostname, b.Hostname)
	})
	return out
}

func (s *ServerState) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []ServerInfo
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	for i := range list {
		s.servers[list[i].Hostname] = &list[i]
	}
}

func (s *ServerState) save() {
	list := make([]ServerInfo, 0, len(s.servers))
	for _, info := range s.servers {
		list = append(list, *info)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		dc.LogMsg(s.log, dc.LvlERR, "dashboard: marshal servers failed", fmt.Sprintf("error=%q", err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		dc.LogMsg(s.log, dc.LvlERR, "dashboard: create data dir failed", fmt.Sprintf("error=%q", err))
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		dc.LogMsg(s.log, dc.LvlERR, "dashboard: write tmp file failed", fmt.Sprintf("error=%q", err))
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		dc.LogMsg(s.log, dc.LvlERR, "dashboard: rename tmp file failed", fmt.Sprintf("error=%q", err))
	}
}
