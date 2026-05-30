package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/ai/providers"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix/bridgev2"
)

const (
	chatGPTDeviceLoginStepID        = "com.beeper.ai.login.chatgpt.device"
	chatGPTClientID                 = "app_EMoamEEZ73f0CkXaXp7hrann"
	chatGPTDeviceCodeTimeoutSeconds = 15 * 60
	chatGPTProviderID               = "com.beeper.ai.provider.chatgpt"
	chatGPTProviderName             = "ChatGPT"
	chatGPTCodexBaseURL             = "https://chatgpt.com/backend-api"
)

var (
	chatGPTDeviceUserCodeURL     = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	chatGPTDeviceTokenURL        = "https://auth.openai.com/api/accounts/deviceauth/token"
	chatGPTDeviceVerificationURI = "https://auth.openai.com/codex/device"
	chatGPTTokenURL              = "https://auth.openai.com/oauth/token"
	chatGPTDeviceRedirectURI     = "https://auth.openai.com/deviceauth/callback"
	chatGPTHTTPClient            = &http.Client{Timeout: 20 * time.Second}
)

type ChatGPTDeviceLogin struct {
	Main       *Connector
	User       *bridgev2.User
	device     chatGPTDeviceAuthInfo
	cancelMu   sync.Mutex
	cancelWait context.CancelFunc
}

var _ bridgev2.LoginProcessDisplayAndWait = (*ChatGPTDeviceLogin)(nil)

func (l *ChatGPTDeviceLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	device, err := startChatGPTDeviceAuth(ctx)
	if err != nil {
		return nil, err
	}
	l.device = device
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeDisplayAndWait,
		StepID:       chatGPTDeviceLoginStepID,
		Instructions: fmt.Sprintf("Open %s and enter code %s.", chatGPTDeviceVerificationURI, device.UserCode),
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
			Type: bridgev2.LoginDisplayTypeNothing,
		},
	}, nil
}

func (l *ChatGPTDeviceLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	waitCtx, cancel := context.WithCancel(ctx)
	l.setCancel(cancel)
	defer func() {
		cancel()
		l.setCancel(nil)
	}()
	token, err := pollChatGPTDeviceAuth(waitCtx, l.device)
	if err != nil {
		return nil, err
	}
	credentials, err := exchangeChatGPTAuthorizationCode(ctx, token.AuthorizationCode, token.CodeVerifier, chatGPTDeviceRedirectURI)
	if err != nil {
		return nil, err
	}
	provider, err := chatGPTCodexProvider(credentials)
	if err != nil {
		return nil, err
	}
	login, err := l.Main.UpsertProviderLogin(ctx, l.User, provider)
	if err != nil {
		return nil, fmt.Errorf("failed to create ChatGPT provider login: %w", err)
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       loginStepComplete,
		Instructions: "ChatGPT login added",
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: login.ID,
			UserLogin:   login,
		},
	}, nil
}

func (l *ChatGPTDeviceLogin) Cancel() {
	l.cancelMu.Lock()
	cancel := l.cancelWait
	l.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (l *ChatGPTDeviceLogin) setCancel(cancel context.CancelFunc) {
	l.cancelMu.Lock()
	defer l.cancelMu.Unlock()
	l.cancelWait = cancel
}

type chatGPTDeviceAuthInfo struct {
	DeviceAuthID    string
	UserCode        string
	Interval        time.Duration
	ExpiresIn       time.Duration
	VerificationURI string
}

type chatGPTDeviceToken struct {
	AuthorizationCode string
	CodeVerifier      string
}

type chatGPTCredentials struct {
	AccessToken  string
	RefreshToken string
	ExpiresAtMS  int64
	AccountID    string
}

func startChatGPTDeviceAuth(ctx context.Context) (chatGPTDeviceAuthInfo, error) {
	reqBody := strings.NewReader(`{"client_id":` + strconv.Quote(chatGPTClientID) + `}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatGPTDeviceUserCodeURL, reqBody)
	if err != nil {
		return chatGPTDeviceAuthInfo{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := chatGPTHTTPClient.Do(req)
	if err != nil {
		return chatGPTDeviceAuthInfo{}, fmt.Errorf("failed to start ChatGPT device login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text, _ := io.ReadAll(resp.Body)
		return chatGPTDeviceAuthInfo{}, fmt.Errorf("ChatGPT device login failed with HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(text)))
	}
	var body struct {
		DeviceAuthID string      `json:"device_auth_id"`
		UserCode     string      `json:"user_code"`
		Interval     interface{} `json:"interval"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return chatGPTDeviceAuthInfo{}, fmt.Errorf("failed to parse ChatGPT device login response: %w", err)
	}
	intervalSeconds, err := parseChatGPTInterval(body.Interval)
	if err != nil {
		return chatGPTDeviceAuthInfo{}, err
	}
	if body.DeviceAuthID == "" || body.UserCode == "" {
		return chatGPTDeviceAuthInfo{}, fmt.Errorf("ChatGPT device login response missing device_auth_id or user_code")
	}
	return chatGPTDeviceAuthInfo{
		DeviceAuthID:    body.DeviceAuthID,
		UserCode:        body.UserCode,
		Interval:        time.Duration(intervalSeconds) * time.Second,
		ExpiresIn:       chatGPTDeviceCodeTimeoutSeconds * time.Second,
		VerificationURI: chatGPTDeviceVerificationURI,
	}, nil
}

func parseChatGPTInterval(raw interface{}) (int, error) {
	switch value := raw.(type) {
	case float64:
		if value < 0 {
			return 0, fmt.Errorf("invalid negative ChatGPT device polling interval")
		}
		return int(value), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("invalid ChatGPT device polling interval %q", value)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("ChatGPT device login response missing interval")
	}
}

func pollChatGPTDeviceAuth(ctx context.Context, device chatGPTDeviceAuthInfo) (chatGPTDeviceToken, error) {
	if device.DeviceAuthID == "" || device.UserCode == "" {
		return chatGPTDeviceToken{}, fmt.Errorf("ChatGPT device login has not been started")
	}
	interval := device.Interval
	if interval <= 0 {
		interval = time.Second
	}
	expiresIn := device.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = chatGPTDeviceCodeTimeoutSeconds * time.Second
	}
	deadline := time.NewTimer(expiresIn)
	defer deadline.Stop()
	for {
		token, pending, slowDown, err := pollChatGPTDeviceAuthOnce(ctx, device)
		if err != nil {
			return chatGPTDeviceToken{}, err
		}
		if !pending {
			return token, nil
		}
		if slowDown {
			interval += 5 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return chatGPTDeviceToken{}, ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return chatGPTDeviceToken{}, fmt.Errorf("ChatGPT device login timed out")
		case <-timer.C:
		}
	}
}

func pollChatGPTDeviceAuthOnce(ctx context.Context, device chatGPTDeviceAuthInfo) (token chatGPTDeviceToken, pending bool, slowDown bool, err error) {
	body := strings.NewReader(`{"device_auth_id":` + strconv.Quote(device.DeviceAuthID) + `,"user_code":` + strconv.Quote(device.UserCode) + `}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatGPTDeviceTokenURL, body)
	if err != nil {
		return chatGPTDeviceToken{}, false, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := chatGPTHTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return chatGPTDeviceToken{}, false, false, ctx.Err()
		}
		return chatGPTDeviceToken{}, false, false, fmt.Errorf("failed to poll ChatGPT device login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var body struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return chatGPTDeviceToken{}, false, false, fmt.Errorf("failed to parse ChatGPT device token response: %w", err)
		}
		if body.AuthorizationCode == "" || body.CodeVerifier == "" {
			return chatGPTDeviceToken{}, false, false, fmt.Errorf("ChatGPT device token response missing authorization_code or code_verifier")
		}
		return chatGPTDeviceToken{AuthorizationCode: body.AuthorizationCode, CodeVerifier: body.CodeVerifier}, false, false, nil
	}
	responseText, _ := io.ReadAll(resp.Body)
	errorCode := chatGPTErrorCode(string(responseText))
	if errorCode == "deviceauth_authorization_pending" {
		return chatGPTDeviceToken{}, true, false, nil
	}
	if errorCode == "slow_down" {
		return chatGPTDeviceToken{}, true, true, nil
	}
	return chatGPTDeviceToken{}, false, false, fmt.Errorf("ChatGPT device login failed with HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseText)))
}

func chatGPTErrorCode(body string) string {
	var parsed struct {
		Error interface{} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ""
	}
	switch value := parsed.Error.(type) {
	case string:
		return value
	case map[string]interface{}:
		if code, ok := value["code"].(string); ok {
			return code
		}
	}
	return ""
}

func exchangeChatGPTAuthorizationCode(ctx context.Context, code, verifier, redirectURI string) (chatGPTCredentials, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {chatGPTClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}
	return requestChatGPTToken(ctx, form, "exchange")
}

func refreshChatGPTCredentials(ctx context.Context, refreshToken string) (chatGPTCredentials, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {chatGPTClientID},
	}
	return requestChatGPTToken(ctx, form, "refresh", refreshToken)
}

func requestChatGPTToken(ctx context.Context, form url.Values, operation string, fallbackRefreshToken ...string) (chatGPTCredentials, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatGPTTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return chatGPTCredentials{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := chatGPTHTTPClient.Do(req)
	if err != nil {
		return chatGPTCredentials{}, fmt.Errorf("ChatGPT token %s failed: %w", operation, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text, _ := io.ReadAll(resp.Body)
		return chatGPTCredentials{}, fmt.Errorf("ChatGPT token %s failed with HTTP %d: %s", operation, resp.StatusCode, strings.TrimSpace(string(text)))
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return chatGPTCredentials{}, fmt.Errorf("failed to parse ChatGPT token response: %w", err)
	}
	if body.RefreshToken == "" && operation == "refresh" && len(fallbackRefreshToken) > 0 {
		body.RefreshToken = fallbackRefreshToken[0]
	}
	if body.AccessToken == "" || body.ExpiresIn <= 0 || body.RefreshToken == "" {
		return chatGPTCredentials{}, fmt.Errorf("ChatGPT token %s response missing access_token, refresh_token or expires_in", operation)
	}
	accountID, err := providers.ExtractCodexAccountID(body.AccessToken)
	if err != nil {
		return chatGPTCredentials{}, err
	}
	return chatGPTCredentials{
		AccessToken:  body.AccessToken,
		RefreshToken: body.RefreshToken,
		ExpiresAtMS:  time.Now().Add(time.Duration(body.ExpiresIn) * time.Second).UnixMilli(),
		AccountID:    accountID,
	}, nil
}

func chatGPTCodexProvider(credentials chatGPTCredentials) (aiid.ProviderConfig, error) {
	if credentials.AccessToken == "" || credentials.RefreshToken == "" || credentials.AccountID == "" {
		return aiid.ProviderConfig{}, fmt.Errorf("ChatGPT credentials are incomplete")
	}
	defaultModel := defaultChatGPTCodexModel()
	return aiid.ProviderConfig{
		ID:           chatGPTProviderID,
		DisplayName:  chatGPTProviderName,
		API:          ai.ApiOpenAICodexResponses,
		Provider:     ai.ProviderOpenAICodex,
		BaseURL:      chatGPTCodexBaseURL,
		APIKey:       credentials.AccessToken,
		RefreshToken: credentials.RefreshToken,
		ExpiresAtMS:  credentials.ExpiresAtMS,
		DefaultModel: defaultModel,
	}, nil
}

func defaultChatGPTCodexModel() string {
	for _, modelID := range []string{"gpt-5.5", "gpt-5.4", "gpt-5.3-codex", "gpt-5.2-codex", "gpt-5-codex"} {
		if _, ok := ai.GetModel(ai.ProviderOpenAICodex, modelID); ok {
			return modelID
		}
	}
	models := ai.GetModels(ai.ProviderOpenAICodex)
	if len(models) > 0 {
		return models[0].ID
	}
	return "gpt-5-codex"
}
