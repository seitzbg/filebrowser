package share

import (
	"testing"
	"time"
)

type expiryBackend struct {
	StorageBackend
	deleted []string
}

func (b *expiryBackend) Delete(hash string) error { b.deleted = append(b.deleted, hash); return nil }

func TestRemoveAdjacentExpiredLinks(t *testing.T) {
	b := &expiryBackend{}
	s := NewStorage(b)
	past := time.Now().Add(-time.Hour).Unix()
	links, err := s.removeExpired([]*Link{{Hash: "a", Expire: past}, {Hash: "b", Expire: past}, {Hash: "keep"}, {Hash: "c", Expire: past}})
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Hash != "keep" || len(b.deleted) != 3 {
		t.Fatalf("links=%v deleted=%v", links, b.deleted)
	}
}
