package fbhttp

import (
	"github.com/filebrowser/filebrowser/v2/users"
	"os"
	"path/filepath"
	"testing"
)

func TestRedisUploadCoordination(t *testing.T) {
	url := os.Getenv("FILEBROWSER_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set FILEBROWSER_TEST_REDIS_URL for Redis integration test")
	}
	a, err := newRedisUploadCache(url)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := newRedisUploadCache(url)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	key := filepath.Join(t.TempDir(), "upload")
	unlocked, err := a.Lock(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Lock(key); err == nil {
		t.Fatal("second replica acquired active upload")
	}
	unlocked()
	unlock, err := b.Lock(key)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := a.Register(key, 123, nil); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Complete(key) }()
	if err := a.SetStamp(key, "revision"); err != nil {
		t.Fatal(err)
	}
	length, err := b.GetLength(key)
	if err != nil || length != 123 {
		t.Fatalf("length=%d err=%v", length, err)
	}
	stamp, err := b.GetStamp(key)
	if err != nil || stamp != "revision" {
		t.Fatalf("stamp=%s err=%v", stamp, err)
	}
	if err := b.Complete(key); err != nil {
		t.Fatal(err)
	}
	if err := a.SetStamp(key, "stale"); err == nil {
		t.Fatal("completed entry resurrected")
	}
	scope := t.TempDir()
	secret := []byte("redis-upload-test")
	perm := users.Permissions{Create: true, Modify: true}
	st := scopedUserStorage(t, scope, perm, secret)
	srv := newTusTestServer(t, st, b)
	fixture := &tusTestFixture{srv: srv, client: srv.Client(), token: signToken(t, perm, secret), scope: scope}
	uploadURL := fixture.create(t, "file", 6)
	for offset, chunk := range []string{"abc", "def"} {
		res, _ := fixture.patch(t, uploadURL, offset*3, []byte(chunk))
		res.Body.Close()
		if res.StatusCode != 204 {
			t.Fatalf("chunk returned %d", res.StatusCode)
		}
	}
	got, _ := os.ReadFile(filepath.Join(scope, "file"))
	if string(got) != "abcdef" {
		t.Fatalf("upload=%q", got)
	}
	a.Close()
	if err := a.Register(key, 1, nil); err == nil {
		t.Fatal("cache outage swallowed")
	}
}
