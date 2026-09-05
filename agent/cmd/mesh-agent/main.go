// mesh-agent is the future native host service. No host mutations are implemented yet.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"mesh.local/agent/internal/buildinfo"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "info" {
		fmt.Fprintln(os.Stderr, "Usage: mesh-agent info (development skeleton; enrollment and service mode are not implemented)")
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"agentVersion":    buildinfo.Version,
		"platform":        runtime.GOOS,
		"architecture":    runtime.GOARCH,
		"cpuLogicalCores": runtime.NumCPU(),
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
