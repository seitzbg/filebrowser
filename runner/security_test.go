package runner

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

func TestShellHookTreatsFilenameAsData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX shell")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "injected")
	output := filepath.Join(dir, "output")
	filename := "$(touch " + marker + "); quoted ' name"
	r := &Runner{Settings: &settings.Settings{Shell: []string{"sh", "-c"}}}
	if err := r.exec(`printf '%s' "$FILE" > "$DESTINATION"`, "upload", filename, output, &users.User{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("filename executed as shell code")
	}
	got, err := os.ReadFile(output)
	if err != nil || string(got) != filename {
		t.Fatalf("output = %q, err %v", got, err)
	}
}
