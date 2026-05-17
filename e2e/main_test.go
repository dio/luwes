// Package e2e runs integration tests against a real Envoy binary.
//
// TestMain builds a combined .so from all e2e filters (header-auth,
// header-auth-sahl, sse-tap), starts a mock SSE upstream, starts Envoy,
// and tears everything down when done.
//
// Prerequisites:
//   - Envoy binary at .bin/envoy (run: make .bin/envoy) or set ENVOY_BIN
//
// Run:
//
//	make e2e
//
// Or manually:
//
//	ENVOY_BIN=.bin/envoy go test -C e2e -v -timeout=60s ./...
//
// Tests skip automatically when ENVOY_BIN is not present.
// Set LUWES_SKIP_BUILD=1 to reuse a previously built .so (faster iteration).
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
	"testing"
	"time"
)

const (
	// Port 10000: header-auth filter (raw luwes SDK)
	headerAuthAddr = "http://localhost:10000"
	// Port 10001: header-auth-sahl filter (sahl ergonomic layer)
	headerAuthSahlAddr = "http://localhost:10001"
	// Port 10002: sse-tap filter (sahl response observer)
	sseTapAddr = "http://localhost:10002"
	adminAddr  = "http://localhost:9901"
)

var (
	envoyCmd      *exec.Cmd
	projectRoot   string
	mockSSEServer *http.Server
)

func TestMain(m *testing.M) {
	_, file, _, _ := runtime.Caller(0)
	projectRoot = filepath.Dir(file)

	bin := envoyBin()
	if _, err := os.Stat(bin); err != nil {
		fmt.Fprintf(os.Stderr, "SKIP: envoy not found at %s (run: make .bin/envoy)\n", bin)
		os.Exit(0)
	}

	// Start mock SSE upstream before Envoy so the cluster is reachable on load.
	sseUpstreamPort := startMockSSEServer()
	fmt.Fprintf(os.Stderr, "e2e: mock SSE upstream listening on 127.0.0.1:%d\n", sseUpstreamPort)

	soPath := filepath.Join(projectRoot, "libe2e.so")

	if os.Getenv("LUWES_SKIP_BUILD") == "" {
		fmt.Fprintln(os.Stderr, "e2e: building libe2e.so ...")
		cmd := exec.Command("go", "build", "-trimpath", "-buildmode=c-shared", "-o", soPath, "./cmd")
		cmd.Dir = projectRoot
		cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: build failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "e2e: build OK")
	} else {
		if _, err := os.Stat(soPath); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: LUWES_SKIP_BUILD=1 but libe2e.so not found at %s\n", soPath)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "e2e: reusing existing libe2e.so (LUWES_SKIP_BUILD=1)")
	}

	cfgPath := writeEnvoyConfig(sseUpstreamPort)
	defer os.Remove(cfgPath)

	envoyCmd = exec.Command(bin,
		"-c", cfgPath,
		"--log-level", "warning",
		"--component-log-level", "dynamic_modules:info",
	)
	envoyCmd.Env = append(os.Environ(),
		"GODEBUG=cgocheck=0",
		"ENVOY_DYNAMIC_MODULES_SEARCH_PATH="+projectRoot,
	)
	envoyCmd.Stdout = os.Stderr
	envoyCmd.Stderr = os.Stderr

	if err := envoyCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: envoy start failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "e2e: envoy pid=%d\n", envoyCmd.Process.Pid)

	if !waitReady(15 * time.Second) {
		envoyCmd.Process.Kill()
		fmt.Fprintln(os.Stderr, "e2e: envoy not ready in time")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "e2e: envoy ready")

	code := m.Run()

	envoyCmd.Process.Kill()
	envoyCmd.Wait()
	if mockSSEServer != nil {
		mockSSEServer.Close()
	}
	os.Exit(code)
}

func envoyBin() string {
	if b := os.Getenv("ENVOY_BIN"); b != "" {
		return b
	}
	return filepath.Join(projectRoot, "..", ".bin", "envoy")
}

func waitReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(adminAddr + "/ready")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func writeEnvoyConfig(sseUpstreamPort int) string {
	cfg := fmt.Sprintf(`
static_resources:
  listeners:
    # Port 10000: header-auth (raw luwes SDK)
    - name: header-auth
      address:
        socket_address: { address: 0.0.0.0, port_value: 10000 }
      filter_chains:
        - filters:
            - name: envoy.filters.network.http_connection_manager
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
                stat_prefix: header_auth
                http_filters:
                  - name: header-auth
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_modules.v3.DynamicModuleFilter
                      dynamic_module_config:
                        name: e2e
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

    # Port 10001: header-auth-sahl (sahl ergonomic layer)
    - name: header-auth-sahl
      address:
        socket_address: { address: 0.0.0.0, port_value: 10001 }
      filter_chains:
        - filters:
            - name: envoy.filters.network.http_connection_manager
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
                stat_prefix: header_auth_sahl
                http_filters:
                  - name: header-auth-sahl
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_modules.v3.DynamicModuleFilter
                      dynamic_module_config:
                        name: e2e
                      filter_name: header-auth-sahl
                      filter_config:
                        "@type": type.googleapis.com/google.protobuf.StringValue
                        value: '{}'
                  - name: envoy.filters.http.router
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
                route_config:
                  name: sahl_local
                  virtual_hosts:
                    - name: local
                      domains: ["*"]
                      routes:
                        - match: { prefix: "/" }
                          direct_response:
                            status: 200
                            body: { inline_string: "ok" }

    # Port 10002: sse-tap (sahl response observer, proxies to mock SSE upstream)
    - name: sse-tap
      address:
        socket_address: { address: 0.0.0.0, port_value: 10002 }
      filter_chains:
        - filters:
            - name: envoy.filters.network.http_connection_manager
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
                stat_prefix: sse_tap
                http_filters:
                  - name: sse-tap
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_modules.v3.DynamicModuleFilter
                      dynamic_module_config:
                        name: e2e
                      filter_name: sse-tap
                  - name: envoy.filters.http.router
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
                route_config:
                  name: sse_tap
                  virtual_hosts:
                    - name: sse_upstream
                      domains: ["*"]
                      routes:
                        - match: { prefix: "/" }
                          route:
                            cluster: sse_upstream
                            timeout: 30s

    # Port 10003: auth (RegisterFactory, admin listener -- key-admin, key-ops)
    - name: auth-admin
      address:
        socket_address: { address: 0.0.0.0, port_value: 10003 }
      filter_chains:
        - filters:
            - name: envoy.filters.network.http_connection_manager
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
                stat_prefix: auth_admin
                http_filters:
                  - name: auth
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_modules.v3.DynamicModuleFilter
                      dynamic_module_config:
                        name: e2e
                      filter_name: auth
                      filter_config:
                        "@type": type.googleapis.com/google.protobuf.StringValue
                        value: '{"allowed_keys":["key-admin","key-ops"],"header":"x-api-key","metadata_ns":"auth.admin"}'
                  - name: envoy.filters.http.router
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
                route_config:
                  name: auth_admin
                  virtual_hosts:
                    - name: local
                      domains: ["*"]
                      routes:
                        - match: { prefix: "/" }
                          direct_response:
                            status: 200
                            body: { inline_string: "admin ok" }

    # Port 10004: auth (RegisterFactory, user listener -- key-user, key-guest)
    - name: auth-user
      address:
        socket_address: { address: 0.0.0.0, port_value: 10004 }
      filter_chains:
        - filters:
            - name: envoy.filters.network.http_connection_manager
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
                stat_prefix: auth_user
                http_filters:
                  - name: auth
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.dynamic_modules.v3.DynamicModuleFilter
                      dynamic_module_config:
                        name: e2e
                      filter_name: auth
                      filter_config:
                        "@type": type.googleapis.com/google.protobuf.StringValue
                        value: '{"allowed_keys":["key-user","key-guest"],"header":"x-api-key","metadata_ns":"auth.user"}'
                  - name: envoy.filters.http.router
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
                route_config:
                  name: auth_user
                  virtual_hosts:
                    - name: local
                      domains: ["*"]
                      routes:
                        - match: { prefix: "/" }
                          direct_response:
                            status: 200
                            body: { inline_string: "user ok" }

  clusters:
    - name: sse_upstream
      connect_timeout: 5s
      type: STATIC
      load_assignment:
        cluster_name: sse_upstream
        endpoints:
          - lb_endpoints:
              - endpoint:
                  address:
                    socket_address: { address: 127.0.0.1, port_value: %d }

admin:
  address:
    socket_address: { address: 127.0.0.1, port_value: 9901 }
`, sseUpstreamPort)

	f, err := os.CreateTemp("", "luwes-e2e-*.yaml")
	if err != nil {
		panic(err)
	}
	f.WriteString(cfg)
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
