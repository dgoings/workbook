package core

import "testing"

func TestDeriveProjectKey(t *testing.T) {
	cases := map[string]string{
		"workbook":              "WORKBOOK",
		"my-app":                "MYAPP",
		"atomic_object.web":     "ATOMICOBJE",
		"2024-planning":         "PLANNING",
		"001":                   DefaultProjectKey,
		"":                      DefaultProjectKey,
		"---":                   DefaultProjectKey,
		"a":                     DefaultProjectKey,
		"Ünïcode-app":           "NCODEAPP",
		"/Users/me/src/Project": "PROJECT",
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
