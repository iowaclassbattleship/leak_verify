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
	{"ext", "External advisers", "legal"},
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
	issued   []*docIssuance
}

const sampleSource = "Built-in sample briefing"

// keptIssued carries the issuance log across a change of source. The log
// belongs to the user, not to whichever document happens to be loaded.
func (s *Server) keptIssued() []*docIssuance {
	if s.doc == nil {
		return nil
	}
	return s.doc.issued
}

func (s *Server) resetDocument(m *document.Master, fileBase string) {
	s.doc = &docState{master: m, kind: "pdf", source: m.Source, warnings: m.Warnings,
		fileBase: fileBase, ext: ".pdf", issued: s.keptIssued()}
}

func (s *Server) resetOffice(f *office.File, name, source, fileBase string) {
	st := &docState{office: f, kind: string(f.Kind), source: source, fileBase: fileBase,
		ext: "." + string(f.Kind), title: name, issued: s.keptIssued()}
	stats := f.StatsWithKey(s.key)
	if stats.SpacingBits < codec.CodeLen {
		st.warnings = append(st.warnings, fmt.Sprintf("The spacing mark reaches only %d of %d bits in this file, so it can support the other layers but not identify a recipient on its own.", stats.SpacingBits, codec.CodeLen))
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
	{"text", "Invisible characters", "A zero-width character after some words. The only layer that survives copy and paste.", false, false},
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

func (s *Server) docIssued() []document.Issued {
	var out []document.Issued
	for _, is := range s.doc.issued {
		out = append(out, document.Issued{ID: is.id, Label: is.label(), Layers: document.Layers{
			Layout: is.Layers["layout"], Spectral: is.Layers["spectral"],
			Object: is.Layers["object"], Metadata: is.Layers["metadata"],
		}})
	}
	return out
}

func (s *Server) officeIssued() []office.Issued {
	var out []office.Issued
	for _, is := range s.doc.issued {
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
		if f.Stats().Words < 20 {
			webapp.WriteErr(w, http.StatusBadRequest, "this file holds too little text to carry a mark")
			return
		}
		s.mu.Lock()
		s.resetOffice(f, name, fmt.Sprintf("%s, %s", name, f.Kind.Name()), base)
		s.mu.Unlock()
		s.docStateHandler(w, r)
		return
	}

	doc, source, warnings := extractDocument(data, name)
	m := s.engine.NewMaster(doc, source)
	m.Warnings = append(warnings, m.Warnings...)
	if source == sampleSource {
		base = "Project-Halcyon-Board-Briefing"
	}
	s.mu.Lock()
	s.resetDocument(m, base)
	s.mu.Unlock()
	s.docStateHandler(w, r)
}

// extractDocument pulls text from an uploaded PDF or text file. The demo
// re-typesets that text; a production system would perturb the original
// PDF's own content stream in place.
func extractDocument(data []byte, name string) (document.Doc, string, []string) {
	fallback := func(why string) (document.Doc, string, []string) {
		return document.SampleDoc(), sampleSource, []string{why + " The sample document was loaded instead."}
	}
	isPDF := bytes.HasPrefix(bytes.TrimLeft(data, " \r\n\t"), []byte("%PDF"))
	if !isPDF {
		if strings.EqualFold(filepath.Ext(name), ".pdf") {
			return fallback(fmt.Sprintf("%q is not a PDF.", name))
		}
		doc := document.DocFromText(string(data))
		if len(doc.Paragraphs) == 0 {
			return fallback("The file is empty.")
		}
		return doc, name, nil
	}
	f, err := document.ParsePDF(data)
	if err != nil {
		return fallback(fmt.Sprintf("Could not read %q: %v.", name, err))
	}
	var lines []document.ExtractedLine
	for i := range f.Pages {
		lines = append(lines, f.PageContent(i).Lines(i)...)
	}
	doc := document.DocFromLines(lines)
	all := doc.Title + " " + strings.Join(doc.Paragraphs, " ")
	words := len(strings.Fields(all))
	if words < 40 {
		return fallback(fmt.Sprintf("Only %d readable words could be extracted from %q. It may be scanned, encrypted or use an unsupported structure.", words, name))
	}
	if q := document.TextQuality(all); q < 0.85 {
		return fallback(fmt.Sprintf("Text extracted from %q is unreadable, most likely an unsupported font encoding.", name))
	}
	return doc, fmt.Sprintf("%s, %d pages", name, len(f.Pages)),
		[]string{"Text is extracted and set again, so tagged copies do not keep the original layout."}
}

// plural renders a count with the matching noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

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
			FileName: s.doc.fileBase + "_" + fileStem(rc.Name) + s.doc.ext}
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
		s.doc.issued = append(s.doc.issued, is)
		out = append(out, is)
	}
	s.saveLog()
	webapp.WriteJSON(w, out)
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
			name = strings.TrimSuffix(name, ".pdf") + fmt.Sprintf("_%d.pdf", n+1)
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

func (s *Server) docDetect(w http.ResponseWriter, r *http.Request) {
	data, name, ok := webapp.ReadUpload(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var payload any
	switch {
	case s.doc.office != nil && office.IsOfficeFile(data):
		rep, err := office.Detect(s.key, data, s.officeIssued(), s.engine.ReadWatermarkTile)
		if err != nil {
			webapp.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
			return
		}
		payload = rep
	case s.doc.office != nil && isRendering(data):
		v, note, ok := s.engine.ReadWatermarkCapture(data)
		payload = office.DetectCapture(s.key, "Rendering of a page", v, note, ok, s.officeIssued())
	case s.doc.office != nil:
		if !utf8.Valid(data) {
			webapp.WriteErr(w, http.StatusBadRequest, "expected the file, a rendering of it, or text copied out of it")
			return
		}
		payload = office.DetectText(s.key, string(data), s.officeIssued())
	default:
		rep, err := s.engine.Detect(s.doc.master, s.docIssued(), data)
		if err != nil {
			webapp.WriteErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
			return
		}
		payload = rep
	}
	webapp.WriteJSON(w, map[string]any{"name": name, "report": payload})
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
