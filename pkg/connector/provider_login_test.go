package connector

import (
	"context"
	"fmt"
	"testing"

	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

func TestProviderLoginUsesParentGhostIdentity(t *testing.T) {
	providerLogin := &bridgev2.UserLogin{UserLogin: &database.UserLogin{
		ID:       aiid.ProviderLoginID("main-login", "openai-codex"),
		Metadata: &aiid.UserLoginMetadata{},
	}}
	client := &ProviderLoginClient{UserLogin: providerLogin}

	if got := client.GetUserID(); got != networkid.UserID("login:main-login") {
		t.Fatalf("expected provider login to use parent ghost ID, got %q", got)
	}
	if !client.IsThisUser(t.Context(), networkid.UserID("login:main-login")) {
		t.Fatalf("expected parent ghost ID to match provider login")
	}
	if client.IsThisUser(t.Context(), networkid.UserID("login:provider-login")) {
		t.Fatalf("provider login should not expose a separate ghost ID")
	}
	info, err := client.GetUserInfo(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name == nil || *info.Name != "AI" {
		t.Fatalf("expected shared AI ghost info, got %#v", info)
	}
	if info.Avatar == nil || string(info.Avatar.MXC) != defaultAIAssistantAvatarMXC {
		t.Fatalf("expected shared AI ghost avatar %q, got %#v", defaultAIAssistantAvatarMXC, info)
	}
}

func TestConnectProviderLoginConnectsExistingProviderAccount(t *testing.T) {
	fake := &fakeNetworkAPI{}
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "provider-login"}, Client: fake}
	(&Connector{}).connectProviderLogin(t.Context(), login)
	if !fake.connected {
		t.Fatalf("expected existing provider client to be connected")
	}
}

type fakeNetworkAPI struct {
	connected bool
}

func (f *fakeNetworkAPI) Connect(ctx context.Context) {
	f.connected = true
}

func (f *fakeNetworkAPI) Disconnect() {
	f.connected = false
}

func (f *fakeNetworkAPI) IsLoggedIn() bool {
	return f.connected
}

func (f *fakeNetworkAPI) LogoutRemote(ctx context.Context) {
	f.connected = false
}

func (f *fakeNetworkAPI) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return false
}

func (f *fakeNetworkAPI) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeNetworkAPI) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeNetworkAPI) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	return nil
}

func (f *fakeNetworkAPI) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
