// Package inventory reports the physical host, not a synthetic sum of runtimes.
package inventory

import (
	"context"
	"fmt"
	"runtime"

	"github.com/shirou/gopsutil/v4/mem"
	"mesh.local/agent/internal/control"
)

func Read(ctx context.Context) (control.Inventory, error) {
	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return control.Inventory{}, fmt.Errorf("cannot read host memory: %w", err)
	}
	if memory.Total == 0 || memory.Available > memory.Total {
		return control.Inventory{}, fmt.Errorf("host reported invalid memory totals")
	}
	return control.Inventory{CPULogicalCores: runtime.NumCPU(), MemoryTotalBytes: memory.Total,
		MemoryAvailableBytes: memory.Available}, nil
}
