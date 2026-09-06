//go:build !windows

package state

// Unix state is plaintext protected by a private directory and file permissions.
// Keychain integration and encryption at rest are separate future capabilities.
func protect(data []byte) ([]byte, error)   { return data, nil }
func unprotect(data []byte) ([]byte, error) { return data, nil }
