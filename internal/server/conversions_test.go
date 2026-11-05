package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shabbybastard/polka/internal/conversions"
	"github.com/shabbybastard/polka/internal/fb2epub"
)

// conversionUpload uploads one file and decodes the 202/200 response.
func conversionUpload(t *testing.T, ts string, client *http.Client, name string, body []byte) (int, uploadResult, string) {
	t.Helper()
	resp := uploadFiles(t, client, ts+"/admin/books/upload", map[string][]byte{name: body}, nil)
	defer resp.Body.Close()
	var out struct {
		Status  string         `json:"status"`
		JobID   string         `json:"jobId"`
		Results []uploadResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	jobID := out.JobID
	if jobID == "" && len(out.Results) == 1 {
		jobID = out.Results[0].JobID
	}
	result := uploadResult{}
	if len(out.Results) > 0 {
		result = out.Results[0]
	}
	return resp.StatusCode, result, jobID
}

func conversionSetting(t *testing.T, ts string, client *http.Client, key string, value bool) {
	t.Helper()
	body := map[string]any{"convert": map[string]any{key: value}}
	resp := postJSON(t, client, ts+"/admin/settings", body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("set conversion %s: %d", key, resp.StatusCode)
	}
}

func waitConversion(t *testing.T, ts string, client *http.Client, id, want string) conversions.Job {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		resp := getJSONWith(t, client, ts+"/admin/conversions")
		jobs, _ := resp["jobs"].([]any)
		for _, raw := range jobs {
			m := raw.(map[string]any)
			if m["id"] == id {
				job := conversions.Job{
					ID:     m["id"].(string),
					Name:   m["name"].(string),
					Status: m["status"].(string),
				}
				if v, ok := m["error"].(string); ok {
					job.Error = v
				}
				if v, ok := m["bookId"].(float64); ok {
					job.BookID = int64(v)
				}
				if job.Status == want {
					return job
				}
				if want != string(conversions.StatusFailed) && job.Status == string(conversions.StatusFailed) {
					t.Fatalf("job %s failed: %s", id, job.Error)
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", id, want)
	return conversions.Job{}
}

func TestFB2ConversionQueue(t *testing.T) {
	ts, client, st, dir := newManageServerDir(t)
	conversionSetting(t, ts.URL, client, "fb2ToEpub", true)

	status, result, jobID := conversionUpload(t, ts.URL, client, "voyna.fb2", []byte(testFB2))
	if status != 202 || result.Status != "queued" || jobID == "" {
		t.Fatalf("upload = %d %+v job=%q", status, result, jobID)
	}
	job := waitConversion(t, ts.URL, client, jobID, string(conversions.StatusDone))
	if job.BookID == 0 {
		t.Fatal("converted job has no book")
	}
	details, err := st.BookDetails(t.Context(), job.BookID)
	if err != nil {
		t.Fatal(err)
	}
	if details.Folder != "converted" || details.Ext != "epub" {
		t.Fatalf("converted book: folder=%q ext=%q", details.Folder, details.Ext)
	}
	data, err := os.ReadFile(filepath.Join(dir, "lib", "converted", details.File+".epub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fb2epub.Validate(data); err != nil {
		t.Fatalf("catalogued EPUB is invalid: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fb2-sources", jobID+".fb2")); err != nil {
		t.Errorf("source was not archived: %v", err)
	}
}

func TestFB2ConversionInvalid(t *testing.T) {
	ts, client, _, dir := newManageServerDir(t)
	conversionSetting(t, ts.URL, client, "fb2ToEpub", true)

	status, result, jobID := conversionUpload(t, ts.URL, client, "broken.fb2", []byte(`<FictionBook><description>`))
	if status != 202 || result.Status != "queued" || jobID == "" {
		t.Fatalf("upload = %d %+v job=%q", status, result, jobID)
	}
	job := waitConversion(t, ts.URL, client, jobID, string(conversions.StatusFailed))
	if job.Error == "" {
		t.Error("failed job has no error")
	}
	if _, err := os.Stat(filepath.Join(dir, "fb2-failed", jobID+".fb2")); err != nil {
		t.Errorf("failed source was not archived: %v", err)
	}
}

func TestFB2ConversionDuplicate(t *testing.T) {
	ts, client, st, _ := newManageServerDir(t)
	conversionSetting(t, ts.URL, client, "fb2ToEpub", true)

	status, _, firstID := conversionUpload(t, ts.URL, client, "voyna.fb2", []byte(testFB2))
	if status != 202 || firstID == "" {
		t.Fatalf("first upload = %d job=%q", status, firstID)
	}
	first := waitConversion(t, ts.URL, client, firstID, string(conversions.StatusDone))
	status, _, secondID := conversionUpload(t, ts.URL, client, "voyna-copy.fb2", []byte(testFB2))
	if status != 202 || secondID == "" {
		t.Fatalf("second upload = %d job=%q", status, secondID)
	}
	second := waitConversion(t, ts.URL, client, secondID, string(conversions.StatusSkipped))
	if second.BookID != first.BookID {
		t.Errorf("skipped job points to %d, want %d", second.BookID, first.BookID)
	}
	if n, _ := st.BookCount(t.Context()); n != 1 {
		t.Errorf("books after duplicate = %d, want 1", n)
	}
}

func TestFB2ConversionDisabled(t *testing.T) {
	ts, client, st, _ := newManageServerDir(t)

	status, result, jobID := conversionUpload(t, ts.URL, client, "voyna.fb2", []byte(testFB2))
	if status != 200 || result.BookID == 0 || jobID != "" {
		t.Fatalf("upload with conversion off = %d %+v job=%q", status, result, jobID)
	}
	if n, _ := st.BookCount(t.Context()); n != 1 {
		t.Errorf("books = %d, want 1", n)
	}
}

func TestDjvuUploadRejected(t *testing.T) {
	ts, client, _, _ := newManageServerDir(t)
	resp := uploadFiles(t, client, ts.URL+"/admin/books/upload", map[string][]byte{"scan.djvu": []byte("djvu")}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("djvu upload -> %d, want 400", resp.StatusCode)
	}
}
