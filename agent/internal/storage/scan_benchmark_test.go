package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

var benchmarkScanManifest Manifest

func BenchmarkScan(b *testing.B) {
	const (
		fileCount = 500
		fileSize  = 4 * 1024
	)

	dir := b.TempDir()
	data := bytes.Repeat([]byte("mesh"), fileSize/4)
	for i := 0; i < fileCount; i++ {
		name := filepath.Join(dir, fmt.Sprintf("%03d.bin", i))
		if err := os.WriteFile(name, data, 0600); err != nil {
			b.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer root.Close()

	ctx := context.Background()
	b.ReportAllocs()
	b.SetBytes(fileCount * fileSize)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkScanManifest, err = Scan(ctx, root)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()

	if len(benchmarkScanManifest.Entries) != fileCount {
		b.Fatalf("entries = %d, want %d", len(benchmarkScanManifest.Entries), fileCount)
	}
	wantHash := sha256.Sum256(data)
	wantHashString := hex.EncodeToString(wantHash[:])
	for _, entry := range benchmarkScanManifest.Entries {
		if entry.Size != fileSize || entry.SHA256 != wantHashString {
			b.Fatalf("entry %q = size %d hash %q", entry.Path, entry.Size, entry.SHA256)
		}
	}
}
