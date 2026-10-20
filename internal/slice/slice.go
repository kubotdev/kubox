package slice

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Slice struct {
	Name          string        `yaml:"name"`
	App           App           `yaml:"app"`
	Dependencies  []Dependency  `yaml:"dependencies"`
	Quotas        Quotas        `yaml:"quotas"`
	NetworkPolicy NetworkPolicy `yaml:"networkPolicy"`
	Tests         []TestDef     `yaml:"tests"`
	Toolbox       Toolbox       `yaml:"toolbox"`
}

type App struct {
	Image       string            `yaml:"image"`
	Replicas    *int              `yaml:"replicas"`
	Port        int               `yaml:"port"`
	Env         map[string]string `yaml:"env"`
	Build       *Build            `yaml:"build"`
	Resources   Resources         `yaml:"resources"`
	Scaling     *Scaling          `yaml:"scaling"`
	HealthCheck *HealthCheck      `yaml:"healthcheck"`
}

type Scaling struct {
	Min        int `yaml:"min"`
	Max        int `yaml:"max"`
	CPUPercent int `yaml:"cpuPercent"`
}

type HealthCheck struct {
	Path                string `yaml:"path"`
	InitialDelaySeconds int    `yaml:"initialDelaySeconds"`
}

type Resources struct {
	RequestsCPU    string `yaml:"requestsCPU"`
	RequestsMemory string `yaml:"requestsMemory"`
	LimitsCPU      string `yaml:"limitsCPU"`
	LimitsMemory   string `yaml:"limitsMemory"`
}

type Build struct {
	Context string `yaml:"context"`
}

type Dependency struct {
	Name      string            `yaml:"name"`
	Image     string            `yaml:"image"`
	Port      int               `yaml:"port"`
	Env       map[string]string `yaml:"env"`
	Resources Resources         `yaml:"resources"`
	Volume    *Volume           `yaml:"volume"`
}

type Volume struct {
	Size string `yaml:"size"`
	Path string `yaml:"path"`
}

type Quotas struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
	Pods   string `yaml:"pods"`
}

type NetworkPolicy struct {
	Default string `yaml:"default"`
}

type TestDef struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

type Toolbox struct {
	Image string `yaml:"image"`
}

const DefaultToolboxImage = "alpine/k8s:1.30.2"

func Load(dir string) (*Slice, string, error) {
	path := dir
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		path = filepath.Join(dir, "kubox.yaml")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read slice: %w", err)
	}
	var s Slice
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, "", fmt.Errorf("parse kubox.yaml: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, "", err
	}
	return &s, filepath.Dir(path), nil
}

func (s *Slice) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("kubox.yaml: name is required")
	}
	if s.App.Image == "" && s.App.Build == nil {
		return fmt.Errorf("kubox.yaml: app.image (or app.build) is required")
	}
	if s.App.Port == 0 {
		s.App.Port = 8080
	}
	if s.App.Replicas == nil {
		one := 1
		s.App.Replicas = &one
	}
	if s.Quotas.CPU == "" {
		s.Quotas.CPU = "2"
	}
	if s.Quotas.Memory == "" {
		s.Quotas.Memory = "4Gi"
	}
	if s.Quotas.Pods == "" {
		s.Quotas.Pods = "20"
	}
	if s.NetworkPolicy.Default == "" {
		s.NetworkPolicy.Default = "deny-all"
	}
	if s.Toolbox.Image == "" {
		s.Toolbox.Image = DefaultToolboxImage
	}
	s.App.Resources = s.App.Resources.withDefaults()
	for i := range s.Dependencies {
		s.Dependencies[i].Resources = s.Dependencies[i].Resources.withDefaults()
	}
	if s.App.Scaling != nil {
		if s.App.Scaling.Min == 0 {
			s.App.Scaling.Min = 1
		}
		if s.App.Scaling.Min < 1 {
			return fmt.Errorf("kubox.yaml: app.scaling.min must be >= 1, got %d", s.App.Scaling.Min)
		}
		if s.App.Scaling.Max == 0 {
			return fmt.Errorf("kubox.yaml: app.scaling.max is required")
		}
		if s.App.Scaling.Max < s.App.Scaling.Min {
			return fmt.Errorf("kubox.yaml: app.scaling.max (%d) must be >= min (%d)", s.App.Scaling.Max, s.App.Scaling.Min)
		}
		if s.App.Scaling.CPUPercent == 0 {
			s.App.Scaling.CPUPercent = 70
		}
		if s.App.Scaling.CPUPercent < 1 || s.App.Scaling.CPUPercent > 100 {
			return fmt.Errorf("kubox.yaml: app.scaling.cpuPercent must be 1-100, got %d", s.App.Scaling.CPUPercent)
		}
	}
	if s.App.HealthCheck != nil {
		if s.App.HealthCheck.Path == "" {
			return fmt.Errorf("kubox.yaml: app.healthcheck.path is required")
		}
		if s.App.HealthCheck.Path[0] != '/' {
			return fmt.Errorf("kubox.yaml: app.healthcheck.path must start with '/', got %q", s.App.HealthCheck.Path)
		}
		if s.App.HealthCheck.InitialDelaySeconds == 0 {
			s.App.HealthCheck.InitialDelaySeconds = 5
		}
	}
	for i, d := range s.Dependencies {
		if d.Name == "" {
			return fmt.Errorf("kubox.yaml: dependencies[%d].name is required", i)
		}
		if d.Image == "" {
			return fmt.Errorf("kubox.yaml: dependencies[%d] %q needs an image", i, d.Name)
		}
		if d.Volume != nil {
			if d.Volume.Size == "" {
				return fmt.Errorf("kubox.yaml: dependencies[%d] %q volume.size is required", i, d.Name)
			}
			if d.Volume.Path == "" {
				return fmt.Errorf("kubox.yaml: dependencies[%d] %q volume.path is required", i, d.Name)
			}
		}
	}
	return nil
}

func (s *Slice) AppServiceName() string { return s.Name }

func (s *Slice) AppEndpoint() string {
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", s.AppServiceName(), EnvNamespace(s.Name), s.App.Port)
}

const (
	DefaultRequestsCPU    = "50m"
	DefaultRequestsMemory = "64Mi"
)

func (r Resources) withDefaults() Resources {
	if r.RequestsCPU == "" {
		r.RequestsCPU = DefaultRequestsCPU
	}
	if r.RequestsMemory == "" {
		r.RequestsMemory = DefaultRequestsMemory
	}
	return r
}

func (r Resources) IsZero() bool { return r.LimitsCPU == "" && r.LimitsMemory == "" }

func EnvNamespace(envName string) string {
	return "kubox-" + envName
}

// DefaultTTL is the fallback time-to-live when none is given.
const DefaultTTL = time.Hour

func ParseTTL(s string) (time.Duration, error) {
	if s == "" {
		return DefaultTTL, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid ttl %q: %w", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("ttl must be positive, got %s", d)
	}
	return d, nil
}
