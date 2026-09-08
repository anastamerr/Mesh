package storage

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInformation struct {
	ReplaceIfExists byte
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [windows.MAX_PATH]uint16
}

// NtSetInformationFile with ReplaceIfExists unset fails atomically when the
// destination already exists. Both handles are rooted in the os.Root, so a
// concurrent rename of the parent path cannot redirect publication.
func publishDownload(parent *os.Root, source, destination string) error {
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	conn, err := dir.SyscallConn()
	if err != nil {
		return err
	}
	var sourceHandle windows.Handle
	var parentHandle windows.Handle
	var openErr error
	if err = conn.Control(func(raw uintptr) {
		parentHandle = windows.Handle(raw)
		sourceHandle, openErr = openRenameSource(parentHandle, source)
	}); err != nil {
		return err
	}
	if openErr != nil {
		return openErr
	}
	defer windows.CloseHandle(sourceHandle)
	name, err := windows.UTF16FromString(destination)
	if err != nil {
		return err
	}
	fileNameLength := len(name)*2 - 2
	info := &fileRenameInformation{RootDirectory: parentHandle}
	if fileNameLength < 0 || fileNameLength/2 > len(info.FileName) {
		return ErrInvalid
	}
	info.FileNameLength = uint32(fileNameLength)
	copy(info.FileName[:], name)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(sourceHandle, &status, (*byte)(unsafe.Pointer(info)), uint32(unsafe.Offsetof(info.FileName)+uintptr(fileNameLength)), windows.FileRenameInformation)
}

func openRenameSource(parent windows.Handle, name string) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: objectName}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.DELETE|windows.SYNCHRONIZE, attributes, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_DIRECTORY_FILE, 0, 0)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return handle, nil
}
