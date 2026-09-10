package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMarshalReplacesExportPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := marshal(path, map[string]string{"password": "hash"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	before, _ := os.ReadFile(path)
	if err := marshal(path, make(chan int)); err == nil {
		t.Fatal("expected encoding failure")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed export destroyed existing backup")
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".filebrowser-export-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary exports left behind: %v, %v", leftovers, err)
	}
}
