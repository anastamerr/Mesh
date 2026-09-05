// Package buildinfo holds release metadata shared by Mesh's native commands.
package buildinfo

// Version is overridden during release builds using -ldflags.
var Version = "0.1.0-dev"
