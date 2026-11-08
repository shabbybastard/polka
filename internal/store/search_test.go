package store

import (
	"strings"
	"testing"
)

func TestFTSQueryIgnoresTypography(t *testing.T) {
	q := ftsQuery("Мастер — Маргарита", "title")
	if strings.Contains(q, "—") {
		t.Errorf("em dash became a required term: %q", q)
	}
	if !strings.Contains(q, `"Мастер"*`) || !strings.Contains(q, `"Маргарита"*`) {
		t.Errorf("tokens missing: %q", q)
	}
	for _, input := range []string{"«Тихий Дон»", "Тихий\u00a0Дон", "Тихий – Дон"} {
		q := ftsQuery(input, "")
		for _, bad := range []string{"«", "»", "\u00a0", "–"} {
			if strings.Contains(q, bad) {
				t.Errorf("ftsQuery(%q) contains %q: %q", input, bad, q)
			}
		}
	}
}

func TestNormalizeForMatchIgnoresTypography(t *testing.T) {
	if got := normalizeForMatch("«Тихий Дон»"); got != "тихийдон" {
		t.Errorf("normalizeForMatch = %q, want тихийдон", got)
	}
	if got := normalizeForMatch("Мастер — Маргарита"); got != "мастермаргарита" {
		t.Errorf("normalizeForMatch = %q, want мастермаргарита", got)
	}
}
