package scenario

import (
	"errors"
	"strings"
	"testing"

	"github.com/klaro/load-test/internal/model"
)

func valid() model.Scenario {
	return model.Scenario{
		VU: 10, DurationSec: 30, RampUpSec: 5,
		Steps:      []model.Step{{Method: "GET", Path: "/health"}},
		Thresholds: model.Thresholds{HTTPReqDurationP95Ms: 2000, ErrorRate: 0.05},
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(valid()); err != nil {
		t.Fatalf("valid scenario rejected: %v", err)
	}
	bad := []model.Scenario{
		func() model.Scenario { s := valid(); s.VU = 0; return s }(),
		func() model.Scenario { s := valid(); s.DurationSec = 0; return s }(),
		func() model.Scenario { s := valid(); s.Steps = nil; return s }(),
		func() model.Scenario { s := valid(); s.Steps = []model.Step{{Method: "FLY", Path: "/x"}}; return s }(),
		func() model.Scenario { s := valid(); s.Steps = []model.Step{{Method: "GET", Path: "x"}}; return s }(),
	}
	for i, s := range bad {
		if err := Validate(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: expected ErrInvalid, got %v", i, err)
		}
	}
}

func TestGenerate(t *testing.T) {
	src, err := Generate(valid())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"export const options", "stages:", "http.request", "/health", "thresholds"} {
		if !strings.Contains(src, want) {
			t.Errorf("generated script missing %q", want)
		}
	}
}

func TestGenerateInvalidReturnsError(t *testing.T) {
	s := valid()
	s.VU = 0
	if _, err := Generate(s); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}
