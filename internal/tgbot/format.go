package tgbot

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)
var spaceRe = regexp.MustCompile(`[ \t\r\f\v]+`)
var blankLinesRe = regexp.MustCompile(`\n{3,}`)

// plainText turns FB2/EPUB annotation HTML into readable plain text.
func plainText(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = spaceRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, " \n", "\n")
	s = strings.TrimSpace(s)
	s = blankLinesRe.ReplaceAllString(s, "\n\n")
	return s
}

func escHTML(s string) string {
	return html.EscapeString(s)
}

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:max])) + "…"
}

func externalSourceName(id string) string {
	switch strings.ToLower(id) {
	case "livelib":
		return "LiveLib"
	case "fantlab":
		return "FantLab"
	case "google_books":
		return "Google Books"
	case "open_library":
		return "Open Library"
	}
	return id
}

func formatBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	if value >= 10 {
		return fmt.Sprintf("%.0f %cB", value, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f %cB", value, "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 && days == 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if len(parts) == 0 {
		return "0m"
	}
	return strings.Join(parts, " ")
}

func formatRating(value float64, max int, lang string) string {
	if max <= 0 {
		max = 5
	}
	s := strconv.FormatFloat(value, 'f', 2, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" {
		s = "0"
	}
	if lang == "ru" {
		s = strings.ReplaceAll(s, ".", ",")
	}
	return fmt.Sprintf("%s/%d", s, max)
}
