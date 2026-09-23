package server

import (
	"archive/zip"
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
	"attribution/custodial/internal/document"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return New(codec.NewKey(), "qa@example.com", nil).Handler(fstest.MapFS{}, "")
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

func call(t *testing.T, h http.Handler, method, path string, v any) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if v != nil {
		b, _ := json.Marshal(v)
		body = bytes.NewReader(b)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, body))
	return rec
}

// issue creates one copy of whatever is loaded, with every default layer.
func issue(t *testing.T, h http.Handler, name string) (mark string, data []byte) {
	t.Helper()
	var st struct {
		Layers []struct {
			Key     string `json:"key"`
			Default bool   `json:"default"`
		} `json:"layers"`
	}
	json.Unmarshal(call(t, h, "GET", "/api/state", nil).Body.Bytes(), &st)
	layers := map[string]bool{}
	for _, l := range st.Layers {
		layers[l.Key] = l.Default
	}
	rec := call(t, h, "POST", "/api/issue", map[string]any{
		"recipients": []map[string]string{{"name": name, "role": "Tester", "unit": "ext"}},
		"layers":     layers,
	})
	var out []struct {
		MarkID string `json:"markId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out) != 1 {
		t.Fatalf("issue: %s", rec.Body.String())
	}
	copyRec := call(t, h, "GET", "/api/copy?mark="+out[0].MarkID+"&download=1", nil)
	return out[0].MarkID, copyRec.Body.Bytes()
}

func verify(t *testing.T, h http.Handler, name string, data []byte) (status, mark, fileName string) {
	t.Helper()
	rec := upload(t, h, "/api/detect", name, data)
	var out struct {
		Report struct {
			Verdict struct {
				Status string `json:"status"`
				MarkID string `json:"markId"`
			} `json:"verdict"`
		} `json:"report"`
		Issuance struct {
			FileName string `json:"fileName"`
		} `json:"issuance"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("detect %s: %d %s", name, rec.Code, rec.Body.String())
	}
	return out.Report.Verdict.Status, out.Report.Verdict.MarkID, out.Issuance.FileName
}

func memoDocx(t *testing.T) []byte {
	t.Helper()
	var body strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&body, `<w:p><w:r><w:t xml:space="preserve">Paragraph %d of the memo covers item %d, with budget figures, owners and the dates each workstream reports back to the committee.</w:t></w:r></w:p>`, i, i*7)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() + `</w:body></w:document>`,
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

// A copy is verified against the whole issuance log, not only against the
// document loaded last: tag a .docx, then a PDF, then check the .docx.
func TestVerifyAgainstWholeLog(t *testing.T) {
	srv := New(codec.NewKey(), "qa@example.com", nil)
	h := srv.Handler(fstest.MapFS{}, "")
	if rec := upload(t, h, "/api/source", "memo.docx", memoDocx(t)); rec.Code != 200 {
		t.Fatalf("load docx: %s", rec.Body.String())
	}
	docxMark, docx := issue(t, h, "Anna")

	if rec := call(t, h, "POST", "/api/source?sample=1", nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	pdfMark, pdf := issue(t, h, "Ben")

	if st, mark, file := verify(t, h, "memo_Anna.docx", docx); st != "attributed" || mark != docxMark || file != "memo_Anna.docx" {
		t.Errorf("docx after loading a PDF: %s %s %q", st, mark, file)
	}

	// And the other way round: load a .docx again, then check the PDF copy,
	// and a screenshot of it, which could as well be a rendering of the docx.
	upload(t, h, "/api/source", "memo.docx", memoDocx(t))
	if st, mark, _ := verify(t, h, "briefing_Ben.pdf", pdf); st != "attributed" || mark != pdfMark {
		t.Errorf("PDF after loading a docx: %s %s", st, mark)
	}
	shot, err := document.NewEngine(srv.key).Attack(pdf, "screenshot")
	if err != nil {
		t.Fatal(err)
	}
	if st, mark, _ := verify(t, h, "screenshot.png", shot.Data); st != "attributed" || mark != pdfMark {
		t.Errorf("screenshot of the PDF after loading a docx: %s %s", st, mark)
	}
}

// A failed upload leaves the loaded document alone and says why.
func TestFailedUploadKeepsDocument(t *testing.T) {
	h := newTestServer(t)
	rec := upload(t, h, "/api/source", "scan.pdf", []byte("%PDF-1.4\n%%EOF"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "scan.pdf") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

// Two people called Claudia Frei get separate files; issuing to the same
// Claudia Frei twice keeps her file name.
func TestFileNamesUnique(t *testing.T) {
	h := newTestServer(t)
	rec := call(t, h, "POST", "/api/issue", map[string]any{
		"recipients": []map[string]string{
			{"name": "Claudia Frei", "role": "Board Chair", "unit": "board"},
			{"name": "Claudia Frei", "role": "Adviser", "unit": "ext"},
		},
		"layers": map[string]bool{"layout": true},
	})
	var out []struct {
		FileName string `json:"fileName"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out) != 2 || out[0].FileName == out[1].FileName {
		t.Fatalf("file names %+v", out)
	}
	rec = call(t, h, "POST", "/api/issue", map[string]any{
		"recipients": []map[string]string{{"name": "Claudia Frei", "role": "Board Chair", "unit": "board"}},
		"layers":     map[string]bool{"layout": true},
	})
	var again []struct {
		FileName string `json:"fileName"`
	}
	json.Unmarshal(rec.Body.Bytes(), &again)
	if len(again) != 1 || again[0].FileName != out[0].FileName {
		t.Errorf("re-issue named %+v, want %s", again, out[0].FileName)
	}
}
