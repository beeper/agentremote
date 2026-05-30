package connector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix/bridgev2"
)

func TestGetLoginFlowsIncludesChatGPTDeviceLogin(t *testing.T) {
	conn := &Connector{}
	flows := conn.GetLoginFlows()
	if len(flows) == 0 || flows[0].ID != loginFlowDefaultProvider {
		t.Fatalf("expected default Beeper AI login flow first, got %#v", flows)
	}
	found := false
	for _, flow := range flows {
		if flow.ID == loginFlowChatGPTDevice {
			found = flow.Name == "ChatGPT"
		}
	}
	if !found {
		t.Fatalf("expected ChatGPT device login flow in %#v", flows)
	}
	process, err := conn.CreateLogin(t.Context(), &bridgev2.User{}, loginFlowChatGPTDevice)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := process.(*ChatGPTDeviceLogin); !ok {
		t.Fatalf("expected ChatGPTDeviceLogin, got %T", process)
	}
}

func TestChatGPTDeviceLoginStartReturnsCodeStep(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/deviceauth/usercode" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"device_auth_id": "device-1",
			"user_code":      "ABCD-EFGH",
			"interval":       "0",
		})
	}))
	defer server.Close()
	restore := overrideChatGPTEndpoints(t, server.URL)
	defer restore()

	login := &ChatGPTDeviceLogin{}
	step, err := login.Start(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if step.Type != bridgev2.LoginStepTypeDisplayAndWait || step.StepID != chatGPTDeviceLoginStepID {
		t.Fatalf("unexpected step %#v", step)
	}
	if step.DisplayAndWaitParams == nil || step.DisplayAndWaitParams.Type != bridgev2.LoginDisplayTypeNothing || step.DisplayAndWaitParams.Data != "" {
		t.Fatalf("unexpected display params %#v", step.DisplayAndWaitParams)
	}
	if !strings.Contains(step.Instructions, "ABCD-EFGH") || !strings.Contains(step.Instructions, "/codex/device") {
		t.Fatalf("expected instructions to include device URL and code, got %q", step.Instructions)
	}
	if login.device.DeviceAuthID != "device-1" || login.device.Interval != 0 {
		t.Fatalf("device state was not retained: %#v", login.device)
	}
}

func TestChatGPTDeviceTokenExchangeBuildsCodexProvider(t *testing.T) {
	access := chatGPTTestToken("acct_123")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		assertFormValue(t, r.Form, "grant_type", "authorization_code")
		assertFormValue(t, r.Form, "client_id", chatGPTClientID)
		assertFormValue(t, r.Form, "code", "auth-code")
		assertFormValue(t, r.Form, "code_verifier", "verifier")
		assertFormValue(t, r.Form, "redirect_uri", chatGPTDeviceRedirectURI)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  access,
			"refresh_token": "refresh-1",
			"expires_in":    3600,
		})
	}))
	defer server.Close()
	restore := overrideChatGPTEndpoints(t, server.URL)
	defer restore()

	credentials, err := exchangeChatGPTAuthorizationCode(t.Context(), "auth-code", "verifier", chatGPTDeviceRedirectURI)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccountID != "acct_123" || credentials.AccessToken != access || credentials.RefreshToken != "refresh-1" {
		t.Fatalf("unexpected credentials %#v", credentials)
	}
	provider, err := chatGPTCodexProvider(credentials)
	if err != nil {
		t.Fatal(err)
	}
	if provider.ID != chatGPTProviderID || provider.API != ai.ApiOpenAICodexResponses || provider.Provider != ai.ProviderOpenAICodex {
		t.Fatalf("unexpected provider route %#v", provider)
	}
	if provider.BaseURL != chatGPTCodexBaseURL || provider.APIKey != access || provider.RefreshToken != "refresh-1" || provider.DefaultModel == "" {
		t.Fatalf("unexpected provider credentials %#v", provider)
	}
}

func TestChatGPTProviderAuthRefreshesExpiredToken(t *testing.T) {
	refreshedAccess := chatGPTTestToken("acct_456")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		assertFormValue(t, r.Form, "grant_type", "refresh_token")
		assertFormValue(t, r.Form, "refresh_token", "refresh-old")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  refreshedAccess,
			"refresh_token": "refresh-new",
			"expires_in":    3600,
		})
	}))
	defer server.Close()
	restore := overrideChatGPTEndpoints(t, server.URL)
	defer restore()

	client := &Client{}
	auth, err := client.authForProvider(aiid.ProviderConfig{
		ID:           chatGPTProviderID,
		API:          ai.ApiOpenAICodexResponses,
		Provider:     ai.ProviderOpenAICodex,
		APIKey:       chatGPTTestToken("acct_old"),
		RefreshToken: "refresh-old",
		ExpiresAtMS:  time.Now().Add(-time.Minute).UnixMilli(),
	})(t.Context(), ai.Model{})
	if err != nil {
		t.Fatal(err)
	}
	if auth.APIKey != refreshedAccess {
		t.Fatalf("expected refreshed access token, got %q", auth.APIKey)
	}
}

func TestChatGPTRefreshReusesExistingRefreshTokenWhenNotRotated(t *testing.T) {
	refreshedAccess := chatGPTTestToken("acct_789")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		assertFormValue(t, r.Form, "grant_type", "refresh_token")
		assertFormValue(t, r.Form, "refresh_token", "refresh-old")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": refreshedAccess,
			"expires_in":   3600,
		})
	}))
	defer server.Close()
	restore := overrideChatGPTEndpoints(t, server.URL)
	defer restore()

	credentials, err := refreshChatGPTCredentials(t.Context(), "refresh-old")
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessToken != refreshedAccess || credentials.RefreshToken != "refresh-old" {
		t.Fatalf("unexpected credentials %#v", credentials)
	}
}

func TestChatGPTDevicePollReturnsTerminalHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "expired_token"}})
	}))
	defer server.Close()
	restore := overrideChatGPTEndpoints(t, server.URL)
	defer restore()

	_, pending, _, err := pollChatGPTDeviceAuthOnce(t.Context(), chatGPTDeviceAuthInfo{DeviceAuthID: "device-1", UserCode: "ABCD"})
	if err == nil || pending || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("expected terminal HTTP error, pending=%v err=%v", pending, err)
	}
}

func TestChatGPTDeviceLoginCancelStopsWait(t *testing.T) {
	polled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-polled:
		default:
			close(polled)
		}
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "deviceauth_authorization_pending"})
	}))
	defer server.Close()
	restore := overrideChatGPTEndpoints(t, server.URL)
	defer restore()

	login := &ChatGPTDeviceLogin{device: chatGPTDeviceAuthInfo{
		DeviceAuthID: "device-1",
		UserCode:     "ABCD",
		Interval:     time.Hour,
		ExpiresIn:    time.Hour,
	}}
	errCh := make(chan error, 1)
	go func() {
		_, err := login.Wait(context.Background())
		errCh <- err
	}()
	<-polled
	login.Cancel()
	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not stop after Cancel")
	}
}

func overrideChatGPTEndpoints(t *testing.T, baseURL string) func() {
	t.Helper()
	oldUserCodeURL := chatGPTDeviceUserCodeURL
	oldDeviceTokenURL := chatGPTDeviceTokenURL
	oldVerificationURI := chatGPTDeviceVerificationURI
	oldTokenURL := chatGPTTokenURL
	oldRedirectURI := chatGPTDeviceRedirectURI
	chatGPTDeviceUserCodeURL = baseURL + "/api/accounts/deviceauth/usercode"
	chatGPTDeviceTokenURL = baseURL + "/api/accounts/deviceauth/token"
	chatGPTDeviceVerificationURI = baseURL + "/codex/device"
	chatGPTTokenURL = baseURL + "/oauth/token"
	chatGPTDeviceRedirectURI = baseURL + "/deviceauth/callback"
	return func() {
		chatGPTDeviceUserCodeURL = oldUserCodeURL
		chatGPTDeviceTokenURL = oldDeviceTokenURL
		chatGPTDeviceVerificationURI = oldVerificationURI
		chatGPTTokenURL = oldTokenURL
		chatGPTDeviceRedirectURI = oldRedirectURI
	}
}

func chatGPTTestToken(accountID string) string {
	claims := `{"https://api.openai.com/auth":{"chatgpt_account_id":"` + accountID + `"}}`
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
}

func assertFormValue(t *testing.T, form url.Values, key, want string) {
	t.Helper()
	if got := form.Get(key); got != want {
		t.Fatalf("unexpected %s %q, want %q", key, got, want)
	}
}
