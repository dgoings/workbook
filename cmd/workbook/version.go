package main

import "github.com/dgoings/workbook/internal/release"

var (
	version string
	commit  string
)

func configureReleaseMetadata() {
	if version != "" {
		release.Version = version
	}
	if commit != "" {
		release.Commit = commit
	}
}
