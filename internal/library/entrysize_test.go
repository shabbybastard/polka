package library

import (
	"path/filepath"
	"testing"
)

func TestEntrySizeDoesNotReadData(t *testing.T) {
	root := t.TempDir()
	writeZip(t, filepath.Join(root, "arch.zip"), map[string][]byte{
		"book.fb2": []byte("hello"),
	})
	lib := New(root)
	size, err := lib.EntrySize("arch.zip", "book", "fb2")
	if err != nil {
		t.Fatal(err)
	}
	if size != 5 {
		t.Errorf("EntrySize = %d, want 5", size)
	}
	if _, err := lib.EntrySize("arch.zip", "missing", "fb2"); err == nil {
		t.Error("missing entry must return an error")
	}
}
