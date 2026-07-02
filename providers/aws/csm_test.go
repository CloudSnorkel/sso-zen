package aws

import (
	"testing"
	"time"
)

// TestCsmNotificationCache covers the throttle: first call for a key
// passes, subsequent calls within the window are suppressed, and once
// the window elapses the next call passes again. Uses an injected
// clock so we don't sleep.
func TestCsmNotificationCache(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newCsmNotificationCache(10 * time.Minute)
	cache.now = func() time.Time { return now }

	if !cache.ShouldNotify("acct-A") {
		t.Fatalf("first call for key should pass")
	}
	if cache.ShouldNotify("acct-A") {
		t.Errorf("immediate repeat for same key should be throttled")
	}

	// Different key should still pass.
	if !cache.ShouldNotify("acct-B") {
		t.Errorf("first call for distinct key should pass")
	}

	// Advance clock just inside the window — still throttled.
	now = now.Add(9 * time.Minute)
	if cache.ShouldNotify("acct-A") {
		t.Errorf("inside throttle window should still suppress")
	}

	// Past the window — should pass.
	now = now.Add(2 * time.Minute) // total 11m since first call
	if !cache.ShouldNotify("acct-A") {
		t.Errorf("after window elapsed should pass")
	}

	// And the next call after that pass should again be throttled.
	if cache.ShouldNotify("acct-A") {
		t.Errorf("call after just-refreshed timestamp should be throttled")
	}
}
