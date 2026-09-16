package server

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"custodial/internal/codec"
	"custodial/internal/tabular"
)

type tabLeak struct {
	table       *tabular.Table
	description string
	source      string
}

type tabState struct {
	reg   *tabular.Registry
	leaks map[string]*tabLeak
}

func (s *Server) resetTabular(rows int) {
	master := tabular.Generate(rows, uint64(time.Now().UnixNano()))
	s.tab = &tabState{
		reg:   &tabular.Registry{Key: s.key, Master: master, Copies: map[uint16]*tabular.Table{}},
		leaks: map[string]*tabLeak{},
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
	m := s.tab.reg.Master
	writeJSON(w, map[string]any{
		"rows":     len(m.Rows),
		"columns":  m.Columns,
		"preview":  m.Preview(8),
		"tolerant": tabular.Tolerant,
		"issued":   s.tab.reg.Issued,
		"battery":  batteryNames(),
	})
}

func batteryNames() []string {
	var n []string
	for _, b := range tabular.Battery() {
		n = append(n, b.Name)
	}
	return n
}

func (s *Server) tabDataset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rows int `json:"rows"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Rows < 100 || req.Rows > 50000 {
		writeErr(w, http.StatusBadRequest, "rows must be between 100 and 50000")
		return
	}
	s.mu.Lock()
	s.resetTabular(req.Rows)
	s.mu.Unlock()
	s.tabStateHandler(w, r)
}

func (s *Server) tabSourceCSV(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	data := s.tab.reg.Master.CSV()
	s.mu.Unlock()
	sendFile(w, "text/csv", "accounts_source_UNMARKED.csv", data, false)
}

func (s *Server) tabIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Recipient  string             `json:"recipient"`
		Org        string             `json:"org"`
		Techniques tabular.Techniques `json:"techniques"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Recipient, req.Org = strings.TrimSpace(req.Recipient), strings.TrimSpace(req.Org)
	if req.Recipient == "" {
		writeErr(w, http.StatusBadRequest, "recipient name required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []uint16
	for _, is := range s.tab.reg.Issued {
		ids = append(ids, is.MarkID)
	}
	id := s.key.AllocateID(ids)
	copyT, is := tabular.MarkCopy(s.key, s.tab.reg.Master, id, req.Techniques)
	is.Recipient, is.Org, is.IssuedAt = req.Recipient, req.Org, time.Now()
	s.tab.reg.Issued = append(s.tab.reg.Issued, is)
	s.tab.reg.Copies[id] = copyT
	writeJSON(w, is)
}

func (s *Server) tabCopy(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.tabIssuance(r.URL.Query().Get("mark"))
	if is == nil {
		writeErr(w, http.StatusNotFound, "unknown mark")
		return
	}
	name := strings.ToLower(strings.Join(strings.Fields(is.Recipient), "_"))
	sendFile(w, "text/csv", "accounts_"+name+".csv", s.tab.reg.Copies[is.MarkID].CSV(), false)
}

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
	leaked := tabular.Apply(s.tab.reg.Copies[is.MarkID], req.Attack)
	id := newID()
	s.tab.leaks[id] = &tabLeak{table: leaked, description: req.Attack.Describe(), source: is.Mark}
	writeJSON(w, map[string]any{
		"id":          id,
		"description": req.Attack.Describe(),
		"truth":       map[string]string{"markId": is.Mark, "recipient": is.Recipient + " (" + is.Org + ")"},
		"rows":        len(leaked.Rows),
		"columns":     leaked.Columns,
		"preview":     leaked.Preview(8),
		"report":      s.tab.reg.Detect(leaked),
	})
}

func (s *Server) tabLeakCSV(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	l := s.tab.leaks[r.URL.Query().Get("id")]
	s.mu.Unlock()
	if l == nil {
		writeErr(w, http.StatusNotFound, "unknown leak")
		return
	}
	sendFile(w, "text/csv", "recovered_leak.csv", l.table.CSV(), false)
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
	writeJSON(w, map[string]any{"name": name, "rows": len(t.Rows), "columns": t.Columns, "preview": t.Preview(8), "report": s.tab.reg.Detect(t)})
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
	for i, b := range tabular.Battery() {
		b.Attack.Seed = uint64(i + 1)
		rep := s.tab.reg.Detect(tabular.Apply(s.tab.reg.Copies[is.MarkID], b.Attack))
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

func cell(status string, correct bool, detail, who string) matrixCell {
	return matrixCell{Status: status, Correct: status == "attributed" && correct, Detail: detail, Who: who}
}
