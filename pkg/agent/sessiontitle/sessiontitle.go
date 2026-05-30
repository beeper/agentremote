package sessiontitle

import (
	"context"
	"strings"

	agent "github.com/beeper/ai-bridge/pkg/agent"
	ai "github.com/beeper/ai-bridge/pkg/ai"
)

const SystemPrompt = `You create short session titles for coding and technical work.
Return exactly one title based only on the user's first message.
Rules:
- Prefer 2 to 6 words
- Use Title Case
- Mention the task, feature, bug, or file focus when clear
- No quotes
- No markdown
- No labels like Title:
- No trailing punctuation
- Maximum 60 characters`

const maxTitleChars = 60

type Options struct {
	Model    ai.Model
	APIKey   string
	Headers  map[string]string
	StreamFn agent.StreamFn
}

func Generate(ctx context.Context, messages []agent.AgentMessage, options Options) (string, error) {
	if options.StreamFn == nil {
		options.StreamFn = ai.StreamSimple
	}
	prompt := firstUserText(messages)
	maxTokens := 64
	stream := options.StreamFn(ctx, options.Model, ai.Context{
		SystemPrompt: SystemPrompt,
		Messages: []ai.Message{{
			Role:    "user",
			Content: []ai.ContentBlock{{Type: "text", Text: prompt}},
		}},
	}, ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		APIKey:    options.APIKey,
		Headers:   options.Headers,
		MaxTokens: &maxTokens,
	}})
	return cleanTitle(assistantText(stream.Result())), nil
}

func firstUserText(messages []agent.AgentMessage) string {
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		return trimPrompt(assistantText(ai.Message(message)))
	}
	return ""
}

func assistantText(message ai.Message) string {
	parts := []string{}
	for _, block := range contentBlocks(message.Content) {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func contentBlocks(content any) []ai.ContentBlock {
	if blocks, ok := content.([]ai.ContentBlock); ok {
		return blocks
	}
	if text, ok := content.(string); ok {
		return []ai.ContentBlock{{Type: "text", Text: text}}
	}
	return nil
}

func trimPrompt(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.TrimSpace(value)
	if len(value) > 4000 {
		return value[:4000]
	}
	return value
}

func cleanTitle(title string) string {
	title = trimCodeFence(title)
	if lines := strings.Split(title, "\n"); len(lines) > 1 {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				title = line
				break
			}
		}
	}
	title = trimCodeFence(title)
	title = strings.TrimSpace(title)
	title = strings.TrimPrefix(title, "- ")
	title = strings.TrimPrefix(title, "* ")
	if rest, ok := stripLabel(title); ok {
		title = rest
	}
	title = strings.Trim(title, "\"'`")
	title = strings.TrimRight(title, ".?!:;,")
	title = strings.Trim(title, "\"'`")
	title = strings.Join(strings.Fields(title), " ")
	runes := []rune(title)
	if len(runes) <= maxTitleChars {
		return title
	}
	title = strings.TrimSpace(string(runes[:maxTitleChars]))
	if lastSpace := strings.LastIndex(title, " "); lastSpace > 20 {
		title = title[:lastSpace]
	}
	return strings.TrimSpace(title)
}

func trimCodeFence(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "```") {
		value = strings.TrimPrefix(value, "```")
		value = strings.TrimLeft(value, " \t")
		if newline := strings.IndexAny(value, "\r\n"); newline >= 0 {
			firstLine := strings.TrimSpace(value[:newline])
			if firstLine != "" && !strings.Contains(firstLine, " ") {
				value = value[newline:]
			}
		}
	}
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, "```")
	return strings.TrimSpace(value)
}

func stripLabel(title string) (string, bool) {
	lower := strings.ToLower(title)
	for _, label := range []string{"title:", "session name:"} {
		if strings.HasPrefix(lower, label) {
			return strings.TrimSpace(title[len(label):]), true
		}
	}
	return title, false
}
