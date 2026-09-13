package executor

import (
	"context"
	"errors"
	"io"
	"net"
	"regexp"
)

var workloadUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type ApplicationDialer interface {
	DialApplication(context.Context, string, uint64, int) (net.Conn, error)
}

func Proxy(ctx context.Context, dialer ApplicationDialer, workloadID string, revision uint64, port int,
	input io.Reader, output io.Writer) error {
	if !workloadUUID.MatchString(workloadID) || revision < 1 || port < 1 || port > 65535 {
		return errors.New("invalid application proxy request")
	}
	connection, err := dialer.DialApplication(ctx, workloadID, revision, port)
	if err != nil {
		return err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	go func() {
		_, _ = io.Copy(connection, input)
		if half, ok := connection.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		}
	}()
	if _, err := io.Copy(output, connection); err != nil && ctx.Err() == nil {
		return errors.New("application proxy stream failed")
	}
	return ctx.Err()
}
