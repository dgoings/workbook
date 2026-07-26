// Package release exposes build metadata for Workbook executables.
package release

var (
	// Version identifies the Workbook release. Release builds replace this value
	// through linker flags; source builds remain explicitly marked as development
	// builds.
	Version = "dev"

	// Commit identifies the Git commit used for the build. Release builds replace
	// this value through linker flags.
	Commit = "unknown"
)

// Metadata is the build information reported by workbook version.
type Metadata struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Current returns the build metadata for this executable.
func Current() Metadata {
	return Metadata{Version: Version, Commit: Commit}
}
