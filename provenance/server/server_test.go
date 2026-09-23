package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"attribution/common/codec"
	"attribution/common/store"
)

func newTest(t *testing.T, st *store.Store) http.Handler {
	t.Helper()
	return New(codec.NewKey(), "qa@example.com", st).Handler(fstest.MapFS{}, "")
}

func upload(t *testing.T, h http.Handler, path, name string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	req := httptest.NewRequest("POST", path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func call(t *testing.T, h http.Handler, method, path string, v any) []byte {
	t.Helper()
	var body io.Reader
	if v != nil {
		b, _ := json.Marshal(v)
		body = bytes.NewReader(b)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, body))
	if rec.Code != 200 {
		t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

// swissCSV is an Excel export from a Swiss locale: BOM, semicolons, decimal
// commas.
func swissCSV(n int) []byte {
	var b strings.Builder
	b.WriteString("\uFEFFKunden-Nr;Vorname;Nachname;E-Mail;Saldo;Ort;Eröffnet\r\n")
	first := []string{"Luca", "Anna", "Noah", "Mia", "Elias", "Lea", "Leon", "Sara"}
	last := []string{"Keller", "Meier", "Frei", "Graf", "Roth", "Huber", "Moser", "Weber", "Suter"}
	for i := 0; i < n; i++ {
		f, l := first[i%len(first)], last[(i*7)%len(last)]
		fmt.Fprintf(&b, "%d;%s;%s;%s.%s%d@example.ch;%d,%03d;Zürich;2023-%02d-%02d\r\n",
			2000+i, f, l, strings.ToLower(f), strings.ToLower(l), i, 100+i*37%9000, (i*53)%1000, 1+i%12, 1+i%28)
	}
	return []byte(b.String())
}

type detectOut struct {
	Report struct {
		MatchesSource bool `json:"matchesSource"`
		Verdict       struct {
			Status string `json:"status"`
			MarkID string `json:"markId"`
		} `json:"verdict"`
		Results []struct {
			Technique string `json:"technique"`
			Detail    string `json:"detail"`
			Decision  *struct {
				Ranking []struct {
					MarkID string `json:"markId"`
				} `json:"ranking"`
			} `json:"decision"`
		} `json:"results"`
	} `json:"report"`
	Issuances map[string]struct {
		Recipient string `json:"recipient"`
		Purpose   string `json:"purpose"`
	} `json:"issuances"`
}

func TestProvenanceFlow(t *testing.T) {
	h := newTest(t, nil)
	if rec := upload(t, h, "/api/source", "kunden.csv", swissCSV(600)); rec.Code != 200 {
		t.Fatalf("load: %s", rec.Body.String())
	}
	var st struct {
		Schema struct {
			Key    string   `json:"key"`
			Redact []string `json:"redact"`
		} `json:"schema"`
		Columns []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"columns"`
		Source string `json:"source"`
	}
	json.Unmarshal(call(t, h, "GET", "/api/state", nil), &st)
	if len(st.Columns) != 7 || st.Schema.Key != "Kunden-Nr" {
		t.Fatalf("columns %+v key %q", st.Columns, st.Schema.Key)
	}
	if got := strings.Join(st.Schema.Redact, ","); got != "Vorname,Nachname,E-Mail" {
		t.Errorf("redact %s", got)
	}
	if strings.Contains(st.Source, "rows") {
		t.Errorf("source %q repeats the row count", st.Source)
	}

	// The Mark tab touches its unassigned copy, then two copies are issued.
	tech := map[string]any{"canary": true, "allocate": true, "redact": true, "dummy": true}
	call(t, h, "POST", "/api/preview", map[string]any{"techniques": tech})
	var issued []struct {
		MarkID   string `json:"markId"`
		FileName string `json:"fileName"`
	}
	json.Unmarshal(call(t, h, "POST", "/api/issue", map[string]any{
		"recipients": []map[string]string{
			{"name": "Acme Analytics AG", "org": "churn study", "unit": "commercial"},
			{"name": "Beta Research GmbH", "org": "pricing", "unit": "research"},
		},
		"techniques": tech,
	}), &issued)
	if len(issued) != 2 {
		t.Fatalf("issued %+v", issued)
	}

	// Verify Acme's copy: named with purpose, never the unassigned mark.
	acme := call(t, h, "GET", "/api/copy?mark="+issued[0].MarkID, nil)
	if !bytes.HasPrefix(acme, []byte("\uFEFFKunden-Nr;")) || !bytes.Contains(acme, []byte(",")) {
		t.Errorf("copy not written in the source's dialect: %.80q", acme)
	}
	var out detectOut
	json.Unmarshal(upload(t, h, "/api/detect", "found.csv", acme).Body.Bytes(), &out)
	v := out.Report.Verdict
	if v.Status != "attributed" || v.MarkID != issued[0].MarkID {
		for _, r := range out.Report.Results {
			t.Logf("%-12s %s", r.Technique, r.Detail)
		}
		t.Fatalf("verdict %+v, issued %+v", v, issued)
	}
	if who := out.Issuances[v.MarkID]; who.Recipient != "Acme Analytics AG" || who.Purpose != "churn study" {
		t.Errorf("issuance %+v", who)
	}
	for _, r := range out.Report.Results {
		if strings.Contains(r.Detail, "MK-2C41") {
			t.Errorf("%s mentions the unassigned mark: %s", r.Technique, r.Detail)
		}
		if r.Decision != nil {
			for _, c := range r.Decision.Ranking {
				if c.MarkID == "MK-2C41" {
					t.Errorf("%s ranks the unassigned mark", r.Technique)
				}
			}
		}
	}

	// The source itself.
	src := call(t, h, "GET", "/api/source.csv", nil)
	json.Unmarshal(upload(t, h, "/api/detect", "source.csv", src).Body.Bytes(), &out)
	if !out.Report.MatchesSource || out.Report.Verdict.Status != "absent" {
		t.Errorf("source: %+v", out.Report.Verdict)
	}

	// The log is scoped: licensing sees Acme only, without mark IDs.
	var lg struct {
		Log []struct {
			MarkID    string `json:"markId"`
			Recipient string `json:"recipient"`
		} `json:"log"`
		Hidden int `json:"hidden"`
	}
	json.Unmarshal(call(t, h, "GET", "/api/state?viewer=licensing-lead", nil), &lg)
	if len(lg.Log) != 1 || lg.Log[0].Recipient != "Acme Analytics AG" || lg.Log[0].MarkID != "restricted" || lg.Hidden != 1 {
		t.Errorf("licensing view %+v", lg)
	}
	json.Unmarshal(call(t, h, "GET", "/api/state?viewer=dpo", nil), &lg)
	if len(lg.Log) != 2 || lg.Log[0].MarkID == "restricted" {
		t.Errorf("DPO view %+v", lg)
	}
}

// Loading another table keeps the first table's issuances in the log, and
// loading the first again restores them under the same roles.
func TestLogSurvivesSwitchingTables(t *testing.T) {
	st := &store.Store{Dir: t.TempDir()}
	h := newTest(t, st)
	upload(t, h, "/api/source", "kunden.csv", swissCSV(300))
	call(t, h, "POST", "/api/issue", map[string]any{
		"recipients": []map[string]string{{"name": "Acme"}},
		"techniques": map[string]any{"canary": true, "redact": true},
	})
	call(t, h, "POST", "/api/source?sample=1", nil)
	var lg struct {
		Log []struct {
			Recipient string `json:"recipient"`
		} `json:"log"`
	}
	json.Unmarshal(call(t, h, "GET", "/api/state", nil), &lg)
	if len(lg.Log) != 1 {
		t.Fatalf("log after switching tables: %+v", lg.Log)
	}
	// Issue from the sample: the first table's entry must still be there.
	call(t, h, "POST", "/api/issue", map[string]any{
		"recipients": []map[string]string{{"name": "Beta"}},
		"techniques": map[string]any{"canary": true},
	})
	json.Unmarshal(call(t, h, "GET", "/api/state", nil), &lg)
	if len(lg.Log) != 2 {
		t.Fatalf("log lost an entry: %+v", lg.Log)
	}
	upload(t, h, "/api/source", "kunden.csv", swissCSV(300))
	var state struct {
		Issued []struct {
			Recipient string `json:"recipient"`
		} `json:"issued"`
	}
	json.Unmarshal(call(t, h, "GET", "/api/state", nil), &state)
	if len(state.Issued) != 1 || state.Issued[0].Recipient != "Acme" {
		t.Errorf("reloading the first table restored %+v", state.Issued)
	}
}
