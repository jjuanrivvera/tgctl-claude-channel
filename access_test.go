package main

import (
	"os"
	"path/filepath"
	"testing"
)

func boolp(b bool) *bool { return &b }

func TestGate_Disabled(t *testing.T) {
	a := access{DMPolicy: policyDisabled}
	r, _ := gate(&a, "private", "1", "1", false, 0)
	if r.action != gateDrop {
		t.Fatal("disabled policy must drop")
	}
}

func TestGate_EmptySenderDrops(t *testing.T) {
	a := defaultAccess()
	r, _ := gate(&a, "private", "", "1", false, 0)
	if r.action != gateDrop {
		t.Fatal("empty sender must drop")
	}
}

func TestGate_PrivateAllowlisted(t *testing.T) {
	a := access{DMPolicy: policyAllowlist, AllowFrom: []string{"7"}}
	r, _ := gate(&a, "private", "7", "7", false, 0)
	if r.action != gateDeliver {
		t.Fatal("allowlisted DM should deliver")
	}
	r, _ = gate(&a, "private", "8", "8", false, 0)
	if r.action != gateDrop {
		t.Fatal("non-allowlisted DM under allowlist policy must drop")
	}
}

func TestGate_PairingLifecycle(t *testing.T) {
	a := defaultAccess() // pairing
	r, changed := gate(&a, "private", "5", "5", false, 1000)
	if r.action != gatePair || r.isResend || r.code == "" || !changed {
		t.Fatalf("first contact should issue a fresh pairing code, got %+v", r)
	}
	code := r.code
	r2, _ := gate(&a, "private", "5", "5", false, 1000)
	if r2.action != gatePair || !r2.isResend || r2.code != code {
		t.Fatalf("second contact should resend the same code, got %+v", r2)
	}
	r3, _ := gate(&a, "private", "5", "5", false, 1000)
	if r3.action != gateDrop {
		t.Fatal("third contact should go silent (replies capped)")
	}
}

func TestGate_PairingCap(t *testing.T) {
	a := defaultAccess()
	for _, id := range []string{"a", "b", "c"} {
		gate(&a, "private", id, id, false, 1000)
	}
	r, _ := gate(&a, "private", "d", "d", false, 1000)
	if r.action != gateDrop {
		t.Fatalf("4th pending should be dropped (cap 3), got %+v; pending=%d", r, len(a.Pending))
	}
}

func TestGate_Groups(t *testing.T) {
	a := defaultAccess()
	// no policy for this group → drop
	r, _ := gate(&a, "group", "1", "-100", false, 0)
	if r.action != gateDrop {
		t.Fatal("unconfigured group must drop")
	}
	// require-mention group: unmentioned drops, mentioned delivers
	a.Groups["-100"] = groupPolicy{RequireMention: boolp(true)}
	if r, _ := gate(&a, "group", "1", "-100", false, 0); r.action != gateDrop {
		t.Fatal("unmentioned message in a require-mention group must drop")
	}
	if r, _ := gate(&a, "supergroup", "1", "-100", true, 0); r.action != gateDeliver {
		t.Fatal("mentioned message should deliver")
	}
	// per-group allowFrom
	a.Groups["-200"] = groupPolicy{RequireMention: boolp(false), AllowFrom: []string{"9"}}
	if r, _ := gate(&a, "group", "1", "-200", false, 0); r.action != gateDrop {
		t.Fatal("sender not in group allowFrom must drop")
	}
	if r, _ := gate(&a, "group", "9", "-200", false, 0); r.action != gateDeliver {
		t.Fatal("sender in group allowFrom, no mention required, should deliver")
	}
}

func TestPruneExpired(t *testing.T) {
	a := defaultAccess()
	a.Pending["x"] = pendingEntry{ExpiresAt: 100}
	a.Pending["y"] = pendingEntry{ExpiresAt: 10_000}
	if !pruneExpired(&a, 5000) {
		t.Fatal("should report a change")
	}
	if _, ok := a.Pending["x"]; ok {
		t.Fatal("expired code should be gone")
	}
	if _, ok := a.Pending["y"]; !ok {
		t.Fatal("live code should remain")
	}
}

func TestAssertAllowedChat(t *testing.T) {
	a := access{AllowFrom: []string{"1"}, Groups: map[string]groupPolicy{"-100": {}}}
	if assertAllowedChat(a, "1") != nil {
		t.Error("allowlisted DM chat should pass")
	}
	if assertAllowedChat(a, "-100") != nil {
		t.Error("configured group should pass")
	}
	if assertAllowedChat(a, "999") == nil {
		t.Error("unknown chat should be rejected")
	}
}

func TestIsMentioned(t *testing.T) {
	ents := []msgEntity{{Type: "mention", Offset: 0, Length: 4}}
	if !isMentioned("@bot hi", "bot", ents, "", nil) {
		t.Error("@mention entity should count")
	}
	tm := []msgEntity{{Type: "text_mention", User: &tgUser{IsBot: true, Username: "bot"}}}
	if !isMentioned("hey", "bot", tm, "", nil) {
		t.Error("text_mention of the bot should count")
	}
	if !isMentioned("hey", "bot", nil, "bot", nil) {
		t.Error("reply to the bot should count")
	}
	if !isMentioned("please claude help", "bot", nil, "", []string{"claude"}) {
		t.Error("configured pattern should count")
	}
	if isMentioned("nothing here", "bot", nil, "", nil) {
		t.Error("no mention should be false")
	}
}

func TestStore_SeedFromEnvAllowlist(t *testing.T) {
	dir := t.TempDir()
	s := newAccessStore(dir, []string{"42"}, "", false)
	a := s.load()
	if a.DMPolicy != policyAllowlist || !containsStr(a.AllowFrom, "42") {
		t.Fatalf("env allowlist should seed allowlist policy, got %+v", a)
	}
}

func TestStore_SeedPairingWhenNoEnv(t *testing.T) {
	s := newAccessStore(t.TempDir(), nil, "", false)
	if s.load().DMPolicy != policyPairing {
		t.Fatal("no env allowlist should default to pairing")
	}
}

func TestStore_SaveReadRoundtrip(t *testing.T) {
	s := newAccessStore(t.TempDir(), nil, "", false)
	s.update(func(a *access) bool {
		a.AllowFrom = append(a.AllowFrom, "77")
		return true
	})
	if !containsStr(s.read().AllowFrom, "77") {
		t.Fatal("saved allowFrom should survive a reload")
	}
}

func TestStore_EnsureSeedWritesFile(t *testing.T) {
	dir := t.TempDir()
	s := newAccessStore(dir, []string{"5"}, "", false)
	s.ensureSeed()
	if _, err := os.Stat(filepath.Join(dir, "access.json")); err != nil {
		t.Fatal("ensureSeed should create access.json")
	}
	s.update(func(a *access) bool { a.AllowFrom = []string{"5", "6"}; return true })
	s.ensureSeed() // must not clobber an existing file
	if len(s.read().AllowFrom) != 2 {
		t.Fatal("ensureSeed must not overwrite an existing file")
	}
}

func TestStore_CorruptRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newAccessStore(dir, nil, "", false)
	a := s.load() // must not panic; should seed fresh
	if a.DMPolicy != policyPairing {
		t.Fatal("corrupt file should recover to a fresh default")
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) == 0 {
		t.Fatal("corrupt file should be moved aside")
	}
}

func TestAccessAck(t *testing.T) {
	if (access{}).ack() != "👀" {
		t.Error("nil AckReaction should default to 👀")
	}
	empty := ""
	if (access{AckReaction: &empty}).ack() != "" {
		t.Error("explicit empty AckReaction should disable")
	}
}
