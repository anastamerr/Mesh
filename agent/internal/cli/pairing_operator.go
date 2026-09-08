package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"mesh.local/agent/internal/control"
)

func pairingOperatorCommand(ctx context.Context, args []string, input io.Reader, output, logs io.Writer) error {
	flags := flag.NewFlagSet("pair "+args[0], flag.ContinueOnError)
	flags.SetOutput(logs)
	var server, code, fingerprint string
	var operatorStdin bool
	flags.StringVar(&server, "server", "", "Mesh controller HTTPS origin")
	flags.BoolVar(&operatorStdin, "operator-stdin", false, "read operator credential from standard input")
	if args[0] == "approve" {
		flags.StringVar(&code, "code", "", "pairing code shown on the other device")
		flags.StringVar(&fingerprint, "fingerprint", "", "device fingerprint verified on the other device")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || !operatorStdin {
		return errors.New("use --server and --operator-stdin")
	}
	client, err := control.New(server)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(input, 257))
	if err != nil || len(data) > 256 {
		return errors.New("invalid operator credential")
	}
	operator := strings.TrimSpace(string(data))
	challenges, err := client.PairingChallenges(ctx, operator)
	if err != nil {
		return err
	}
	if args[0] == "list" {
		return json.NewEncoder(output).Encode(challenges)
	}
	for _, challenge := range challenges {
		if challenge.Code == strings.ToUpper(code) {
			if challenge.PublicKeyFingerprint != fingerprint {
				return errors.New("device fingerprint does not match; pairing was not approved")
			}
			if err := client.ApprovePairing(ctx, operator, challenge.ID, fingerprint); err != nil {
				return err
			}
			_, err = fmt.Fprintf(output, "Paired %s.\n", challenge.Name)
			return err
		}
	}
	return errors.New("pairing code not found or expired")
}
