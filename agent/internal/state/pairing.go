package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// PendingPairing is saved before contacting the controller so a lost response
// cannot strand an approved node with an unknown credential.
type PendingPairing struct {
	ID           string `json:"id"`
	Server       string `json:"server"`
	Name         string `json:"name"`
	AgentVersion string `json:"agentVersion,omitempty"`
	Secret       string `json:"secret"`
	Credential   string `json:"credential"`
}

func (s *Store) LoadPairing() (PendingPairing, error) {
	var value PendingPairing
	name := filepath.Join(s.dir, "pairing.json")
	info, err := os.Lstat(name)
	if err != nil {
		return value, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxStateBytes || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return value, errors.New("invalid pairing state file")
	}
	data, err := os.ReadFile(name)
	if err == nil {
		data, err = unprotect(data)
	}
	if err == nil {
		err = json.Unmarshal(data, &value)
	}
	if err != nil || !nodeID.MatchString(value.ID) || !credential.MatchString(value.Credential) || len(value.Secret) != 53 || value.Server == "" || value.Name == "" {
		return value, errors.New("pairing state is corrupt; it was not reset")
	}
	return value, nil
}

func (s *Store) SavePairing(value PendingPairing) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.saveProtected("pairing.json", data)
}

func (s *Store) ClearPairing() error {
	err := os.Remove(filepath.Join(s.dir, "pairing.json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
