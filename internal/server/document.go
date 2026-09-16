package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"custodial/internal/codec"
	"custodial/internal/document"
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
	pdf       []byte
	MarkID    string          `json:"markId"`
	Name      string          `json:"name"`
	Role      string          `json:"role"`
	Unit      string          `json:"unit"`
	IssuedAt  time.Time       `json:"issuedAt"`
	IssuedBy  string          `json:"issuedBy"`
	Layers    document.Layers `json:"layers"`
	SizeBytes int             `json:"sizeBytes"`
	FileName  string          `json:"fileName"`
}

func (d *docIssuance) label() string { return d.Name + " (" + d.Role + ")" }

type docState struct {
	master   *document.Master
	fileBase string // download name stem, from the uploaded file name
	issued   []*docIssuance
}

const sampleSource = "Built-in sample briefing"

func (s *Server) resetDocument(m *document.Master, fileBase string) {
	s.doc = &docState{master: m, fileBase: fileBase}
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// fileStem turns free text into a conservative file-name fragment.
func fileStem(text string) string {
	stem := strings.Trim(unsafeFileChars.ReplaceAllString(document.Normalize(text), "-"), "-.")
	if len(stem) > 60 {
		stem = strings.TrimRight(stem[:60], "-.")
	}
	if stem == "" {
		return "document"
	}
	return stem
}

func (s *Server) docIssued() []document.Issued {
	var out []document.Issued
	for _, is := range s.doc.issued {
		out = append(out, document.Issued{ID: is.id, Label: is.label(), Layers: is.Layers})
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
	writeJSON(w, map[string]any{
		"master":  s.doc.master,
		"roster":  roster,
		"units":   units,
		"viewers": viewers,
		"viewer":  viewer,
		"log":     log,
		"hidden":  hidden,
		"attacks": document.Attacks,
		"marks": func() []map[string]string {
			var m []map[string]string
			for _, is := range s.doc.issued {
				m = append(m, map[string]string{"markId": is.MarkID, "label": is.label()})
			}
			return m
		}(),
		"params": map[string]any{"shiftPt": document.ShiftPt, "tintGray": document.TintGray, "cellPt": document.CellPt, "tilePt": document.SpecTilePt},
	})
}

func (s *Server) docSource(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("sample") == "1" {
		s.mu.Lock()
		s.resetDocument(s.engine.NewMaster(document.SampleDoc(), sampleSource), "Project-Halcyon-Board-Briefing")
		s.mu.Unlock()
		s.docStateHandler(w, r)
		return
	}
	data, name, ok := readUpload(w, r)
	if !ok {
		return
	}
	doc, source, warnings := extractDocument(data, name)
	m := s.engine.NewMaster(doc, source)
	m.Warnings = append(warnings, m.Warnings...)
	base := fileStem(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)))
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

func (s *Server) docMasterPDF(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	data := s.doc.master.PDF
	s.mu.Unlock()
	sendFile(w, "application/pdf", "master_UNMARKED.pdf", data, true)
}

func (s *Server) docIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Recipients []rosterEntry   `json:"recipients"`
		Layers     document.Layers `json:"layers"`
		IssuedBy   string          `json:"issuedBy"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Recipients) == 0 || len(req.Recipients) > 50 {
		writeErr(w, http.StatusBadRequest, "issue between 1 and 50 copies")
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
		pdf := s.engine.IssueCopy(s.doc.master, id, req.Layers)
		is := &docIssuance{id: id, pdf: pdf, MarkID: codec.FormatID(id), Name: rc.Name, Role: rc.Role, Unit: rc.Unit,
			IssuedAt: time.Now(), IssuedBy: req.IssuedBy, Layers: req.Layers, SizeBytes: len(pdf),
			FileName: s.doc.fileBase + "_" + fileStem(rc.Name) + ".pdf"}
		s.doc.issued = append(s.doc.issued, is)
		out = append(out, is)
	}
	writeJSON(w, out)
}

func (s *Server) docCopy(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	is := s.docFind(r.URL.Query().Get("mark"))
	s.mu.Unlock()
	if is == nil {
		writeErr(w, http.StatusNotFound, "unknown mark")
		return
	}
	sendFile(w, "application/pdf", is.FileName, is.pdf, r.URL.Query().Get("download") == "")
}

// docBundle zips the requested tagged copies for one download.
func (s *Server) docBundle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var copies []*docIssuance
	for _, mark := range strings.Split(r.URL.Query().Get("marks"), ",") {
		if is := s.docFind(strings.TrimSpace(mark)); is != nil {
			copies = append(copies, is)
		}
	}
	base := s.doc.fileBase
	s.mu.Unlock()
	if len(copies) == 0 {
		writeErr(w, http.StatusNotFound, "no matching tagged copies")
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
			_, err = f.Write(is.pdf)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	zw.Close()
	sendFile(w, "application/zip", base+"_tagged-copies.zip", buf.Bytes(), false)
}

func (s *Server) docDetect(w http.ResponseWriter, r *http.Request) {
	data, name, ok := readUpload(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rep, err := s.engine.Detect(s.doc.master, s.docIssued(), data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", name, err))
		return
	}
	writeJSON(w, map[string]any{"name": name, "report": rep})
}
