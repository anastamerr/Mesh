package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScanProgressReportsEachCompletedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a-empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b-data"), []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	var completed []TransferEvent
	_, err = scan(context.Background(), root, func(event TransferEvent) {
		if len(completed) == 0 || event.Files > completed[len(completed)-1].Files {
			completed = append(completed, event)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 2 {
		t.Fatalf("completed file events = %d, want 2", len(completed))
	}
	if completed[0].Files != 1 || completed[0].Completed != 0 {
		t.Fatalf("first completion = %+v, want one empty file and zero bytes", completed[0])
	}
	if completed[1].Files != 2 || completed[1].Completed != 3 {
		t.Fatalf("final completion = %+v, want two files and three bytes", completed[1])
	}
}
