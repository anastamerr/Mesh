package storage

import (
	"encoding/hex"
	"strings"
	"testing"
)

func FuzzValidID(f *testing.F) {
	for _, id := range []string{"", emptySHA256, strings.Repeat("a", 63), strings.Repeat("a", 65),
		strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("é", 32)} {
		f.Add(id)
	}
	f.Fuzz(func(t *testing.T, id string) {
		decoded, err := hex.DecodeString(id)
		want := err == nil && len(decoded) == 32 && strings.ToLower(id) == id
		if got := validID(id); got != want {
			t.Fatalf("validID(%q) = %v, want %v", id, got, want)
		}
	})
}
