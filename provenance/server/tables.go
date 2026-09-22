package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"attribution/common/codec"
	"attribution/common/webapp"
	"attribution/provenance/internal/tabular"
)

type tabLeak struct {
	table       *tabular.Table
	description string
}

type tabState struct {
	reg      *tabular.Registry
	columns  []tabular.Column
	source   string // where the table came from, for display
	fileBase string
	leaks    map[string]*tabLeak
}

const sampleTableSource = "Sample account data"

func (s *Server) reset(t *tabular.Table, source, fileBase string) {
	schema, cols := tabular.DetectSchema(t)
	s.tab = &tabState{
		reg:      &tabular.Registry{Key: s.key, Master: t, Schema: schema, Copies: map[uint16]*tabular.Table{}},
		columns:  cols,
		source:   source,
		fileBase: fileBase,
		leaks:    map[string]*tabLeak{},
	}
	s.restore()
}

func (s *Server) issuance(mark string) *tabular.Issuance {
	for _, is := range s.tab.reg.Issued {
		if is.Mark == mark {
			return is
		}
	}
	return nil
}

func (s *Server) stateHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tab.reg.Master
	webapp.WriteJSON(w, map[string]any{
		"source":   s.tab.source,
		"rows":     len(t.Rows),
		"columns":  s.tab.columns,
		"preview":  t.Preview(6),
		"schema":   s.tab.reg.Schema,
		"issued":   s.tab.reg.Issued,
		"attacks":  tabular.Battery(s.tab.reg.Schema),
		"marks":    s.marks(),
		"hasTable": len(t.Rows) > 0,
	})
}

func (s *Server) marks() []map[string]string {
	var out []map[string]string
	for _, is := range s.tab.reg.Issued {
		out = append(out, map[string]string{"markId": is.Mark, "label": is.Recipient})
	}
	return out
}

// tabSource loads a CSV upload or the built-in sample table.
func (s *Server) source(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("sample") == "1" {
		s.mu.Lock()
		s.reset(tabular.Generate(2000, uint64(time.Now().UnixNano())), sampleTableSource, "accounts")
		s.mu.Unlock()
		s.stateHandler(w, r)
		return
	}
	data, name, ok := webapp.ReadUpload(w, r)
	if !ok {
		return
	}
	t, err := tabular.ParseCSV(bytes.NewReader(data))
	if err != nil {
		webapp.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
		return
	}
	if len(t.Rows) < 20 {
		webapp.WriteErr(w, http.StatusBadRequest, "the table needs at least 20 rows to carry a mark")
		return
	}
	s.mu.Lock()
	s.reset(t, fmt.Sprintf("%s, %s", name, plural(len(t.Rows), "row", "rows")),
		fileStem(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))))
	s.mu.Unlock()
	s.stateHandler(w, r)
}

// tabSchema stores the column roles the data owner confirmed.
func (s *Server) schema(w http.ResponseWriter, r *http.Request) {
	var req tabular.Schema
	if !webapp.ReadJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := req.Validate(s.tab.reg.Master); err != nil {
		webapp.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.tab.reg.Schema = req
	s.tab.reg.Issued = nil
	s.tab.reg.Copies = map[uint16]*tabular.Table{}
	s.restore() // the schema changed, so the copies must be regenerated
	webapp.WriteJSON(w, map[string]any{"schema": req})
}

// previewID is the fixed demonstration mark the Mark tab works with, so
// toggling a measure changes only the measure and not the recipient.
const previewID = 0x2C41

// markedCSV downloads the copy the preview is showing, marked with whichever
// measures the query string turns on.
func (s *Server) markedCSV(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	on := func(k string) bool { return q.Get(k) == "1" }
	num := func(k string) int { n, _ := strconv.Atoi(q.Get(k)); return n }
	tech := tabular.Techniques{
		Canary: on("canary"), LowBit: on("lowBit"), Dummy: on("dummy"),
		Allocate: on("allocate"), Order: on("order"), Format: on("format"), Noise: on("noise"),
		Redact: on("redact"),
		Params: tabular.Params{
			CanaryRate: num("canaryRate"), AllocRate: num("allocRate"), LowBitGamma: num("lowBitGamma"),
			LowBitDepth: num("lowBitDepth"),
			NoiseRate:   num("noiseRate"), NoiseSpan: num("noiseSpan"),
			OrderGamma: num("orderGamma"), FormatRate: num("formatRate"),
		},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(tech)
	t := s.tab.reg.Copies[previewID]
	name := fmt.Sprintf("%s_%s.csv", s.tab.fileBase, codec.FormatID(previewID))
	webapp.SendFile(w, "text/csv", name, t.CSV(), false)
}

// record puts the copy the Mark tab is configured to produce on the issuance
// log, so Verify has something to check a recovered file against. There is one
// copy in this demonstrator, so registering it again simply replaces it.
func (s *Server) record(tech tabular.Techniques) {
	copyT, iss := tabular.MarkCopy(s.key, s.tab.reg.Master, s.tab.reg.Schema, previewID, tech)
	iss.Recipient = "the copy from the Mark tab"

	// Replace only the Mark tab's own entry. Copies issued to real recipients
	// stay on the log, or changing a measure would erase them.
	kept := s.tab.reg.Issued[:0]
	for _, is := range s.tab.reg.Issued {
		if is.MarkID != previewID {
			kept = append(kept, is)
		}
	}
	s.tab.reg.Issued = append(kept, iss)
	s.tab.reg.Copies[previewID] = copyT
}

// preview shows what the selected measures do to the head of the table,
// without issuing anything.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Techniques tabular.Techniques `json:"techniques"`
		Rows       int                `json:"rows"`
	}
	if !webapp.ReadJSON(w, r, &req) {
		return
	}
	if req.Rows <= 0 || req.Rows > 50 {
		req.Rows = 10
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(req.Techniques)
	res := tabular.PreviewMark(s.key, s.tab.reg.Master, s.tab.reg.Schema, previewID, req.Techniques, req.Rows)
	webapp.WriteJSON(w, map[string]any{
		"preview": res,
		"markId":  codec.FormatID(previewID),
		"source":  s.tab.source,
		"total":   len(s.tab.reg.Master.Rows),
	})
}

func (s *Server) sourceCSV(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	data, name := s.tab.reg.Master.CSV(), s.tab.fileBase+"_source.csv"
	s.mu.Unlock()
	webapp.SendFile(w, "text/csv", name, data, false)
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Recipients []struct {
			Name string `json:"name"`
			Org  string `json:"org"`
		} `json:"recipients"`
		Techniques tabular.Techniques `json:"techniques"`
	}
	if !webapp.ReadJSON(w, r, &req) {
		return
	}
	if len(req.Recipients) == 0 || len(req.Recipients) > 50 {
		webapp.WriteErr(w, http.StatusBadRequest, "issue between 1 and 50 copies")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*tabular.Issuance
	for _, rc := range req.Recipients {
		rc.Name = strings.TrimSpace(rc.Name)
		if rc.Name == "" {
			continue
		}
		var ids []uint16
		for _, is := range s.tab.reg.Issued {
			ids = append(ids, is.MarkID)
		}
		id := s.key.AllocateID(ids)
		copyT, is := tabular.MarkCopy(s.key, s.tab.reg.Master, s.tab.reg.Schema, id, req.Techniques)
		is.Recipient, is.Org, is.IssuedAt = rc.Name, strings.TrimSpace(rc.Org), time.Now()
		is.FileName = s.tab.fileBase + "_" + fileStem(rc.Name) + ".csv"
		s.tab.reg.Issued = append(s.tab.reg.Issued, is)
		s.tab.reg.Copies[id] = copyT
		out = append(out, is)
	}
	s.saveLog()
	webapp.WriteJSON(w, out)
}

// recall removes an issuance from the log. The copy already handed over is of
// course still out there; what goes is this centre's record of who has it, so
// a recovered file will no longer be traced to them.
func (s *Server) recall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mark string `json:"mark"`
	}
	if !webapp.ReadJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.tab.reg.Issued[:0]
	found := false
	for _, is := range s.tab.reg.Issued {
		if is.Mark == req.Mark && is.MarkID != previewID {
			delete(s.tab.reg.Copies, is.MarkID)
			found = true
			continue
		}
		kept = append(kept, is)
	}
	s.tab.reg.Issued = kept
	if !found {
		webapp.WriteErr(w, http.StatusNotFound, "unknown mark")
		return
	}
	s.saveLog()
	webapp.WriteJSON(w, map[string]any{"recalled": req.Mark})
}

func (s *Server) copy(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issuance(r.URL.Query().Get("mark"))
	if is == nil {
		webapp.WriteErr(w, http.StatusNotFound, "unknown mark")
		return
	}
	webapp.SendFile(w, "text/csv", is.FileName, s.tab.reg.Copies[is.MarkID].CSV(), false)
}

func (s *Server) bundle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var copies []*tabular.Issuance
	for _, mark := range strings.Split(r.URL.Query().Get("marks"), ",") {
		if is := s.issuance(strings.TrimSpace(mark)); is != nil {
			copies = append(copies, is)
		}
	}
	base := s.tab.fileBase
	var data [][]byte
	for _, is := range copies {
		data = append(data, s.tab.reg.Copies[is.MarkID].CSV())
	}
	s.mu.Unlock()
	if len(copies) == 0 {
		webapp.WriteErr(w, http.StatusNotFound, "no matching copies")
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, is := range copies {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: is.FileName, Method: zip.Deflate, Modified: is.IssuedAt})
		if err == nil {
			_, err = f.Write(data[i])
		}
		if err != nil {
			webapp.WriteErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	zw.Close()
	webapp.SendFile(w, "application/zip", base+"_marked-copies.zip", buf.Bytes(), false)
}

func (s *Server) detect(w http.ResponseWriter, r *http.Request) {
	data, name, ok := webapp.ReadUpload(w, r)
	if !ok {
		return
	}
	t, err := tabular.ParseCSV(bytes.NewReader(data))
	if err != nil {
		webapp.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tab.reg.Issued) == 0 {
		webapp.WriteErr(w, http.StatusBadRequest, "nothing has been marked yet: choose measures on the Mark tab first")
		return
	}
	webapp.WriteJSON(w, map[string]any{
		"name": name, "rows": len(t.Rows), "columns": t.Columns, "size": len(data),
		"preview": t.Preview(6), "report": s.tab.reg.Detect(t),
	})
}

// ---- testing page: leak simulator and robustness matrix ----

func (s *Server) leak(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mark   string         `json:"mark"`
		Attack tabular.Attack `json:"attack"`
	}
	if !webapp.ReadJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issuance(req.Mark)
	if is == nil {
		webapp.WriteErr(w, http.StatusNotFound, "unknown mark")
		return
	}
	if req.Attack.Seed == 0 {
		req.Attack.Seed = uint64(time.Now().UnixNano())
	}
	leaked := tabular.Apply(s.tab.reg.Copies[is.MarkID], s.tab.reg.Schema, req.Attack)
	id := newID()
	s.tab.leaks[id] = &tabLeak{table: leaked, description: req.Attack.Describe()}
	webapp.WriteJSON(w, map[string]any{
		"id":          id,
		"description": req.Attack.Describe(),
		"truth":       map[string]string{"markId": is.Mark, "recipient": is.Recipient},
		"rows":        len(leaked.Rows),
		"columns":     leaked.Columns,
		"preview":     leaked.Preview(6),
		"report":      s.tab.reg.Detect(leaked),
	})
}

func (s *Server) leakCSV(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	l := s.tab.leaks[r.URL.Query().Get("id")]
	s.mu.Unlock()
	if l == nil {
		webapp.WriteErr(w, http.StatusNotFound, "unknown file")
		return
	}
	webapp.SendFile(w, "text/csv", "recovered.csv", l.table.CSV(), false)
}

func (s *Server) matrix(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mark string `json:"mark"`
	}
	if !webapp.ReadJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issuance(req.Mark)
	if is == nil {
		webapp.WriteErr(w, http.StatusNotFound, "unknown mark")
		return
	}
	var techniques []string
	var rows []matrixRow
	for i, b := range tabular.Battery(s.tab.reg.Schema) {
		b.Attack.Seed = uint64(i + 1)
		rep := s.tab.reg.Detect(tabular.Apply(s.tab.reg.Copies[is.MarkID], s.tab.reg.Schema, b.Attack))
		row := matrixRow{Attack: b.Name, Description: b.Attack.Describe()}
		for _, res := range rep.Results {
			if i == 0 {
				techniques = append(techniques, res.Name)
			}
			row.Cells = append(row.Cells, cell(res.Status, res.MarkID == is.Mark, res.Detail, res.Recipient))
		}
		row.Verdict = cell(rep.Verdict.Status, rep.Verdict.MarkID == is.Mark, rep.Verdict.Detail, rep.Verdict.Recipient)
		rows = append(rows, row)
	}
	webapp.WriteJSON(w, map[string]any{"truth": codec.FormatID(is.MarkID), "recipient": is.Recipient, "techniques": techniques, "rows": rows})
}

type matrixCell struct {
	Status  string `json:"status"`
	Correct bool   `json:"correct"`
	Detail  string `json:"detail"`
	Who     string `json:"who,omitempty"`
}

type matrixRow struct {
	Attack      string       `json:"attack"`
	Description string       `json:"description"`
	Cells       []matrixCell `json:"cells"`
	Verdict     matrixCell   `json:"verdict"`
}

func cell(status string, correct bool, detail, who string) matrixCell {
	return matrixCell{Status: status, Correct: status == "attributed" && correct, Detail: detail, Who: who}
}

// plural renders a count with the matching noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
