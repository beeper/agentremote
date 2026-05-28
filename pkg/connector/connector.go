package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aidb"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type Connector struct {
	Bridge           *bridgev2.Bridge
	Config           Config
	Store            *aidb.Store
	AppServiceToken  string
	HomeserverDomain string
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
	if processor, ok := bridge.Commands.(*commands.Processor); ok {
		processor.AddHandler(c.commandAddProvider())
	}
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
	ensureMetadataDefaults(meta, c.defaultProviderConfig(), c.configuredProviders())
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
	config := c.Config
	config.ApplyDefaults()
	baseURL := config.DefaultProvider.BaseURL
	if baseURL == "" {
		baseURL = c.defaultAIServicesProxyBaseURL()
	}
	models := make([]ai.Model, 0, len(config.DefaultProvider.Models))
	for _, model := range config.DefaultProvider.Models {
		models = append(models, normalizeDefaultModel(model, baseURL))
	}
	return aiid.ProviderConfig{
		ID:            aiid.DefaultProvider,
		DisplayName:   "Beeper AI",
		API:           config.DefaultProvider.API,
		Provider:      config.DefaultProvider.Provider,
		BaseURL:       normalizeResponsesBaseURL(baseURL),
		DefaultModel:  defaultDefaultModelID(config.DefaultProvider.DefaultModel, config.DefaultProvider.AllowedModels, models),
		AllowedModels: append([]string{}, config.DefaultProvider.AllowedModels...),
		Models:        models,
		Enabled:       true,
	}
}

func (c *Connector) defaultAIServicesProxyBaseURL() string {
	domain := "beeper.com"
	if c != nil && c.HomeserverDomain != "" {
		domain = c.HomeserverDomain
	}
	return defaultAIServicesProxyBaseURL(domain)
}

func (c *Connector) configuredProviders() map[string]aiid.ProviderConfig {
	config := c.Config
	config.ApplyDefaults()
	providers := map[string]aiid.ProviderConfig{
		aiid.DefaultProvider: c.defaultProviderConfig(),
	}
	for id, provider := range config.Providers {
		provider = normalizeConfiguredProvider(id, provider)
		providers[provider.ID] = provider
	}
	return providers
}

func normalizeConfiguredProvider(id string, provider aiid.ProviderConfig) aiid.ProviderConfig {
	if provider.ID == "" {
		provider.ID = id
	}
	if provider.DisplayName == "" {
		provider.DisplayName = provider.ID
	}
	if provider.Provider == "" {
		provider.Provider = ai.Provider(provider.ID)
	}
	if provider.API == "" {
		switch provider.Provider {
		case ai.ProviderOpenRouter:
			provider.API = ai.ApiOpenAICompletions
		default:
			provider.API = ai.ApiOpenAIResponses
		}
	}
	provider.BaseURL = normalizeResponsesBaseURL(provider.BaseURL)
	if provider.DefaultModel == "" {
		if len(provider.AllowedModels) > 0 {
			provider.DefaultModel = provider.AllowedModels[0]
		} else if len(provider.Models) > 0 {
			provider.DefaultModel = provider.Models[0].ID
		}
	}
	for i := range provider.Models {
		provider.Models[i] = normalizeProviderModel(provider.Models[i], provider)
	}
	return provider
}

func ensureMetadataDefaults(meta *aiid.UserLoginMetadata, defaultProvider aiid.ProviderConfig, configuredProviders ...map[string]aiid.ProviderConfig) {
	if meta.Kind == "" {
		meta.Kind = aiid.LoginKindMain
	}
	if meta.Providers == nil {
		meta.Providers = map[string]aiid.ProviderConfig{}
	}
	if _, ok := meta.Providers[defaultProvider.ID]; !ok && meta.SyntheticDefault {
		meta.Providers[defaultProvider.ID] = defaultProvider
	}
	if len(configuredProviders) > 0 {
		for id, provider := range configuredProviders[0] {
			meta.Providers[id] = provider
		}
	}
	if meta.DefaultProviderID == "" {
		meta.DefaultProviderID = defaultProvider.ID
	}
	if meta.DefaultModelID == "" {
		meta.DefaultModelID = defaultProvider.DefaultModel
	}
}

func defaultModelID(models []ai.Model) string {
	if len(models) == 0 {
		return "gpt-5"
	}
	return models[0].ID
}

func defaultDefaultModelID(configured string, allowed []string, models []ai.Model) string {
	if configured != "" {
		return configured
	}
	if len(allowed) > 0 {
		return allowed[0]
	}
	return defaultModelID(models)
}

func (c *Connector) defaultLoginID(mxid id.UserID) networkid.UserLoginID {
	if c.Bridge != nil && c.Bridge.Bot != nil {
		if localpart := c.Bridge.Bot.GetMXID().Localpart(); localpart != "" {
			return networkid.UserLoginID(strings.TrimSuffix(localpart, "bot"))
		}
	}
	return aiid.DefaultLoginID(mxid)
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
			ensureMetadataDefaults(meta, c.defaultProviderConfig(), c.configuredProviders())
		}
		return cached, nil
	}
	meta := &aiid.UserLoginMetadata{SyntheticDefault: true}
	ensureMetadataDefaults(meta, c.defaultProviderConfig(), c.configuredProviders())
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
