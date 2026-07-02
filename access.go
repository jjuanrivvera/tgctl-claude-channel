package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Access control mirrors the official plugin's model so a public deployment gets
// the same guarantees: pairing to capture user_ids without touching numbers, an
// allowlist to lock down, and per-group mention gating. State is a JSON file the
// operator manages; the gate is a pure function so it is exhaustively testable.

type dmPolicy string

const (
	policyPairing   dmPolicy = "pairing"
	policyAllowlist dmPolicy = "allowlist"
	policyDisabled  dmPolicy = "disabled"
)

type groupPolicy struct {
	// RequireMention nil defaults to true — a bot in a group should only answer
	// when addressed, never every message.
	RequireMention *bool    `json:"requireMention,omitempty"`
	AllowFrom      []string `json:"allowFrom,omitempty"`
}

func (g groupPolicy) requireMention() bool { return g.RequireMention == nil || *g.RequireMention }

type pendingEntry struct {
	SenderID  string `json:"senderId"`
	ChatID    string `json:"chatId"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
	Replies   int    `json:"replies"`
}

type access struct {
	DMPolicy        dmPolicy                `json:"dmPolicy"`
	AllowFrom       []string                `json:"allowFrom"`
	Groups          map[string]groupPolicy  `json:"groups"`
	Pending         map[string]pendingEntry `json:"pending"`
	MentionPatterns []string                `json:"mentionPatterns,omitempty"`
	AckReaction     *string                 `json:"ackReaction,omitempty"` // nil → default; "" → disabled
	ReplyToMode     string                  `json:"replyToMode,omitempty"` // off|first|all (default first)
	TextChunkLimit  int                     `json:"textChunkLimit,omitempty"`
	ChunkMode       string                  `json:"chunkMode,omitempty"` // length|newline
}

func defaultAccess() access {
	return access{DMPolicy: policyPairing, AllowFrom: []string{}, Groups: map[string]groupPolicy{}, Pending: map[string]pendingEntry{}}
}

func normalizeAccess(a *access) {
	if a.DMPolicy == "" {
		a.DMPolicy = policyPairing
	}
	if a.AllowFrom == nil {
		a.AllowFrom = []string{}
	}
	if a.Groups == nil {
		a.Groups = map[string]groupPolicy{}
	}
	if a.Pending == nil {
		a.Pending = map[string]pendingEntry{}
	}
}

const pairingTTL = int64(60 * 60 * 1000) // 1h in ms

// --- gate: the pure security decision -------------------------------------------------

type gateAction int

const (
	gateDrop gateAction = iota
	gateDeliver
	gatePair
)

type gateResult struct {
	action   gateAction
	code     string
	isResend bool
}

// gate decides what to do with an inbound message. It may mutate a.Pending (issuing
// or bumping a pairing code); the bool reports whether the caller must persist.
func gate(a *access, chatType, senderID, chatID string, mentioned bool, now int64) (gateResult, bool) {
	changed := pruneExpired(a, now)
	if a.DMPolicy == policyDisabled || senderID == "" {
		return gateResult{action: gateDrop}, changed
	}

	switch chatType {
	case "private":
		if containsStr(a.AllowFrom, senderID) {
			return gateResult{action: gateDeliver}, changed
		}
		if a.DMPolicy == policyAllowlist {
			return gateResult{action: gateDrop}, changed
		}
		// pairing mode
		for code, p := range a.Pending {
			if p.SenderID == senderID {
				if p.Replies >= 2 { // initial + one reminder, then silent
					return gateResult{action: gateDrop}, changed
				}
				p.Replies++
				a.Pending[code] = p
				return gateResult{action: gatePair, code: code, isResend: true}, true
			}
		}
		if len(a.Pending) >= 3 { // cap concurrent pending
			return gateResult{action: gateDrop}, changed
		}
		code := newPairingCode()
		a.Pending[code] = pendingEntry{SenderID: senderID, ChatID: chatID, CreatedAt: now, ExpiresAt: now + pairingTTL, Replies: 1}
		return gateResult{action: gatePair, code: code}, true

	case "group", "supergroup":
		policy, ok := a.Groups[chatID]
		if !ok {
			return gateResult{action: gateDrop}, changed
		}
		if len(policy.AllowFrom) > 0 && !containsStr(policy.AllowFrom, senderID) {
			return gateResult{action: gateDrop}, changed
		}
		if policy.requireMention() && !mentioned {
			return gateResult{action: gateDrop}, changed
		}
		return gateResult{action: gateDeliver}, changed
	}
	return gateResult{action: gateDrop}, changed
}

// assertAllowedChat gates OUTBOUND: reply/react/edit can only target chats the
// inbound gate would deliver from, so a prompt-injected chat_id can't reach a
// stranger. Telegram DM chat_id == user_id, so allowFrom covers DMs.
func assertAllowedChat(a access, chatID string) error {
	if containsStr(a.AllowFrom, chatID) {
		return nil
	}
	if _, ok := a.Groups[chatID]; ok {
		return nil
	}
	return fmt.Errorf("chat %s is not allowlisted — add it via access.json", chatID)
}

func pruneExpired(a *access, now int64) bool {
	changed := false
	for code, p := range a.Pending {
		if p.ExpiresAt < now {
			delete(a.Pending, code)
			changed = true
		}
	}
	return changed
}

// isMentioned reports whether the bot was addressed in a group message: an @mention
// entity, a text_mention of the bot, a reply to one of the bot's messages, or a
// configured regex pattern.
func isMentioned(text, botUsername string, entities []msgEntity, replyToBotUser string, patterns []string) bool {
	lower := "@" + strings.ToLower(botUsername)
	rn := []rune(text)
	for _, e := range entities {
		if e.Type == "mention" && e.Offset >= 0 && e.Offset+e.Length <= len(rn) {
			if strings.ToLower(string(rn[e.Offset:e.Offset+e.Length])) == lower {
				return true
			}
		}
		if e.Type == "text_mention" && e.User != nil && e.User.IsBot && e.User.Username == botUsername {
			return true
		}
	}
	if botUsername != "" && replyToBotUser == botUsername {
		return true
	}
	for _, p := range patterns {
		if re, err := regexp.Compile("(?i)" + p); err == nil && re.MatchString(text) {
			return true
		}
	}
	return false
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func newPairingCode() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:]) // 6 hex chars
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// --- accessStore: durable, concurrency-safe file I/O ----------------------------------

type accessStore struct {
	mu       sync.Mutex
	path     string
	stateDir string
	seed     func() access
}

func newAccessStore(stateDir string, envAllow []string, envAck string, hasAck bool) *accessStore {
	return &accessStore{
		path:     filepath.Join(stateDir, "access.json"),
		stateDir: stateDir,
		seed: func() access {
			a := defaultAccess()
			if len(envAllow) > 0 { // seed from the env allowlist so existing deploys keep working
				a.AllowFrom = append([]string{}, envAllow...)
				a.DMPolicy = policyAllowlist
			}
			if hasAck {
				ack := envAck
				a.AckReaction = &ack
			}
			return a
		},
	}
}

// load reads the file fresh on every call (the operator may edit it out of band).
// A missing file seeds from env; a corrupt file is moved aside, not fatal.
func (s *accessStore) load() access {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return s.seed()
	}
	var a access
	if err := json.Unmarshal(b, &a); err != nil {
		_ = os.Rename(s.path, fmt.Sprintf("%s.corrupt-%d", s.path, nowMillis()))
		return s.seed()
	}
	normalizeAccess(&a)
	return a
}

func (s *accessStore) save(a access) error {
	if err := os.MkdirAll(s.stateDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ensureSeed writes the seeded access.json on first run so the operator has a file to
// edit (add groups, switch policy). A no-op once the file exists.
func (s *accessStore) ensureSeed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		_ = s.save(s.seed())
	}
}

// read returns a snapshot under lock.
func (s *accessStore) read() access {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// update runs a load-modify-save transaction under lock; fn returns whether to persist.
func (s *accessStore) update(fn func(a *access) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.load()
	if fn(&a) {
		_ = s.save(a)
	}
}
