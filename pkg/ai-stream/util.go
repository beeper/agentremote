package aistream

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

func jsonString(value any) any {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		return text
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

func jsonValue(value any) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return value
	}
	return parsed
}

func PreviewFromText(text string, budget int) Preview {
	preview := BoundedPreview(text, budget)
	return Preview{Text: preview, Truncated: len(preview) < len(text)}
}

func BoundedPreview(text string, budget int) string {
	text = strings.TrimSpace(text)
	if budget <= 0 || len(text) <= budget {
		return text
	}
	end := budget
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	if end <= 0 {
		return ""
	}
	return strings.TrimSpace(text[:end])
}

func SplitTextUTF8(text string, maxBytes int) []string {
	if maxBytes <= 0 {
		return nil
	}
	if len(text) <= maxBytes {
		return []string{text}
	}
	var chunks []string
	start := 0
	for start < len(text) {
		end := start + maxBytes
		if end >= len(text) {
			chunks = append(chunks, text[start:])
			break
		}
		for end > start && !utf8.RuneStart(text[end]) {
			end--
		}
		if end == start {
			_, size := utf8.DecodeRuneInString(text[start:])
			end = start + size
		}
		chunks = append(chunks, text[start:end])
		start = end
	}
	return chunks
}

func JSONSize(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return math.MaxInt
	}
	return len(raw)
}
