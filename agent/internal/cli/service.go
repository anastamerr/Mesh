package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	meshservice "mesh.local/agent/internal/service"
	"mesh.local/agent/internal/state"
)

func serviceCommand(ctx context.Context, arguments []string, input io.Reader, output, logs io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("usage: mesh-agent service install | start | stop | status | uninstall")
	}
	action := arguments[0]
	if action != "install" && action != "start" && action != "stop" && action != "status" && action != "uninstall" {
		return errors.New("unknown service command")
	}
	if action != "install" {
		if len(arguments) != 1 {
			return errors.New("service management commands accept no options")
		}
		switch action {
		case "start":
			return meshservice.Start(ctx)
		case "stop":
			return meshservice.Stop(ctx)
		case "uninstall":
			return meshservice.Uninstall(ctx)
		default:
			status, err := meshservice.Status(ctx)
			if err == nil {
				_, err = fmt.Fprintln(output, status)
			}
			return err
		}
	}
	flags := flag.NewFlagSet("service install", flag.ContinueOnError)
	flags.SetOutput(logs)
	var account, stateDirectory, root, relayOrigin, relayCA, distribution string
	var passwordStdin, compute bool
	var interval time.Duration
	flags.StringVar(&account, "account", "", "current Windows account in DOMAIN\\user form")
	flags.BoolVar(&passwordStdin, "password-stdin", false, "read the Windows account password from stdin")
	flags.StringVar(&stateDirectory, "state-dir", "", "absolute directory containing the enrolled identity")
	flags.StringVar(&root, "root", "", "absolute dedicated storage directory")
	flags.StringVar(&relayOrigin, "relay", "", "optional Mesh relay HTTPS origin override")
	flags.StringVar(&relayCA, "relay-ca", "", "optional absolute private relay CA PEM path")
	flags.BoolVar(&compute, "compute", false, "enable WSL container workloads")
	flags.StringVar(&distribution, "wsl-distribution", "Mesh", "dedicated WSL distribution")
	flags.DurationVar(&interval, "interval", 15*time.Second, "control interval, between 1s and 30s")
	if err := flags.Parse(arguments[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || !passwordStdin || account == "" || stateDirectory == "" || root == "" {
		return errors.New("install requires --account, --password-stdin, --state-dir, and --root")
	}
	current, err := user.Current()
	if err != nil || !strings.EqualFold(account, current.Username) {
		return errors.New("service account must exactly match the current enrolled Windows user")
	}
	for name, path := range map[string]string{"state directory": stateDirectory, "storage root": root} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be absolute", name)
		}
	}
	if relayCA != "" && !filepath.IsAbs(relayCA) {
		return errors.New("relay CA path must be absolute")
	}
	passwordData, err := io.ReadAll(io.LimitReader(input, 513))
	if err != nil || len(passwordData) > 512 {
		return errors.New("invalid Windows account password")
	}
	password := strings.TrimRight(string(passwordData), "\r\n")
	if password == "" || strings.ContainsRune(password, '\x00') || strings.ContainsAny(password, "\r\n") {
		return errors.New("invalid Windows account password")
	}
	store, err := state.Open(stateDirectory)
	if err != nil {
		return err
	}
	_, loadError := store.Load()
	closeError := store.Close()
	if err := errors.Join(loadError, closeError); err != nil {
		return errors.New("state directory does not contain a readable enrolled identity for this user")
	}
	runArguments := []string{"--state-dir", stateDirectory, "--root", root,
		"--interval", interval.String(), "--wsl-distribution", distribution}
	if relayOrigin != "" {
		runArguments = append(runArguments, "--relay", relayOrigin)
	}
	if relayCA != "" {
		runArguments = append(runArguments, "--relay-ca", relayCA)
	}
	if compute {
		runArguments = append(runArguments, "--compute")
	}
	if _, err := parseOptions(append([]string{"run"}, runArguments...), io.Discard); err != nil {
		return err
	}
	if err := meshservice.Install(ctx, account, password, runArguments); err != nil {
		return err
	}
	if err := meshservice.Start(ctx); err != nil {
		return errors.New("service installed but could not start; inspect Windows Event Viewer and run service start")
	}
	_, err = fmt.Fprintln(output, "Mesh service installed and started.")
	return err
}
