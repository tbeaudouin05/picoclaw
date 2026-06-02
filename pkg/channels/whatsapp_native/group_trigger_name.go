//go:build whatsapp_native

package whatsapp

import (
	"strings"
	"unicode"

	"github.com/sipeed/picoclaw/pkg/config"
)

func groupTriggerName(cfg *config.WhatsAppSettings) string {
	if cfg == nil {
		return ""
	}
	return cfg.GroupTriggerName
}

func shouldRequireTriggerName(isGroup bool, cfg *config.WhatsAppSettings) bool {
	if isGroup {
		return true
	}
	if cfg == nil {
		return false
	}
	return cfg.RequireTriggerNameInDirect
}

func shouldDropGroupMessageForTriggerName(groupTriggerName, content string) bool {
	name := strings.TrimSpace(groupTriggerName)
	if name == "" {
		return false
	}
	return !containsWordFold(content, name)
}

func containsWordFold(content, word string) bool {
	if word == "" {
		return false
	}
	contentRunes := []rune(content)
	wordRunes := []rune(word)
	if len(wordRunes) == 0 || len(contentRunes) < len(wordRunes) {
		return false
	}
	for i := 0; i <= len(contentRunes)-len(wordRunes); i++ {
		if i > 0 && isWordRune(contentRunes[i-1]) {
			continue
		}
		if i+len(wordRunes) < len(contentRunes) && isWordRune(contentRunes[i+len(wordRunes)]) {
			continue
		}
		matched := true
		for j := range wordRunes {
			if !strings.EqualFold(string(contentRunes[i+j]), string(wordRunes[j])) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// stripTriggerNamePrefix removes a leading trigger-name word from content
// (case-insensitive, whole-word) and returns the trimmed remainder.
// If triggerName is empty or not the leading word, content is returned as-is.
// Used to allow "bot /ai off" to be recognized as "/ai off" for command parsing.
func stripTriggerNamePrefix(triggerName, content string) string {
	tn := strings.TrimSpace(triggerName)
	if tn == "" {
		return content
	}
	trimmed := strings.TrimSpace(content)
	tnRunes := []rune(tn)
	contentRunes := []rune(trimmed)
	if len(contentRunes) < len(tnRunes) {
		return content
	}
	for i, r := range tnRunes {
		if !strings.EqualFold(string(contentRunes[i:i+1]), string([]rune{r})) {
			return content
		}
	}
	after := contentRunes[len(tnRunes):]
	if len(after) > 0 && isWordRune(after[0]) {
		return content
	}
	return strings.TrimSpace(string(after))
}
