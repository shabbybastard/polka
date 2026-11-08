package store

import "testing"

func TestMapBookIDs(t *testing.T) {
	oldKeys := map[int64]string{
		1: "lib:a",
		2: "lib:b",
		3: "file:arch/f3.fb2",
		4: "lib:gone",
	}
	newKeys := map[int64]string{
		10: "lib:b",
		11: "lib:a",
		12: "file:arch/f3.fb2",
	}
	got := MapBookIDs(oldKeys, newKeys)
	want := map[int64]int64{1: 11, 2: 10, 3: 12, 4: 0}
	for oldID, newID := range want {
		if got[oldID] != newID {
			t.Errorf("mapping[%d] = %d, want %d", oldID, got[oldID], newID)
		}
	}

	// Duplicate keys are ambiguous and must not attach data to a random copy.
	dup := MapBookIDs(
		map[int64]string{1: "lib:a", 2: "lib:a"},
		map[int64]string{3: "lib:a", 4: "lib:b"},
	)
	if dup[1] != 0 || dup[2] != 0 {
		t.Errorf("ambiguous duplicate mapping = %v, want zeros", dup)
	}
}
