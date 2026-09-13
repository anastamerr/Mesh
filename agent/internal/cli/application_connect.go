package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/relay"
	"mesh.local/agent/internal/state"
	"mesh.local/agent/internal/stream"
)

func serveApplication(ctx context.Context, controller *control.Client, operator, workloadID, listenAddress,
	relayCA string, output, logs io.Writer) error {
	relayOrigin, err := controller.RelayOrigin(ctx)
	if err != nil {
		return err
	}
	if relayOrigin == "" {
		return errors.New("controller has no relay configured")
	}
	trust, err := relayTrust(relayCA)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() {
		return errors.New("application connections may listen only on a loopback address")
	}
	_, _ = fmt.Fprintf(output, "Application listening on %s while this command is running.\n", listener.Addr())
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	for {
		local, acceptError := listener.Accept()
		if acceptError != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("local application listener failed")
		}
		go connectApplication(ctx, controller, operator, workloadID, relayOrigin, trust, local, logs)
	}
}

func connectApplication(ctx context.Context, controller *control.Client, operator, workloadID, relayOrigin string,
	trust *tls.Config, local net.Conn, logs io.Writer) {
	defer local.Close()
	ticket, err := controller.ApplicationConsumerTicket(ctx, operator, workloadID)
	if err != nil {
		fmt.Fprintln(logs, "Application is unavailable; local connection closed.")
		return
	}
	route, err := relay.DialRoute(ctx, relayOrigin, ticket.NodeID, ticket.Route, relay.RoleConsumer, ticket.Token, trust)
	if err != nil {
		fmt.Fprintln(logs, "Application relay is unavailable; local connection closed.")
		return
	}
	secure := tls.Client(route, state.PinnedDeviceTLS(ticket.PublicKeyFingerprint))
	handshake, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := secure.HandshakeContext(handshake); err != nil {
		_ = secure.Close()
		fmt.Fprintln(logs, "Application device identity could not be verified; local connection closed.")
		return
	}
	stream.Bridge(ctx, local, secure)
}
