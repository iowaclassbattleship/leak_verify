package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
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
	s.tables[source] = t
	schema, cols := tabular.DetectSchema(t)
	if prev := s.loggedSchema(source); prev != nil && prev.Validate(t) == nil {
		schema = *prev // copies of this table were issued under these roles
	}
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

// Unit is a node of the hierarchy recipients are issued to. A viewer sees the
// log entries for their own subtree; only Data Governance sees the mark IDs
// that tie a recovered file to a recipient.
type Unit struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Parent string `json:"parent"`
}

var units = []Unit{
	{"gov", "Data Governance", ""},
	{"research", "Research Partners", "gov"},
	{"commercial", "Commercial Licensees", "gov"},
	{"internal", "Internal Analytics", "gov"},
	{"external", "Other External Recipients", "gov"},
}

type Viewer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Unit string `json:"unit"`
}

var viewers = []Viewer{
	{"dpo", "Data Protection Officer", "gov"},
	{"research-lead", "Head of Research Partnerships", "research"},
	{"licensing-lead", "Head of Data Licensing", "commercial"},
}

func inSubtree(unit, root string) bool {
	for u := unit; u != ""; {
		if u == root {
			return true
		}
		parent := ""
		for _, x := range units {
			if x.ID == u {
				parent = x.Parent
			}
		}
		u = parent
	}
	return false
}

func (s *Server) stateHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tab.reg.Master
	viewer := viewers[0]
	for _, v := range viewers {
		if v.ID == r.URL.Query().Get("viewer") {
			viewer = v
		}
	}
	type logEntry struct {
		MarkID     string             `json:"markId"`
		Recipient  string             `json:"recipient"`
		Purpose    string             `json:"purpose"`
		Unit       string             `json:"unit"`
		IssuedAt   time.Time          `json:"issuedAt"`
		FileName   string             `json:"fileName"`
		Source     string             `json:"source"`
		Techniques tabular.Techniques `json:"techniques"`
	}
	var log []logEntry
	hidden := 0
	for _, e := range s.logged {
		unit := e.Unit
		if unit == "" {
			unit = "external"
		}
		if !inSubtree(unit, viewer.Unit) {
			hidden++
			continue
		}
		mark := e.Mark
		if viewer.Unit != "gov" {
			mark = "restricted"
		}
		log = append(log, logEntry{mark, e.Recipient, e.Org, unit, e.IssuedAt, e.FileName, e.Source, e.Techniques})
	}
	webapp.WriteJSON(w, map[string]any{
		"source":   s.tab.source,
		"format":   t.Format,
		"rows":     len(t.Rows),
		"columns":  s.tab.columns,
		"preview":  t.Preview(6),
		"schema":   s.tab.reg.Schema,
		"issued":   s.tab.reg.Issued,
		"attacks":  tabular.Battery(s.tab.reg.Schema),
		"marks":    s.marks(),
		"hasTable": len(t.Rows) > 0,
		"units":    units,
		"viewers":  viewers,
		"viewer":   viewer,
		"log":      log,
		"hidden":   hidden,
		"held":     s.heldSources(),
	})
}

// issued lists the copies handed to real recipients, without the Mark tab's
// own unassigned copy.
func (s *Server) issued() []*tabular.Issuance {
	var out []*tabular.Issuance
	for _, is := range s.tab.reg.Issued {
		if is.MarkID != previewID {
			out = append(out, is)
		}
	}
	return out
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
		s.reset(s.sampleTable(), sampleTableSource, "accounts")
		s.mu.Unlock()
		s.stateHandler(w, r)
		return
	}
	data, name, ok := webapp.ReadUpload(w, r)
	if !ok {
		return
	}
	t, err := tabular.ParseCSVWith(bytes.NewReader(data), formatFrom(r))
	if err != nil {
		webapp.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
		return
	}
	if len(t.Rows) < 20 {
		webapp.WriteErr(w, http.StatusUnprocessableEntity, fmt.Sprintf("%s has %s, and a mark needs at least 20", name, plural(len(t.Rows), "row", "rows")))
		return
	}
	if len(t.Columns) < 2 {
		webapp.WriteErr(w, http.StatusUnprocessableEntity, fmt.Sprintf("%s reads as a single column. Choose the delimiter it uses and load it again.", name))
		return
	}
	s.mu.Lock()
	s.reset(t, name, fileStem(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))))
	s.mu.Unlock()
	s.stateHandler(w, r)
}

// formatFrom reads a CSV dialect override from the query string: delimiter
// is one of , ; tab |, and decimal=comma reads 7951,14 as a number.
func formatFrom(r *http.Request) tabular.Format {
	var f tabular.Format
	switch d := r.URL.Query().Get("delimiter"); d {
	case ",", ";", "|":
		f.Delimiter = d
	case "tab", "\t":
		f.Delimiter = "\t"
	}
	f.DecimalComma = r.URL.Query().Get("decimal") == "comma"
	return f
}

// tabSchema stores the column roles the data owner confirmed.
func (s *Server) schema(w http.ResponseWriter, r *http.Request) {
	var req tabular.Schema
	if !webapp.ReadJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.issued()); n > 0 {
		// Every copy of a table is read back under one set of roles. Changing
		// them now would make the copies already handed out unverifiable.
		webapp.WriteErr(w, http.StatusConflict, fmt.Sprintf("the column roles are fixed, because %s issued under them. Recall %s to change the roles.", plural(n, "copy was", "copies were"), map[bool]string{true: "it", false: "them"}[n == 1]))
		return
	}
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
			Unit string `json:"unit"`
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
		is.Unit = "external"
		for _, u := range units {
			if u.ID == rc.Unit && u.Parent != "" {
				is.Unit = u.ID
			}
		}
		is.FileName = s.uniqueFileName(rc.Name, is.Org)
		s.tab.reg.Issued = append(s.tab.reg.Issued, is)
		s.tab.reg.Copies[id] = copyT
		out = append(out, is)
	}
	if len(out) == 0 {
		webapp.WriteErr(w, http.StatusBadRequest, "every recipient needs a name")
		return
	}
	s.saveLog()
	webapp.WriteJSON(w, out)
}

// uniqueFileName names a copy after the table and the recipient, adding a
// number when another copy already carries that name, so no download
// overwrites another.
func (s *Server) uniqueFileName(name, purpose string) string {
	stem := s.tab.fileBase + "_" + fileStem(name)
	for n := 1; ; n++ {
		file := stem + ".csv"
		if n > 1 {
			file = fmt.Sprintf("%s_%d.csv", stem, n)
		}
		taken := false
		for _, is := range s.tab.reg.Issued {
			if strings.EqualFold(is.FileName, file) {
				taken = true
			}
		}
		if !taken {
			return file
		}
	}
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

// detect checks a recovered file against every copy on the issuance log,
// whichever table the Mark tab has loaded. Each table the copies were issued
// from is regenerated with the roles they were issued under, and the
// strongest reading wins. The Mark tab's unassigned copy is never a
// candidate: nobody received it.
func (s *Server) detect(w http.ResponseWriter, r *http.Request) {
	data, name, ok := webapp.ReadUpload(w, r)
	if !ok {
		return
	}
	t, err := tabular.ParseCSVWith(bytes.NewReader(data), formatFrom(r))
	if err != nil {
		webapp.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	regs, missing := s.registries()
	if len(regs) == 0 {
		msg := "nothing has been issued yet: issue a copy on the Mark tab first"
		if len(missing) > 0 {
			msg = fmt.Sprintf("the copies on the log were issued from %s, which is not loaded in this session. Load it on the Mark tab, then verify again", joinAnd(missing))
		}
		webapp.WriteErr(w, http.StatusBadRequest, msg)
		return
	}
	var best *tableReading
	for _, tr := range regs {
		rd := &tableReading{source: tr.source, report: tr.reg.Detect(t), reg: tr.reg}
		if best == nil || rd.better(best) {
			best = rd
		}
	}
	rep := best.report
	who := map[string]any{}
	add := func(mark string) {
		for _, is := range best.reg.Issued {
			if is.Mark == mark {
				who[mark] = map[string]any{"recipient": is.Recipient, "purpose": is.Org, "issuedAt": is.IssuedAt, "fileName": is.FileName, "unit": is.Unit}
			}
		}
	}
	add(rep.Verdict.MarkID)
	for _, c := range rep.Verdict.Candidates {
		add(c.MarkID)
	}
	webapp.WriteJSON(w, map[string]any{
		"name": name, "rows": len(t.Rows), "columns": t.Columns, "size": len(data),
		"preview": t.Preview(6), "report": rep, "issuances": who,
		"table": best.source, "tables": len(regs), "unavailable": missing,
	})
}

type sourceRegistry struct {
	source string
	reg    *tabular.Registry
}

// registries builds one registry per table that has issued copies and is
// held in this session. missing lists the tables with copies on the log that
// are not loaded, so they cannot be regenerated.
func (s *Server) registries() (out []sourceRegistry, missing []string) {
	var order []string
	bySource := map[string][]loggedIssuance{}
	for _, e := range s.logged {
		if _, seen := bySource[e.Source]; !seen {
			order = append(order, e.Source)
		}
		bySource[e.Source] = append(bySource[e.Source], e)
	}
	for _, src := range order {
		if src == s.tab.source {
			reg := *s.tab.reg
			reg.Issued = s.issued()
			if len(reg.Issued) > 0 {
				out = append(out, sourceRegistry{src, &reg})
			}
			continue
		}
		master := s.tables[src]
		if master == nil {
			missing = append(missing, src)
			continue
		}
		schema, _ := tabular.DetectSchema(master)
		if prev := s.loggedSchema(src); prev != nil && prev.Validate(master) == nil {
			schema = *prev
		}
		reg := &tabular.Registry{Key: s.key, Master: master, Schema: schema, Copies: map[uint16]*tabular.Table{}}
		for _, e := range bySource[src] {
			copyT, iss := tabular.MarkCopy(s.key, master, schema, e.MarkID, e.Techniques)
			iss.Recipient, iss.Org, iss.Unit, iss.IssuedAt, iss.FileName = e.Recipient, e.Org, e.Unit, e.IssuedAt, e.FileName
			reg.Issued = append(reg.Issued, iss)
			reg.Copies[e.MarkID] = copyT
		}
		out = append(out, sourceRegistry{src, reg})
	}
	return out, missing
}

// tableReading is a recovered file read against one table's copies.
type tableReading struct {
	source string
	report tabular.Report
	reg    *tabular.Registry
}

func (a *tableReading) rank() int {
	switch v := a.report.Verdict; {
	case v.Status == "attributed" || v.Status == "merged":
		return 4
	case a.report.MatchesSource:
		return 3
	case v.Status == "inconclusive":
		return 2
	}
	return 1
}

// better prefers a stronger verdict, then the table more rows match.
func (a *tableReading) better(b *tableReading) bool {
	if a.rank() != b.rank() {
		return a.rank() > b.rank()
	}
	return a.report.ResolvedRows > b.report.ResolvedRows
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
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

// heldSources lists the tables loaded in this session.
func (s *Server) heldSources() []string {
	out := make([]string, 0, len(s.tables))
	for src := range s.tables {
		out = append(out, src)
	}
	sort.Strings(out)
	return out
}
