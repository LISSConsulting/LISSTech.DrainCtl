//go:build windows

package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"
)

// Session holds an authenticated dashboard user's active access grant.
type Session struct {
	Token      string
	Username   string
	Groups     []string
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// SessionStore is an in-memory store for active dashboard sessions.
// A background goroutine reaps expired entries every 15 minutes.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewSessionStore creates a SessionStore and starts a background reaper.
// The reaper exits when ctx is cancelled.
func NewSessionStore(ctx context.Context) *SessionStore {
	s := &SessionStore{sessions: make(map[string]*Session)}
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.reap()
			}
		}
	}()
	return s
}

func (s *SessionStore) reap() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, sess := range s.sessions {
		if now.Sub(sess.LastSeenAt) > 8*time.Hour || now.Sub(sess.CreatedAt) > 24*time.Hour {
			delete(s.sessions, token)
			slog.Debug("session reaped", "user", sess.Username)
		}
	}
}

// Create generates a new session for the given AuthInfo and returns the token.
func (s *SessionStore) Create(info *AuthInfo) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	now := time.Now()
	s.mu.Lock()
	s.sessions[token] = &Session{
		Token:      token,
		Username:   info.Username,
		Groups:     info.Groups,
		CreatedAt:  now,
		LastSeenAt: now,
	}
	s.mu.Unlock()
	return token, nil
}

// Get looks up a session by token, refreshes LastSeenAt, and returns it.
// Returns nil if the token is not found, malformed, or expired.
func (s *SessionStore) Get(token string) *Session {
	if len(token) != 64 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok {
		return nil
	}
	now := time.Now()
	if now.Sub(sess.LastSeenAt) > 8*time.Hour || now.Sub(sess.CreatedAt) > 24*time.Hour {
		delete(s.sessions, token)
		return nil
	}
	sess.LastSeenAt = now
	return sess
}

// Delete removes a session from the store. No-op if token is not found.
func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}
