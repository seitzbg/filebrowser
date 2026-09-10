package users

import (
	"errors"
	"sync"
	"time"

	fberrors "github.com/filebrowser/filebrowser/v2/errors"
)

// StorageBackend is the interface to implement for a users storage.
type StorageBackend interface {
	GetBy(interface{}) (*User, error)
	GetByScope(scope string) (*User, error)
	Gets() ([]*User, error)
	Save(u *User) error
	Update(u *User, fields ...string) error
	DeleteByID(uint) error
	DeleteByUsername(string) error
	CountAdmins() (int, error)
}

type Store interface {
	Get(baseScope string, followExternalSymlinks bool, id interface{}) (user *User, err error)
	GetByScope(scope string) (*User, error)
	Gets(baseScope string, followExternalSymlinks bool) ([]*User, error)
	Update(user *User, fields ...string) error
	Save(user *User) error
	SaveProvisioned(user *User, derivedScope bool) error
	Delete(id interface{}) error
	LastUpdate(id uint) int64
}

// Storage is a users storage.
type Storage struct {
	back    StorageBackend
	updated map[uint]int64
	mux     sync.RWMutex

	// provision serializes the scope-collision check and the save of newly
	// provisioned users, which must not interleave. See SaveProvisioned.
	provision sync.Mutex
	mutation  sync.Mutex
}

// NewStorage creates a users storage from a backend.
func NewStorage(back StorageBackend) *Storage {
	return &Storage{
		back:    back,
		updated: map[uint]int64{},
	}
}

// Get allows you to get a user by its name or username. The provided
// id must be a string for username lookup or a uint for id lookup. If id
// is neither, a ErrInvalidDataType will be returned.
func (s *Storage) Get(baseScope string, followExternalSymlinks bool, id interface{}) (user *User, err error) {
	user, err = s.back.GetBy(id)
	if err != nil {
		return
	}
	if err := user.Clean(baseScope, followExternalSymlinks); err != nil {
		return nil, err
	}
	return
}

// GetByScope returns the first user whose scope matches the given one, or
// ErrNotExist if none does. The user is returned as stored, without setting up
// its filesystem, as it is meant for existence checks rather than serving.
func (s *Storage) GetByScope(scope string) (*User, error) {
	return s.back.GetByScope(scope)
}

// Gets gets a list of all users.
func (s *Storage) Gets(baseScope string, followExternalSymlinks bool) ([]*User, error) {
	users, err := s.back.Gets()
	if err != nil {
		return nil, err
	}

	for _, user := range users {
		if err := user.Clean(baseScope, followExternalSymlinks); err != nil {
			return nil, err
		}
	}

	return users, err
}

// Update updates a user in the database.
func (s *Storage) Update(user *User, fields ...string) error {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	if err := s.checkAdminChange(user, fields...); err != nil {
		return err
	}
	err := user.Clean("", false, fields...)
	if err != nil {
		return err
	}

	err = s.back.Update(user, fields...)
	if err != nil {
		return err
	}

	s.mux.Lock()
	s.updated[user.ID] = time.Now().Unix()
	s.mux.Unlock()
	return nil
}

// Save saves the user in a storage.
func (s *Storage) Save(user *User) error {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	if err := s.checkAdminChange(user); err != nil {
		return err
	}
	if err := user.Clean("", false); err != nil {
		return err
	}

	return s.back.Save(user)
}

// SaveProvisioned saves a user that is being provisioned (via signup, proxy
// auth or hook auth). When its scope was derived from the username, it first
// rejects the save if another user already owns that scope, so that distinct
// usernames cannot silently share one home directory.
//
// The check and the save are held under a single lock. Performing them as two
// independent operations lets two concurrent provisioning requests both observe
// a free scope and both save, leaving two users sharing one home directory.
func (s *Storage) SaveProvisioned(user *User, derivedScope bool) error {
	if !derivedScope {
		return s.Save(user)
	}

	s.provision.Lock()
	defer s.provision.Unlock()

	switch _, err := s.back.GetByScope(user.Scope); {
	case err == nil:
		return fberrors.ErrExist
	case !errors.Is(err, fberrors.ErrNotExist):
		return err
	}

	return s.Save(user)
}

// Delete allows you to delete a user by its name or username. The provided
// id must be a string for username lookup or a uint for id lookup. If id
// is neither, a ErrInvalidDataType will be returned.
func (s *Storage) Delete(id interface{}) error {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	switch id := id.(type) {
	case string:
		user, err := s.back.GetBy(id)
		if err != nil {
			return err
		}
		if s.IsUniqueAdmin(user) {
			return fberrors.ErrRootUserDeletion
		}

		return s.back.DeleteByUsername(id)
	case uint:
		user, err := s.back.GetBy(id)
		if err != nil {
			return err
		}
		if s.IsUniqueAdmin(user) {
			return fberrors.ErrRootUserDeletion
		}

		return s.back.DeleteByID(id)
	default:
		return fberrors.ErrInvalidDataType
	}
}

func (s *Storage) checkAdminChange(user *User, fields ...string) error {
	if user == nil {
		return fberrors.ErrInvalidDataType
	}
	changesPerm := len(fields) == 0
	for _, field := range fields {
		changesPerm = changesPerm || field == "Perm"
	}
	if !changesPerm || user.ID == 0 || user.Perm.Admin {
		return nil
	}
	previous, err := s.back.GetBy(user.ID)
	if errors.Is(err, fberrors.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.IsUniqueAdmin(previous) {
		return fberrors.ErrRootUserDeletion
	}
	return nil
}

// Import atomically validates and imports a batch of users.
func (s *Storage) Import(list []*User, replace, overwrite bool) error {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	for _, user := range list {
		if user == nil {
			return fberrors.ErrInvalidDataType
		}
		if err := user.Clean("", false); err != nil {
			return err
		}
	}
	backend, ok := s.back.(interface {
		Import([]*User, bool, bool) error
	})
	if !ok {
		return errors.New("storage backend does not support atomic imports")
	}
	return backend.Import(list, replace, overwrite)
}

// LastUpdate gets the timestamp for the last update of an user.
func (s *Storage) LastUpdate(id uint) int64 {
	s.mux.RLock()
	defer s.mux.RUnlock()
	if val, ok := s.updated[id]; ok {
		return val
	}
	return 0
}

func (s *Storage) IsUniqueAdmin(user *User) bool {
	if !user.Perm.Admin {
		return false
	}

	count, err := s.back.CountAdmins()
	if err != nil {
		return true
	}
	return count <= 1
}
