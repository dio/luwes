//go:build e2e

// Package e2e contains integration tests that run a real Envoy process
// against filters compiled with luwes.
//
// Prerequisites:
//   - Envoy binary: ~/.local/share/boe/envoy-versions/1.37.1/bin/envoy
//   - CGO_ENABLED=1, zig in PATH (or .bin/zig from make)
//
// Build the .so and run:
//
//	cd .. && make build EXAMPLE=header-auth
//	cd e2e && LUWES_SO=../dist/libheader-auth.so go test -tags=e2e -v -run TestHeaderAuth ./...
//
// The ENVOY_BIN env var overrides the default Envoy path.
package e2e

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	envoyAddr = "http://localhost:10000"
	adminAddr = "http://localhost:9901"
)

var (
	projectRoot string
	envoyCmd    *exec.Cmd
	soPath      string
)

func envoyBin() string {
	if b := os.Getenv("ENVOY_BIN"); b != "" {
		return b
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/share/boe/envoy-versions/1.37.1/bin/envoy")
}

func TestMain(m *testing.M) {
	_, file, _, _ := runtime.Caller(0)
	projectRoot = filepath.Dir(file)

	soPath = os.Getenv("LUWES_SO")
	if soPath == "" {
		soPath = filepath.Join(projectRoot, "..", "dist", "libheader-auth.so")
	}
	if _, err := os.Stat(soPath); err != nil {
		fmt.Fprintf(os.Stderr, "SKIP: .so not found at %s -- run: make build EXAMPLE=header-auth\n", soPath)
		os.Exit(0)
	}

	soDir := filepath.Dir(soPath)
	cfgPath := writeEnvoyConfig(soDir)
	defer os.Remove(cfgPath)

	envoyCmd = exec.Command(envoyBin(),
		"-c", cfgPath,
		"--log-level", "warning",
	)
	envoyCmd.Env = append(os.Environ(),
		"GODEBUG=cgocheck=0",
		"ENVOY_DYNAMIC_MODULES_SEARCH_PATH="+soDir,
	)
	envoyCmd.Stdout = os.Stdout
	envoyCmd.Stderr = os.Stderr

	if err := envoyCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start Envoy: %v\n", err)
		os.Exit(1)
	}

	waitForEnvoy(adminAddr, 15*time.Second)

	code := m.Run()

	envoyCmd.Process.Kill()
	envoyCmd.Wait()
	os.Exit(code)
}

func waitForEnvoy(addr string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(addr + "/ready")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	panic("Envoy did not become ready within " + timeout.String())
}

// writeEnvoyConfig writes a minimal envoy.yaml to a temp file and returns its path.
func writeEnvoyConfig(soDir string) string {
	// Extract just the .so name without lib prefix and .so suffix for dynamic_module_config.name
	base := filepath.Base(soPath)
	base = strings.TrimPrefix(base, "lib")
	base = strings.TrimSuffix(base, ".so")

	cfg := fmt.Sprintf(`
static_resources:
  listeners:
    - name: listener_0
      address:
        socket_address: { address: 0.0.0.0, port_value: 10000 }
      filter_chains:
        - filters:
            - name: envoy.filters.network.http_connection_manager
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
                stat_prefix: ingress
                http_filters:
                  - name: %s
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_modules.v3.DynamicModuleFilter
                      dynamic_module_config:
                        name: %s
                      filter_name: header-auth
                      filter_config:
                        "@type": type.googleapis.com/google.protobuf.StringValue
                        value: '{}'
                  - name: envoy.filters.http.router
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
                route_config:
                  name: local
                  virtual_hosts:
                    - name: local
                      domains: ["*"]
                      routes:
                        - match: { prefix: "/" }
                          direct_response:
                            status: 200
                            body: { inline_string: "ok" }
admin:
  address:
    socket_address: { address: 127.0.0.1, port_value: 9901 }
`, base, base)

	f, err := os.CreateTemp("", "luwes-e2e-*.yaml")
	if err != nil {
		panic(err)
	}
	if _, err := f.WriteString(cfg); err != nil {
		panic(err)
	}
	f.Close()
	return f.Name()
}

// helpers

func mustDo(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func freePort() int {
	l, _ := net.Listen("tcp", ":0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
