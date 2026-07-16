package session

import (
	"bytes"
	"testing"
)

func TestRingBuffer(t *testing.T) {
	tests := []struct {
		name   string
		max    int
		writes [][]byte
		want   []byte
	}{
		{
			name:   "under capacity",
			max:    16,
			writes: [][]byte{[]byte("abc"), []byte("def")},
			want:   []byte("abcdef"),
		},
		{
			name:   "exact capacity",
			max:    6,
			writes: [][]byte{[]byte("abc"), []byte("def")},
			want:   []byte("abcdef"),
		},
		{
			name:   "overflow drops oldest",
			max:    4,
			writes: [][]byte{[]byte("abc"), []byte("def")},
			want:   []byte("cdef"),
		},
		{
			name:   "single write larger than capacity keeps tail",
			max:    4,
			writes: [][]byte{[]byte("abcdefgh")},
			want:   []byte("efgh"),
		},
		{
			name:   "boundary: fill then one more",
			max:    3,
			writes: [][]byte{[]byte("abc"), []byte("d")},
			want:   []byte("bcd"),
		},
		{
			name:   "many small writes",
			max:    3,
			writes: [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d"), []byte("e")},
			want:   []byte("cde"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rb := NewRingBuffer(tc.max)
			for _, w := range tc.writes {
				n, err := rb.Write(w)
				if err != nil {
					t.Fatalf("Write(%q) error: %v", w, err)
				}
				if n != len(w) {
					t.Fatalf("Write(%q) = %d, want %d", w, n, len(w))
				}
			}
			if got := rb.Snapshot(); !bytes.Equal(got, tc.want) {
				t.Fatalf("Snapshot() = %q, want %q", got, tc.want)
			}
			if rb.Len() != len(tc.want) {
				t.Fatalf("Len() = %d, want %d", rb.Len(), len(tc.want))
			}
		})
	}
}

func TestRingBufferSnapshotIsCopy(t *testing.T) {
	rb := NewRingBuffer(8)
	rb.Write([]byte("hello"))
	snap := rb.Snapshot()
	snap[0] = 'H'
	if got := rb.Snapshot(); !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("mutating a snapshot leaked into the buffer: %q", got)
	}
}
