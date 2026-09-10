package fbhttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

func TestTusOverwritePreservesOriginal(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			if backend == "redis" && os.Getenv("FILEBROWSER_TEST_REDIS_URL") == "" {
				t.Skip("set FILEBROWSER_TEST_REDIS_URL for Redis integration test")
			}
			for _, scenario := range []string{"complete", "interrupted", "overlength", "cancel", "changed", "before-hook", "after-hook", "empty", "cache-failure", "revoked"} {
				t.Run(scenario, func(t *testing.T) {
					var first, second UploadCache
					if backend == "redis" {
						var err error
						first, err = newRedisUploadCache(os.Getenv("FILEBROWSER_TEST_REDIS_URL"))
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(first.Close)
						second, err = newRedisUploadCache(os.Getenv("FILEBROWSER_TEST_REDIS_URL"))
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(second.Close)
					} else {
						first = newMemoryUploadCache()
						second = first
						t.Cleanup(first.Close)
					}
					root := t.TempDir()
					dst := filepath.Join(root, "file")
					if err := os.WriteFile(dst, []byte("original"), 0600); err != nil {
						t.Fatal(err)
					}
					key := []byte("test-key")
					perm := users.Permissions{Create: true, Modify: true, Delete: true}
					st := scopedUserStorage(t, root, perm, key)
					commands := map[string][]string{}
					switch scenario {
					case "before-hook":
						commands["before_upload"] = []string{"filebrowser-missing-hook"}
					case "after-hook":
						commands["after_upload"] = []string{"filebrowser-missing-hook"}
					}
					if err := st.Settings.Save(&settings.Settings{Key: key, Commands: commands, FileMode: 0644, DirMode: 0755}); err != nil {
						t.Fatal(err)
					}
					call := func(method string, body io.Reader, offset string) *httptest.ResponseRecorder {
						t.Helper()
						req := httptest.NewRequest(method, "/file?override=true", body)
						req.Header.Set("X-Auth", signToken(t, perm, key))
						req.Header.Set("Upload-Length", "10")
						if scenario == "empty" {
							req.Header.Set("Upload-Length", "0")
						}
						req.Header.Set("Upload-Offset", offset)
						req.Header.Set("Content-Type", "application/offset+octet-stream")
						handler := tusPostHandler(first)
						switch method {
						case "PATCH":
							handler = tusPatchHandler(second)
						case "HEAD":
							handler = tusHeadHandler(second)
						case "DELETE":
							handler = tusDeleteHandler(second)
						}
						rec := httptest.NewRecorder()
						handle(handler, "", st, &settings.Server{EnableExec: true}).ServeHTTP(rec, req)
						return rec
					}
					assertContent := func(want string) {
						t.Helper()
						got, err := os.ReadFile(dst)
						if err != nil || string(got) != want {
							t.Fatalf("content = %q, %v; want %q", got, err, want)
						}
					}
					if scenario == "cache-failure" {
						first = stampFailureCache{first}
					}
					post := call("POST", nil, "")
					if scenario == "before-hook" || scenario == "cache-failure" {
						if post.Code < 400 {
							t.Fatalf("POST = %d", post.Code)
						}
						assertContent("original")
						entries, _ := os.ReadDir(root)
						if len(entries) != 1 {
							t.Fatal("failed initialization leaked staged file")
						}
						return
					}
					if post.Code != http.StatusCreated {
						t.Fatalf("POST = %d: %s", post.Code, post.Body)
					}
					if scenario == "empty" {
						assertContent("")
						return
					}
					assertContent("original")
					var body io.Reader = strings.NewReader("partial")
					wantCode := 204
					switch scenario {
					case "interrupted":
						body, wantCode = &interruptedReader{}, 500
					case "overlength":
						body, wantCode = strings.NewReader("too much content"), 413
					}
					if rec := call("PATCH", body, "0"); rec.Code != wantCode {
						t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body)
					}
					assertContent("original")
					if scenario == "overlength" {
						if rec := call("HEAD", nil, ""); rec.Code != 200 || rec.Header().Get("Upload-Offset") != "0" {
							t.Fatalf("HEAD = %d, %v", rec.Code, rec.Header())
						}
						return
					}
					if rec := call("HEAD", nil, ""); rec.Code != 200 || rec.Header().Get("Upload-Offset") != "7" {
						t.Fatalf("HEAD = %d, %v", rec.Code, rec.Header())
					}
					if scenario == "cancel" {
						if rec := call("DELETE", nil, ""); rec.Code != 204 {
							t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body)
						}
						assertContent("original")
						entries, _ := os.ReadDir(root)
						if len(entries) != 1 {
							t.Fatal("cancel leaked staged file")
						}
						return
					}
					wantCode = 204
					switch scenario {
					case "changed":
						if err := os.WriteFile(dst, []byte("changed externally"), 0600); err != nil {
							t.Fatal(err)
						}
						wantCode = 409
					case "after-hook":
						wantCode = 500
					case "revoked":
						user, err := st.Users.Get("", false, uint(1))
						if err != nil {
							t.Fatal(err)
						}
						user.Perm.Modify = false
						if err := st.Users.Update(user, "Perm"); err != nil {
							t.Fatal(err)
						}
						wantCode = 403
					}
					if rec := call("PATCH", strings.NewReader("end"), "7"); rec.Code != wantCode {
						t.Fatalf("final PATCH = %d: %s", rec.Code, rec.Body)
					}
					switch scenario {
					case "changed":
						assertContent("changed externally")
					case "revoked":
						assertContent("original")
					default:
						assertContent("partialend")
						entries, _ := os.ReadDir(root)
						if len(entries) != 1 {
							t.Fatal("completed upload leaked staged file")
						}
					}
				})
			}
		})
	}
}
