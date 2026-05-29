package connector

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aidb"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type Connector struct {
	Bridge            *bridgev2.Bridge
	Config            Config
	Store             *aidb.Store
	AppServiceToken   string
	HomeserverAddress string
}

var _ bridgev2.NetworkConnector = (*Connector)(nil)
var _ bridgev2.ConfigValidatingNetwork = (*Connector)(nil)

func (c *Connector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "AI Chats",
		NetworkURL:           "https://www.beeper.com/ai",
		NetworkIcon:          "mxc://beeper.com/51a668657dd9e0132cc823ad9402c6c2d0fc3321",
		NetworkID:            aiid.NetworkID,
		BeeperBridgeType:     aiid.BeeperBridgeType,
		DefaultPort:          29344,
		DefaultCommandPrefix: "!ai",
	}
}

func (c *Connector) Init(bridge *bridgev2.Bridge) {
	configureBridgeV2MessageStatuses()
	c.Config.ApplyDefaults()
	c.Bridge = bridge
	c.Store = aidb.NewStore(bridge.DB.Database, dbutil.ZeroLogger(bridge.Log.With().Str("db_section", "ai").Logger()))
}

func configureBridgeV2MessageStatuses() {
	bridgev2.ErrNoPortal = bridgev2.WrapErrorInStatus(errors.New("room is not an AI chat")).
		WithStatus(event.MessageStatusFail).
		WithErrorReason(event.MessageStatusUnsupported).
		WithMessage("This room is not linked to an AI chat. Start a new AI chat or recreate this portal.").
		WithIsCertain(true).
		WithSendNotice(true)
}

func (c *Connector) Start(ctx context.Context) error {
	if _, ok := c.Bridge.Matrix.(bridgev2.MatrixConnectorWithBeeperStreams); !ok {
		return fmt.Errorf("AI bridge requires a Matrix connector with Beeper stream support")
	}
	if err := c.Store.Upgrade(ctx); err != nil {
		return bridgev2.DBUpgradeError{Err: err, Section: "ai"}
	}
	if err := c.ensureDefaultLoginsForConfiguredUsers(ctx); err != nil {
		return err
	}
	return c.ensureDefaultLoginsForExistingUsers(ctx)
}

func (c *Connector) ValidateConfig() error {
	c.Config.ApplyDefaults()
	return nil
}

func (c *Connector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	meta := login.Metadata.(*aiid.UserLoginMetadata)
	if meta.Kind == aiid.LoginKindProvider {
		login.Client = &ProviderLoginClient{
			Main:      c,
			UserLogin: login,
			loggedIn:  true,
		}
		return nil
	}
	ensureMetadataDefaults(meta, c.defaultProviderConfig())
	login.Client = &Client{
		Main:      c,
		UserLogin: login,
		loggedIn:  true,
	}
	return nil
}

func (c *Connector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 3
}

func (c *Connector) defaultProviderConfig() aiid.ProviderConfig {
	baseURL := c.defaultAIServicesProxyBaseURL()
	return aiid.ProviderConfig{
		ID:           aiid.DefaultProvider,
		DisplayName:  "Beeper AI",
		API:          ai.ApiOpenAIResponses,
		Provider:     ai.ProviderOpenAI,
		BaseURL:      normalizeResponsesBaseURL(baseURL),
		DefaultModel: defaultBeeperAIModel,
		Enabled:      true,
	}
}

func (c *Connector) defaultAIServicesProxyBaseURL() string {
	address := ""
	if c != nil {
		address = c.HomeserverAddress
	}
	return defaultAIServicesProxyBaseURL(address)
}

func ensureMetadataDefaults(meta *aiid.UserLoginMetadata, defaultProvider aiid.ProviderConfig) {
	if meta.Kind == "" {
		meta.Kind = aiid.LoginKindMain
	}
	if meta.Providers == nil {
		meta.Providers = map[string]aiid.ProviderConfig{}
	}
	if meta.SyntheticDefault {
		meta.Providers[defaultProvider.ID] = defaultProvider
	}
	if meta.DefaultProviderID == "" {
		meta.DefaultProviderID = defaultProvider.ID
	}
	if meta.DefaultModelID == "" {
		meta.DefaultModelID = defaultProvider.DefaultModel
	}
}

func (c *Connector) defaultLoginID(mxid id.UserID) networkid.UserLoginID {
	if c.Bridge != nil && c.Bridge.Bot != nil {
		if localpart := c.Bridge.Bot.GetMXID().Localpart(); localpart != "" {
			return networkid.UserLoginID(strings.TrimSuffix(localpart, "bot"))
		}
	}
	return aiid.DefaultLoginID(mxid)
}

func (c *Connector) configuredLoginUserMXIDs() []id.UserID {
	if c == nil || c.Bridge == nil || c.Bridge.Config == nil {
		return nil
	}
	mxids := make([]id.UserID, 0, len(c.Bridge.Config.Permissions))
	for rawMXID, permissions := range c.Bridge.Config.Permissions {
		if permissions == nil || !permissions.Login || !strings.HasPrefix(rawMXID, "@") {
			continue
		}
		mxid := id.UserID(rawMXID)
		if _, _, err := mxid.Parse(); err != nil {
			continue
		}
		mxids = append(mxids, mxid)
	}
	sort.Slice(mxids, func(i, j int) bool {
		return mxids[i] < mxids[j]
	})
	return mxids
}

func (c *Connector) ensureDefaultLoginsForConfiguredUsers(ctx context.Context) error {
	for _, mxid := range c.configuredLoginUserMXIDs() {
		user, err := c.Bridge.GetUserByMXID(ctx, mxid)
		if err != nil {
			return fmt.Errorf("load configured user %s: %w", mxid, err)
		}
		if _, err = c.EnsureDefaultLogin(ctx, user); err != nil {
			return fmt.Errorf("ensure default login for configured user %s: %w", mxid, err)
		}
	}
	return nil
}

func (c *Connector) ensureDefaultLoginsForExistingUsers(ctx context.Context) error {
	rows, err := c.Bridge.DB.User.GetDB().Query(ctx, `SELECT mxid FROM "user" WHERE bridge_id=$1`, c.Bridge.ID)
	if err != nil {
		return fmt.Errorf("query existing users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mxid id.UserID
		if err = rows.Scan(&mxid); err != nil {
			return fmt.Errorf("scan existing user: %w", err)
		}
		user, err := c.Bridge.GetExistingUserByMXID(ctx, mxid)
		if err != nil {
			return fmt.Errorf("load existing user %s: %w", mxid, err)
		}
		if user == nil {
			continue
		}
		if _, err = c.EnsureDefaultLogin(ctx, user); err != nil {
			return fmt.Errorf("ensure default login for %s: %w", mxid, err)
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("iterate existing users: %w", err)
	}
	return nil
}

func (c *Connector) EnsureDefaultLogin(ctx context.Context, user *bridgev2.User) (*bridgev2.UserLogin, error) {
	loginID := c.defaultLoginID(user.MXID)
	if cached := c.Bridge.GetCachedUserLoginByID(loginID); cached != nil {
		if meta, ok := cached.Metadata.(*aiid.UserLoginMetadata); ok {
			meta.SyntheticDefault = true
			ensureMetadataDefaults(meta, c.defaultProviderConfig())
		}
		return cached, nil
	}
	meta := &aiid.UserLoginMetadata{SyntheticDefault: true}
	ensureMetadataDefaults(meta, c.defaultProviderConfig())
	return user.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		RemoteName: aiid.DefaultLoginName,
		Metadata:   meta,
	}, &bridgev2.NewLoginParams{})
}

func (c *Connector) ResolveLogin(ctx context.Context, user *bridgev2.User, requested networkid.UserLoginID) (*bridgev2.UserLogin, error) {
	if requested == "" {
		return c.EnsureDefaultLogin(ctx, user)
	}
	if cached := c.Bridge.GetCachedUserLoginByID(requested); cached != nil && cached.UserMXID == user.MXID {
		return cached, nil
	}
	return nil, fmt.Errorf("unknown or inaccessible login %s", requested)
}
