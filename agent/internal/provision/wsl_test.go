package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeWSL struct {
	commands       [][]string
	distributions  string
	doctorFailures int
	importFailure  bool
}

func (fake *fakeWSL) run(_ context.Context, _ string, arguments ...string) ([]byte, error) {
	fake.commands = append(fake.commands, append([]string(nil), arguments...))
	joined := strings.Join(arguments, " ")
	switch {
	case joined == "--status":
		return []byte("ready"), nil
	case joined == "--list --quiet":
		return []byte(fake.distributions), nil
	case strings.HasPrefix(joined, "--import "):
		if fake.importFailure {
			return nil, errors.New("import failed")
		}
		fake.distributions = "Mesh\r\n"
		return nil, nil
	case strings.HasPrefix(joined, "--unregister "):
		fake.distributions = ""
		return nil, nil
	case strings.Contains(joined, "mesh-executor doctor"):
		if fake.doctorFailures > 0 {
			fake.doctorFailures--
			return nil, errors.New("not ready")
		}
		return []byte(`{"architecture":"amd64","runtime":"docker","version":"28.1.0"}`), nil
	default:
		return nil, nil
	}
}

func setupFixture(t *testing.T, fake *fakeWSL) (*Manager, SetupOptions) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "mesh-rootfs.tar")
	data := []byte("verified-rootfs")
	if err := os.WriteFile(bundle, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	manager := &Manager{Program: "wsl.exe", Platform: "windows", Attempts: 2, RetryDelay: time.Nanosecond,
		Command: fake.run, Wait: func(context.Context, time.Duration) error { return nil }}
	return manager, SetupOptions{Distribution: "Mesh", Bundle: bundle,
		BundleSHA256: hex.EncodeToString(digest[:]), InstallRoot: filepath.Join(t.TempDir(), "Mesh")}
}

func TestSetupVerifiesImportsAndWaitsForReadiness(t *testing.T) {
	fake := &fakeWSL{doctorFailures: 1}
	manager, options := setupFixture(t, fake)
	status, err := manager.Setup(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "ready" || status.Architecture != "amd64" || status.InstallRoot != options.InstallRoot {
		t.Fatalf("wrong setup status: %#v", status)
	}
	var imported bool
	for _, command := range fake.commands {
		if strings.Join(command, " ") == "--import Mesh "+options.InstallRoot+" "+options.Bundle+" --version 2" {
			imported = true
		}
	}
	if !imported {
		t.Fatalf("dedicated distribution was not imported: %v", fake.commands)
	}
}

func TestSetupRejectsTamperedBundleBeforeImport(t *testing.T) {
	fake := &fakeWSL{}
	manager, options := setupFixture(t, fake)
	options.BundleSHA256 = strings.Repeat("0", 64)
	if _, err := manager.Setup(context.Background(), options); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal("tampered bundle was accepted")
	}
	for _, command := range fake.commands {
		if len(command) > 0 && command[0] == "--import" {
			t.Fatal("tampered bundle reached WSL import")
		}
	}
}

func TestSetupRollsBackOnlyNewUnreadyDistribution(t *testing.T) {
	fake := &fakeWSL{doctorFailures: 3}
	manager, options := setupFixture(t, fake)
	if _, err := manager.Setup(context.Background(), options); err == nil {
		t.Fatal("unready environment reported success")
	}
	if _, err := os.Stat(options.InstallRoot); !os.IsNotExist(err) {
		t.Fatal("new partial install root was retained")
	}
	if strings.Join(fake.commands[len(fake.commands)-1], " ") != "--unregister Mesh" {
		t.Fatalf("new partial distribution was not rolled back: %v", fake.commands)
	}
}

func TestSetupImportFailureDoesNotUnregisterAnotherDistribution(t *testing.T) {
	fake := &fakeWSL{importFailure: true}
	manager, options := setupFixture(t, fake)
	if _, err := manager.Setup(context.Background(), options); err == nil {
		t.Fatal("failed import reported success")
	}
	if _, err := os.Stat(options.InstallRoot); !os.IsNotExist(err) {
		t.Fatal("unused partial install root was retained")
	}
	for _, command := range fake.commands {
		if len(command) > 0 && command[0] == "--unregister" {
			t.Fatal("an uncertain failed import unregistered a distribution")
		}
	}
}

func TestDoctorParsesNullSeparatedDistributionList(t *testing.T) {
	fake := &fakeWSL{distributions: "M\x00e\x00s\x00h\x00\r\x00\n\x00"}
	manager := &Manager{Program: "wsl.exe", Platform: "windows", Command: fake.run}
	status, err := manager.Doctor(context.Background(), "Mesh")
	if err != nil || status.State != "ready" {
		t.Fatalf("valid imported distribution was not found: %#v %v", status, err)
	}
}
