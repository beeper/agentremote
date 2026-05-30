package connector

import (
	"context"
	"fmt"
	"strings"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
)

type ProviderLoginClient struct {
	Main      *Connector
	UserLogin *bridgev2.UserLogin
	loggedIn  bool
}

var _ bridgev2.NetworkAPI = (*ProviderLoginClient)(nil)
var _ bridgev2.IdentifierResolvingNetworkAPI = (*ProviderLoginClient)(nil)
var _ bridgev2.ContactListingNetworkAPI = (*ProviderLoginClient)(nil)
var _ bridgev2.UserSearchingNetworkAPI = (*ProviderLoginClient)(nil)

func (cl *ProviderLoginClient) Connect(ctx context.Context) {
	cl.loggedIn = cl.providerAvailable(ctx)
	if cl.loggedIn {
		cl.sendBridgeState(status.StateConnected)
	} else {
		cl.sendBridgeState(status.StateLoggedOut)
	}
}

func (cl *ProviderLoginClient) Disconnect() {
	cl.loggedIn = false
}

func (cl *ProviderLoginClient) IsLoggedIn() bool {
	return cl.loggedIn
}

func (cl *ProviderLoginClient) LogoutRemote(ctx context.Context) {
	parent, providerID, err := cl.parentLogin(ctx)
	if err == nil {
		if meta, ok := parent.Metadata.(*aiid.UserLoginMetadata); ok && meta.Providers != nil {
			delete(meta.Providers, providerID)
			_ = parent.Save(ctx)
		}
	}
	cl.loggedIn = false
	cl.sendBridgeState(status.StateLoggedOut)
}

func (cl *ProviderLoginClient) GetUserID() networkid.UserID {
	return networkid.UserID("login:" + string(cl.parentLoginID()))
}

func (cl *ProviderLoginClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	if cl != nil && cl.Main != nil && cl.Main.Bridge != nil {
		if parent, _, err := cl.parentClient(ctx); err == nil {
			return parent.IsThisUser(ctx, userID)
		}
	}
	return userID == cl.GetUserID()
}

func (cl *ProviderLoginClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	return nil, fmt.Errorf("provider logins do not own AI rooms")
}

func (cl *ProviderLoginClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	if cl != nil && cl.Main != nil && cl.Main.Bridge != nil {
		parent, _, err := cl.parentClient(ctx)
		if err == nil {
			return parent.GetUserInfo(ctx, ghost)
		}
	}
	return aiAssistantUserInfo(), nil
}

func (cl *ProviderLoginClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	supportsAIState := cl != nil && cl.Main != nil && cl.Main.aiRoomStateStore().canRead()
	return roomFeaturesForModel(ai.Model{}, supportsAIState)
}

func (cl *ProviderLoginClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	return nil, fmt.Errorf("provider logins do not handle AI room messages")
}

func (cl *ProviderLoginClient) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	provider, ok, err := cl.provider(ctx)
	if err != nil || !ok {
		return nil, err
	}
	return providerModelContacts(ctx, cl.bridge(), provider, ""), nil
}

func (cl *ProviderLoginClient) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	provider, ok, err := cl.provider(ctx)
	if err != nil || !ok {
		return nil, err
	}
	return providerModelContacts(ctx, cl.bridge(), provider, strings.TrimSpace(query)), nil
}

func (cl *ProviderLoginClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	parent, providerID, err := cl.parentClient(ctx)
	if err != nil {
		return nil, err
	}
	provider, ok := parent.loginMetadata().Providers[providerID]
	if !ok {
		return nil, fmt.Errorf("provider %s is not available", providerID)
	}
	model, ok := resolveModelForProvider(provider, identifier)
	if !ok {
		return nil, fmt.Errorf("unknown AI model %s", identifier)
	}
	resp := modelContactWithGhost(ctx, cl.bridge(), provider, model)
	if !createChat {
		return resp, nil
	}
	chat, err := parent.createModelChat(ctx, provider, model)
	if err != nil {
		return nil, err
	}
	resp.Chat = chat
	return resp, nil
}

func (cl *ProviderLoginClient) parentClient(ctx context.Context) (*Client, string, error) {
	parent, providerID, err := cl.parentLogin(ctx)
	if err != nil {
		return nil, "", err
	}
	if parent.Client == nil {
		if cl.Main == nil {
			return nil, "", fmt.Errorf("parent login %s is unavailable without connector", parent.ID)
		}
		if err = cl.Main.LoadUserLogin(ctx, parent); err != nil {
			return nil, "", err
		}
	}
	client, ok := parent.Client.(*Client)
	if !ok {
		return nil, "", fmt.Errorf("parent login %s is not an AI main login", parent.ID)
	}
	return client, providerID, nil
}

func (cl *ProviderLoginClient) parentLogin(ctx context.Context) (*bridgev2.UserLogin, string, error) {
	parentID, providerID, ok := aiid.ParseProviderLoginID(cl.UserLogin.ID)
	if !ok {
		return nil, "", fmt.Errorf("login %s is not a provider login", cl.UserLogin.ID)
	}
	if cl.Main == nil || cl.Main.Bridge == nil {
		return &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			ID:       parentID,
			UserMXID: cl.UserLogin.UserMXID,
			Metadata: &aiid.UserLoginMetadata{},
		}}, providerID, nil
	}
	parent, err := cl.Main.Bridge.GetExistingUserLoginByID(ctx, parentID)
	if err != nil {
		return nil, "", err
	}
	if parent == nil || parent.UserMXID != cl.UserLogin.UserMXID {
		return nil, "", fmt.Errorf("parent login %s is unavailable", parentID)
	}
	return parent, providerID, nil
}

func (cl *ProviderLoginClient) parentLoginID() networkid.UserLoginID {
	if cl != nil && cl.UserLogin != nil {
		if parentID, _, ok := aiid.ParseProviderLoginID(cl.UserLogin.ID); ok {
			return parentID
		}
		return cl.UserLogin.ID
	}
	return ""
}

func (cl *ProviderLoginClient) bridge() *bridgev2.Bridge {
	if cl == nil || cl.Main == nil {
		return nil
	}
	return cl.Main.Bridge
}

func (cl *ProviderLoginClient) provider(ctx context.Context) (aiid.ProviderConfig, bool, error) {
	parent, providerID, err := cl.parentClient(ctx)
	if err != nil {
		return aiid.ProviderConfig{}, false, err
	}
	provider, ok := parent.loginMetadata().Providers[providerID]
	if !ok {
		return aiid.ProviderConfig{}, false, nil
	}
	return provider, true, nil
}

func (cl *ProviderLoginClient) providerAvailable(ctx context.Context) bool {
	_, ok, err := cl.provider(ctx)
	return err == nil && ok
}

func (cl *ProviderLoginClient) sendBridgeState(state status.BridgeStateEvent) {
	if cl != nil && cl.UserLogin != nil && cl.UserLogin.BridgeState != nil {
		cl.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: state})
	}
}
