package fbhttp

import (
	"bufio"
	"context"
	"io"
	"log"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/filebrowser/filebrowser/v2/runner"
)

const (
	WSWriteDeadline = 10 * time.Second
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

var (
	cmdNotAllowed = []byte("Command not allowed.")
)

func wsErr(ws *websocket.Conn, r *http.Request, status int, err error) {
	txt := http.StatusText(status)
	if err != nil || status >= 400 {
		log.Printf("%s: %v %s %v", r.URL.Path, status, r.RemoteAddr, err)
	}
	if err := ws.WriteControl(websocket.CloseInternalServerErr, []byte(txt), time.Now().Add(WSWriteDeadline)); err != nil {
		log.Print(err)
	}
}

var commandsHandler = withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !d.server.EnableExec || !d.user.Perm.Execute || !d.Check(r.URL.Path) {
		return http.StatusForbidden, nil
	}
	info, err := d.user.Fs.Stat(r.URL.Path)
	if err != nil {
		return errToStatus(err), err
	}
	if !info.IsDir() {
		return http.StatusBadRequest, nil
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer conn.Close()
	conn.SetReadLimit(16 << 10)
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	_ = conn.SetWriteDeadline(time.Now().Add(WSWriteDeadline))

	var raw string

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			wsErr(conn, r, http.StatusInternalServerError, err)
			return 0, nil
		}

		raw = strings.TrimSpace(string(msg))
		if raw != "" {
			break
		}
	}

	// Interactive commands must execute the allowlisted binary directly.
	// Passing raw input to a configured shell permits additional commands.
	name, args, err := runner.SplitCommandAndArgs(raw)
	if err != nil {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(err.Error())); err != nil {
			wsErr(conn, r, http.StatusInternalServerError, err)
		}
		return 0, nil
	}

	if !slices.Contains(d.user.Commands, name) {
		if err := conn.WriteMessage(websocket.TextMessage, cmdNotAllowed); err != nil {
			wsErr(conn, r, http.StatusInternalServerError, err)
		}

		return 0, nil
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	_ = conn.SetReadDeadline(time.Time{})
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	cmd.Dir = d.user.FullPath(r.URL.Path)

	// os/exec copies both streams concurrently into the same writer. Reading
	// stdout to EOF before stderr can deadlock a child with a full stderr pipe.
	output, sink := io.Pipe()
	defer output.Close()
	cmd.Stdout, cmd.Stderr = sink, sink
	done := make(chan error, 1)
	go func() {
		err := cmd.Run()
		_ = sink.Close()
		done <- err
	}()
	s := bufio.NewScanner(output)
	s.Buffer(make([]byte, 4096), 1<<20)
	for s.Scan() {
		_ = conn.SetWriteDeadline(time.Now().Add(WSWriteDeadline))
		if err := conn.WriteMessage(websocket.TextMessage, s.Bytes()); err != nil {
			cancel()
			break
		}
	}
	if err := s.Err(); err != nil {
		cancel()
		wsErr(conn, r, http.StatusInternalServerError, err)
	}
	_ = output.Close()
	if err := <-done; err != nil {
		wsErr(conn, r, http.StatusInternalServerError, err)
	}

	return 0, nil
})
