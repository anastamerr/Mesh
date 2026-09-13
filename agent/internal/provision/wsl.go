// Package provision installs and verifies the dedicated Windows compute environment.
package provision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	maxCommandOutput = 64 * 1024
	maxBundleBytes   = int64(4) << 30
)

var (
	distributionName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	bundleDigest     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	runtimeVersion   = regexp.MustCompile(`^[A-Za-z0-9.+-]{1,40}$`)
)

type SetupOptions struct {
	Distribution string
	Bundle       string
	BundleSHA256 string
	InstallRoot  string
}

type Status struct {
	State        string `json:"state"`
	Distribution string `json:"distribution"`
	Architecture string `json:"architecture"`
	Runtime      string `json:"runtime"`
	Version      string `json:"version"`
	InstallRoot  string `json:"installRoot,omitempty"`
}

type Manager struct {
	Program    string
	Platform   string
	Attempts   int
	RetryDelay time.Duration
	Command    func(context.Context, string, ...string) ([]byte, error)
	Wait       func(context.Context, time.Duration) error
	RemoveAll  func(string) error
	Progress   func(string)
}

func NewManager() *Manager {
	return &Manager{Program: "wsl.exe", Platform: runtime.GOOS, Attempts: 10, RetryDelay: 2 * time.Second}
}

func (manager *Manager) progress(message string) {
	if manager.Progress != nil {
		manager.Progress(message)
	}
}

func (manager *Manager) run(ctx context.Context, arguments ...string) ([]byte, error) {
	if manager.Command != nil {
		return manager.Command(ctx, manager.Program, arguments...)
	}
	command := exec.CommandContext(ctx, manager.Program, arguments...)
	var output limitedOutput
	output.remaining = maxCommandOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("WSL command failed")
	}
	if output.truncated {
		return nil, errors.New("WSL response exceeded limit")
	}
	return output.buffer.Bytes(), nil
}

type limitedOutput struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func (output *limitedOutput) Write(data []byte) (int, error) {
	written := len(data)
	if len(data) > output.remaining {
		data = data[:output.remaining]
		output.truncated = true
	}
	output.remaining -= len(data)
	_, _ = output.buffer.Write(data)
	return written, nil
}

func (manager *Manager) wait(ctx context.Context) error {
	if manager.Wait != nil {
		return manager.Wait(ctx, manager.RetryDelay)
	}
	timer := time.NewTimer(manager.RetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (manager *Manager) requireWindows() error {
	if manager.Platform != "windows" {
		return errors.New("WSL compute setup is available only on Windows")
	}
	return nil
}

func (manager *Manager) distributions(ctx context.Context) ([]string, error) {
	if _, err := manager.run(ctx, "--status"); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("WSL2 status is unavailable; enable WSL2 if needed and run Mesh as the Windows user that owns its distributions")
	}
	data, err := manager.run(ctx, "--list", "--quiet")
	if err != nil {
		return nil, err
	}
	// Older wsl.exe releases emit UTF-16LE-like output even when stdout is a pipe.
	text := strings.ReplaceAll(string(data), "\x00", "")
	text = strings.TrimPrefix(text, "\ufeff")
	var names []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

func containsName(names []string, expected string) bool {
	for _, name := range names {
		if strings.EqualFold(name, expected) {
			return true
		}
	}
	return false
}

func validateDistribution(name string) error {
	if !distributionName.MatchString(name) {
		return errors.New("WSL distribution name is invalid")
	}
	return nil
}

func (manager *Manager) Doctor(ctx context.Context, distribution string) (Status, error) {
	var status Status
	if err := manager.requireWindows(); err != nil {
		return status, err
	}
	if err := validateDistribution(distribution); err != nil {
		return status, err
	}
	names, err := manager.distributions(ctx)
	if err != nil {
		return status, err
	}
	if !containsName(names, distribution) {
		return status, errors.New("Mesh compute environment is not installed; run 'mesh-agent compute setup'")
	}
	data, err := manager.run(ctx, "--distribution", distribution, "--user", "root", "--exec", "mesh-executor", "doctor")
	if err != nil {
		if ctx.Err() != nil {
			return status, ctx.Err()
		}
		return status, errors.New("Mesh compute environment is installed but Docker is not ready")
	}
	var response struct {
		Architecture string `json:"architecture"`
		Runtime      string `json:"runtime"`
		Version      string `json:"version"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil || response.Runtime != "docker" ||
		(response.Architecture != "amd64" && response.Architecture != "arm64") || !runtimeVersion.MatchString(response.Version) {
		return status, errors.New("Mesh compute environment returned an invalid readiness response")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return status, errors.New("Mesh compute environment returned an invalid readiness response")
	}
	return Status{State: "ready", Distribution: distribution, Architecture: response.Architecture,
		Runtime: response.Runtime, Version: response.Version}, nil
}

func validateSetup(options SetupOptions) error {
	if err := validateDistribution(options.Distribution); err != nil {
		return err
	}
	if options.Bundle == "" || options.InstallRoot == "" || !filepath.IsAbs(options.InstallRoot) ||
		filepath.Dir(filepath.Clean(options.InstallRoot)) == filepath.Clean(options.InstallRoot) {
		return errors.New("setup requires a bundle and an absolute, dedicated install root")
	}
	if !bundleDigest.MatchString(strings.ToLower(options.BundleSHA256)) {
		return errors.New("setup requires the bundle's 64-character SHA-256 digest")
	}
	return nil
}

func verifyBundle(path, expected string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > maxBundleBytes {
		return errors.New("compute bundle must be a regular file no larger than 4 GiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxBundleBytes+1))
	if err != nil || written != info.Size() {
		return errors.New("compute bundle changed while it was being verified")
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(expected) {
		return errors.New("compute bundle SHA-256 does not match; do not install it")
	}
	return nil
}

func (manager *Manager) rollback(distribution, root string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := manager.run(ctx, "--unregister", distribution); err != nil {
		return
	}
	manager.removeRoot(root)
}

func (manager *Manager) removeRoot(root string) {
	remove := manager.RemoveAll
	if remove == nil {
		remove = os.RemoveAll
	}
	_ = remove(root)
}

func (manager *Manager) Setup(ctx context.Context, options SetupOptions) (Status, error) {
	var status Status
	if err := manager.requireWindows(); err != nil {
		return status, err
	}
	if err := validateSetup(options); err != nil {
		return status, err
	}
	manager.progress("Checking WSL2...")
	names, err := manager.distributions(ctx)
	if err != nil {
		return status, err
	}
	if containsName(names, options.Distribution) {
		status, err = manager.Doctor(ctx, options.Distribution)
		if err != nil {
			return status, errors.New("the named WSL distribution already exists but is not a ready Mesh environment")
		}
		return status, nil
	}
	if _, err = os.Lstat(options.InstallRoot); !os.IsNotExist(err) {
		if err == nil {
			return status, errors.New("compute install root already exists; choose an empty new location")
		}
		return status, err
	}
	manager.progress("Verifying compute bundle...")
	if err = verifyBundle(options.Bundle, options.BundleSHA256); err != nil {
		return status, err
	}
	if err = os.MkdirAll(options.InstallRoot, 0700); err != nil {
		return status, err
	}
	manager.progress("Importing the dedicated WSL2 environment...")
	if _, err = manager.run(ctx, "--import", options.Distribution, options.InstallRoot, options.Bundle, "--version", "2"); err != nil {
		checkContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		current, listError := manager.distributions(checkContext)
		cancel()
		if listError == nil && !containsName(current, options.Distribution) {
			manager.removeRoot(options.InstallRoot)
		}
		return status, errors.New("could not import the Mesh WSL2 environment; no existing distribution was changed")
	}
	attempts := manager.Attempts
	if attempts < 1 {
		attempts = 10
	}
	manager.progress("Starting Docker and checking readiness...")
	for attempt := 0; attempt < attempts; attempt++ {
		status, err = manager.Doctor(ctx, options.Distribution)
		if err == nil {
			status.InstallRoot = options.InstallRoot
			return status, nil
		}
		if attempt+1 < attempts {
			if err = manager.wait(ctx); err != nil {
				manager.rollback(options.Distribution, options.InstallRoot)
				return Status{}, err
			}
		}
	}
	manager.rollback(options.Distribution, options.InstallRoot)
	return Status{}, errors.New("Docker did not become ready; the new Mesh environment was rolled back")
}
