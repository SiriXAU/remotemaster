package session

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

const (
	pendingSessionTTL = 10 * time.Minute
	activeSessionTTL  = 8 * time.Hour
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
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewStore() *Store {
	s := &Store{sessions: make(map[string]*Session)}
	go s.expireLoop()
	return s
}

func (s *Store) Create(conn *websocket.Conn) (*Session, error) {
	code, err := s.generateCode()
	if err != nil {
		return nil, err
	}
	sess := &Session{
		Code:       code,
		ClientConn: conn,
		CreatedAt:  time.Now(),
		joined:     make(chan struct{}),
	}
	s.mu.Lock()
	s.sessions[code] = sess
	s.mu.Unlock()
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

func (s *Store) generateCode() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
		now := time.Now()
		var expired []*Session

		s.mu.Lock()
		for code, sess := range s.sessions {
			ttl := pendingSessionTTL
			start := sess.CreatedAt
			if !sess.JoinedAt.IsZero() {
				ttl = activeSessionTTL
				start = sess.JoinedAt
			}
			if now.Sub(start) > ttl {
				delete(s.sessions, code)
				expired = append(expired, sess)
			}
		}
		s.mu.Unlock()

		for _, sess := range expired {
			if sess.ClientConn != nil {
				sess.ClientConn.Close(websocket.StatusNormalClosure, "session expired")
			}
			if sess.AgentConn != nil {
				sess.AgentConn.Close(websocket.StatusNormalClosure, "session expired")
			}
		}
	}
}
