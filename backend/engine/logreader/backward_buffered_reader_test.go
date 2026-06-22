package logreader

import (
	"io"
	"testing"
)

func TestBackwardBufferedReaderCachesReverseWindow(t *testing.T) {
	src := &countingReaderAt{data: []byte("0123456789abcdef")}
	r := NewBackwardBufferedReader(src)
	r.buf = make([]byte, 8)

	got := make([]byte, 4)
	if err := r.ReadAt(got, 12); err != nil {
		t.Fatal(err)
	}
	if string(got) != "cdef" {
		t.Fatalf("first read = %q, want %q", string(got), "cdef")
	}
	if src.reads != 1 {
		t.Fatalf("reads after first read = %d, want 1", src.reads)
	}

	if err := r.ReadAt(got, 8); err != nil {
		t.Fatal(err)
	}
	if string(got) != "89ab" {
		t.Fatalf("second read = %q, want %q", string(got), "89ab")
	}
	if src.reads != 1 {
		t.Fatalf("reads after cached read = %d, want 1", src.reads)
	}

	if err := r.ReadAt(got, 4); err != nil {
		t.Fatal(err)
	}
	if string(got) != "4567" {
		t.Fatalf("third read = %q, want %q", string(got), "4567")
	}
	if src.reads != 2 {
		t.Fatalf("reads after refill = %d, want 2", src.reads)
	}
}

func TestBackwardBufferedReaderLargeReadBypassesBuffer(t *testing.T) {
	src := &countingReaderAt{data: []byte("0123456789abcdef")}
	r := NewBackwardBufferedReader(src)
	r.buf = make([]byte, 4)

	got := make([]byte, 6)
	if err := r.ReadAt(got, 5); err != nil {
		t.Fatal(err)
	}
	if string(got) != "56789a" {
		t.Fatalf("large read = %q, want %q", string(got), "56789a")
	}
	if src.reads != 1 {
		t.Fatalf("reads = %d, want 1", src.reads)
	}
	if r.bufFrom != 0 || r.bufTo != 0 {
		t.Fatalf("large read updated buffer window to [%d,%d), want empty", r.bufFrom, r.bufTo)
	}
}

type countingReaderAt struct {
	data  []byte
	reads int
}

func (r *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	if off < 0 || off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
