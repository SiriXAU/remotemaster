package session

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

// Default session lifetimes, overridable per store via NewStoreWithTTLs.
const (
	DefaultPendingTTL = 10 * time.Minute
	DefaultActiveTTL  = 8 * time.Hour
)

type Session struct {
	Code       string
	ClientConn *websocket.Conn
	AgentConn  *websocket.Conn
	CreatedAt  time.Time
	JoinedAt   time.Time

	joined  chan struct{}
	probeMu sync.Mutex
}

type Store struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	pendingTTL time.Duration
	activeTTL  time.Duration
}

func NewStore() *Store {
	return NewStoreWithTTLs(DefaultPendingTTL, DefaultActiveTTL)
}

// NewStoreWithTTLs creates a store with custom session lifetimes. Non-positive
// values fall back to the defaults.
func NewStoreWithTTLs(pendingTTL, activeTTL time.Duration) *Store {
	if pendingTTL <= 0 {
		pendingTTL = DefaultPendingTTL
	}
	if activeTTL <= 0 {
		activeTTL = DefaultActiveTTL
	}
	s := &Store{
		sessions:   make(map[string]*Session),
		pendingTTL: pendingTTL,
		activeTTL:  activeTTL,
	}
	go s.expireLoop()
	return s
}

func (s *Store) Create(conn *websocket.Conn) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate and insert under the same write lock so two concurrent Create
	// calls cannot both claim the same code (a check-then-insert race would let
	// the second overwrite the first, orphaning its connection).
	code, err := s.generateCodeLocked()
	if err != nil {
		return nil, err
	}
	sess := &Session{
		Code:       code,
		ClientConn: conn,
		CreatedAt:  time.Now(),
		joined:     make(chan struct{}),
	}
	s.sessions[code] = sess
	return sess, nil
}

// Join attaches an agent to the session and returns the full session.
// Returns false if the code is unknown or an agent is already attached.
func (s *Store) Join(code string, agent *websocket.Conn) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[code]
	if !ok || !sess.JoinedAt.IsZero() {
		return nil, false
	}
	sess.AgentConn = agent
	sess.JoinedAt = time.Now()
	close(sess.joined)
	return sess, true
}

// Counts returns a snapshot of how many sessions are waiting for an agent
// (pending) and how many have one attached (active).
func (s *Store) Counts() (pending, active int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sess := range s.sessions {
		if sess.JoinedAt.IsZero() {
			pending++
		} else {
			active++
		}
	}
	return pending, active
}

func (s *Store) Remove(code string) *Session {
	s.mu.Lock()
	sess := s.sessions[code]
	if sess != nil {
		delete(s.sessions, code)
	}
	s.mu.Unlock()
	return sess
}

// Joined is closed when an agent claims the session.
func (s *Session) Joined() <-chan struct{} {
	return s.joined
}

// ProbePending runs fn only while the session is still waiting for an agent.
// The probe mutex lets the agent side wait for any in-flight ping before the
// relay starts using the same WebSocket connection.
func (s *Session) ProbePending(fn func() error) (joined bool, err error) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()

	select {
	case <-s.joined:
		return true, nil
	default:
		return false, fn()
	}
}

func (s *Session) WaitPendingProbe() {
	s.probeMu.Lock()
	s.probeMu.Unlock()
}

// generateCodeLocked returns an unused 6-digit code. The caller must hold
// s.mu for writing.
func (s *Store) generateCodeLocked() (string, error) {
	for range 20 {
		b := make([]byte, 3)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		n := (int(b[0])<<16|int(b[1])<<8|int(b[2]))%900000 + 100000
		code := fmt.Sprintf("%06d", n)
		if _, exists := s.sessions[code]; !exists {
			return code, nil
		}
	}
	return "", fmt.Errorf("code space exhausted")
}

func (s *Store) expireLoop() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		for _, sess := range s.expireOnce(time.Now()) {
			if sess.ClientConn != nil {
				sess.ClientConn.Close(websocket.StatusNormalClosure, "session expired")
			}
			if sess.AgentConn != nil {
				sess.AgentConn.Close(websocket.StatusNormalClosure, "session expired")
			}
		}
	}
}

// expireOnce removes and returns every session past its TTL as of now.
func (s *Store) expireOnce(now time.Time) []*Session {
	var expired []*Session

	s.mu.Lock()
	defer s.mu.Unlock()
	for code, sess := range s.sessions {
		ttl := s.pendingTTL
		start := sess.CreatedAt
		if !sess.JoinedAt.IsZero() {
			ttl = s.activeTTL
			start = sess.JoinedAt
		}
		if now.Sub(start) > ttl {
			delete(s.sessions, code)
			expired = append(expired, sess)
		}
	}
	return expired
}
