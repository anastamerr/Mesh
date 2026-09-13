package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mesh.local/agent/internal/state"
	"mesh.local/agent/internal/storage"
)

func setupCommand(ctx context.Context, arguments []string, input io.Reader, output, logs io.Writer) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(logs)
	host, _ := os.Hostname()
	var server, name, stateDirectory, root, bundle, distribution, relayCA, account string
	var computeEnabled, installService, passwordStdin, directLAN bool
	flags.StringVar(&server, "server", "", "Mesh controller HTTPS origin")
	flags.StringVar(&name, "name", host, "device name shown during pairing")
	flags.StringVar(&stateDirectory, "state-dir", "", "private identity directory")
	flags.StringVar(&root, "root", "", "absolute dedicated storage directory")
	flags.BoolVar(&directLAN, "direct-lan", false, "publish authenticated direct LAN storage access")
	flags.BoolVar(&computeEnabled, "compute", false, "enable the dedicated WSL compute environment")
	flags.StringVar(&bundle, "compute-bundle", "", "optional verified WSL rootfs tar to install")
	flags.StringVar(&distribution, "wsl-distribution", "Mesh", "dedicated WSL distribution")
	flags.BoolVar(&installService, "install-service", false, "install and start unattended Windows service hosting")
	flags.StringVar(&account, "account", "", "current Windows account in DOMAIN\\user form")
	flags.BoolVar(&passwordStdin, "password-stdin", false, "read the service account password from stdin")
	flags.StringVar(&relayCA, "relay-ca", "", "optional absolute private relay CA PEM path")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	name = strings.TrimSpace(name)
	if flags.NArg() != 0 || server == "" || name == "" || len(name) > 100 || root == "" || !filepath.IsAbs(root) {
		return errors.New("setup requires --server, a device name, and an absolute --root")
	}
	if stateDirectory == "" {
		var err error
		stateDirectory, err = state.DefaultDir()
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(stateDirectory) {
		return errors.New("setup state directory must be absolute")
	}
	if pathsOverlap(stateDirectory, root) {
		return errors.New("identity state and contributed storage must use separate directories")
	}
	if bundle != "" {
		computeEnabled = true
	}
	if !installService && (account != "" || passwordStdin) {
		return errors.New("service account options require --install-service, which requires --account and --password-stdin")
	}
	if installService && (account == "" || !passwordStdin) {
		return errors.New("service installation requires --account and --password-stdin")
	}
	if relayCA != "" && !filepath.IsAbs(relayCA) {
		return errors.New("relay CA path must be absolute")
	}
	saved, enrolled, err := setupIdentity(stateDirectory)
	if err != nil {
		return err
	}
	canonicalServer := strings.TrimSuffix(server, "/")
	if enrolled {
		if saved.Server != canonicalServer || saved.Name != name {
			return errors.New("existing identity belongs to a different controller or device name")
		}
		fmt.Fprintln(logs, "Existing paired identity verified; continuing setup.")
	} else if err := pairCommand(ctx, []string{"--server", server, "--name", name, "--state-dir", stateDirectory}, output, logs); err != nil {
		return err
	}
	store, err := storage.Open(root)
	if err != nil {
		return fmt.Errorf("cannot initialize contributed storage: %w", err)
	}
	if err := store.Close(); err != nil {
		return err
	}
	if computeEnabled {
		computeArguments := []string{"doctor", "--wsl-distribution", distribution}
		if bundle != "" {
			computeArguments = []string{"setup", "--wsl-distribution", distribution, "--bundle", bundle}
		}
		if err := computeCommand(ctx, computeArguments, output, logs); err != nil {
			return err
		}
	}
	if installService {
		serviceArguments := []string{"install", "--account", account, "--password-stdin",
			"--state-dir", stateDirectory, "--root", root, "--wsl-distribution", distribution}
		if directLAN {
			serviceArguments = append(serviceArguments, "--direct-lan")
		}
		if computeEnabled {
			serviceArguments = append(serviceArguments, "--compute")
		}
		if relayCA != "" {
			serviceArguments = append(serviceArguments, "--relay-ca", relayCA)
		}
		if err := serviceCommand(ctx, serviceArguments, input, output, logs); err != nil {
			return err
		}
		fmt.Fprintln(output, "Setup complete. Mesh will start automatically with Windows.")
		return nil
	}
	command := fmt.Sprintf("mesh-agent run --state-dir %q --root %q", stateDirectory, root)
	if computeEnabled {
		command += " --compute --wsl-distribution " + fmt.Sprintf("%q", distribution)
	}
	if directLAN {
		command += " --direct-lan"
	}
	if relayCA != "" {
		command += " --relay-ca " + fmt.Sprintf("%q", relayCA)
	}
	fmt.Fprintf(output, "Setup complete. Start Mesh with:\n%s\n", command)
	return nil
}

func setupIdentity(directory string) (state.State, bool, error) {
	store, err := state.Open(directory)
	if err != nil {
		return state.State{}, false, err
	}
	saved, loadError := store.Load()
	closeError := store.Close()
	if os.IsNotExist(loadError) {
		return state.State{}, false, closeError
	}
	if err := errors.Join(loadError, closeError); err != nil {
		return state.State{}, false, err
	}
	return saved, true, nil
}

func pathsOverlap(first, second string) bool {
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return contains(first, second) || contains(second, first)
}
