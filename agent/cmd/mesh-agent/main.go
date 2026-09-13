package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"mesh.local/agent/internal/cli"
	meshservice "mesh.local/agent/internal/service"
)

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "service" && os.Args[2] == "run" {
		err := meshservice.Run(os.Args[3:], func(ctx context.Context, logs io.Writer) error {
			return cli.Execute(ctx, append([]string{"run"}, os.Args[3:]...), strings.NewReader(""), io.Discard, logs)
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
