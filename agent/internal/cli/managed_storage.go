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
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/storage"
)

type managedOptions struct {
	command, controller, server, node, source, collection, destination, name string
	operatorStdin, json                                                      bool
}

func managedStorage(ctx context.Context, args []string, input io.Reader, output, logs io.Writer) error {
	o := managedOptions{command: args[0]}
	flags := flag.NewFlagSet("storage "+o.command, flag.ContinueOnError)
	flags.SetOutput(logs)
	flags.StringVar(&o.controller, "controller", "http://127.0.0.1:3000", "controller origin")
	flags.StringVar(&o.node, "node", "", "node name or full ID")
	flags.BoolVar(&o.operatorStdin, "operator-stdin", false, "read operator credential from stdin; normally supplied by npm run mesh")
	flags.BoolVar(&o.json, "json", false, "emit machine-readable results")
	if o.command != "catalog" {
		flags.StringVar(&o.server, "server", "http://127.0.0.1:7332", "storage origin")
	}
	if o.command == "copy" {
		flags.StringVar(&o.source, "source", "", "folder to copy")
		flags.StringVar(&o.name, "name", "", "catalogue name, defaults to folder name")
	}
	if o.command == "get" {
		flags.StringVar(&o.collection, "collection", "", "collection name or full ID")
		flags.StringVar(&o.destination, "destination", "", "new destination folder")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || o.node == "" || !o.operatorStdin {
		return errors.New("use --node and --operator-stdin, or run this command through npm run mesh")
	}
	if o.command == "copy" && o.source == "" {
		return errors.New("copy requires --source")
	}
	if o.command == "get" && (o.collection == "" || o.destination == "") {
		return errors.New("get requires --collection and --destination")
	}
	controller, err := control.New(o.controller)
	if err != nil {
		return err
	}
	if o.command != "catalog" {
		if _, err := control.New(o.server); err != nil {
			return err
		}
	}
	if o.command == "get" {
		if _, err := os.Lstat(o.destination); err == nil {
			return errors.New("destination already exists; choose a new folder")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	data, err := io.ReadAll(io.LimitReader(input, 257))
	if err != nil {
		return err
	}
	key := strings.TrimSpace(string(data))
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`).MatchString(key) {
		return errors.New("invalid operator credential")
	}
	fmt.Fprintln(logs, "Connecting to controller...")
	node, err := controller.ResolveNode(ctx, key, o.node)
	if err != nil {
		return err
	}
	switch o.command {
	case "catalog":
		return showCatalogue(ctx, controller, key, node, o.json, output)
	case "copy":
		return copyManaged(ctx, controller, key, node, o, output, logs)
	case "get":
		return getManaged(ctx, controller, key, node, o, output, logs)
	}
	return errors.New("unknown managed storage command")
}
func copyManaged(ctx context.Context, controller *control.Client, key, node string, o managedOptions, output, logs io.Writer) error {
	fmt.Fprintln(logs, "Scanning folder and calculating checksums...")
	folder, err := storage.Prepare(ctx, o.source, progressPrinter(logs))
	if err != nil {
		return err
	}
	defer folder.Close()
	id, err := folder.Manifest.ID()
	if err != nil {
		return err
	}
	name := o.name
	if name == "" {
		absolute, err := filepath.Abs(o.source)
		if err != nil {
			return err
		}
		name = filepath.Base(absolute)
	}
	count, total := folder.Manifest.Statistics()
	entry := control.Collection{ID: id, Name: name, FileCount: count, TotalBytes: total}
	if err = controller.RegisterCollection(ctx, key, node, entry); err != nil {
		return err
	}
	client, err := managedClient(ctx, controller, key, node, o.server, "write", id, logs)
	if err != nil {
		return err
	}
	if _, err = uploadWithRecovery(ctx, client, folder, logs); err != nil {
		return fmt.Errorf("copy interrupted; rerun the same command to resume: %w", err)
	}
	if o.json {
		return json.NewEncoder(output).Encode(entry)
	}
	_, err = fmt.Fprintf(output, "Copied %q: %d files, %s.\nCollection: %s\n", name, count, formatBytes(total), id)
	return err
}

// Re-read durable offsets after a transient failure instead of replaying an
// uncertain write. Source hashing is retained; a changed source still fails verification.
func uploadWithRecovery(ctx context.Context, client *storage.Client, folder *storage.PreparedFolder, logs io.Writer) (string, error) {
	for attempt := 0; ; attempt++ {
		id, err := client.UploadPrepared(ctx, folder)
		var unavailable *storage.UnavailableError
		if err == nil || attempt == 2 || !errors.As(err, &unavailable) || ctx.Err() != nil {
			return id, err
		}
		delay := time.Duration(1<<attempt) * time.Second
		fmt.Fprintf(logs, "%v. Retrying in %s (%d/3); checking saved progress...\n", err, delay, attempt+2)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func getManaged(ctx context.Context, controller *control.Client, key, node string, o managedOptions, output, logs io.Writer) error {
	var found *control.Collection
	ambiguous := false
	after := ""
lookup:
	for {
		entries, err := controller.Collections(ctx, key, node, after)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			// An exact immutable ID wins over display names and needs no more pages.
			if entry.ID == o.collection {
				found = &entry
				ambiguous = false
				break lookup
			}
			if entry.Name == o.collection {
				ambiguous = ambiguous || found != nil
				found = &entry
			}
		}
		if len(entries) < 100 {
			break
		}
		after = entries[len(entries)-1].ID
	}
	if ambiguous {
		return errors.New("collection name is ambiguous; use its full ID from catalog")
	}
	if found == nil {
		return errors.New("collection not found; run catalog to see copies on this node")
	}
	if found.ConfirmedAt == nil {
		return errors.New("copy is not confirmed yet; rerun its copy command to finish or reconcile it")
	}
	client, err := managedClient(ctx, controller, key, node, o.server, "read", found.ID, logs)
	if err != nil {
		return err
	}
	if err = client.Download(ctx, found.ID, o.destination); err != nil {
		return fmt.Errorf("download failed; retry into a new destination: %w", err)
	}
	if o.json {
		return json.NewEncoder(output).Encode(found)
	}
	_, err = fmt.Fprintf(output, "Retrieved %q to %s.\n", found.Name, o.destination)
	return err
}
func managedClient(ctx context.Context, controller *control.Client, key, node, server, access, id string, logs io.Writer) (*storage.Client, error) {
	renew := func(ctx context.Context) (string, error) { return controller.StorageGrant(ctx, key, node, access, &id) }
	token, err := renew(ctx)
	if err != nil {
		return nil, err
	}
	client, err := storage.NewClient(server, token)
	if err != nil {
		return nil, err
	}
	client.RenewCredential = renew
	client.Progress = progressPrinter(logs)
	return client, nil
}
func showCatalogue(ctx context.Context, controller *control.Client, key, node string, asJSON bool, output io.Writer) error {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if !asJSON {
		fmt.Fprintln(writer, "NAME\tFILES\tSIZE\tSTATE\tCOLLECTION")
	}
	after := ""
	count := 0
	for {
		entries, err := controller.Collections(ctx, key, node, after)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			count++
			if asJSON {
				if err = json.NewEncoder(output).Encode(entry); err != nil {
					return err
				}
				continue
			}
			status := "pending"
			if entry.ConfirmedAt != nil {
				status = "confirmed"
			}
			if _, err = fmt.Fprintf(writer, "%s\t%d\t%s\t%s\t%s\n", entry.Name, entry.FileCount, formatBytes(entry.TotalBytes), status, entry.ID); err != nil {
				return err
			}
		}
		if len(entries) < 100 {
			break
		}
		after = entries[len(entries)-1].ID
	}
	if count == 0 && !asJSON {
		fmt.Fprintln(writer, "No collections yet. Use copy to add a folder.")
	}
	return writer.Flush()
}
func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1<<20 {
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}
func progressPrinter(logs io.Writer) func(storage.TransferEvent) {
	var last time.Time
	phase := ""
	var began time.Time
	var initial, reused int64
	return func(event storage.TransferEvent) {
		if event.Phase == "Scanning" && phase == "" {
			phase, last = event.Phase, time.Now()
			return
		}
		if event.Phase != phase || event.Reused != reused {
			reused = event.Reused
			began = time.Now()
			initial = event.Completed
		}

		if event.Phase == phase && time.Since(last) < time.Second {
			return
		}
		phase = event.Phase
		last = time.Now()
		if phase == "Scanning" {
			fmt.Fprintf(logs, "Scanning: %d files hashed, %s read...\n", event.Files, formatBytes(event.Completed))
			return
		}
		percent := float64(100)
		if event.Total > 0 {
			percent = 100 * float64(event.Completed) / float64(event.Total)
		}
		fmt.Fprintf(logs, "%s: %s / %s (%.0f%%)", phase, formatBytes(event.Completed), formatBytes(event.Total), percent)
		elapsed := time.Since(began).Seconds()
		if (phase == "Uploading" || phase == "Downloading") && elapsed >= 1 && event.Completed > initial {
			rate := float64(event.Completed-initial) / elapsed
			fmt.Fprintf(logs, "; %s/s", formatBytes(int64(rate)))
			if event.Total > event.Completed {
				seconds := float64(event.Total-event.Completed) / rate
				if seconds < 24*60*60 {
					fmt.Fprintf(logs, "; about %s left", (time.Duration(seconds * float64(time.Second))).Round(time.Second))
				} else {
					fmt.Fprint(logs, "; more than 1 day left")
				}
			}
		}
		if event.Reused > 0 {
			fmt.Fprintf(logs, "; %s already present", formatBytes(event.Reused))
		}
		fmt.Fprintln(logs)
	}
}
