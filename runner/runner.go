package runner

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

// Runner is a commands runner.
type Runner struct {
	Enabled bool
	*settings.Settings
}

// RunHook runs the hooks for the before and after event.
func (r *Runner) RunHook(fn func() error, evt, path, dst string, user *users.User) error {
	if err := r.RunBeforeHook(evt, path, dst, user); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	return r.RunAfterHook(evt, path, dst, user)
}

func (r *Runner) RunBeforeHook(evt, path, dst string, user *users.User) error {
	return r.runEvent("before_"+evt, path, dst, user)
}

func (r *Runner) RunAfterHook(evt, path, dst string, user *users.User) error {
	return r.runEvent("after_"+evt, path, dst, user)
}

func (r *Runner) runEvent(evt, path, dst string, user *users.User) error {
	if !r.Enabled {
		return nil
	}
	for _, command := range r.Commands[evt] {
		if err := r.exec(command, evt, user.FullPath(path), user.FullPath(dst), user); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) exec(raw, evt, path, dst string, user *users.User) error {
	blocking := true

	if strings.HasSuffix(raw, "&") {
		blocking = false
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "&"))
	}

	command, _, err := ParseCommand(r.Settings, raw)
	if err != nil {
		return err
	}

	envMapping := func(key string) string {
		switch key {
		case "FILE":
			return path
		case "SCOPE":
			return user.Scope
		case "TRIGGER":
			return evt
		case "USERNAME":
			return user.Username
		case "DESTINATION":
			return dst
		default:
			return os.Getenv(key)
		}
	}
	for i, arg := range command {
		// Shells expand the environment themselves. Substituting into shell
		// source here would turn user-controlled filenames into executable code.
		if i == 0 || (len(r.Shell) > 0 && r.Shell[0] != "") {
			continue
		}

		command[i] = os.Expand(arg, envMapping)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), fmt.Sprintf("FILE=%s", path))
	cmd.Env = append(cmd.Env, fmt.Sprintf("SCOPE=%s", user.Scope))
	cmd.Env = append(cmd.Env, fmt.Sprintf("TRIGGER=%s", evt))
	cmd.Env = append(cmd.Env, fmt.Sprintf("USERNAME=%s", user.Username))
	cmd.Env = append(cmd.Env, fmt.Sprintf("DESTINATION=%s", dst))

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if !blocking {
		log.Printf("[INFO] Nonblocking Command: \"%s\"", strings.Join(command, " "))
		if err := cmd.Start(); err != nil {
			cancel()
			return err
		}
		go func() {
			defer cancel()
			if err := cmd.Wait(); err != nil {
				log.Printf("[INFO] Nonblocking Command \"%s\" failed: %s", strings.Join(command, " "), err)
			}
		}()
		return nil
	}

	defer cancel()
	log.Printf("[INFO] Blocking Command: \"%s\"", strings.Join(command, " "))
	return cmd.Run()
}
