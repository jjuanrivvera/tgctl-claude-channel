package main

import (
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	if safeName("a<b>[c];\nd") != "a_b__c___d" {
		t.Errorf("safeName strips delimiters, got %q", safeName("a<b>[c];\nd"))
	}
	if safeName("") != "" {
		t.Error("empty stays empty")
	}
}

func TestSanitizers(t *testing.T) {
	if sanitizeToken("aB9_-!@#") != "aB9_-" {
		t.Errorf("token = %q", sanitizeToken("aB9_-!@#"))
	}
	if sanitizeToken("!!!") != "dl" {
		t.Error("empty token → dl")
	}
	if sanitizeExt("jp!g") != "jpg" || sanitizeExt("...") != "bin" {
		t.Error("ext sanitize")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "b") != "b" || firstNonEmpty("a", "b") != "a" {
		t.Error("firstNonEmpty")
	}
}

func TestCollectMedia_PhotoDownloads(t *testing.T) {
	s, ft, _ := newTestServer(t, "1")
	m := &message{Photo: []fileRef{{FileID: "small"}, {FileID: "large", FileUniqueID: "uniq"}}}
	path, attach := s.collectMedia(m)
	if attach != nil {
		t.Fatal("a photo yields an image path, not an attachment")
	}
	if path == "" || !strings.Contains(ft.all(), "file download") || !strings.Contains(ft.all(), "large") {
		t.Fatalf("largest photo should be downloaded; got path=%q calls=%q", path, ft.all())
	}
}

func TestCollectMedia_Attachments(t *testing.T) {
	s, _, _ := newTestServer(t, "1")
	cases := []struct {
		m    *message
		kind string
	}{
		{&message{Document: &fileRef{FileID: "d", FileName: "a.pdf", MimeType: "application/pdf"}}, "document"},
		{&message{Voice: &fileRef{FileID: "v"}}, "voice"},
		{&message{Audio: &fileRef{FileID: "a", Title: "song"}}, "audio"},
		{&message{Video: &fileRef{FileID: "vid"}}, "video"},
		{&message{VideoNote: &fileRef{FileID: "vn"}}, "video_note"},
		{&message{Sticker: &sticker{FileID: "s"}}, "sticker"},
	}
	for _, c := range cases {
		path, attach := s.collectMedia(c.m)
		if path != "" || attach == nil || attach.Kind != c.kind {
			t.Errorf("%s: got path=%q attach=%+v", c.kind, path, attach)
		}
	}
}

func TestCollectMedia_None(t *testing.T) {
	s, _, _ := newTestServer(t, "1")
	if p, a := s.collectMedia(&message{Text: "just text"}); p != "" || a != nil {
		t.Error("a text message has no media")
	}
}
