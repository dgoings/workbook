// Package autosync resolves whether a Workbook command synchronizes shared
// task refs with origin as part of its own work.
//
// Resolution is the package's whole responsibility. Performing the fetch and
// the targeted push belongs to the repository, and deciding which commands
// participate belongs to the CLI.
package autosync

import (
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/userconfig"
)

// PreferenceKey names the automatic synchronization preference in the user
// configuration's forward-compatible preference map.
const PreferenceKey = "autoSync"

// Source names the configuration layer that decided a policy.
type Source string

const (
	SourceFlag    Source = "flag"
	SourceProject Source = "project"
	SourceUser    Source = "user"
	SourceDefault Source = "default"
)

// Policy is a resolved automatic synchronization decision. Source is reported
// in command output so a surprised user can tell which layer decided.
type Policy struct {
	Enabled bool   `json:"enabled"`
	Source  Source `json:"source"`
}

// Resolve applies the precedence chain: the --no-sync flag, then the tracked
// project policy, then the user preference, then the built-in default.
//
// A tracked project policy outranks a personal preference so that a team can
// require synchronization in a repository. --no-sync remains the per-command
// escape hatch.
func Resolve(noSyncFlag bool, project core.ProjectConfig, user userconfig.Config) (Policy, error) {
	if noSyncFlag {
		return Policy{Enabled: false, Source: SourceFlag}, nil
	}
	if project.AutoSync != core.AutoSyncUnset {
		return Policy{Enabled: project.AutoSync.Enabled(true), Source: SourceProject}, nil
	}
	preference, found, err := userPreference(user)
	if err != nil {
		return Policy{}, err
	}
	if found {
		return Policy{Enabled: preference, Source: SourceUser}, nil
	}
	return Policy{Enabled: true, Source: SourceDefault}, nil
}

// userPreference reads the preference map without silently tolerating a
// misspelled value; a preference the user believes is disabling
// synchronization must not be ignored.
func userPreference(user userconfig.Config) (bool, bool, error) {
	value, found := user.Preferences[PreferenceKey]
	if !found {
		return false, false, nil
	}
	enabled, ok := value.(bool)
	if !ok {
		return false, false, core.Errorf(core.CategoryValidation,
			"user configuration preference %q must be true or false", PreferenceKey)
	}
	return enabled, true, nil
}
