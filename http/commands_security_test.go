package fbhttp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
	"github.com/gorilla/websocket"
)

func TestCommandStreamsAndAllowlist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX commands")
	}
	scope := t.TempDir()
	key := []byte("command-test-key")
	perm := users.Permissions{Execute: true}
	st := scopedUserStorage(t, scope, perm, key)
	u, err := st.Users.Get("", false, uint(1))
	if err != nil {
		t.Fatal(err)
	}
	u.Commands = []string{"sh", "printf"}
	if err := st.Users.Update(u, "Commands"); err != nil {
		t.Fatal(err)
	}
	if err := st.Settings.Save(&settings.Settings{Key: key, Shell: []string{"sh", "-c"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handle(commandsHandler, "", st, &settings.Server{EnableExec: true}))
	defer srv.Close()
	run := func(command string) string {
		t.Helper()
		headers := http.Header{"X-Auth": []string{signToken(t, perm, key)}}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/", headers)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, []byte(command)); err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				if e, ok := err.(interface{ Timeout() bool }); ok && e.Timeout() {
					t.Fatal("command output deadlocked")
				}
				break
			}
			output.Write(message)
		}
		return output.String()
	}
	output := run(`sh -c 'head -c 131072 /dev/zero | tr "\000" x >&2; echo finished'`)
	if len(output) < 131072 || !strings.Contains(output, "finished") {
		t.Fatal("stdout/stderr output incomplete")
	}
	marker := filepath.Join(scope, "injected")
	run(`printf safe; touch ` + marker)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("configured shell bypassed allowlist")
	}
}
