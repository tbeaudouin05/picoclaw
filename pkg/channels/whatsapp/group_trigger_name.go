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
