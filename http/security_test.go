package fbhttp

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

type unavailableUploadCache struct{ UploadCache }

func (unavailableUploadCache) Register(string, int64, func() error) error {
	return errors.New("cache unavailable")
}

func TestTusZeroLengthAndCacheFailure(t *testing.T) {
	scope := t.TempDir()
	key := []byte("test-key")
	perm := users.Permissions{Create: true, Modify: true}
	st := scopedUserStorage(t, scope, perm, key)
	cache := newMemoryUploadCache()
	defer cache.Close()
	post := func(c UploadCache, name, length string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/"+name+"?override=true", http.NoBody)
		req.Header.Set("X-Auth", signToken(t, perm, key))
		req.Header.Set("Upload-Length", length)
		rec := httptest.NewRecorder()
		handle(tusPostHandler(c), "", st, &settings.Server{}).ServeHTTP(rec, req)
		return rec
	}
	if rec := post(cache, "empty", "0"); rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if cache.cache.Len() != 0 {
		t.Fatal("empty upload left active expiry entry")
	}
	if _, err := os.Stat(filepath.Join(scope, "empty")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "existing"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if rec := post(unavailableUploadCache{cache}, "existing", "5"); rec.Code != 503 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	got, _ := os.ReadFile(filepath.Join(scope, "existing"))
	if string(got) != "original" {
		t.Fatal("cache failure truncated existing content")
	}
}

func TestTusRejectsReplacedFile(t *testing.T) {
	f := newTusTestFixture(t)
	url := f.create(t, "replaced", 20)
	if err := os.WriteFile(filepath.Join(f.scope, "replaced"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	res, _ := f.patch(t, url, 11, []byte("intrusion"))
	res.Body.Close()
	if res.StatusCode != 409 {
		t.Fatalf("replaced upload accepted: %d", res.StatusCode)
	}
	got, _ := os.ReadFile(filepath.Join(f.scope, "replaced"))
	if string(got) != "replacement" {
		t.Fatalf("modified replacement: %s", got)
	}
}

func TestTusInvalidLengthPreservesExistingFile(t *testing.T) {
	f := newTusTestFixture(t)
	path := filepath.Join(f.scope, "existing")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, length := range []string{"", "-1", "invalid", "9223372036854775807"} {
		req := httptest.NewRequest("POST", f.srv.URL+"/api/tus/existing?override=true", http.NoBody)
		req.RequestURI = ""
		req.Header.Set("X-Auth", f.token)
		req.Header.Set("Upload-Length", length)
		res, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("length %q: %d", length, res.StatusCode)
		}
		got, _ := os.ReadFile(path)
		if string(got) != "original" {
			t.Fatal("invalid request truncated existing file")
		}
	}
}

func TestTusConcurrentChunksDoNotInterleave(t *testing.T) {
	f := newTusTestFixture(t)
	url := f.create(t, "concurrent", 6)
	reader, writer := io.Pipe()
	first, _ := http.NewRequest("PATCH", url, reader)
	first.Header.Set("X-Auth", f.token)
	first.Header.Set("Upload-Offset", "0")
	first.Header.Set("Content-Type", "application/offset+octet-stream")
	done := make(chan *http.Response, 1)
	errors := make(chan error, 1)
	go func() {
		res, err := f.client.Do(first)
		if err != nil {
			errors <- err
			return
		}
		done <- res
	}()
	if _, err := writer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, _ := os.Stat(filepath.Join(f.scope, "concurrent"))
		if info != nil && info.Size() == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first chunk never arrived")
		}
		time.Sleep(time.Millisecond)
	}
	second, _ := http.NewRequest("PATCH", url, bytes.NewBufferString("xyz"))
	second.Header = first.Header.Clone()
	res, err := f.client.Do(second)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 409 {
		t.Fatalf("concurrent status %d", res.StatusCode)
	}
	writer.Close()
	select {
	case res := <-done:
		res.Body.Close()
		if res.StatusCode != 204 {
			t.Fatalf("first status %d", res.StatusCode)
		}
	case err := <-errors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("first upload stuck")
	}
	got, _ := os.ReadFile(filepath.Join(f.scope, "concurrent"))
	if string(got) != "abc" {
		t.Fatalf("interleaved content %q", got)
	}
}

func TestRulesDenyInScopeSymlinkAliases(t *testing.T) {
	scope := t.TempDir()
	if err := os.Mkdir(filepath.Join(scope, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "private", "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("private", filepath.Join(scope, "alias")); err != nil {
		t.Skip(err)
	}
	key := []byte("test-signing-key")
	perm := users.Permissions{Download: true, Create: true, Modify: true}
	st := denyRuleStorage(t, scope, "/private", perm, key)
	for _, path := range []string{"/alias/secret", "/alias/new"} {
		req := httptest.NewRequest("GET", path, http.NoBody)
		req.Header.Set("X-Auth", signToken(t, perm, key))
		rec := httptest.NewRecorder()
		handle(rawHandler, "", st, &settings.Server{}).ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Fatalf("%s returned %d", path, rec.Code)
		}
	}
}

func TestSessionCookieAndSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "https://example.com/browser/api/session", http.NoBody)
	setAuthCookie(rec, req, &data{server: &settings.Server{BaseURL: "/browser"}}, "token", 3600)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Path != "/browser/" || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookies=%v", cookies)
	}
	rec = httptest.NewRecorder()
	secureHandler(http.NotFoundHandler()).ServeHTTP(rec, req)
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Fatal("missing fallback CSP")
	}
}

func TestPasswordLimiterIgnoresForwardedHeaders(t *testing.T) {
	l := attemptLimiter{entries: make(map[string]attemptWindow)}
	req := httptest.NewRequest("POST", "/api/login", nil)
	for i := 0; i < 20; i++ {
		if !l.allow(req) {
			t.Fatal("early limit")
		}
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	if l.allow(req) {
		t.Fatal("forwarded header bypassed limit")
	}
}
