package connector

import (
	_ "embed"
	"net/url"
	"strings"

	up "go.mau.fi/util/configupgrade"
	"gopkg.in/yaml.v3"
)

//go:embed example-config.yaml
var ExampleConfig string

const defaultAIServicesProxyPrefix = "https://ai-services."
const defaultAIServicesProxyPath = "/proxy/openai/v1"
const defaultBeeperAIModel = "gpt-5.5"
const defaultTitleGenerationModel = "gpt-5-mini"
const openRouterTitleGenerationModel = "openai/gpt-5-mini"

type Config struct {
	DefaultSystemPrompt   string       `yaml:"default_system_prompt"`
	DefaultReasoningLevel string       `yaml:"default_reasoning_level"`
	Fetch                 FetchConfig  `yaml:"fetch"`
	Search                SearchConfig `yaml:"search"`
}

type FetchConfig struct {
	TimeoutMS int   `yaml:"timeout_ms"`
	MaxBytes  int64 `yaml:"max_bytes"`
	MaxChars  int   `yaml:"max_chars"`
}

type SearchConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"`
	APIKey   string `yaml:"api_key"`
}

type umConfig Config

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	if err := node.Decode((*umConfig)(c)); err != nil {
		return err
	}
	c.ApplyDefaults()
	return nil
}

func (c *Config) ApplyDefaults() {
	if c.DefaultSystemPrompt == "" {
		c.DefaultSystemPrompt = "You are a helpful assistant inside Beeper."
	}
	if c.DefaultReasoningLevel == "" {
		c.DefaultReasoningLevel = "off"
	}
	if c.Fetch.TimeoutMS == 0 {
		c.Fetch.TimeoutMS = 10000
	}
	if c.Fetch.MaxBytes == 0 {
		c.Fetch.MaxBytes = 2 * 1024 * 1024
	}
	if c.Fetch.MaxChars == 0 {
		c.Fetch.MaxChars = 20000
	}
}

func normalizeResponsesBaseURL(baseURL string) string {
	return strings.TrimSuffix(baseURL, "/responses")
}

func defaultAIServicesProxyBaseURL(homeserverAddress string) string {
	domain := normalizeHomeserverAddress(homeserverAddress)
	if domain == "" {
		return ""
	}
	return defaultAIServicesProxyPrefix + domain + defaultAIServicesProxyPath
}

func normalizeHomeserverAddress(value string) string {
	value = strings.TrimSpace(value)
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		value = parsed.Host
	}
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	value = strings.Trim(value, "/")
	value = strings.TrimPrefix(value, "matrix.")
	return value
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "default_system_prompt")
	helper.Copy(up.Str, "default_reasoning_level")
	helper.Copy(up.Map, "fetch")
	helper.Copy(up.Map, "search")
}

func (c *Connector) GetConfig() (string, any, up.Upgrader) {
	c.Config.ApplyDefaults()
	return ExampleConfig, &c.Config, up.SimpleUpgrader(upgradeConfig)
}
