package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"attribution/common/codec"
	"attribution/common/webapp"
	"attribution/custodial/internal/document"
	"attribution/custodial/internal/office"
)

// Unit is a node of the privilege hierarchy. A viewer sees issuance-log
// entries for recipients in their own subtree; only the Security Office sees
// the mark IDs that link a leaked artifact to a person.
type Unit struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Parent string `json:"parent"`
}

var units = []Unit{
	{"sec", "Security Office", ""},
	{"board", "Board of Directors", "sec"},
	{"exec", "Executive Committee", "board"},
	{"fin", "Finance", "exec"},
	{"legal", "Legal", "exec"},
	{"ext", "External Advisers", "legal"},
}

type Viewer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Unit string `json:"unit"`
}

var viewers = []Viewer{
	{"cso", "Chief Security Officer", "sec"},
	{"secretary", "Company Secretary (Board)", "board"},
	{"counsel", "General Counsel", "legal"},
	{"cfo", "Chief Financial Officer", "fin"},
}

type rosterEntry struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Unit string `json:"unit"`
}

var roster = []rosterEntry{
	{"Claudia Frei", "Board Chair", "board"},
	{"Martin Keller", "Non-executive Director", "board"},
	{"Sophie Laurent", "Chief Executive Officer", "exec"},
	{"Daniel Roth", "Chief Financial Officer", "fin"},
	{"Aline Moser", "Head of Treasury", "fin"},
	{"Peter Graf", "General Counsel", "legal"},
	{"Nora Bianchi", "External Counsel", "ext"},
	{"Lukas Weber", "M&A Adviser", "ext"},
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

type docIssuance struct {
	id        uint16
	data      []byte
	tag       string          // metadata value, for Open XML files
	MarkID    string          `json:"markId"`
	Name      string          `json:"name"`
	Role      string          `json:"role"`
	Unit      string          `json:"unit"`
	IssuedAt  time.Time       `json:"issuedAt"`
	IssuedBy  string          `json:"issuedBy"`
	Layers    map[string]bool `json:"layers"`
	SizeBytes int             `json:"sizeBytes"`
	FileName  string          `json:"fileName"`
	Source    string          `json:"source"`
	Document  string          `json:"document"`           // the file or sample the copy was made from
	Restored  bool            `json:"restored,omitempty"` // issued before a restart, bytes gone
}

func (d *docIssuance) label() string { return d.Name + " (" + d.Role + ")" }

// docState holds the one document being shared: either a PDF the demo
// typesets itself, or an Open XML file marked in place.
type docState struct {
	master   *document.Master // PDF pipeline
	office   *office.File     // Open XML pipeline
	kind     string           // pdf | docx | pptx | xlsx
	source   string           // shown in the UI
	warnings []string
	fileBase string // download name stem, from the uploaded file name
	ext      string
	title    string
	name     string // what the log calls the document: the uploaded file name, or the sample
	issued   []*docIssuance
}

const (
	sampleSource = "Built-in sample briefing"
	sampleName   = "Project Halcyon board briefing (sample)"
)

// keptIssued carries the issuance log across a change of source. The log
// belongs to the user, not to whichever document happens to be loaded.
func (s *Server) keptIssued() []*docIssuance {
	if s.doc == nil {
		return nil
	}
	return s.doc.issued
}

func (s *Server) resetDocument(m *document.Master, fileBase string) {
	s.masters[m.Source] = m
	s.doc = &docState{master: m, kind: "pdf", source: m.Source, warnings: m.Warnings,
		fileBase: fileBase, ext: ".pdf", issued: s.keptIssued(), name: sampleName}
}

func (s *Server) resetOffice(f *office.File, name, source, fileBase string) {
	st := &docState{office: f, kind: string(f.Kind), source: source, fileBase: fileBase,
		ext: "." + string(f.Kind), title: name, name: name, issued: s.keptIssued()}
	stats := f.StatsWithKey(s.key)
	if stats.SpacingBits < codec.CodeLen {
		st.warnings = append(st.warnings, fmt.Sprintf("The spacing mark reaches only %d of %d bits in this file. It still names a recipient when it reads back without errors, but a result from spacing alone is reported as weak evidence.", stats.SpacingBits, codec.CodeLen))
	}
	if stats.TextBits < codec.CodeLen {
		st.warnings = append(st.warnings, fmt.Sprintf("The invisible-character mark reaches only %d of %d bits, because the text is short or repetitive.", stats.TextBits, codec.CodeLen))
	}
	s.doc = st
}

// layerSpec describes one marking layer for the UI, per file kind.
type layerSpec struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Weak        bool   `json:"weak"`
	Default     bool   `json:"default"`
}

var pdfLayers = []layerSpec{
	{"layout", "Layout", "Baselines shifted 0.45 pt. Survives print and scan.", false, true},
	{"spectral", "Frequency domain", "Faint texture behind the page. Survives cropped screenshots.", false, true},
	{"object", "Object layer", "Near-white tint grid. Survives screenshots, not print.", false, true},
	{"metadata", "Metadata", "A document property. Any re-save strips it.", true, true},
}

var officeLayers = []layerSpec{
	{"background", "Background watermark", "Faint texture behind the page. Survives editing and export to PDF.", false, true},
	{"customxml", "Custom XML part", "A hidden data part. Survives any edit, removed by the inspector.", false, true},
	{"spacing", "Spacing", "Character spacing per word. Lost when runs are retyped.", false, true},
	{"text", "Invisible characters", "A zero-width character after some words. The only layer that survives copy and paste. Off by default because it changes the text itself: search, compare and some editors can show it.", false, false},
	{"metadata", "Metadata", "A document property. The inspector removes it.", true, true},
}

func (s *Server) layers() []layerSpec {
	if s.doc.kind == "pdf" {
		return pdfLayers
	}
	return officeLayers
}

// fileStem turns free text into a conservative file-name fragment.
func fileStem(text string) string { return webapp.FileStem(document.Normalize(text), "document") }

// isPDF tells the two pipelines' issuances apart in the shared log.
func (is *docIssuance) isPDF() bool { return strings.EqualFold(filepath.Ext(is.FileName), ".pdf") }

// docIssued lists the PDF issuances as candidates. The layout layer is only
// comparable against the master it was typeset from, so it is offered only
// for copies of source.
func (s *Server) docIssued(source string) []document.Issued {
	var out []document.Issued
	for _, is := range s.doc.issued {
		if !is.isPDF() {
			continue
		}
		out = append(out, document.Issued{ID: is.id, Label: is.label(), Layers: document.Layers{
			Layout: is.Layers["layout"] && is.Source == source, Spectral: is.Layers["spectral"],
			Object: is.Layers["object"], Metadata: is.Layers["metadata"],
		}})
	}
	return out
}

func (s *Server) officeIssued() []office.Issued {
	var out []office.Issued
	for _, is := range s.doc.issued {
		if is.isPDF() {
			continue
		}
		out = append(out, office.Issued{ID: is.id, Label: is.label(), Tag: is.tag, Layers: office.Layers{
			Spacing: is.Layers["spacing"], Text: is.Layers["text"], Metadata: is.Layers["metadata"],
			CustomXML: is.Layers["customxml"], Background: is.Layers["background"],
		}})
	}
	return out
}

func (s *Server) docFind(mark string) *docIssuance {
	for _, is := range s.doc.issued {
		if is.MarkID == mark {
			return is
		}
	}
	return nil
}

func (s *Server) docStateHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	viewerID := r.URL.Query().Get("viewer")
	viewer := viewers[0]
	for _, v := range viewers {
		if v.ID == viewerID {
			viewer = v
		}
	}
	type logEntry struct {
		*docIssuance
		MarkID string `json:"markId"`
	}
	var log []logEntry
	hidden := 0
	for _, is := range s.doc.issued {
		if !inSubtree(is.Unit, viewer.Unit) {
			hidden++
			continue
		}
		e := logEntry{docIssuance: is, MarkID: is.MarkID}
		if viewer.Unit != "sec" {
			e.MarkID = "restricted"
		}
		log = append(log, e)
	}
	webapp.WriteJSON(w, map[string]any{
		"document": s.documentInfo(),
		"layers":   s.layers(),
		"roster":   roster,
		"units":    units,
		"viewers":  viewers,
		"viewer":   viewer,
		"log":      log,
		"hidden":   hidden,
		"marks": func() []map[string]string {
			var m []map[string]string
			for _, is := range s.doc.issued {
				m = append(m, map[string]string{"markId": is.MarkID, "label": is.label()})
			}
			return m
		}(),
	})
}

// documentInfo summarises the loaded source for the UI.
func (s *Server) documentInfo() map[string]any {
	d := s.doc
	info := map[string]any{
		"kind": d.kind, "source": d.source, "warnings": d.warnings,
		"extension": d.ext, "preview": d.kind == "pdf",
	}
	switch {
	case d.master != nil:
		info["title"] = d.master.Title
		info["stats"] = []map[string]any{
			{"label": "Pages", "value": d.master.Pages},
			{"label": "Words", "value": d.master.Words},
			{"label": "Lines carrying bits", "value": d.master.BodyLines},
			{"label": "Tint cells per page", "value": d.master.CellCount},
		}
	case d.office != nil:
		st := d.office.StatsWithKey(s.key)
		info["title"] = d.title
		info["kindName"] = d.office.Kind.Name()
		info["stats"] = []map[string]any{
			{"label": "Words", "value": st.Words},
			{"label": "Spacing bits", "value": st.SpacingBits},
			{"label": "Text bits", "value": st.TextBits},
			{"label": "Parts", "value": st.Parts},
		}
	}
	return info
}

func (s *Server) docSource(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("sample") == "1" {
		s.mu.Lock()
		s.resetDocument(s.engine.NewMaster(document.SampleDoc(), sampleSource), "Project-Halcyon-Board-Briefing")
		s.mu.Unlock()
		s.docStateHandler(w, r)
		return
	}
	data, name, ok := webapp.ReadUpload(w, r)
	if !ok {
		return
	}
	base := fileStem(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)))

	if office.IsOfficeFile(data) {
		f, err := office.Parse(data)
		if err != nil {
			webapp.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
			return
		}
		if n := f.Stats().Words; n < 20 {
			webapp.WriteErr(w, http.StatusUnprocessableEntity, fmt.Sprintf("%s holds only %s of text, and a mark needs at least 20", name, plural(n, "word", "words")))
			return
		}
		s.mu.Lock()
		s.resetOffice(f, name, fmt.Sprintf("%s, %s", name, f.Kind.Name()), base)
		s.mu.Unlock()
		s.docStateHandler(w, r)
		return
	}

	doc, source, warnings, err := extractDocument(data, name)
	if err != nil {
		// Never fall back to the sample: tagging a different document than
		// the one the user chose is worse than refusing.
		webapp.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	m := s.engine.NewMaster(doc, source)
	m.Warnings = append(warnings, m.Warnings...)
	s.mu.Lock()
	s.resetDocument(m, base)
	s.doc.name = name
	s.mu.Unlock()
	s.docStateHandler(w, r)
}

// extractDocument pulls text from an uploaded PDF or text file. The demo
// re-typesets that text; a production system would perturb the original
// PDF's own content stream in place.
func extractDocument(data []byte, name string) (document.Doc, string, []string, error) {
	isPDF := bytes.HasPrefix(bytes.TrimLeft(data, " \r\n\t"), []byte("%PDF"))
	if !isPDF {
		if strings.EqualFold(filepath.Ext(name), ".pdf") {
			return document.Doc{}, "", nil, fmt.Errorf("%s is not a PDF, although its name ends in .pdf", name)
		}
		if !utf8.Valid(data) {
			return document.Doc{}, "", nil, fmt.Errorf("%s is not a Word, PowerPoint, Excel, PDF or plain-text file", name)
		}
		doc := document.DocFromText(string(data))
		if len(doc.Paragraphs) == 0 {
			return document.Doc{}, "", nil, fmt.Errorf("%s is empty", name)
		}
		return doc, name, nil, nil
	}
	f, err := document.ParsePDF(data)
	if err != nil {
		return document.Doc{}, "", nil, fmt.Errorf("could not read %s: %v", name, err)
	}
	var lines []document.ExtractedLine
	for i := range f.Pages {
		lines = append(lines, f.PageContent(i).Lines(i)...)
	}
	doc := document.DocFromLines(lines)
	all := doc.Title + " " + strings.Join(doc.Paragraphs, " ")
	words := len(strings.Fields(all))
	if words < 40 {
		return document.Doc{}, "", nil, fmt.Errorf("only %s could be extracted from %s. It may be a scan, encrypted, or hold its text in a form this demo does not read", plural(words, "readable word", "readable words"), name)
	}
	if q := document.TextQuality(all); q < 0.85 {
		return document.Doc{}, "", nil, fmt.Errorf("the text extracted from %s is unreadable, most likely a font encoding this demo does not support", name)
	}
	return doc, fmt.Sprintf("%s, %s", name, plural(len(f.Pages), "page", "pages")),
		[]string{"Text is extracted and set again, so tagged copies do not keep the original layout."}, nil
}

// plural renders a count with the matching noun.
func plural(n int, one, many string) string { return codec.Plural(n, one, many) }

func (s *Server) docMasterPDF(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	data := s.doc.master.PDF
	s.mu.Unlock()
	webapp.SendFile(w, "application/pdf", "master_UNMARKED.pdf", data, true)
}

func (s *Server) docIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Recipients []rosterEntry   `json:"recipients"`
		Layers     map[string]bool `json:"layers"`
		IssuedBy   string          `json:"issuedBy"`
	}
	if !webapp.ReadJSON(w, r, &req) {
		return
	}
	if len(req.Recipients) == 0 || len(req.Recipients) > 50 {
		webapp.WriteErr(w, http.StatusBadRequest, "issue between 1 and 50 copies")
		return
	}
	if strings.TrimSpace(req.IssuedBy) == "" {
		req.IssuedBy = "Security Office"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*docIssuance
	for _, rc := range req.Recipients {
		rc.Name = strings.TrimSpace(rc.Name)
		if rc.Name == "" {
			continue
		}
		var ids []uint16
		for _, is := range s.doc.issued {
			ids = append(ids, is.id)
		}
		id := s.key.AllocateID(ids)
		is := &docIssuance{id: id, MarkID: codec.FormatID(id), Name: rc.Name, Role: rc.Role, Unit: rc.Unit,
			IssuedAt: time.Now(), IssuedBy: req.IssuedBy, Layers: req.Layers,
			FileName: s.uniqueFileName(rc)}
		is.tag = s.engine.MetaTag(id)

		var err error
		if s.doc.office != nil {
			var tile []byte
			if req.Layers["background"] {
				if tile, err = s.engine.WatermarkTilePNG(s.key.Encode(id)); err != nil {
					webapp.WriteErr(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
			is.data, err = s.doc.office.Mark(s.key, s.key.Encode(id), is.tag, office.Layers{
				Spacing: req.Layers["spacing"], Text: req.Layers["text"], Metadata: req.Layers["metadata"],
				CustomXML: req.Layers["customxml"], Background: req.Layers["background"],
			}, tile)
		} else {
			is.data = s.engine.IssueCopy(s.doc.master, id, document.Layers{
				Layout: req.Layers["layout"], Spectral: req.Layers["spectral"],
				Object: req.Layers["object"], Metadata: req.Layers["metadata"],
			})
		}
		if err != nil {
			webapp.WriteErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		is.SizeBytes = len(is.data)
		is.Source = s.doc.source
		is.Document = s.doc.name
		s.doc.issued = append(s.doc.issued, is)
		out = append(out, is)
	}
	if len(out) == 0 {
		webapp.WriteErr(w, http.StatusBadRequest, "every recipient needs a name")
		return
	}
	s.saveLog()
	webapp.WriteJSON(w, out)
}

// uniqueFileName names a recipient's copy after the document and the
// recipient. Two different people with the same name get distinct files, so
// neither download overwrites the other; the same person issued the same
// document again keeps their file name.
func (s *Server) uniqueFileName(rc rosterEntry) string {
	stem := s.doc.fileBase + "_" + fileStem(rc.Name)
	for n := 1; ; n++ {
		name := stem + s.doc.ext
		if n > 1 {
			name = fmt.Sprintf("%s_%d%s", stem, n, s.doc.ext)
		}
		taken := false
		for _, is := range s.doc.issued {
			if strings.EqualFold(is.FileName, name) && (is.Name != rc.Name || is.Role != rc.Role || is.Unit != rc.Unit) {
				taken = true
				break
			}
		}
		if !taken {
			return name
		}
	}
}

// docRecall removes an issuance from the log. The copy already handed over is
// of course still out there; what goes is the record of who has it.
func (s *Server) docRecall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mark string `json:"mark"`
	}
	if !webapp.ReadJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.doc.issued[:0]
	found := false
	for _, is := range s.doc.issued {
		if is.MarkID == req.Mark {
			found = true
			continue
		}
		kept = append(kept, is)
	}
	s.doc.issued = kept
	if !found {
		webapp.WriteErr(w, http.StatusNotFound, "unknown mark")
		return
	}
	s.saveLog()
	webapp.WriteJSON(w, map[string]any{"recalled": req.Mark})
}

func (s *Server) docCopy(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	is := s.docFind(r.URL.Query().Get("mark"))
	s.mu.Unlock()
	if is == nil {
		webapp.WriteErr(w, http.StatusNotFound, "unknown mark")
		return
	}
	if len(is.data) == 0 {
		// Restored from the log, so the bytes are gone. The entry still names
		// the recipient when a recovered file is verified.
		webapp.WriteErr(w, http.StatusGone, "this copy was issued before a restart, so the file is no longer held here. Issue it again to download it.")
		return
	}
	webapp.SendFile(w, mimeFor(s.doc.kind), is.FileName, is.data, s.doc.kind == "pdf" && r.URL.Query().Get("download") == "")
}

// docBundle zips the requested tagged copies for one download.
func (s *Server) docBundle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var copies []*docIssuance
	for _, mark := range strings.Split(r.URL.Query().Get("marks"), ",") {
		if is := s.docFind(strings.TrimSpace(mark)); is != nil && len(is.data) > 0 {
			copies = append(copies, is)
		}
	}
	base := s.doc.fileBase
	s.mu.Unlock()
	if len(copies) == 0 {
		webapp.WriteErr(w, http.StatusNotFound, "no matching tagged copies")
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	used := map[string]int{}
	for _, is := range copies {
		name := is.FileName
		if n := used[name]; n > 0 { // same recipient issued twice
			ext := filepath.Ext(name)
			name = strings.TrimSuffix(name, ext) + fmt.Sprintf("_%d%s", n+1, ext)
		}
		used[is.FileName]++
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: is.IssuedAt})
		if err == nil {
			_, err = f.Write(is.data)
		}
		if err != nil {
			webapp.WriteErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	zw.Close()
	webapp.SendFile(w, "application/zip", base+"_tagged-copies.zip", buf.Bytes(), false)
}

// docDetect checks a recovered file against the whole issuance log, whatever
// document happens to be loaded for tagging. Each pipeline that could have
// produced the file reads it, and the strongest report wins.
func (s *Server) docDetect(w http.ResponseWriter, r *http.Request) {
	data, name, ok := webapp.ReadUpload(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	hasOffice, hasPDF := false, false
	for _, is := range s.doc.issued {
		if is.isPDF() {
			hasPDF = true
		} else {
			hasOffice = true
		}
	}
	// With nothing issued yet, read the file the way the loaded document's
	// pipeline would, so the report still explains what it found.
	if !hasOffice && !hasPDF {
		hasOffice, hasPDF = s.doc.office != nil, s.doc.office == nil
	}

	var reports []detection
	switch {
	case office.IsOfficeFile(data):
		rep, err := office.Detect(s.key, data, s.officeIssued(), s.engine.ReadWatermarkTile)
		if err != nil {
			webapp.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
			return
		}
		reports = append(reports, newDetection(rep, rep.Verdict.Status, rep.Verdict.MarkID, rep.Verdict.Decision))
	case isRendering(data):
		if hasOffice {
			v, note, ok := s.engine.ReadWatermarkCapture(data)
			rep := office.DetectCapture(s.key, "Rendering of a page", v, note, ok, s.officeIssued())
			reports = append(reports, newDetection(rep, rep.Verdict.Status, rep.Verdict.MarkID, rep.Verdict.Decision))
		}
		if hasPDF {
			for _, m := range s.pdfMasters() {
				rep, err := s.engine.Detect(m, s.docIssued(m.Source), data)
				if err != nil {
					webapp.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
					return
				}
				reports = append(reports, newDetection(rep, rep.Verdict.Status, rep.Verdict.MarkID, rep.Verdict.Decision))
			}
		}
	case utf8.Valid(data):
		// Only the invisible characters of an Open XML copy survive as text.
		// A PDF copy has no text-level layer, and its report says so.
		if hasOffice {
			rep := office.DetectText(s.key, string(data), s.officeIssued())
			reports = append(reports, newDetection(rep, rep.Verdict.Status, rep.Verdict.MarkID, rep.Verdict.Decision))
		}
		if hasPDF {
			rep, _ := s.engine.Detect(s.masterFor(), s.docIssued(""), data)
			reports = append(reports, newDetection(rep, rep.Verdict.Status, rep.Verdict.MarkID, rep.Verdict.Decision))
		}
	default:
		webapp.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("%s is not a file Custodial reads. Expected Word, PowerPoint, Excel, PDF, PNG, JPEG or plain text.", name))
		return
	}
	best := reports[0]
	for _, d := range reports[1:] {
		if d.better(best) {
			best = d
		}
	}
	out := map[string]any{"name": name, "report": best.report}
	if is := s.docFind(best.markID); is != nil {
		out["issuance"] = map[string]any{"fileName": is.FileName, "source": is.Source, "issuedAt": is.IssuedAt, "issuedBy": is.IssuedBy}
	}
	out["checked"] = len(s.doc.issued)
	webapp.WriteJSON(w, out)
}

// detection is one pipeline's reading of a recovered file.
type detection struct {
	report   any
	status   string
	markID   string
	decision *codec.Decision
}

func newDetection(rep any, status, markID string, d *codec.Decision) detection {
	return detection{rep, status, markID, d}
}

var statusRank = map[string]int{"attributed": 3, "conflict": 3, "inconclusive": 2, "absent": 1}

func (d detection) better(o detection) bool {
	if statusRank[d.status] != statusRank[o.status] {
		return statusRank[d.status] > statusRank[o.status]
	}
	fp := func(x *codec.Decision) float64 {
		if x == nil {
			return 1
		}
		return x.FalseProb
	}
	return fp(d.decision) < fp(o.decision)
}

// pdfMasters returns the masters that PDF copies in the log were typeset
// from and that are still held, plus the loaded one. A copy whose master is
// gone (an upload from before a restart) still reads through every layer
// except layout.
func (s *Server) pdfMasters() []*document.Master {
	seen := map[string]bool{}
	var out []*document.Master
	add := func(m *document.Master) {
		if m != nil && !seen[m.Source] {
			seen[m.Source] = true
			out = append(out, m)
		}
	}
	if s.doc.master != nil {
		add(s.doc.master)
	}
	for _, is := range s.doc.issued {
		if is.isPDF() {
			add(s.masters[is.Source])
		}
	}
	if len(out) == 0 {
		add(s.masterFor())
	}
	return out
}

// masterFor returns the loaded master, or the sample when an Open XML file
// is loaded, for the layers that do not depend on the source.
func (s *Server) masterFor() *document.Master {
	if s.doc.master != nil {
		return s.doc.master
	}
	return s.masters[sampleSource]
}

// isRendering reports whether the upload is a rendering of a page rather than
// the file itself: a PDF export, a screenshot or a photo.
func isRendering(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(data, " \r\n\t"), []byte("%PDF")) ||
		bytes.HasPrefix(data, []byte("\x89PNG")) || bytes.HasPrefix(data, []byte("\xff\xd8"))
}

var mimeByKind = map[string]string{
	"pdf":  "application/pdf",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

func mimeFor(kind string) string {
	if m := mimeByKind[kind]; m != "" {
		return m
	}
	return "application/octet-stream"
}
