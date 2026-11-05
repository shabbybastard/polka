package server

import (
	"net/http"
	"testing"
)

func TestRatingSyncRequiresSource(t *testing.T) {
	f := newFullServer(t, "required")

	resp := postJSON(t, f.admin, f.ts.URL+"/admin/settings", map[string]any{
		"enrichment": map[string]bool{
			"livelib": false, "fantlab": false, "google_books": false, "open_library": false,
		},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable rating sources -> %d", resp.StatusCode)
	}

	if got := code(t, f.admin, "POST", f.ts.URL+"/admin/ratings/sync", ""); got != http.StatusConflict {
		t.Errorf("rating sync without sources -> %d, want 409", got)
	}
	if got := code(t, f.admin, "POST", f.ts.URL+"/admin/ratings/sync?mode=empty", ""); got != http.StatusConflict {
		t.Errorf("empty rating sync without sources -> %d, want 409", got)
	}
	if got := code(t, f.admin, "POST", f.ts.URL+"/admin/ratings/sync?mode=bogus", ""); got != http.StatusBadRequest {
		t.Errorf("invalid rating mode -> %d, want 400", got)
	}
	status := getJSONWith(t, f.admin, f.ts.URL+"/admin/ratings/status")
	if status["running"] != false {
		t.Errorf("rating status: %v", status)
	}
}
