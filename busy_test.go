package main

import (
	"strings"
	"testing"
	"time"
)

// waitForNoCalls asserts the fake transport stays quiet for a short window — used to
// prove a cancelled notice never fires.
func assertNoSendWithin(t *testing.T, ft *fakeTransport, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ft.count() > 0 {
			t.Fatalf("expected no tgctl calls, got: %s", ft.all())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestBusyNotifier_DisabledWhenDelayNonPositive(t *testing.T) {
	if b := newBusyNotifier(&fakeTransport{}, 0, ""); b != nil {
		t.Errorf("delay 0 must disable the notifier (got %v)", b)
	}
	if b := newBusyNotifier(&fakeTransport{}, -1, ""); b != nil {
		t.Errorf("negative delay must disable the notifier (got %v)", b)
	}
}

func TestBusyNotifier_NilReceiverIsNoOp(t *testing.T) {
	var b *busyNotifier
	// Must not panic.
	b.start("123")
	b.stop("123")
}

func TestBusyNotifier_FiresAfterDelay(t *testing.T) {
	ft := &fakeTransport{}
	b := newBusyNotifier(ft, 15*time.Millisecond, "")
	b.start("123")
	waitForCalls(t, ft, 1)
	got := ft.all()
	if !strings.HasPrefix(got, "send 123 ") {
		t.Fatalf("expected a send to chat 123, got: %s", got)
	}
	if !strings.Contains(got, defaultBusyNoticeText) {
		t.Errorf("expected default notice text, got: %s", got)
	}
}

func TestBusyNotifier_StopBeforeDelayCancels(t *testing.T) {
	ft := &fakeTransport{}
	b := newBusyNotifier(ft, 50*time.Millisecond, "")
	b.start("123")
	b.stop("123")
	assertNoSendWithin(t, ft, 80*time.Millisecond)
}

func TestBusyNotifier_CustomText(t *testing.T) {
	ft := &fakeTransport{}
	b := newBusyNotifier(ft, 10*time.Millisecond, "en cola")
	b.start("42")
	waitForCalls(t, ft, 1)
	if !strings.Contains(ft.all(), "en cola") {
		t.Errorf("expected custom text, got: %s", ft.all())
	}
}

func TestBusyNotifier_DedupesRepeatStart(t *testing.T) {
	ft := &fakeTransport{}
	b := newBusyNotifier(ft, 15*time.Millisecond, "")
	b.start("123")
	b.start("123") // a burst of queued messages → still one notice
	b.start("123")
	waitForCalls(t, ft, 1)
	// Give any duplicate goroutine a chance to fire, then assert only one send total.
	time.Sleep(30 * time.Millisecond)
	if n := ft.count(); n != 1 {
		t.Errorf("expected exactly one notice for a chat, got %d: %s", n, ft.all())
	}
}

func TestBusyNotifier_EmptyChatIDIgnored(t *testing.T) {
	ft := &fakeTransport{}
	b := newBusyNotifier(ft, 10*time.Millisecond, "")
	b.start("")
	assertNoSendWithin(t, ft, 30*time.Millisecond)
}

func TestBusyNotifier_ReArmAfterFire(t *testing.T) {
	ft := &fakeTransport{}
	b := newBusyNotifier(ft, 10*time.Millisecond, "")
	b.start("123")
	waitForCalls(t, ft, 1)
	// Once the notice fired and cleared, a fresh turn re-arms and fires again.
	b.start("123")
	waitForCalls(t, ft, 2)
}

func TestParseBusyDelay(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"-5", 0},
		{"45", 45 * time.Second},
		{"30s", 30 * time.Second},
		{"2m", 2 * time.Minute},
		{"-10s", 0},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := parseBusyDelay(c.in); got != c.want {
			t.Errorf("parseBusyDelay(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestBusyNotifier_StopClearedByReply proves the wired-in reply path cancels a pending
// notice: deliver a turn (arms busy), then a reply tool call stops it.
func TestBusyNotifier_StopClearedByReply(t *testing.T) {
	s, ft, _ := newTestServer(t, "55")
	s.busy = newBusyNotifier(ft, 60*time.Millisecond, "")

	s.busy.start("55")
	if _, err := call(t, s, "reply", map[string]any{"chat_id": "55", "text": "hi"}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	// The reply already produced one send; assert the busy notice never adds a second.
	waitForCalls(t, ft, 1)
	time.Sleep(90 * time.Millisecond)
	if n := ft.count(); n != 1 {
		t.Errorf("busy notice should have been cancelled by the reply; got %d calls: %s", n, ft.all())
	}
}
