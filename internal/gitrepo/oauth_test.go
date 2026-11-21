package gitrepo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func withFakeGitHubDeviceFlow(t *testing.T, deviceResp map[string]interface{}, tokenResp map[string]interface{}) {
	t.Helper()

	t.Setenv("PAISA_GITHUB_OAUTH_CLIENT_ID", "test-client-id")

	deviceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceResp)
	}))
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(tokenResp)
	}))

	origDevice, origToken := githubDeviceCodeURL, githubAccessTokenURL
	githubDeviceCodeURL = deviceServer.URL
	githubAccessTokenURL = tokenServer.URL

	t.Cleanup(func() {
		deviceServer.Close()
		tokenServer.Close()
		githubDeviceCodeURL = origDevice
		githubAccessTokenURL = origToken
	})
}

func TestStartDeviceFlow_GitHub(t *testing.T) {
	withFakeGitHubDeviceFlow(t, map[string]interface{}{
		"device_code":      "devcode123",
		"user_code":        "ABCD-1234",
		"verification_uri": "https://github.com/login/device",
		"expires_in":       900,
		"interval":         5,
	}, nil)

	session, sessionID, err := StartDeviceFlow(ProviderGitHub)
	assert.NoError(t, err)
	assert.NotEmpty(t, sessionID)
	assert.Equal(t, "ABCD-1234", session.UserCode)
	assert.Equal(t, "devcode123", session.DeviceCode)
	assert.Equal(t, "https://github.com/login/device", session.VerificationURI)

	t.Cleanup(func() { ConsumeSession(sessionID) })
}

func TestPollDeviceFlow_Pending(t *testing.T) {
	withFakeGitHubDeviceFlow(t, map[string]interface{}{
		"device_code": "devcode123", "user_code": "ABCD-1234",
		"verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5,
	}, map[string]interface{}{
		"error": "authorization_pending",
	})

	_, sessionID, err := StartDeviceFlow(ProviderGitHub)
	assert.NoError(t, err)
	defer ConsumeSession(sessionID)

	status, err := PollDeviceFlow(sessionID)
	assert.NoError(t, err)
	assert.Equal(t, "pending", status)
}

func TestPollDeviceFlow_SlowDown(t *testing.T) {
	withFakeGitHubDeviceFlow(t, map[string]interface{}{
		"device_code": "devcode123", "user_code": "ABCD-1234",
		"verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5,
	}, map[string]interface{}{
		"error": "slow_down",
	})

	_, sessionID, err := StartDeviceFlow(ProviderGitHub)
	assert.NoError(t, err)
	defer ConsumeSession(sessionID)

	status, err := PollDeviceFlow(sessionID)
	assert.NoError(t, err)
	assert.Equal(t, "slow_down", status)
}

func TestPollDeviceFlow_ExpiredToken(t *testing.T) {
	withFakeGitHubDeviceFlow(t, map[string]interface{}{
		"device_code": "devcode123", "user_code": "ABCD-1234",
		"verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5,
	}, map[string]interface{}{
		"error": "expired_token",
	})

	_, sessionID, err := StartDeviceFlow(ProviderGitHub)
	assert.NoError(t, err)
	defer ConsumeSession(sessionID)

	status, err := PollDeviceFlow(sessionID)
	assert.NoError(t, err)
	assert.Equal(t, "expired", status)
}

func TestPollDeviceFlow_AccessDenied(t *testing.T) {
	withFakeGitHubDeviceFlow(t, map[string]interface{}{
		"device_code": "devcode123", "user_code": "ABCD-1234",
		"verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5,
	}, map[string]interface{}{
		"error": "access_denied",
	})

	_, sessionID, err := StartDeviceFlow(ProviderGitHub)
	assert.NoError(t, err)
	defer ConsumeSession(sessionID)

	status, err := PollDeviceFlow(sessionID)
	assert.NoError(t, err)
	assert.Equal(t, "denied", status)
}

func TestPollDeviceFlow_Success(t *testing.T) {
	withFakeGitHubDeviceFlow(t, map[string]interface{}{
		"device_code": "devcode123", "user_code": "ABCD-1234",
		"verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5,
	}, map[string]interface{}{
		"access_token": "gho_faketoken",
	})

	_, sessionID, err := StartDeviceFlow(ProviderGitHub)
	assert.NoError(t, err)
	defer ConsumeSession(sessionID)

	status, err := PollDeviceFlow(sessionID)
	assert.NoError(t, err)
	assert.Equal(t, "success", status)

	_, token, ok := GetSessionToken(sessionID)
	assert.True(t, ok)
	assert.Equal(t, "gho_faketoken", token)
}

func TestPollDeviceFlow_UnknownSession(t *testing.T) {
	status, err := PollDeviceFlow("does-not-exist")
	assert.NoError(t, err)
	assert.Equal(t, "expired", status)
}

func TestStartDeviceFlow_NotConfigured(t *testing.T) {
	t.Setenv("PAISA_GITHUB_OAUTH_CLIENT_ID", "")
	t.Setenv("PAISA_GITLAB_OAUTH_CLIENT_ID", "")

	_, _, err := StartDeviceFlow(ProviderGitHub)
	assert.ErrorIs(t, err, errGitHubNotConfigured)

	_, _, err = StartDeviceFlow(ProviderGitLab)
	assert.ErrorIs(t, err, errGitLabNotConfigured)
}
