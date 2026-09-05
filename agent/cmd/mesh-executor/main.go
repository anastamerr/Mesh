// mesh-executor will run inside Linux and own the restricted Docker integration.
package main

import (
	"fmt"
	"os"

	"mesh.local/agent/internal/buildinfo"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println(buildinfo.Version)
		return
	}
	fmt.Fprintln(os.Stderr, "Mesh executor skeleton: workload execution is not implemented")
	os.Exit(2)
}
