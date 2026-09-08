package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mesh.local/agent/internal/storage"
	"time"
)

func downloadWithRecovery(ctx context.Context, client *storage.Client, id, destination string, logs io.Writer) error {
	for attempt := 0; ; attempt++ {
		err := client.DownloadResumable(ctx, id, destination)
		var unavailable *storage.UnavailableError
		if err == nil || attempt == 2 || !errors.As(err, &unavailable) || ctx.Err() != nil {
			return err
		}
		delay := time.Duration(1<<attempt) * time.Second
		fmt.Fprintf(logs, "Connection interrupted. Retrying in %s; checking saved download progress...\n", delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
