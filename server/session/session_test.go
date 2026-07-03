package session

import (
	"testing"
	"time"
)

func TestJoinClaimsSessionOnce(t *testing.T) {
	store := NewStore()
	sess, err := store.Create(nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	select {
	case <-sess.Joined():
		t.Fatal("session joined channel closed before Join")
	default:
	}

	got, ok := store.Join(sess.Code, nil)
	if !ok {
		t.Fatal("Join returned false")
	}
	if got != sess {
		t.Fatal("Join returned a different session")
	}

	select {
	case <-sess.Joined():
	default:
		t.Fatal("session joined channel was not closed")
	}

	if _, ok := store.Join(sess.Code, nil); ok {
		t.Fatal("second Join returned true")
	}
}

func TestRemoveReturnsRemovedSession(t *testing.T) {
	store := NewStore()
	sess, err := store.Create(nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got := store.Remove(sess.Code); got != sess {
		t.Fatal("Remove did not return the removed session")
	}
	if got := store.Remove(sess.Code); got != nil {
		t.Fatal("Remove returned a session after it was already removed")
	}
}

func TestExpireOnceHonorsConfiguredTTLs(t *testing.T) {
	store := NewStoreWithTTLs(10*time.Minute, time.Hour)

	pending, err := store.Create(nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	active, err := store.Create(nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := store.Join(active.Code, nil); !ok {
		t.Fatal("Join returned false")
	}

	// Just before the pending TTL nothing expires.
	if got := store.expireOnce(pending.CreatedAt.Add(9 * time.Minute)); len(got) != 0 {
		t.Fatalf("expired %d sessions before TTL, want 0", len(got))
	}

	// Past the pending TTL only the unjoined session is reclaimed: the joined
	// one is measured against the (longer) active TTL from its join time.
	got := store.expireOnce(pending.CreatedAt.Add(11 * time.Minute))
	if len(got) != 1 || got[0] != pending {
		t.Fatalf("expired %v, want just the pending session", got)
	}

	// Past the active TTL the joined session goes too.
	got = store.expireOnce(active.JoinedAt.Add(61 * time.Minute))
	if len(got) != 1 || got[0] != active {
		t.Fatalf("expired %v, want just the active session", got)
	}
}

func TestNewStoreWithTTLsRejectsNonPositive(t *testing.T) {
	store := NewStoreWithTTLs(0, -time.Hour)
	if store.pendingTTL != DefaultPendingTTL || store.activeTTL != DefaultActiveTTL {
		t.Fatalf("TTLs = %v/%v, want defaults", store.pendingTTL, store.activeTTL)
	}
}

func TestProbePendingSkipsAfterJoin(t *testing.T) {
	store := NewStore()
	sess, err := store.Create(nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	joined, err := sess.ProbePending(func() error { return nil })
	if joined || err != nil {
		t.Fatalf("ProbePending before join = joined %v err %v", joined, err)
	}

	if _, ok := store.Join(sess.Code, nil); !ok {
		t.Fatal("Join returned false")
	}

	called := false
	joined, err = sess.ProbePending(func() error {
		called = true
		return nil
	})
	if !joined || err != nil {
		t.Fatalf("ProbePending after join = joined %v err %v", joined, err)
	}
	if called {
		t.Fatal("ProbePending ran probe after join")
	}
}
