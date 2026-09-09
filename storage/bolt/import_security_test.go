package bolt

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/asdine/storm/v3"
	"github.com/filebrowser/filebrowser/v2/users"
)

func TestAtomicImportsAndLastAdmin(t *testing.T) {
	db, err := storm.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := NewStorage(db)
	if err != nil {
		t.Fatal(err)
	}
	u := &users.User{Username: "admin", Password: "hash", Perm: users.Permissions{Admin: true}}
	if err := st.Users.Save(u); err != nil {
		t.Fatal(err)
	}
	importer := st.Users.(interface {
		Import([]*users.User, bool, bool) error
	})
	if err := importer.Import([]*users.User{{Username: "ordinary", Password: "hash"}}, true, false); err == nil {
		t.Fatal("replacement without admin succeeded")
	}
	if _, err := st.Users.Get("", false, "admin"); err != nil {
		t.Fatal("failed replacement removed original admin", err)
	}
	if err := importer.Import([]*users.User{{Username: "new", Password: "hash"}, {Username: "admin", Password: "duplicate"}}, false, false); err == nil {
		t.Fatal("duplicate import succeeded")
	}
	if _, err := st.Users.Get("", false, "new"); err == nil {
		t.Fatal("failed batch partially committed")
	}
	if err := importer.Import([]*users.User{{Username: "replacement", Password: "hash", Perm: users.Permissions{Admin: true}}}, true, false); err != nil {
		t.Fatal(err)
	}
	u, err = st.Users.Get("", false, "replacement")
	if err != nil {
		t.Fatal(err)
	}
	u.Perm.Admin = false
	if err := st.Users.Update(u, "Perm"); err == nil {
		t.Fatal("last admin demoted")
	}
	if err := st.Users.Save(u); err == nil {
		t.Fatal("last admin overwritten")
	}
	u.Perm.Admin = true
	u.Locale = "fr"
	if err := importer.Import([]*users.User{u}, false, true); err != nil {
		t.Fatal("existing-ID overwrite failed", err)
	}
	updated, err := st.Users.Get("", false, u.ID)
	if err != nil || updated.Locale != "fr" {
		t.Fatal("overwrite did not persist", err)
	}
	v := &users.User{Username: "second", Password: "hash", Perm: users.Permissions{Admin: true}}
	if err := st.Users.Save(v); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, id := range []uint{u.ID, v.ID} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = st.Users.Delete(id) }()
	}
	wg.Wait()
	count, err := (usersBackend{db: db}).CountAdmins()
	if err != nil || count != 1 {
		t.Fatalf("admins=%d err=%v", count, err)
	}
}
