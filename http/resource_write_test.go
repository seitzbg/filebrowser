package fbhttp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/filebrowser/filebrowser/v2/diskcache"
	fberrors "github.com/filebrowser/filebrowser/v2/errors"
	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

var errWriteFailure = errors.New("injected write failure")

type interruptedReader struct{ sent bool }

func (r *interruptedReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errWriteFailure
	}
	r.sent = true
	return copy(p, "partial"), nil
}

type failingWriteFS struct {
	afero.Fs
	fail string
}

func (f failingWriteFS) OpenFile(name string, flag int, mode os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, mode)
	if err != nil {
		return nil, err
	}
	return failingWriteFile{File: file, fail: f.fail}, nil
}

func (f failingWriteFS) Rename(src, dst string) error {
	if f.fail == "rename" {
		return errWriteFailure
	}
	return f.Fs.Rename(src, dst)
}

type failingWriteFile struct {
	afero.File
	fail string
}

func (f failingWriteFile) Write(p []byte) (int, error) {
	if f.fail == "write" {
		return 0, errWriteFailure
	}
	return f.File.Write(p)
}

func (f failingWriteFile) WriteString(s string) (int, error) {
	return f.Write([]byte(s))
}

func (f failingWriteFile) Sync() error {
	if f.fail == "sync" {
		return errWriteFailure
	}
	return f.File.Sync()
}

func (f failingWriteFile) Stat() (os.FileInfo, error) {
	if f.fail == "stat" {
		return nil, errWriteFailure
	}
	return f.File.Stat()
}

func (f failingWriteFile) Close() error {
	err := f.File.Close()
	if f.fail == "close" {
		return errWriteFailure
	}
	return err
}

func TestWriteFilePreservesDestinationOnFailure(t *testing.T) {
	for _, fail := range []string{"read", "write", "sync", "stat", "close", "rename"} {
		for _, existing := range []bool{false, true} {
			t.Run(fail+"/existing="+map[bool]string{true: "yes", false: "no"}[existing], func(t *testing.T) {
				base := afero.NewMemMapFs()
				if existing {
					if err := afero.WriteFile(base, "/file", []byte("original"), 0640); err != nil {
						t.Fatal(err)
					}
				}
				var body io.Reader = strings.NewReader("replacement")
				if fail == "read" {
					body = &interruptedReader{}
				}
				_, err := writeFile(failingWriteFS{Fs: base, fail: fail}, "/file", body, 0644, 0755, true)
				if !errors.Is(err, errWriteFailure) {
					t.Fatalf("error = %v", err)
				}
				content, err := afero.ReadFile(base, "/file")
				if existing && (err != nil || string(content) != "original") {
					t.Fatalf("destination = %q, %v", content, err)
				}
				if !existing && !os.IsNotExist(err) {
					t.Fatalf("failed upload created destination: %v", err)
				}
				entries, err := afero.ReadDir(base, "/")
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Name() != "file" {
						t.Fatalf("temporary file left behind: %s", entry.Name())
					}
				}
			})
		}
	}
}

func TestResourceWritesPreserveFilesOnHookAndTransferFailures(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		for _, failure := range []string{"before", "transfer", "after", "none"} {
			t.Run(method+"/"+failure, func(t *testing.T) {
				root := t.TempDir()
				dst := filepath.Join(root, "file")
				if err := os.WriteFile(dst, []byte("original"), 0640); err != nil {
					t.Fatal(err)
				}
				key := []byte("test-key")
				perm := users.Permissions{Create: true, Modify: true}
				st := scopedUserStorage(t, root, perm, key)
				event := "upload"
				handler := resourcePostHandler(diskcache.NewNoOp())
				if method == http.MethodPut {
					event = "save"
					handler = resourcePutHandler
				}
				if err := st.Settings.Save(&settings.Settings{Key: key, FileMode: 0644, DirMode: 0755,
					Commands: map[string][]string{failure + "_" + event: {"filebrowser-missing-hook-command"}},
				}); err != nil {
					t.Fatal(err)
				}
				var body io.Reader = strings.NewReader("replacement")
				if failure == "transfer" {
					body = &interruptedReader{}
				}
				req := httptest.NewRequest(method, "/file?override=true", body)
				req.Header.Set("X-Auth", signToken(t, perm, key))
				rec := httptest.NewRecorder()
				handle(handler, "", st, &settings.Server{EnableExec: true}).ServeHTTP(rec, req)
				if (rec.Code >= 400) != (failure != "none") {
					t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
				}
				want := "original"
				if failure == "after" || failure == "none" {
					want = "replacement"
				}
				content, err := os.ReadFile(dst)
				if err != nil || string(content) != want {
					t.Fatalf("destination = %q, %v; want %q", content, err, want)
				}
				entries, _ := os.ReadDir(root)
				if len(entries) != 1 {
					t.Fatalf("unexpected files: %v", entries)
				}
			})
		}
	}
}

func TestWriteFileThroughScopedSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	info, err := writeFile(files.NewScopedFs(afero.NewOsFs(), root), "/link", strings.NewReader("new"), 0644, 0755, true)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("write = %v, %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(root, "link")); err != nil || target != "target" {
		t.Fatalf("symlink changed: %q, %v", target, err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "target")); err != nil || string(content) != "new" {
		t.Fatalf("target = %q, %v", content, err)
	}
}

type changingReader struct {
	io.Reader
	change func()
}

func (r changingReader) Read(p []byte) (int, error) {
	r.change()
	return r.Reader.Read(p)
}

func TestWriteFileDetectsDestinationChange(t *testing.T) {
	for _, existed := range []bool{false, true} {
		base := afero.NewMemMapFs()
		if existed {
			if err := afero.WriteFile(base, "/file", []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		body := changingReader{Reader: strings.NewReader("replacement"), change: func() {
			if err := afero.WriteFile(base, "/file", []byte("changed during transfer"), 0600); err != nil {
				t.Fatal(err)
			}
		}}
		if _, err := writeFile(base, "/file", body, 0600, 0755, existed); !errors.Is(err, fberrors.ErrExist) {
			t.Fatalf("existed=%v: error = %v", existed, err)
		}
		got, err := afero.ReadFile(base, "/file")
		if err != nil || string(got) != "changed during transfer" {
			t.Fatalf("destination = %q, %v", got, err)
		}
	}
}
