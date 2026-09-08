package storage

import (
	"golang.org/x/sys/unix"
	"os"
)

func publishDownload(parent *os.Root, source, destination string) error {
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return unix.Renameat2(int(dir.Fd()), source, int(dir.Fd()), destination, unix.RENAME_NOREPLACE)
}
