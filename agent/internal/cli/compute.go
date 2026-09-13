package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mesh.local/agent/internal/provision"
)

func computeCommand(ctx context.Context, arguments []string, output, logs io.Writer) error {
	if len(arguments) == 0 || (arguments[0] != "setup" && arguments[0] != "doctor") {
		return errors.New("usage: mesh-agent compute setup | doctor")
	}
	action := arguments[0]
	flags := flag.NewFlagSet("compute "+action, flag.ContinueOnError)
	flags.SetOutput(logs)
	var distribution, bundle, digest, installRoot string
	var asJSON bool
	flags.StringVar(&distribution, "wsl-distribution", "Mesh", "dedicated WSL distribution name")
	flags.BoolVar(&asJSON, "json", false, "emit machine-readable readiness")
	if action == "setup" {
		flags.StringVar(&bundle, "bundle", "", "verified Mesh WSL rootfs tar")
		flags.StringVar(&digest, "bundle-sha256", "", "expected bundle digest; defaults to <bundle>.sha256")
		flags.StringVar(&installRoot, "install-root", "", "new directory for the dedicated WSL virtual disk")
	}
	if err := flags.Parse(arguments[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("compute commands accept named options only")
	}
	manager := provision.NewManager()
	manager.Progress = func(message string) { fmt.Fprintln(logs, message) }
	var status provision.Status
	var err error
	if action == "doctor" {
		status, err = manager.Doctor(ctx, distribution)
	} else {
		if bundle == "" {
			return errors.New("compute setup requires --bundle")
		}
		if digest == "" {
			digest, err = readBundleDigest(bundle + ".sha256")
			if err != nil {
				return errors.New("supply --bundle-sha256 or place a valid digest in <bundle>.sha256")
			}
		}
		if installRoot == "" {
			base, rootError := os.UserConfigDir()
			if rootError != nil {
				return rootError
			}
			installRoot = filepath.Join(base, "Mesh", "wsl", distribution)
		}
		status, err = manager.Setup(ctx, provision.SetupOptions{Distribution: distribution,
			Bundle: bundle, BundleSHA256: strings.ToLower(digest), InstallRoot: installRoot})
	}
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(output).Encode(status)
	}
	_, err = fmt.Fprintf(output, "Mesh compute is %s: %s (%s, %s %s).\n",
		status.State, status.Distribution, status.Architecture, status.Runtime, status.Version)
	return err
}

func readBundleDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 64 || info.Size() > 256 {
		return "", errors.New("invalid bundle digest file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil || len(data) > 256 {
		return "", errors.New("invalid bundle digest file")
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 || len(fields[0]) != 64 {
		return "", errors.New("invalid bundle digest file")
	}
	for _, character := range fields[0] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') ||
			(character >= 'A' && character <= 'F')) {
			return "", errors.New("invalid bundle digest file")
		}
	}
	return strings.ToLower(fields[0]), nil
}
