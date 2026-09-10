package files

import (
	"testing"

	"github.com/spf13/afero"
)

func TestResolvedPathWithVirtualBase(t *testing.T) {
	memory := afero.NewMemMapFs()
	if err := memory.MkdirAll("/virtual-only/nested/private", 0755); err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(memory, "/virtual-only/nested/private/file", []byte("virtual"), 0600); err != nil {
		t.Fatal(err)
	}
	base := afero.NewBasePathFs(afero.NewBasePathFs(memory, "/virtual-only"), "/nested")
	for _, path := range []string{"/private/file", "/private/new/file"} {
		got, err := ResolvedPath(base, path)
		if err != nil || got != path {
			t.Fatalf("ResolvedPath(%q) = %q, %v", path, got, err)
		}
	}
}
