package connector

import (
	_ "embed"
	"strings"

	up "go.mau.fi/util/configupgrade"
	"gopkg.in/yaml.v3"
)

//go:embed example-config.yaml
var ExampleConfig string

const defaultAIServicesProxyPath = "/proxy/openai/v1"
const defaultBeeperAIModel = "beeper/default"
const defaultTitleGenerationModel = "gpt-5-mini"
const openRouterTitleGenerationModel = "openai/gpt-5-mini"

type Config struct {
	DefaultSystemPrompt   string      `yaml:"default_system_prompt"`
	DefaultReasoningLevel string      `yaml:"default_reasoning_level"`
	Fetch                 FetchConfig `yaml:"fetch"`
}

type FetchConfig struct {
	TimeoutMS int   `yaml:"timeout_ms"`
	MaxBytes  int64 `yaml:"max_bytes"`
	MaxChars  int   `yaml:"max_chars"`
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

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "default_system_prompt")
	helper.Copy(up.Str, "default_reasoning_level")
	helper.Copy(up.Map, "fetch")
}

func (c *Connector) GetConfig() (string, any, up.Upgrader) {
	c.Config.ApplyDefaults()
	return ExampleConfig, &c.Config, up.SimpleUpgrader(upgradeConfig)
}
