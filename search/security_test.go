package search

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

type denyPrivate struct{}

func (denyPrivate) Check(path string) bool { return path != "/private" }

func TestSearchErrorsAndDeniedDirectories(t *testing.T) {
	f := afero.NewMemMapFs()
	if err := f.MkdirAll("/private", 0755); err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(f, "/private/secret", []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Search(context.Background(), f, "/", "", denyPrivate{}, func(path string, info os.FileInfo) error {
		if info == nil || strings.HasPrefix(path, "private") {
			t.Fatalf("unauthorized result %q", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Search(context.Background(), f, "/missing", "", denyPrivate{}, func(string, os.FileInfo) error { t.Fatal("callback on stat failure"); return nil }); !os.IsNotExist(err) {
		t.Fatalf("err = %v", err)
	}
}
