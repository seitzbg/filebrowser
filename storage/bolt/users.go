package bolt

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"

	"github.com/asdine/storm/v3"
	"github.com/asdine/storm/v3/q"
	bolt "go.etcd.io/bbolt"

	fberrors "github.com/filebrowser/filebrowser/v2/errors"
	"github.com/filebrowser/filebrowser/v2/users"
)

type usersBackend struct {
	db *storm.DB
}

func (st usersBackend) GetBy(i interface{}) (user *users.User, err error) {
	user = &users.User{}

	var arg string
	switch i.(type) {
	case uint:
		arg = "ID"
	case string:
		arg = "Username"
	default:
		return nil, fberrors.ErrInvalidDataType
	}

	err = st.db.One(arg, i, user)

	if err != nil {
		if errors.Is(err, storm.ErrNotFound) {
			return nil, fberrors.ErrNotExist
		}
		return nil, err
	}

	return
}

func (st usersBackend) GetByScope(scope string) (*users.User, error) {
	user := &users.User{}
	// Match case-insensitively: on a case-insensitive filesystem two scopes
	// that differ only in case (e.g. /users/Alice and /users/alice) resolve to
	// the same home directory, so they must be treated as a collision.
	pattern := "(?i)^" + regexp.QuoteMeta(scope) + "$"
	err := st.db.Select(q.Re("Scope", pattern)).First(user)
	if err != nil {
		if errors.Is(err, storm.ErrNotFound) {
			return nil, fberrors.ErrNotExist
		}
		return nil, err
	}

	return user, nil
}

func (st usersBackend) Gets() ([]*users.User, error) {
	var allUsers []*users.User
	err := st.db.All(&allUsers)
	if errors.Is(err, storm.ErrNotFound) {
		return nil, fberrors.ErrNotExist
	}

	if err != nil {
		return allUsers, err
	}

	return allUsers, err
}

func (st usersBackend) Update(user *users.User, fields ...string) error {
	if len(fields) == 0 {
		return st.Save(user)
	}

	tx, err := st.db.Begin(true)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, field := range fields {
		userField := reflect.ValueOf(user).Elem().FieldByName(field)
		if !userField.IsValid() {
			return fmt.Errorf("invalid field: %s", field)
		}
		val := userField.Interface()
		if err := tx.UpdateField(user, field, val); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (st usersBackend) Import(list []*users.User, replace, overwrite bool) error {
	tx, err := st.db.Begin(true)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var existing []*users.User
	if err := tx.All(&existing); err != nil && !errors.Is(err, storm.ErrNotFound) {
		return err
	}
	if replace {
		for _, user := range existing {
			if err := tx.DeleteStruct(user); err != nil {
				return err
			}
		}
	}
	for _, user := range list {
		var old users.User
		err := tx.One("ID", user.ID, &old)
		if err == nil && !overwrite {
			return fmt.Errorf("user %d is already registered", user.ID)
		}
		if err != nil && !errors.Is(err, storm.ErrNotFound) {
			return err
		}
		if !replace && errors.Is(err, storm.ErrNotFound) {
			user.ID = 0
		}
		if err := tx.Save(user); err != nil {
			return err
		}
	}
	var result []*users.User
	if err := tx.All(&result); err != nil && !errors.Is(err, storm.ErrNotFound) {
		return err
	}
	for _, user := range result {
		if user.Perm.Admin {
			return tx.Commit()
		}
	}
	return fberrors.ErrRootUserDeletion
}

func (st usersBackend) Save(user *users.User) error {
	err := st.db.Save(user)
	if errors.Is(err, storm.ErrAlreadyExists) {
		return fberrors.ErrExist
	}
	return err
}

func (st usersBackend) DeleteByID(id uint) error {
	return st.db.DeleteStruct(&users.User{ID: id})
}

func (st usersBackend) DeleteByUsername(username string) error {
	user, err := st.GetBy(username)
	if err != nil {
		return err
	}

	return st.db.DeleteStruct(user)
}

func (st usersBackend) CountAdmins() (int, error) {
	count := 0

	err := st.db.Bolt.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(reflect.TypeOf(users.User{}).Name()))
		if bucket == nil {
			return nil
		}

		c := bucket.Cursor()
		for _, v := c.First(); v != nil; _, v = c.Next() {
			var u users.User
			if err := st.db.Codec().Unmarshal(v, &u); err != nil {
				return err
			}
			if u.Perm.Admin {
				count++
			}
		}

		return nil
	})

	return count, err
}
