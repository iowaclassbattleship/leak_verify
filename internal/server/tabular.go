package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"custodial/internal/codec"
	"custodial/internal/tabular"
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

func (s *Server) resetTabular(t *tabular.Table, source, fileBase string) {
	schema, cols := tabular.DetectSchema(t)
	s.tab = &tabState{
		reg:      &tabular.Registry{Key: s.key, Master: t, Schema: schema, Copies: map[uint16]*tabular.Table{}},
		columns:  cols,
		source:   source,
		fileBase: fileBase,
		leaks:    map[string]*tabLeak{},
	}
}

func (s *Server) tabIssuance(mark string) *tabular.Issuance {
	for _, is := range s.tab.reg.Issued {
		if is.Mark == mark {
			return is
		}
	}
	return nil
}

func (s *Server) tabStateHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tab.reg.Master
	writeJSON(w, map[string]any{
		"source":   s.tab.source,
		"rows":     len(t.Rows),
		"columns":  s.tab.columns,
		"preview":  t.Preview(6),
		"schema":   s.tab.reg.Schema,
		"issued":   s.tab.reg.Issued,
		"attacks":  tabular.Battery(s.tab.reg.Schema),
		"marks":    s.tabMarks(),
		"hasTable": len(t.Rows) > 0,
	})
}

func (s *Server) tabMarks() []map[string]string {
	var out []map[string]string
	for _, is := range s.tab.reg.Issued {
		out = append(out, map[string]string{"markId": is.Mark, "label": is.Recipient})
	}
	return out
}

// tabSource loads a CSV upload or the built-in sample table.
func (s *Server) tabSource(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("sample") == "1" {
		s.mu.Lock()
		s.resetTabular(tabular.Generate(2000, uint64(time.Now().UnixNano())), sampleTableSource, "accounts")
		s.mu.Unlock()
		s.tabStateHandler(w, r)
		return
	}
	data, name, ok := readUpload(w, r)
	if !ok {
		return
	}
	t, err := tabular.ParseCSV(bytes.NewReader(data))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
		return
	}
	if len(t.Rows) < 20 {
		writeErr(w, http.StatusBadRequest, "the table needs at least 20 rows to carry a mark")
		return
	}
	s.mu.Lock()
	s.resetTabular(t, fmt.Sprintf("%s, %s", name, plural(len(t.Rows), "row", "rows")),
		fileStem(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))))
	s.mu.Unlock()
	s.tabStateHandler(w, r)
}

// tabSchema stores the column roles the data owner confirmed.
func (s *Server) tabSchema(w http.ResponseWriter, r *http.Request) {
	var req tabular.Schema
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := req.Validate(s.tab.reg.Master); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.tab.reg.Schema = req
	s.tab.reg.Issued = nil
	s.tab.reg.Copies = map[uint16]*tabular.Table{}
	writeJSON(w, map[string]any{"schema": req})
}

func (s *Server) tabSourceCSV(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	data, name := s.tab.reg.Master.CSV(), s.tab.fileBase+"_source.csv"
	s.mu.Unlock()
	sendFile(w, "text/csv", name, data, false)
}

func (s *Server) tabIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Recipients []struct {
			Name string `json:"name"`
			Org  string `json:"org"`
		} `json:"recipients"`
		Techniques tabular.Techniques `json:"techniques"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Recipients) == 0 || len(req.Recipients) > 50 {
		writeErr(w, http.StatusBadRequest, "issue between 1 and 50 copies")
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
	writeJSON(w, out)
}

func (s *Server) tabCopy(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.tabIssuance(r.URL.Query().Get("mark"))
	if is == nil {
		writeErr(w, http.StatusNotFound, "unknown mark")
		return
	}
	sendFile(w, "text/csv", is.FileName, s.tab.reg.Copies[is.MarkID].CSV(), false)
}

func (s *Server) tabBundle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var copies []*tabular.Issuance
	for _, mark := range strings.Split(r.URL.Query().Get("marks"), ",") {
		if is := s.tabIssuance(strings.TrimSpace(mark)); is != nil {
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
		writeErr(w, http.StatusNotFound, "no matching tagged copies")
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
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	zw.Close()
	sendFile(w, "application/zip", base+"_tagged-copies.zip", buf.Bytes(), false)
}

func (s *Server) tabDetect(w http.ResponseWriter, r *http.Request) {
	data, name, ok := readUpload(w, r)
	if !ok {
		return
	}
	t, err := tabular.ParseCSV(bytes.NewReader(data))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, map[string]any{
		"name": name, "rows": len(t.Rows), "columns": t.Columns,
		"preview": t.Preview(6), "report": s.tab.reg.Detect(t),
	})
}

// ---- testing page: leak simulator and robustness matrix ----

func (s *Server) tabLeak(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mark   string         `json:"mark"`
		Attack tabular.Attack `json:"attack"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.tabIssuance(req.Mark)
	if is == nil {
		writeErr(w, http.StatusNotFound, "unknown mark")
		return
	}
	if req.Attack.Seed == 0 {
		req.Attack.Seed = uint64(time.Now().UnixNano())
	}
	leaked := tabular.Apply(s.tab.reg.Copies[is.MarkID], s.tab.reg.Schema, req.Attack)
	id := newID()
	s.tab.leaks[id] = &tabLeak{table: leaked, description: req.Attack.Describe()}
	writeJSON(w, map[string]any{
		"id":          id,
		"description": req.Attack.Describe(),
		"truth":       map[string]string{"markId": is.Mark, "recipient": is.Recipient},
		"rows":        len(leaked.Rows),
		"columns":     leaked.Columns,
		"preview":     leaked.Preview(6),
		"report":      s.tab.reg.Detect(leaked),
	})
}

func (s *Server) tabLeakCSV(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	l := s.tab.leaks[r.URL.Query().Get("id")]
	s.mu.Unlock()
	if l == nil {
		writeErr(w, http.StatusNotFound, "unknown file")
		return
	}
	sendFile(w, "text/csv", "recovered.csv", l.table.CSV(), false)
}

func (s *Server) tabMatrix(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mark string `json:"mark"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.tabIssuance(req.Mark)
	if is == nil {
		writeErr(w, http.StatusNotFound, "unknown mark")
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
	writeJSON(w, map[string]any{"truth": codec.FormatID(is.MarkID), "recipient": is.Recipient, "techniques": techniques, "rows": rows})
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
