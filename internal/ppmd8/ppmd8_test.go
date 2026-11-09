package ppmd8

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestDecode(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.ppmd")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/sample.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(data, int64(len(want)))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded %q, want %q", got, want)
	}
}

func TestDecodeVariants(t *testing.T) {
	for _, name := range []string{"order6_restart", "order16_cutoff"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + name + ".ppmd")
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile("testdata/" + name + ".txt")
			if err != nil {
				t.Fatal(err)
			}
			got, err := Decode(data, int64(len(want)))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("decoded %d bytes, want %d", len(got), len(want))
			}
		})
	}
}

func TestDecodeConcurrent(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.ppmd")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/sample.txt")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 6
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			got, err := Decode(data, int64(len(want)))
			if err == nil && !bytes.Equal(got, want) {
				err = errors.New("wrong output")
			}
			errs <- err
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDecodeRejectsBadProps(t *testing.T) {
	// props 0xffff: order 16, 256 MiB, restore method 15 (unsupported).
	if _, err := Decode([]byte{0xff, 0xff, 0, 0, 0, 0}, 1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("bad props error = %v", err)
	}
	if _, err := Decode([]byte{0x0b}, 1); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("short props error = %v", err)
	}
}

func TestDecodeRejectsTooLarge(t *testing.T) {
	if _, err := Decode([]byte{0x0b, 0x10}, MaxOutputSize+1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large error = %v", err)
	}
}

func TestDecodeTruncated(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.ppmd")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 8 {
		t.Fatal("fixture too short")
	}
	if _, err := Decode(data[:8], 82); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated error = %v", err)
	}
}
