package storage

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Compare the same full scan/upload/publication/download path on both revisions.
// Setup is excluded; transfers retain checksums, file syncs and SQLite commits.
func BenchmarkLargeFileTransfer(b *testing.B) {
	const size = 32 << 20
	source := b.TempDir()
	if err := os.WriteFile(filepath.Join(source, "large.bin"), bytes.Repeat([]byte("mesh"), size/4), 0600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(size * 2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s, err := Open(b.TempDir())
		if err != nil {
			b.Fatal(err)
		}
		server := httptest.NewServer(Handler(s, testKey))
		client, err := NewClient(server.URL, testKey)
		if err != nil {
			b.Fatal(err)
		}
		destination := filepath.Join(b.TempDir(), "copy")
		b.StartTimer()
		id, err := client.Upload(testContext, source)
		if err == nil {
			err = client.Download(testContext, id, destination)
		}
		b.StopTimer()
		server.Close()
		closeErr := s.Close()
		if err != nil || closeErr != nil {
			b.Fatalf("transfer: %v; close: %v", err, closeErr)
		}
	}
}

// A small tail batch must not inspect every other file's durable offset.
// Collection registration and fixture setup are outside the timed region.
func BenchmarkBatchJournalScaling(b *testing.B) {
	for _, count := range []int{128, MaxEntries} {
		b.Run(fmt.Sprintf("entries=%d", count), func(b *testing.B) {
			m := Manifest{Version: 1, Entries: make([]Entry, count)}
			for i := range m.Entries {
				m.Entries[i] = entry(fmt.Sprintf("%05d", i), []byte("x"))
			}
			indices := []int{count - 2, count - 1}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				s, err := Open(b.TempDir())
				if err != nil {
					b.Fatal(err)
				}
				p, err := s.Begin(testContext, m)
				if err != nil {
					b.Fatal(err)
				}
				// Both revisions start with their immutable manifest cache warm.
				if _, _, err := s.manifest(testContext, p.ID, false); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				_, err = s.writeBatch(testContext, p.ID, m, indices, []byte("xx"))
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
				got, err := s.Begin(testContext, m)
				if err != nil || got.Offsets[count-2] != 1 || got.Offsets[count-1] != 1 || got.Offsets[0] != 0 {
					b.Fatalf("durable offsets incorrect: %v", err)
				}
				if err := s.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
