package relay

import "testing"

type fakeClipboard struct {
	text    string
	setErrs int
}

func (f *fakeClipboard) GetText() (string, error) { return f.text, nil }
func (f *fakeClipboard) SetText(text string) error {
	f.text = text
	return nil
}

func TestNoteLocalClipboardPrimesWithoutSending(t *testing.T) {
	c := &Client{}

	// First observation is only a baseline — pre-session clipboard contents
	// must never be shipped to the agent.
	if c.noteLocalClipboard("pre-session secret") {
		t.Fatal("first observation was marked for sending")
	}
	// Unchanged text is not re-sent.
	if c.noteLocalClipboard("pre-session secret") {
		t.Fatal("unchanged text marked for sending")
	}
	// A change during the session is sent.
	if !c.noteLocalClipboard("copied during session") {
		t.Fatal("changed text not marked for sending")
	}
	// And not sent again.
	if c.noteLocalClipboard("copied during session") {
		t.Fatal("same text marked for sending twice")
	}
}

func TestApplyRemoteClipboardSuppressesEcho(t *testing.T) {
	clip := &fakeClipboard{}
	c := &Client{Clip: clip}

	c.applyRemoteClipboard("from agent")
	if clip.text != "from agent" {
		t.Fatalf("clipboard = %q, want text from agent", clip.text)
	}

	// The poll loop will now observe the text we just installed; it must not
	// echo it back to the agent.
	if c.noteLocalClipboard("from agent") {
		t.Fatal("remote-installed text echoed back")
	}

	// But a subsequent local copy still syncs.
	if !c.noteLocalClipboard("local copy") {
		t.Fatal("local change after remote set not marked for sending")
	}
}

func TestApplyRemoteClipboardNilClipboard(t *testing.T) {
	c := &Client{} // no clipboard available on this machine
	c.applyRemoteClipboard("text") // must not panic
}
