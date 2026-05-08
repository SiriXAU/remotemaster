package session

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

const sessionTTL = 8 * time.Hour

type Session struct {
	Code       string
	ClientConn *websocket.Conn
	AgentConn  *websocket.Conn
	CreatedAt  time.Time
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
	if !ok || sess.AgentConn != nil {
		return nil, false
	}
	sess.AgentConn = agent
	return sess, true
}

func (s *Store) Remove(code string) {
	s.mu.Lock()
	delete(s.sessions, code)
	s.mu.Unlock()
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
		s.mu.Lock()
		for code, sess := range s.sessions {
			if now.Sub(sess.CreatedAt) > sessionTTL {
				delete(s.sessions, code)
			}
		}
		s.mu.Unlock()
	}
}
