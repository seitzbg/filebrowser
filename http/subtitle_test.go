package fbhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/filebrowser/filebrowser/v2/files"
)

func TestNormalizeSRTLineBreaks(t *testing.T) {
	input := []byte("first<br>second<BR/>third<br />fourth<br class=\"x\">fifth")
	got := string(normalizeSRTLineBreaks(input))
	want := "first\nsecond\nthird\nfourth\nfifth"
	if got != want {
		t.Fatalf("normalizeSRTLineBreaks() = %q, want %q", got, want)
	}
}

func TestSubtitleFileHandlerConvertsSRTBreakTags(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/sample.srt"
	const content = "1\n" +
		"00:00:01,000 --> 00:00:02,000\n" +
		"First<br>Second<BR/>Third<br />Fourth\n\n"

	if err := afero.WriteFile(fs, path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write subtitle: %v", err)
	}
	info, err := fs.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat subtitle: %v", err)
	}

	file := &files.FileInfo{
		Fs:      fs,
		Path:    path,
		Name:    "sample.srt",
		ModTime: info.ModTime(),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/subtitle/sample.srt?inline=true", http.NoBody)
	rec := httptest.NewRecorder()

	status, err := subtitleFileHandler(rec, req, file)
	if err != nil {
		t.Fatalf("subtitleFileHandler returned error: %v", err)
	}
	if status != 0 {
		t.Fatalf("subtitleFileHandler status = %d, want 0", status)
	}

	body := rec.Body.String()
	if strings.Contains(body, "FirstSecond") {
		t.Fatalf("WebVTT output collapsed SRT <br> tags: %q", body)
	}
	if !strings.Contains(body, "First\nSecond\nThird\nFourth") {
		t.Fatalf("WebVTT output = %q, want converted SRT <br> tags as line breaks", body)
	}
	file.Name = "SAMPLE.SRT"
	rec = httptest.NewRecorder()
	if status, err := subtitleFileHandler(rec, req, file); err != nil || status != 0 || !strings.Contains(rec.Body.String(), "WEBVTT") {
		t.Fatalf("uppercase subtitle conversion: status=%d err=%v", status, err)
	}
}

func TestSubtitleInputLimitIgnoresStaleSize(t *testing.T) {
	f := afero.NewMemMapFs()
	if err := afero.WriteFile(f, "/large.srt", []byte(strings.Repeat("x", (8<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	file := &files.FileInfo{Fs: f, Path: "/large.srt", Name: "large.srt", Size: 0}
	status, err := subtitleFileHandler(httptest.NewRecorder(), httptest.NewRequest("GET", "/large.srt", nil), file)
	if err != nil || status != 413 {
		t.Fatalf("large subtitle: status=%d err=%v", status, err)
	}
}
