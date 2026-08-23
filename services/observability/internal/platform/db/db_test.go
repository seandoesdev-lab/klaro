package db

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestValidOrgID(t *testing.T) {
	valid := []string{
		"00000000-0000-0000-0000-000000000001",
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		"3F2504E0-4F89-11D3-9A0C-0305E82C3301",
	}
	for _, s := range valid {
		if !ValidOrgID(s) {
			t.Errorf("ValidOrgID(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",
		"not-a-uuid",
		"00000000-0000-0000-0000-00000000000",   // too short
		"00000000-0000-0000-0000-0000000000011", // too long
		"0000000000000-000-0000-000000000001",   // dashes misplaced
		"gggggggg-0000-0000-0000-000000000001",  // non-hex
		// Injection shapes must not slip past the validator; set_config binds
		// the value as a parameter, but the guard is the first line of defence.
		"00000000-0000-0000-0000-000000000001'; DROP TABLE observability_keys--",
		"' OR '1'='1",
	}
	for _, s := range invalid {
		if ValidOrgID(s) {
			t.Errorf("ValidOrgID(%q) = true, want false", s)
		}
	}
}

func TestMigrationFilesAreOrderedAndComplete(t *testing.T) {
	fsys := fstest.MapFS{
		"0007_plans_observability.sql": {},
		"0001_observability_keys.sql":  {},
		"0000_shared_prereq.sql":       {},
		"embed.go":                     {},
		"README.md":                    {},
	}
	got, err := MigrationFiles(fsys)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0000_shared_prereq.sql", "0001_observability_keys.sql", "0007_plans_observability.sql"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("MigrationFiles = %v, want %v", got, want)
	}
}
