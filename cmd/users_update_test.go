package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asdine/storm/v3"
	"github.com/spf13/cobra"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/storage/bolt"
	"github.com/filebrowser/filebrowser/v2/users"
)

func TestUsersUpdatePreservesUnsetFlags(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "users.db")
	db, err := storm.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := bolt.NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Settings.Save(&settings.Settings{Key: []byte("test-key")}); err != nil {
		t.Fatal(err)
	}
	if err := st.Users.Save(&users.User{Username: "user", Password: "hash", LockPassword: true,
		DateFormat: true, HideDotfiles: true, AceEditorTheme: "monokai", SingleClick: true,
		Perm: users.Permissions{Admin: true, Download: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		command := &cobra.Command{Use: "update"}
		flags := command.Flags()
		flags.String("config", "", "config")
		flags.String("database", "", "database")
		flags.String("username", "", "username")
		flags.String("password", "", "password")
		addUserFlags(flags)
		args := []string{"--config", configPath, "--database", dbPath, "--locale=fr"}
		if explicit {
			args = append(args, "--lockPassword=false", "--dateFormat=false", "--hideDotfiles=false", "--aceEditorTheme=github")
		}
		if err := flags.Parse(args); err != nil {
			t.Fatal(err)
		}
		if err := usersUpdateCmd.RunE(command, []string{"user"}); err != nil {
			t.Fatal(err)
		}
		db, err := storm.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		var stored users.User
		err = db.One("Username", "user", &stored)
		closeErr := db.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("reading user: %v, %v", err, closeErr)
		}
		if stored.LockPassword != !explicit || stored.DateFormat != !explicit || stored.HideDotfiles != !explicit {
			t.Fatalf("explicit=%v flags changed incorrectly: %+v", explicit, stored)
		}
		theme := "monokai"
		if explicit {
			theme = "github"
		}
		if stored.AceEditorTheme != theme || stored.Locale != "fr" || !stored.Perm.Admin || !stored.SingleClick {
			t.Fatalf("explicit=%v preferences changed incorrectly: %+v", explicit, stored)
		}
	}
}
