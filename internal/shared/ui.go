package shared

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

func TruncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func SingleLine(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	return strings.Join(fields, " ")
}

func ParsePercent(input string) (float64, error) {
	value := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(input), "%"))
	if value == "" {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseFloat(value, 64)
}

func FormatPercent(value float64) string {
	text := strconv.FormatFloat(value, 'f', 2, 64)
	text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	if text == "" {
		text = "0"
	}
	return text + "%"
}
