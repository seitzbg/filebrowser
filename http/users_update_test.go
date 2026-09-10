package fbhttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"

	"github.com/filebrowser/filebrowser/v2/auth"
	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

func TestUserUpdateAuthorization(t *testing.T) {
	const password = "current-password"
	hash, err := users.HashPwd(password)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		which    []string
		password string
		admin    bool
		locked   bool
		badPerm  bool
		status   int
	}{
		{name: "omitted fields require password", admin: true, status: 400},
		{name: "empty fields require password", which: []string{}, admin: true, status: 400},
		{name: "all requires password", which: []string{"ALL"}, admin: true, status: 400},
		{name: "camel case lock requires password", which: []string{"lockPassword"}, admin: true, status: 400},
		{name: "uppercase lock requires password", which: []string{"LOCKPASSWORD"}, admin: true, status: 400},
		{name: "rules require password", which: []string{"rules"}, admin: true, status: 400},
		{name: "wrong password", which: []string{"password"}, password: "wrong", admin: true, status: 400},
		{name: "full update", password: password, admin: true, status: 200},
		{name: "case insensitive full update", which: []string{"ALL"}, password: password, admin: true, status: 200},
		{name: "lock update", which: []string{"LOCKPASSWORD"}, password: password, admin: true, status: 200},
		{name: "full update validates permissions", password: password, admin: true, badPerm: true, status: 400},
		{name: "nonadmin full update", password: password, status: 403},
		{name: "nonadmin cannot unlock", which: []string{"LOCKPASSWORD"}, password: password, locked: true, status: 403},
		{name: "locked password cannot change", which: []string{"PASSWORD"}, password: password, locked: true, status: 403},
		{name: "duplicate password hashed once", which: []string{"password", "PASSWORD"}, password: password, status: 200},
		{name: "preferences need no password", which: []string{"locale", "dateFormat"}, status: 200},
		{name: "internal fields rejected", which: []string{"Fs"}, password: password, admin: true, status: 400},
		{name: "id rejected", which: []string{"ID"}, password: password, admin: true, status: 400},
		{name: "mixed all rejected", which: []string{"all", "locale"}, password: password, admin: true, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := []byte("test-key")
			perm := users.Permissions{Admin: tc.admin, Download: true}
			st := scopedUserStorage(t, t.TempDir(), perm, key)
			original, err := st.Users.Get("", false, uint(1))
			if err != nil {
				t.Fatal(err)
			}
			original.Password = hash
			original.LockPassword = tc.locked
			if err := st.Users.Update(original); err != nil {
				t.Fatal(err)
			}
			if err := st.Settings.Save(&settings.Settings{Key: key, AuthMethod: auth.MethodJSONAuth, MinimumPasswordLength: 1}); err != nil {
				t.Fatal(err)
			}
			updated := *original
			updated.Password = "replacement-password"
			updated.LockPassword = !original.LockPassword
			updated.Locale = "fr"
			updated.DateFormat = true
			if tc.badPerm {
				updated.Perm.Share, updated.Perm.Download = true, false
			}
			body, err := json.Marshal(map[string]interface{}{
				"what": "user", "which": tc.which, "data": &updated, "current_password": tc.password,
			})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPut, "/1", bytes.NewReader(body))
			req = mux.SetURLVars(req, map[string]string{"id": "1"})
			req.Header.Set("X-Auth", signToken(t, perm, key))
			rec := httptest.NewRecorder()
			handle(userPutHandler, "", st, &settings.Server{}).ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			stored, err := st.Users.Get("", false, uint(1))
			if err != nil {
				t.Fatal(err)
			}
			if tc.status != 200 {
				if stored.Password != hash || stored.LockPassword != original.LockPassword || stored.Locale != original.Locale {
					t.Fatal("rejected request changed stored user")
				}
			} else if tc.name == "duplicate password hashed once" && !users.CheckPwd("replacement-password", stored.Password) {
				t.Fatal("duplicate fields corrupted password")
			}
		})
	}
}
