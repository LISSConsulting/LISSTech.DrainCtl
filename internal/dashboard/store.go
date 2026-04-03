//go:build windows

package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

// ServerState manages the set of registered servers, persisted to servers.json.
type ServerState struct {
	mu      sync.RWMutex
	servers map[string]*ServerInfo
	path    string
}

// NewServerState creates a ServerState backed by servers.json in dataDir.
// If the file exists it is loaded; otherwise the state starts empty.
func NewServerState(dataDir string) *ServerState {
	s := &ServerState{
		servers: make(map[string]*ServerInfo),
		path:    filepath.Join(dataDir, "servers.json"),
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

// Update sets the last result and last-seen time for a registered host.
func (s *ServerState) Update(hostname string, result *dc.CheckResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if info, ok := s.servers[hostname]; ok {
		info.LastResult = result
		info.LastSeen = time.Now()
		s.save()
	}
}

// All returns a snapshot of all servers sorted by hostname.
func (s *ServerState) All() []ServerInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ServerInfo, 0, len(s.servers))
	for _, info := range s.servers {
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Hostname < out[j].Hostname
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
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	_ = os.WriteFile(s.path, data, 0o644)
}
