package gitrepo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid"
)

type Provider string

const (
	ProviderGitHub Provider = "github"
	ProviderGitLab Provider = "gitlab"
	ProviderOther  Provider = "other"
)

// OAuth client ids for device-flow authorization are intentionally not
// hardcoded in source. Public clients using the device flow don't need a
// client secret, but the ids themselves are still deployment-specific: an
// operator (or a Paisa maintainer, for the officially hosted client) must
// register a GitHub OAuth App (with Device Flow enabled) and/or a GitLab
// Application, then supply the resulting client ids via environment
// variables. When unset, the corresponding provider's device flow is
// unavailable and StartDeviceFlow returns a clear error.
func githubClientID() string {
	return os.Getenv("PAISA_GITHUB_OAUTH_CLIENT_ID")
}

func gitlabClientID() string {
	return os.Getenv("PAISA_GITLAB_OAUTH_CLIENT_ID")
}

var (
	githubDeviceCodeURL    = "https://github.com/login/device/code"
	githubAccessTokenURL   = "https://github.com/login/oauth/access_token"
	gitlabDeviceCodeURL    = "https://gitlab.com/oauth/authorize_device"
	gitlabAccessTokenURL   = "https://gitlab.com/oauth/token"
	httpClient             = &http.Client{Timeout: 15 * time.Second}
	errUnsupportedProvider = errors.New("device flow is only supported for github and gitlab")
	errGitHubNotConfigured = errors.New("GitHub OAuth is not configured on this server (PAISA_GITHUB_OAUTH_CLIENT_ID is unset) — connect using a personal access token instead")
	errGitLabNotConfigured = errors.New("GitLab OAuth is not configured on this server (PAISA_GITLAB_OAUTH_CLIENT_ID is unset) — connect using a personal access token instead")
)

type DeviceFlowSession struct {
	Provider        Provider
	DeviceCode      string
	UserCode        string
	VerificationURI string
	Interval        int
	ExpiresAt       time.Time
	Token           string // populated once polling succeeds, consumed by the repo-picker finalize step
}

const sessionTTL = 15 * time.Minute

var (
	sessionsMu sync.Mutex
	sessions   = map[string]*DeviceFlowSession{}
)

func newSessionID() string {
	id, _ := uuid.NewV4()
	return id.String()
}

func putSession(s *DeviceFlowSession) string {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()

	pruneExpiredLocked()

	id := newSessionID()
	sessions[id] = s
	return id
}

func pruneExpiredLocked() {
	now := time.Now()
	for id, s := range sessions {
		if now.After(s.ExpiresAt) {
			delete(sessions, id)
		}
	}
}

// GetSessionToken returns the token stashed in an already-authenticated
// session (OAuth success or a manually validated PAT), used by the
// repository-picker endpoints.
func GetSessionToken(sessionID string) (Provider, string, bool) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()

	s, ok := sessions[sessionID]
	if !ok || s.Token == "" || time.Now().After(s.ExpiresAt) {
		return "", "", false
	}
	return s.Provider, s.Token, true
}

// ConsumeSession removes a session once it has been finalized (repo
// selected) or explicitly abandoned.
func ConsumeSession(sessionID string) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	delete(sessions, sessionID)
}

// PutTokenSession stashes a manually-provided PAT for provider under a new
// session, so it can flow through the same repository picker as OAuth.
func PutTokenSession(provider Provider, token string) string {
	return putSession(&DeviceFlowSession{
		Provider:  provider,
		Token:     token,
		ExpiresAt: time.Now().Add(sessionTTL),
	})
}

func StartDeviceFlow(provider Provider) (*DeviceFlowSession, string, error) {
	switch provider {
	case ProviderGitHub:
		return startGitHubDeviceFlow()
	case ProviderGitLab:
		return startGitLabDeviceFlow()
	default:
		return nil, "", errUnsupportedProvider
	}
}

func startGitHubDeviceFlow() (*DeviceFlowSession, string, error) {
	clientID := githubClientID()
	if clientID == "" {
		return nil, "", errGitHubNotConfigured
	}

	form := url.Values{"client_id": {clientID}, "scope": {"repo"}}
	req, err := http.NewRequest(http.MethodPost, githubDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	var body struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, "", err
	}
	if body.DeviceCode == "" {
		return nil, "", fmt.Errorf("unexpected response starting GitHub device flow")
	}

	session := &DeviceFlowSession{
		Provider:        ProviderGitHub,
		DeviceCode:      body.DeviceCode,
		UserCode:        body.UserCode,
		VerificationURI: body.VerificationURI,
		Interval:        body.Interval,
		ExpiresAt:       time.Now().Add(time.Duration(body.ExpiresIn) * time.Second),
	}
	return session, putSession(session), nil
}

func startGitLabDeviceFlow() (*DeviceFlowSession, string, error) {
	clientID := gitlabClientID()
	if clientID == "" {
		return nil, "", errGitLabNotConfigured
	}

	form := url.Values{"client_id": {clientID}, "scope": {"write_repository"}}
	req, err := http.NewRequest(http.MethodPost, gitlabDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	var body struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, "", err
	}
	if body.DeviceCode == "" {
		return nil, "", fmt.Errorf("unexpected response starting GitLab device flow")
	}

	session := &DeviceFlowSession{
		Provider:        ProviderGitLab,
		DeviceCode:      body.DeviceCode,
		UserCode:        body.UserCode,
		VerificationURI: body.VerificationURI,
		Interval:        body.Interval,
		ExpiresAt:       time.Now().Add(time.Duration(body.ExpiresIn) * time.Second),
	}
	return session, putSession(session), nil
}

// PollDeviceFlow polls the provider's token endpoint once. status is one of
// "pending", "success", "expired", "denied". On success the token is also
// stashed on the session for the repo-picker step to consume.
func PollDeviceFlow(sessionID string) (status string, err error) {
	sessionsMu.Lock()
	session, ok := sessions[sessionID]
	sessionsMu.Unlock()

	if !ok {
		return "expired", nil
	}
	if time.Now().After(session.ExpiresAt) {
		return "expired", nil
	}

	var token string
	var pollErr string

	switch session.Provider {
	case ProviderGitHub:
		token, pollErr, err = pollGitHub(session.DeviceCode)
	case ProviderGitLab:
		token, pollErr, err = pollGitLab(session.DeviceCode)
	default:
		return "", errUnsupportedProvider
	}
	if err != nil {
		return "", err
	}

	switch pollErr {
	case "":
		sessionsMu.Lock()
		session.Token = token
		sessionsMu.Unlock()
		return "success", nil
	case "authorization_pending":
		return "pending", nil
	case "slow_down":
		return "slow_down", nil
	case "expired_token":
		return "expired", nil
	case "access_denied":
		return "denied", nil
	default:
		return "", fmt.Errorf("device flow error: %s", pollErr)
	}
}

func pollGitHub(deviceCode string) (token string, pollErr string, err error) {
	form := url.Values{
		"client_id":   {githubClientID()},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequest(http.MethodPost, githubAccessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", err
	}

	return body.AccessToken, body.Error, nil
}

func pollGitLab(deviceCode string) (token string, pollErr string, err error) {
	form := url.Values{
		"client_id":   {gitlabClientID()},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequest(http.MethodPost, gitlabAccessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", err
	}

	return body.AccessToken, body.Error, nil
}
