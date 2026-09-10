package img

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestRejectExcessivePixelCountBeforeDecode(t *testing.T) {
	// A valid GIF header declaring 25 million pixels, with no pixel allocation.
	header := make([]byte, 13)
	copy(header, "GIF89a")
	binary.LittleEndian.PutUint16(header[6:8], 5000)
	binary.LittleEndian.PutUint16(header[8:10], 5000)
	_, _, err := New(1).detectFormat(bytes.NewReader(header))
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("err=%v", err)
	}
}
