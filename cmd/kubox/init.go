package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

const sliceTemplate = `name: %s
app:
  image: %s:v1          # the tag ` + "`kubox build`" + ` produces, or any registry image
  port: 8080
  # build:              # optional: build the image locally instead of pulling
  #   context: app      # directory with the Dockerfile, relative to this slice
  healthcheck:
    path: /healthz      # readiness + liveness probes; adjust or drop
dependencies: []
  # - name: postgres
  #   image: postgres:16-alpine
  #   port: 5432
  #   env: {POSTGRES_PASSWORD: kubox}
quotas:
  cpu: "2"
  memory: 2Gi
  pods: "10"
`

const bt = "`"

var loadScriptTemplate = `import http from 'k6/http';
import {check, sleep} from 'k6';

export const options = {
  stages: [
    {duration: '10s', target: 5},
    {duration: '20s', target: 5},
    {duration: '5s', target: 0},
  ],
  thresholds: {
    http_req_duration: ['p(95)<300'],
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

export default function () {
  const res = http.get(` + bt + `${__ENV.BASE_URL}/` + bt + `);
  check(res, {'status is 200': (r) => r.status === 200});
  sleep(0.5);
}
`

func newInitCmd() *cobra.Command {
	var name string
	var force bool
	cmd := &cobra.Command{
		Use:   "init <dir>",
		Short: "Scaffold a new slice: kubox.yaml + a starter k6 load test",
		Long: "Creates <dir>/kubox.yaml (a minimal slice: app, healthcheck, " +
			"quotas) and <dir>/tests/load.js (a k6 script with pass/fail " +
			"thresholds). Both are plain text — edit them to match your app.\n\n" +
			"The slice references an image; point app.build.context at a " +
			"Dockerfile directory and `kubox build` will produce that tag, " +
			"or use any image from a registry.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			if name == "" {
				name = sanitizeName(filepath.Base(dir))
			}
			slicePath := filepath.Join(dir, "kubox.yaml")
			scriptPath := filepath.Join(dir, "tests", "load.js")
			if !force {
				for _, p := range []string{slicePath, scriptPath} {
					if _, err := os.Stat(p); err == nil {
						return usageErrf("%s already exists (use --force to overwrite)", p)
					}
				}
			}
			if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
				return fmt.Errorf("create %s: %w", filepath.Dir(scriptPath), err)
			}
			if err := os.WriteFile(slicePath, []byte(fmt.Sprintf(sliceTemplate, name, name)), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", slicePath, err)
			}
			if err := os.WriteFile(scriptPath, []byte(loadScriptTemplate), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", scriptPath, err)
			}
			fmt.Printf("created %s\ncreated %s\n\n", slicePath, scriptPath)
			fmt.Printf("next:\n  kubox up %s --name %s --ttl 1h\n", dir, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "",
		"slice name (default: sanitized directory name)")
	cmd.Flags().BoolVar(&force, "force", false,
		"overwrite existing files")
	return cmd
}

var invalidNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	s = invalidNameChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	if s == "" {
		s = "app"
	}
	return s
}
