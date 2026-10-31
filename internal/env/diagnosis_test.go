package env

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

// kubotExec stubs the toolbox: it asserts the exact kubot command shape and
// returns the canned output.
func kubotExec(stdout string, exitCode int, runErr error) func(context.Context, string, []string) (*ExecResult, error) {
	return func(_ context.Context, _ string, command []string) (*ExecResult, error) {
		joined := strings.Join(command, " ")
		if !strings.HasPrefix(joined, "kubot inspect -n ") || !strings.HasSuffix(joined, " --json") {
			return nil, errors.New("unexpected command: " + joined)
		}
		if runErr != nil {
			return nil, runErr
		}
		return &ExecResult{ExitCode: exitCode, Stdout: stdout}, nil
	}
}

func TestKubotDiagnosisCriticalFails(t *testing.T) {
	m := NewManager(fake.NewSimpleClientset(), nil, "")
	stdout := `{"schema_version":"1","status":"critical","issues":[` +
		`{"severity":"critical","resource":"pod/shop-1","reason":"pod_crashloop","message":"pod shop-1 is crash looping","recommendation":"check logs"},` +
		`{"severity":"warning","resource":"deployment/shop","reason":"memory_near_limit","message":"memory at 85% of limit"}]}`
	m.execFn = kubotExec(stdout, 0, nil)

	c := m.RunKubotDiagnosis(context.Background(), "shop")
	if c.Type != CheckTypeDiagnosis {
		t.Errorf("type = %q, want %q", c.Type, CheckTypeDiagnosis)
	}
	if c.Status != CheckFail {
		t.Errorf("status = %s, want fail on a critical finding", c.Status)
	}
	if len(c.Findings) != 2 || c.Findings[0].Metric != "pod_crashloop" {
		t.Errorf("findings = %+v, want both issues with stable reasons", c.Findings)
	}
	var rep kubotReport
	if err := json.Unmarshal(c.Detail, &rep); err != nil {
		t.Fatalf("detail does not decode as a kubot report: %v", err)
	}
	if rep.Status != "critical" || len(rep.Issues) != 2 {
		t.Errorf("detail = %+v, want the kubot report verbatim", rep)
	}
}

func TestKubotDiagnosisWarningsDoNotGate(t *testing.T) {
	m := NewManager(fake.NewSimpleClientset(), nil, "")
	stdout := `{"schema_version":"1","status":"warning","issues":[` +
		`{"severity":"warning","resource":"hpa/shop","reason":"hpa_at_max","message":"HPA at max replicas"}],` +
		`"degraded":["nodes"]}`
	m.execFn = kubotExec(stdout, 0, nil)

	c := m.RunKubotDiagnosis(context.Background(), "shop")
	if c.Status != CheckPass {
		t.Errorf("status = %s, want pass — only critical findings gate", c.Status)
	}
	if len(c.Findings) != 2 {
		t.Fatalf("findings = %+v, want the warning plus the degraded note", c.Findings)
	}
	if !strings.Contains(c.Findings[1].Message, "degraded") {
		t.Errorf("second finding = %+v, want degraded-collector note", c.Findings[1])
	}
}

func TestKubotDiagnosisSuppressedCriticalPasses(t *testing.T) {
	m := NewManager(fake.NewSimpleClientset(), nil, "")
	m.execFn = kubotExec(
		`{"schema_version":"1","status":"critical","issues":[`+
			`{"severity":"critical","reason":"muted_thing","message":"muted by .kubot.toml","suppressed":true}]}`,
		0, nil)

	c := m.RunKubotDiagnosis(context.Background(), "shop")
	if c.Status != CheckPass {
		t.Errorf("status = %s, want pass — suppressed findings never gate", c.Status)
	}
	if len(c.Findings) != 0 {
		t.Errorf("findings = %+v, want none", c.Findings)
	}
}

func TestKubotDiagnosisUnavailableSkips(t *testing.T) {
	m := NewManager(fake.NewSimpleClientset(), nil, "")
	m.execFn = kubotExec("", 0, errors.New("exec: command not found"))

	c := m.RunKubotDiagnosis(context.Background(), "shop")
	if c.Status != CheckSkipped {
		t.Errorf("status = %s, want skipped when kubot is unavailable", c.Status)
	}
	if len(c.Findings) == 0 || !strings.Contains(c.Findings[0].Message, "unavailable") {
		t.Errorf("findings = %+v, want an unavailable note", c.Findings)
	}
}

func TestKubotDiagnosisUnparseableSkips(t *testing.T) {
	m := NewManager(fake.NewSimpleClientset(), nil, "")
	m.execFn = kubotExec("Error: dial tcp 10.96.0.1:443: connect: connection refused", 3, nil)

	c := m.RunKubotDiagnosis(context.Background(), "shop")
	if c.Status != CheckSkipped {
		t.Errorf("status = %s, want skipped on unparseable output", c.Status)
	}
	if len(c.Findings) == 0 || !strings.Contains(c.Findings[0].Message, "unparseable") {
		t.Errorf("findings = %+v, want an unparseable note", c.Findings)
	}
}
