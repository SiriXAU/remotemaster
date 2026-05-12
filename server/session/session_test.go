package session

import "testing"

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
