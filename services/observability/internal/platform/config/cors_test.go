package config

import "testing"

// devEnv is the minimum a Load call needs to be about CORS and nothing else.
func devEnv(extra map[string]string) func(string) string {
	base := map[string]string{
		"OBS_ENV":                  "development",
		"OBS_JWT_HS_SECRET":        "a-development-secret-of-at-least-32b",
		"OBS_INTERNAL_ADDR":        "",
		"OBS_DATABASE_URL":         "postgres://klaro_obs_app:x@localhost:5432/klaro_obs?sslmode=disable",
		"OBS_MIGRATE_DATABASE_URL": "postgres://klaro:x@localhost:5432/klaro_obs?sslmode=disable",
	}
	for k, v := range extra {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestDevCORSOriginsAreSplitAndTrimmed(t *testing.T) {
	c, err := Load(devEnv(map[string]string{
		"OBS_DEV_CORS_ORIGINS": " http://localhost:3100 , http://127.0.0.1:3100 ,",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"http://localhost:3100", "http://127.0.0.1:3100"}
	if len(c.DevCORSOrigins) != len(want) {
		t.Fatalf("origins = %v, want %v", c.DevCORSOrigins, want)
	}
	for i := range want {
		if c.DevCORSOrigins[i] != want[i] {
			t.Errorf("origins[%d] = %q, want %q", i, c.DevCORSOrigins[i], want[i])
		}
	}
}

func TestUnsetDevCORSOriginsIsEmpty(t *testing.T) {
	c, err := Load(devEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.DevCORSOrigins) != 0 {
		t.Errorf("origins = %v, want none", c.DevCORSOrigins)
	}
}

// An entry that is not an origin would be an allowlist line that silently
// matches nothing, which is worse than a boot failure: the dashboard fails in
// the browser and the configuration looks correct.
func TestMalformedDevCORSOriginIsRejected(t *testing.T) {
	for _, bad := range []string{"localhost:3100", "http://localhost:3100/", "http://localhost:3100/app", "*"} {
		if _, err := Load(devEnv(map[string]string{"OBS_DEV_CORS_ORIGINS": bad})); err == nil {
			t.Errorf("Load accepted origin %q", bad)
		}
	}
}

// Production has no business holding a browser origin policy: it is a gateway's
// job there, and a second place for one is a second place to get it wrong.
func TestDevCORSIsRefusedInProduction(t *testing.T) {
	_, err := Load(devEnv(map[string]string{
		"OBS_ENV":                  "production",
		"OBS_DATABASE_URL":         "postgres://klaro_obs_app:x@db:5432/klaro_obs?sslmode=require",
		"OBS_MIGRATE_DATABASE_URL": "postgres://klaro:x@db:5432/klaro_obs?sslmode=require",
		"OBS_DEV_CORS_ORIGINS":     "http://localhost:3100",
	}))
	if err == nil {
		t.Fatal("the production profile accepted OBS_DEV_CORS_ORIGINS")
	}
}
