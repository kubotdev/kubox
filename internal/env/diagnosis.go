package env

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kubotdev/kubox/internal/slice"
)

// RunKubotDiagnosis runs `kubot inspect -n <ns> --json` inside the
// environment's toolbox and folds the findings into a non-gating diagnosis check
func (m *Manager) RunKubotDiagnosis(ctx context.Context, envName string) Check {
	start := time.Now()
	ns := slice.EnvNamespace(envName)
	res, err := m.runExec(ctx, envName, []string{"kubot", "inspect", "-n", ns, "--json"})
	if err != nil {
		return skippedDiagnosis(fmt.Sprintf("kubot unavailable: %v", err), time.Since(start))
	}
	rep, err := parseKubotReport(res.Stdout)
	if err != nil {
		return skippedDiagnosis(
			fmt.Sprintf("kubot output unparseable (exit %d): %v", res.ExitCode, err), time.Since(start))
	}

	check := Check{
		Type:       CheckTypeDiagnosis,
		Status:     CheckPass,
		DurationMS: time.Since(start).Milliseconds(),
	}
	critical := 0
	for _, is := range rep.Issues {
		if is.Suppressed {
			continue
		}
		if is.Severity == "critical" {
			critical++
		}
		check.Findings = append(check.Findings, Finding{
			Metric:  is.Reason,
			Actual:  is.Severity,
			Message: fmt.Sprintf("[%s] %s", is.Severity, is.Message),
		})
	}
	if len(rep.Degraded) > 0 {
		check.Findings = append(check.Findings, Finding{
			Message: "kubot collectors degraded: " + strings.Join(rep.Degraded, ", "),
		})
	}
	if critical > 0 {
		check.Status = CheckFail
	}
	if b, err := json.Marshal(rep); err == nil {
		check.Detail = b
	}
	return check
}

func skippedDiagnosis(msg string, d time.Duration) Check {
	return Check{
		Type:       CheckTypeDiagnosis,
		Status:     CheckSkipped,
		DurationMS: d.Milliseconds(),
		Findings:   []Finding{{Message: msg}},
	}
}

// Mirrors kubot's `inspect --json` schema (kubot/internal/model).
type kubotReport struct {
	SchemaVersion string       `json:"schema_version"`
	Status        string       `json:"status"` // ok|warning|critical
	Issues        []kubotIssue `json:"issues"`
	Degraded      []string     `json:"degraded,omitempty"`
}

type kubotIssue struct {
	Severity       string `json:"severity"`
	Reason         string `json:"reason"`
	Message        string `json:"message"`
	Recommendation string `json:"recommendation,omitempty"`
	Suppressed     bool   `json:"suppressed,omitempty"`
}

func parseKubotReport(stdout string) (*kubotReport, error) {
	raw := strings.TrimSpace(stdout)
	if !strings.HasPrefix(raw, "{") {
		i := strings.Index(raw, "{")
		j := strings.LastIndex(raw, "}")
		if i < 0 || j <= i {
			return nil, fmt.Errorf("no JSON object in output")
		}
		raw = raw[i : j+1]
	}
	var rep kubotReport
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		return nil, err
	}
	return &rep, nil
}
