package fbhttp

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/filebrowser/filebrowser/v2/rules"
	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
	"github.com/spf13/afero"
)

func TestProxyClientIdentity(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("2001:db8:1::/64")}
	for _, tc := range []struct{ name, peer, forwarded, want string }{
		{"direct spoof", "192.0.2.1:1234", "203.0.113.8", "192.0.2.1"},
		{"trusted proxy", "10.0.0.2:1234", "192.0.2.1", "192.0.2.1"},
		{"spoofed prefix", "10.0.0.2:1234", "203.0.113.8, 192.0.2.1", "192.0.2.1"},
		{"trusted chain", "10.0.0.2:1234", "192.0.2.1, 10.0.0.3", "192.0.2.1"},
		{"untrusted middle", "10.0.0.2:1234", "203.0.113.8, 192.0.2.1, 10.0.0.3", "192.0.2.1"},
		{"malformed last hop", "10.0.0.2:1234", "192.0.2.1, unknown", "10.0.0.2"},
		{"missing header", "10.0.0.2:1234", "", "10.0.0.2"},
		{"IPv6 proxy", "[2001:db8:1::1]:1234", "2001:db8:2::1", "2001:db8:2::1"},
		{"mapped IPv4", "[::ffff:10.0.0.2]:1234", "::ffff:192.0.2.1", "192.0.2.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/login", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Forwarded-For", tc.forwarded)
			if got := clientIdentity(req, trusted); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTrustedProxyClientsHaveSeparateLimits(t *testing.T) {
	limiter := attemptLimiter{entries: make(map[string]attemptWindow)}
	handler := secureHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(r) {
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
	}), netip.MustParsePrefix("10.0.0.2/32"))
	for i := 0; i < 21; i++ {
		for _, client := range []string{"192.0.2.1", "192.0.2.2"} {
			req := httptest.NewRequest("POST", "/api/login", nil)
			req.RemoteAddr = "10.0.0.2:1234"
			req.Header.Set("X-Forwarded-For", client)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			want := 200
			if i == 20 {
				want = 429
			}
			if rec.Code != want {
				t.Fatalf("client %s attempt %d: got %d want %d", client, i+1, rec.Code, want)
			}
		}
	}
}

func TestLogoutRequiresBrowserIntent(t *testing.T) {
	for _, tc := range []struct {
		name, header, site string
		want               int
	}{
		{"form post", "", "cross-site", 403},
		{"missing header", "", "same-origin", 403},
		{"cross-site", "FileBrowser", "cross-site", 403},
		{"expired session", "FileBrowser", "same-origin", 204},
		{"older browser", "FileBrowser", "", 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "https://example.com/api/logout", nil)
			req.Header.Set("X-Requested-With", tc.header)
			req.Header.Set("Sec-Fetch-Site", tc.site)
			rec := httptest.NewRecorder()
			status, err := logoutHandler(rec, req, &data{server: &settings.Server{}})
			if err != nil || status != tc.want {
				t.Fatalf("status=%d err=%v", status, err)
			}
			cookies := rec.Result().Cookies()
			if status == 403 && len(cookies) != 0 {
				t.Fatal("rejected request changed cookie")
			}
			if status == 204 && (len(cookies) != 1 || cookies[0].MaxAge != -1) {
				t.Fatal("logout did not expire cookie")
			}
		})
	}
}

type stampFailureCache struct{ UploadCache }

func (stampFailureCache) SetStamp(string, string) error { return os.ErrPermission }

type statFailureAfterCreate struct {
	afero.Fs
	created bool
}

func (f *statFailureAfterCreate) OpenFile(path string, flag int, mode os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(path, flag, mode)
	if err == nil && flag&os.O_CREATE != 0 {
		f.created = true
	}
	return file, err
}
func (f *statFailureAfterCreate) Stat(path string) (os.FileInfo, error) {
	if f.created && filepath.Base(path) == "upload.txt" {
		return nil, os.ErrPermission
	}
	return f.Fs.Stat(path)
}

func TestTusPostReleasesFailedInitialization(t *testing.T) {
	for _, failStat := range []bool{false, true} {
		scope := t.TempDir()
		key := []byte("test-key")
		perm := users.Permissions{Create: true}
		st := scopedUserStorage(t, scope, perm, key)
		memory := newMemoryUploadCache()
		defer memory.Close()
		var cache UploadCache = stampFailureCache{memory}
		want := 503
		if failStat {
			cache = memory
			want = 403
			st.Users = &customFSUser{Store: st.Users, fs: afero.NewBasePathFs(&statFailureAfterCreate{Fs: afero.NewOsFs()}, scope)}
		}
		req := httptest.NewRequest("POST", "/upload.txt", nil)
		req.Header.Set("X-Auth", signToken(t, perm, key))
		req.Header.Set("Upload-Length", "5")
		rec := httptest.NewRecorder()
		handle(tusPostHandler(cache), "", st, &settings.Server{}).ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("failStat=%v status=%d body=%s", failStat, rec.Code, rec.Body.String())
		}
		if memory.cache.Len() != 0 {
			t.Fatal("failed upload registration left active")
		}
	}
}

func TestVirtualFilesystemRules(t *testing.T) {
	f := afero.NewMemMapFs()
	if err := f.MkdirAll("/virtual/private", 0755); err != nil {
		t.Fatal(err)
	}
	d := &data{server: &settings.Server{}, settings: &settings.Settings{Rules: []rules.Rule{{Path: "/private", Allow: false}}}, user: &users.User{Fs: afero.NewBasePathFs(f, "/virtual")}}
	if !d.CheckRules("/allowed") || d.CheckRules("/private/file") {
		t.Fatal("virtual filesystem rules were not preserved")
	}
}
