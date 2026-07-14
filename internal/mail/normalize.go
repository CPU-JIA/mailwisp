package mail

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func cleanHeaderValue(value string) string {
	value = strings.ToValidUTF8(value, "")
	value = strings.Map(func(character rune) rune {
		switch character {
		case '\r', '\n', '\x00':
			return -1
		default:
			return character
		}
	}, value)
	return strings.TrimSpace(value)
}

func normalizeBodyText(value []byte) string {
	text := strings.ToValidUTF8(string(value), "")
	text = strings.ReplaceAll(text, "\x00", "")
	return strings.TrimSpace(text)
}

func normalizeFilename(value string, maxBytes int) (string, bool) {
	original := value
	value = strings.ToValidUTF8(value, "")
	value = strings.Map(func(character rune) rune {
		switch {
		case character == '/' || character == '\\':
			return '_'
		case unicode.IsControl(character):
			return -1
		default:
			return character
		}
	}, value)
	value = strings.Trim(value, " .")
	if value == "" || value == "." || value == ".." {
		return "", original != ""
	}
	value = truncateUTF8(value, maxBytes)
	return value, value != original
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func remainingPreviewBytes(existing string, maxBytes int) int {
	remaining := maxBytes - len(existing)
	if existing != "" && remaining > 0 {
		remaining--
	}
	if remaining < 0 {
		return 0
	}
	return remaining
}

func appendPreview(existing, value string, maxBytes int) string {
	if value == "" || len(existing) >= maxBytes {
		return existing
	}
	if existing != "" {
		existing += "\n"
	}
	remaining := maxBytes - len(existing)
	if remaining <= 0 {
		return truncateUTF8(existing, maxBytes)
	}
	return existing + truncateUTF8(value, remaining)
}
