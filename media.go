package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type attachment struct {
	Kind   string
	FileID string
	Size   int64
	Mime   string
	Name   string
}

// collectMedia inspects a message for media. A photo is downloaded eagerly to the
// inbox (small, and the assistant almost always wants to Read it) and its local path
// returned. Heavier attachments are described in meta and fetched on demand via the
// download_attachment tool. Returns (imagePath, attachment); either may be empty.
func (s *server) collectMedia(m *message) (string, *attachment) {
	switch {
	case len(m.Photo) > 0:
		best := m.Photo[len(m.Photo)-1] // Telegram orders photo sizes smallest→largest
		return s.downloadToInbox(best.FileID, best.FileUniqueID, "jpg"), nil
	case m.Document != nil:
		return "", &attachment{Kind: "document", FileID: m.Document.FileID, Size: m.Document.FileSize, Mime: m.Document.MimeType, Name: safeName(m.Document.FileName)}
	case m.Voice != nil:
		return "", &attachment{Kind: "voice", FileID: m.Voice.FileID, Size: m.Voice.FileSize, Mime: m.Voice.MimeType}
	case m.Audio != nil:
		return "", &attachment{Kind: "audio", FileID: m.Audio.FileID, Size: m.Audio.FileSize, Mime: m.Audio.MimeType, Name: safeName(firstNonEmpty(m.Audio.Title, m.Audio.FileName))}
	case m.Video != nil:
		return "", &attachment{Kind: "video", FileID: m.Video.FileID, Size: m.Video.FileSize, Mime: m.Video.MimeType, Name: safeName(m.Video.FileName)}
	case m.VideoNote != nil:
		return "", &attachment{Kind: "video_note", FileID: m.VideoNote.FileID, Size: m.VideoNote.FileSize}
	case m.Sticker != nil:
		return "", &attachment{Kind: "sticker", FileID: m.Sticker.FileID, Size: m.Sticker.FileSize}
	}
	return "", nil
}

// defaultCaption gives a media message a textual stand-in when it has no caption, so
// the notification content is never empty.
func (m *message) defaultCaption() string {
	if c := m.textOrCaption(); c != "" {
		return c
	}
	switch {
	case len(m.Photo) > 0:
		return "(photo)"
	case m.Document != nil:
		return "(document: " + firstNonEmpty(safeName(m.Document.FileName), "file") + ")"
	case m.Voice != nil:
		return "(voice message)"
	case m.Audio != nil:
		return "(audio: " + firstNonEmpty(safeName(firstNonEmpty(m.Audio.Title, m.Audio.FileName)), "audio") + ")"
	case m.Video != nil:
		return "(video)"
	case m.VideoNote != nil:
		return "(video note)"
	case m.Sticker != nil:
		if m.Sticker.Emoji != "" {
			return "(sticker " + m.Sticker.Emoji + ")"
		}
		return "(sticker)"
	}
	return ""
}

// downloadAttachment fetches any attachment by file_id to the inbox and returns the
// local path, resolving the extension from the remote file path first.
func (s *server) downloadAttachment(fileID string) (string, error) {
	ext := "bin"
	if out, err := s.tg.cmd("file", "info", "--file-id", fileID, "-o", "json"); err == nil {
		var fi struct {
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal([]byte(out), &fi) == nil && strings.Contains(fi.FilePath, ".") {
			ext = fi.FilePath[strings.LastIndex(fi.FilePath, ".")+1:]
		}
	}
	path := s.downloadToInbox(fileID, fileID, ext)
	if path == "" {
		return "", fmt.Errorf("download failed for file_id %s", fileID)
	}
	return path, nil
}

func (s *server) downloadToInbox(fileID, uniqueID, ext string) string {
	inbox := filepath.Join(s.cfg.StateDir, "inbox")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		return ""
	}
	dest := filepath.Join(inbox, strconv.FormatInt(nowMillis(), 10)+"-"+sanitizeToken(uniqueID)+"."+sanitizeExt(ext))
	if _, err := s.tg.cmd("file", "download", "--file-id", fileID, "--dest", dest); err != nil {
		return ""
	}
	return dest
}

// safeName strips delimiter chars from uploader-controlled names so a crafted filename
// can't break out of the <channel> notification tag the client renders.
func safeName(s string) string {
	if s == "" {
		return ""
	}
	return strings.NewReplacer("<", "_", ">", "_", "[", "_", "]", "_", ";", "_", "\r", "_", "\n", "_").Replace(s)
}

func sanitizeToken(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return "dl"
	}
	return string(out)
}

func sanitizeExt(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return "bin"
	}
	return string(out)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
