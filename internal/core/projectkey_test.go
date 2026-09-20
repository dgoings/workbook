package core

import "testing"

func TestDeriveProjectKey(t *testing.T) {
	cases := map[string]string{
		"workbook":                "WORK",
		"my-app":                  "MA",
		"atomic_object.web":       "AOW",
		"2024-planning":           "W2P",
		"001":                     "W001",
		"":                        "WB",
		"---":                     "WB",
		"a":                       "AW",
		"x":                       "XW",
		"ab":                      "AB",
		"Ünïcode-app":             "NCA",
		"/Users/me/src/Project":   "PROJ",
		"/Users/me/src/Project/":  "PROJ",
		"MyAppService":            "MAS",
		"workbook-desktop-shell":  "WDS",
		"café":                    "CAF",
		"日本語":                     "WB",
		"日本語-app":                 "APP",
		".dotfiles":               "DOTF",
		"acme-site":               "AS",
		"my_app":                  "MA",
		"2024":                    "W2024",
		"24x7monitor":             "W24X7",
		"straße":                  "SE",
		"naïve-app":               "NVA",
		"a-b-c-d-e-f-g-h-i-j-k-l": "ABCDEFGHIJ",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got := DeriveProjectKey(name)
			if got != want {
				t.Fatalf("DeriveProjectKey(%q) = %q, want %q", name, got, want)
			}
			if err := ValidateProjectKey(got); err != nil {
				t.Fatalf("DeriveProjectKey(%q) = %q is not a valid key: %v", name, got, err)
			}
		})
	}
}
