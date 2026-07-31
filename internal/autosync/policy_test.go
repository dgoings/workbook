package autosync

import (
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/userconfig"
)

func projectWith(setting core.AutoSyncSetting) core.ProjectConfig {
	return core.ProjectConfig{Format: "workbook.project", Version: 2, Key: "WB", AutoSync: setting}
}

func userWith(preferences map[string]any) userconfig.Config {
	config := userconfig.Default()
	config.Preferences = preferences
	return config
}

func TestResolveEnablesSynchronizationByDefault(t *testing.T) {
	policy, err := Resolve(false, projectWith(core.AutoSyncUnset), userWith(nil))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !policy.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if policy.Source != SourceDefault {
		t.Fatalf("Source = %q, want %q", policy.Source, SourceDefault)
	}
}

func TestResolveHonorsUserPreferenceWhenProjectIsUnset(t *testing.T) {
	policy, err := Resolve(false, projectWith(core.AutoSyncUnset), userWith(map[string]any{"autoSync": false}))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if policy.Enabled {
		t.Fatal("Enabled = true, want false")
	}
	if policy.Source != SourceUser {
		t.Fatalf("Source = %q, want %q", policy.Source, SourceUser)
	}
}

func TestResolveProjectPolicyOverridesUserPreference(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		project core.AutoSyncSetting
		user    bool
		want    bool
	}{
		{name: "project disables what user enabled", project: core.AutoSyncDisabled, user: true, want: false},
		{name: "project enables what user disabled", project: core.AutoSyncEnabled, user: false, want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			policy, err := Resolve(false, projectWith(testCase.project), userWith(map[string]any{"autoSync": testCase.user}))
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if policy.Enabled != testCase.want {
				t.Fatalf("Enabled = %t, want %t", policy.Enabled, testCase.want)
			}
			if policy.Source != SourceProject {
				t.Fatalf("Source = %q, want %q", policy.Source, SourceProject)
			}
		})
	}
}

func TestResolveFlagOverridesEveryConfiguredLayer(t *testing.T) {
	policy, err := Resolve(true, projectWith(core.AutoSyncEnabled), userWith(map[string]any{"autoSync": true}))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if policy.Enabled {
		t.Fatal("Enabled = true, want false")
	}
	if policy.Source != SourceFlag {
		t.Fatalf("Source = %q, want %q", policy.Source, SourceFlag)
	}
}

func TestResolveRejectsNonBooleanUserPreference(t *testing.T) {
	_, err := Resolve(false, projectWith(core.AutoSyncUnset), userWith(map[string]any{"autoSync": "yes"}))
	if err == nil {
		t.Fatal("Resolve() error = nil, want validation error")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("category = %v, want %v", got, core.CategoryValidation)
	}
}
