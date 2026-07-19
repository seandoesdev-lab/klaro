package scenario

import (
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/klaro/load-test/internal/model"
)

var ErrInvalid = errors.New("invalid scenario")

var allowedMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true,
}

func Validate(s model.Scenario) error {
	if s.VU <= 0 {
		return fmt.Errorf("%w: vu must be > 0", ErrInvalid)
	}
	if s.DurationSec <= 0 {
		return fmt.Errorf("%w: duration_sec must be > 0", ErrInvalid)
	}
	if s.RampUpSec < 0 {
		return fmt.Errorf("%w: ramp_up_sec must be >= 0", ErrInvalid)
	}
	if len(s.Steps) == 0 {
		return fmt.Errorf("%w: steps must not be empty", ErrInvalid)
	}
	for i, st := range s.Steps {
		if !allowedMethods[strings.ToUpper(st.Method)] {
			return fmt.Errorf("%w: step %d method %q not allowed", ErrInvalid, i, st.Method)
		}
		if !strings.HasPrefix(st.Path, "/") {
			return fmt.Errorf("%w: step %d path must start with /", ErrInvalid, i)
		}
	}
	return nil
}

type tmplData struct {
	RampUpSec  int
	PlateauSec int
	VU         int
	P95Ms      int
	ErrorRate  float64
	Steps      []model.Step
}

const k6Template = `import http from 'k6/http';
import { sleep } from 'k6';

export const options = {
  stages: [
    { duration: '{{.RampUpSec}}s', target: {{.VU}} },
    { duration: '{{.PlateauSec}}s', target: {{.VU}} },
  ],
  thresholds: {
    http_req_duration: ['p(95)<{{.P95Ms}}'],
    http_req_failed: ['rate<{{.ErrorRate}}'],
  },
};

export default function () {
{{- range .Steps}}
  http.request('{{.Method}}', __ENV.TARGET + '{{.Path}}', {{if .Body}}JSON.stringify({{printf "%s" .Body}}){{else}}null{{end}}, { tags: { step: '{{.Path}}' } });
{{- end}}
  sleep(1);
}
`

// Generate returns a k6 JS script for the scenario. The target base URL is
// passed to k6 at runtime via the TARGET environment variable.
func Generate(s model.Scenario) (string, error) {
	if err := Validate(s); err != nil {
		return "", err
	}
	p95 := s.Thresholds.HTTPReqDurationP95Ms
	if p95 <= 0 {
		p95 = 2000
	}
	rate := s.Thresholds.ErrorRate
	if rate <= 0 {
		rate = 0.05
	}
	plateau := s.DurationSec - s.RampUpSec
	if plateau < 0 {
		plateau = 0
	}
	steps := make([]model.Step, len(s.Steps))
	for i, st := range s.Steps {
		st.Method = strings.ToUpper(st.Method)
		steps[i] = st
	}
	data := tmplData{
		RampUpSec: s.RampUpSec, PlateauSec: plateau, VU: s.VU,
		P95Ms: p95, ErrorRate: rate, Steps: steps,
	}
	t, err := template.New("k6").Parse(k6Template)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}
