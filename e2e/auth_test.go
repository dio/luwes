package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// Port 10003: auth filter, admin config (key-admin, key-ops)
	authAdminAddr = "http://localhost:10003"
	// Port 10004: auth filter, user config (key-user, key-guest)
	authUserAddr = "http://localhost:10004"
)

// TestAuth_AdminKey_Allowed checks that a key in the admin allowed list passes
// through the admin listener and gets a 200 with the injected header present.
func TestAuth_AdminKey_Allowed(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, authAdminAddr+"/", nil)
	req.Header.Set("x-api-key", "key-admin")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "key-admin", resp.Request.Header.Get("x-api-key"),
		"request should have carried the original key header")
}

// TestAuth_AdminKey_Rejected_OnUserListener checks that a key valid on the
// admin listener is rejected by the user listener. This is the core
// per-listener isolation invariant.
func TestAuth_AdminKey_Rejected_OnUserListener(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, authUserAddr+"/", nil)
	req.Header.Set("x-api-key", "key-admin")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(body, &errResp))
	assert.Equal(t, "invalid x-api-key", errResp["error"])
}

// TestAuth_UserKey_Allowed checks that a key in the user allowed list passes
// through the user listener.
func TestAuth_UserKey_Allowed(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, authUserAddr+"/", nil)
	req.Header.Set("x-api-key", "key-user")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestAuth_UserKey_Rejected_OnAdminListener is the symmetric counterpart:
// user key on admin listener must be rejected.
func TestAuth_UserKey_Rejected_OnAdminListener(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, authAdminAddr+"/", nil)
	req.Header.Set("x-api-key", "key-user")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	var errResp map[string]string
	require.NoError(t, json.Unmarshal(body, &errResp))
	assert.Equal(t, "invalid x-api-key", errResp["error"])
}

// TestAuth_MissingKey_BothListeners confirms both listeners reject requests
// with no x-api-key header.
func TestAuth_MissingKey_BothListeners(t *testing.T) {
	for _, addr := range []string{authAdminAddr, authUserAddr} {
		resp, err := http.Get(addr + "/")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "addr=%s", addr)
		body, _ := io.ReadAll(resp.Body)
		var errResp map[string]string
		require.NoError(t, json.Unmarshal(body, &errResp), "addr=%s", addr)
		assert.Equal(t, "missing x-api-key", errResp["error"], "addr=%s", addr)
	}
}

// TestAuth_GuestKey_Allowed checks the second user-listener key (key-guest).
func TestAuth_GuestKey_Allowed(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, authUserAddr+"/", nil)
	req.Header.Set("x-api-key", "key-guest")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
