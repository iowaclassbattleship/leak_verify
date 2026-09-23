package office

import (
	"fmt"
	"regexp"
	"strings"

	"attribution/common/codec"
)

const customPart = "docProps/custom.xml"

// PropertyName is the custom document property that carries the metadata mark.
const PropertyName = "LAPRef"

var reProperty = regexp.MustCompile(`(?s)<property[^>]*name="` + PropertyName + `"[^>]*>\s*<vt:lpwstr>(.*?)</vt:lpwstr>`)

const customTemplate = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/custom-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">%s</Properties>`

// setProperty stores the tag as a custom document property, adding the part,
// its content type and its relationship when the file has none.
func (f *File) setProperty(tag string) {
	prop := fmt.Sprintf(`<property fmtid="{D5CDD505-2E9C-101B-9397-08002B2CF9AE}" pid="2" name="%s"><vt:lpwstr>%s</vt:lpwstr></property>`, PropertyName, tag)
	if cur := f.parts[customPart]; cur != nil {
		s := string(cur)
		if reProperty.MatchString(s) {
			s = reProperty.ReplaceAllString(s, `<property name="`+PropertyName+`"><vt:lpwstr>`+tag)
		} else if i := strings.LastIndex(s, "</Properties>"); i > 0 {
			s = s[:i] + prop + s[i:]
		}
		f.set(customPart, []byte(s))
		return
	}
	f.set(customPart, []byte(fmt.Sprintf(customTemplate, prop)))

	if ct := f.parts["[Content_Types].xml"]; ct != nil {
		s := string(ct)
		if !strings.Contains(s, customPart) {
			ins := `<Override PartName="/` + customPart + `" ContentType="application/vnd.openxmlformats-officedocument.custom-properties+xml"/>`
			s = strings.Replace(s, "</Types>", ins+"</Types>", 1)
			f.set("[Content_Types].xml", []byte(s))
		}
	}
	if rels := f.parts["_rels/.rels"]; rels != nil {
		s := string(rels)
		if !strings.Contains(s, customPart) {
			ins := `<Relationship Id="rIdCustomProps1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/custom-properties" Target="` + customPart + `"/>`
			s = strings.Replace(s, "</Relationships>", ins+"</Relationships>", 1)
			f.set("_rels/.rels", []byte(s))
		}
	}
}

func (f *File) property() string {
	if m := reProperty.FindSubmatch(f.parts[customPart]); m != nil {
		return string(m[1])
	}
	return ""
}

// dropProperty removes the custom property part and its references, the way
// Office's document inspector does.
func (f *File) dropProperty() {
	f.remove(customPart)
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels"} {
		if b := f.parts[name]; b != nil {
			s := regexp.MustCompile(`<(?:Override|Relationship)[^>]*`+regexp.QuoteMeta(customPart)+`[^>]*/>`).ReplaceAllString(string(b), "")
			f.set(name, []byte(s))
		}
	}
}

// ---- report ----

type Result struct {
	Technique  string          `json:"technique"`
	Name       string          `json:"name"`
	Survives   string          `json:"survives"`
	Status     string          `json:"status"` // attributed | inconclusive | absent | invalid; the verdict may also be conflict
	MarkID     string          `json:"markId,omitempty"`
	Recipient  string          `json:"recipient,omitempty"`
	Confidence string          `json:"confidence,omitempty"`
	Detail     string          `json:"detail"`
	Decision   *codec.Decision `json:"decision,omitempty"`

	// Verdict only.
	ReadFrom   []string        `json:"readFrom,omitempty"`   // layers that agree with the named recipient
	Candidates []codec.Support `json:"candidates,omitempty"` // every recipient some layer points at
	Warnings   []string        `json:"warnings,omitempty"`
	Weak       bool            `json:"weak,omitempty"`
}

type Report struct {
	Input   string   `json:"input"`
	Results []Result `json:"results"`
	Verdict Result   `json:"verdict"`
}

// Issued is the detector's view of one issuance-log entry.
type Issued struct {
	ID     uint16
	Label  string
	Layers Layers
	Tag    string // the metadata value issued to this recipient
}

var layerInfo = map[string][2]string{
	"spacing":    {"Spacing", "Survives copies and re-saves that keep formatting. Lost when the text is copied out or the file is converted."},
	"text":       {"Invisible characters", "A zero-width character after about one word in six. Survives copy and paste into another document. Lost to any tool that strips invisible characters."},
	"metadata":   {"Metadata", "Survives a plain forward. Removed by the document inspector."},
	"customxml":  {"Custom XML part", "Touches no text, so any amount of editing leaves it intact. Removed by the document inspector, and lost when the content is copied into a new file."},
	"background": {"Background watermark", "Touches no text, and survives editing, export to PDF and print. Lost when the content is copied into a new file."},
}

func newResult(tech string) Result {
	return Result{Technique: tech, Name: layerInfo[tech][0], Survives: layerInfo[tech][1]}
}

func candidates(issued []Issued, pred func(Layers) bool) []codec.Candidate {
	var c []codec.Candidate
	for _, is := range issued {
		if pred(is.Layers) {
			c = append(c, codec.Candidate{ID: is.ID, Label: is.Label})
		}
	}
	return c
}

func decide(key codec.Key, res *Result, v codec.Votes, cands []codec.Candidate) {
	d := key.Decode(v, cands)
	res.Decision = &d
	res.Status = d.Status
	if d.Status == "attributed" {
		res.MarkID, res.Recipient = d.MarkID, d.Recipient
	}
	if d.Votes > 0 {
		res.Confidence = codec.FormatProb(d.FalseProb)
	}
}

func bitSummary(d *codec.Decision) string {
	return fmt.Sprintf("Recovered %d of %d bits, %s.", d.BitsObserved, codec.CodeLen, plural(d.BitErrors, "error", "errors"))
}

func plural(n int, one, many string) string { return codec.Plural(n, one, many) }

// TileReader reads a watermark tile lifted out of a package. The office
// package holds no imaging code, so the caller supplies this.
type TileReader func(png []byte) (codec.Votes, string, bool)

// Detect reads every carrier of a recovered Open XML file.
func Detect(key codec.Key, data []byte, issued []Issued, readTile TileReader) (Report, error) {
	f, err := Parse(data)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Input: fmt.Sprintf("%s, %s", f.Kind.Name(), plural(f.Stats().Words, "word", "words"))}

	spacing := newResult("spacing")
	sv, slots := f.readSpacing(key)
	decide(key, &spacing, sv, candidates(issued, func(l Layers) bool { return l.Spacing }))
	if sv.Total() == 0 {
		spacing.Status = "absent"
		spacing.Detail = fmt.Sprintf("No spacing marks in %s.", plural(slots, "slot", "slots"))
	} else {
		spacing.Detail = fmt.Sprintf("Read %.0f of %d spacing slots. %s", sv.Total(), slots, bitSummary(spacing.Decision))
	}

	text := newResult("text")
	tv, slots, marked := f.readText(key)
	if marked == 0 {
		tv = codec.Votes{}
	}
	decide(key, &text, tv, candidates(issued, func(l Layers) bool { return l.Text }))
	if marked == 0 {
		text.Status = "absent"
		text.Detail = fmt.Sprintf("None of the %s that could carry the mark holds one.", plural(slots, "word", "words"))
	} else {
		text.Detail = fmt.Sprintf("Read %d of %s that can carry the mark. %s", marked, plural(slots, "word", "words"), bitSummary(text.Decision))
	}

	meta := tagResult(key, "metadata", f.property(), issued,
		"No marking property in the document properties.",
		"Valid marking property. It is easily inspected and removed, so it is weak on its own.")

	custom := tagResult(key, "customxml", f.customXML(), issued,
		"No marking part in the package.",
		"Valid marking part. The text was never touched, so editing the document does not disturb it.")

	bg := newResult("background")
	bg.Status = "absent"
	bg.Detail = "No watermark image in the package."
	var bv codec.Votes
	for _, img := range f.Backgrounds() {
		v, note, ok := readTile(img)
		if !ok {
			bg.Status = "inconclusive"
			bg.Detail = "The package carries a watermark image, but it could not be read: " + note + "."
			continue
		}
		bv = v
		break
	}
	if bv.Total() > 0 {
		decide(key, &bg, bv, candidates(issued, func(l Layers) bool { return l.Background }))
		bg.Detail = fmt.Sprintf("Read the watermark tile from the package. %s", bitSummary(bg.Decision))
	}

	rep.Results = []Result{spacing, text, bg, custom, meta}
	rep.Verdict = combine(key, issued, rep.Results, map[string]codec.Votes{"spacing": sv, "text": tv, "background": bv})
	return rep, nil
}

// tagResult checks a stored tag against the issuance log. The tag carries a
// keyed check value, so a forged or edited one does not verify.
func tagResult(key codec.Key, tech, tag string, issued []Issued, absent, valid string) Result {
	res := newResult(tech)
	res.Status = "absent"
	res.Detail = absent
	if tag == "" {
		return res
	}
	id, ok, verifies := key.ParseTag(tag)
	if !verifies {
		res.Status = "invalid"
		res.Confidence = "Tampered"
		switch {
		case !ok:
			res.Detail = fmt.Sprintf("The file carries %q, which is not a tag this system writes. It was edited or planted.", tag)
		case issuedLabel(issued, id) != "":
			res.Detail = fmt.Sprintf("The file carries %q, which claims %s (%s), but its check value does not verify. The tag was edited or forged, so it is evidence of tampering, not of who the file belongs to.", tag, codec.FormatID(id), issuedLabel(issued, id))
		default:
			res.Detail = fmt.Sprintf("The file carries %q, but its check value does not verify. The tag was edited or forged.", tag)
		}
		return res
	}
	for _, is := range issued {
		if is.ID == id {
			res.Status, res.MarkID, res.Recipient = "attributed", codec.FormatID(is.ID), is.Label
			res.Confidence = "Check value verifies"
			res.Detail = valid
			return res
		}
	}
	res.Status = "inconclusive"
	res.Detail = fmt.Sprintf("The file carries a genuine tag, %s, but that mark is not in the issuance log. It may have been recalled.", codec.FormatID(id))
	return res
}

func issuedLabel(issued []Issued, id uint16) string {
	for _, is := range issued {
		if is.ID == id {
			return is.Label
		}
	}
	return ""
}

// DetectText reads the only carrier that survives copied-out text.
func DetectText(key codec.Key, text string, issued []Issued) Report {
	rep := Report{Input: fmt.Sprintf("Plain text, %s", plural(len(strings.Fields(text)), "word", "words"))}
	res := newResult("text")
	v, slots, marked := ReadTextVotes(key, text)
	if marked == 0 {
		v = codec.Votes{}
	}
	decide(key, &res, v, candidates(issued, func(l Layers) bool { return l.Text }))
	if marked == 0 {
		res.Status = "absent"
		res.Detail = fmt.Sprintf("None of the %s that could carry the mark holds one.", plural(slots, "word", "words"))
	} else {
		res.Detail = fmt.Sprintf("Read %d of %s that can carry the mark. %s", marked, plural(slots, "word", "words"), bitSummary(res.Decision))
	}
	spacing := newResult("spacing")
	spacing.Status = "absent"
	spacing.Detail = "Plain text has no formatting, so the spacing marks were lost."
	rep.Results = []Result{spacing, res}
	for _, tech := range []string{"background", "customxml", "metadata"} {
		r := newResult(tech)
		r.Status = "absent"
		r.Detail = "Plain text carries nothing but the words, so this layer was lost."
		rep.Results = append(rep.Results, r)
	}
	rep.Verdict = combine(key, issued, rep.Results, map[string]codec.Votes{"text": v})
	return rep
}

// DetectCapture reports on a rendering of a marked file, such as a PDF export
// or a screenshot. Only the background watermark can survive that, so the
// caller reads it and passes the result in.
func DetectCapture(key codec.Key, input string, v codec.Votes, note string, ok bool, issued []Issued) Report {
	rep := Report{Input: input}
	bg := newResult("background")
	if !ok {
		bg.Status = "absent"
		bg.Detail = "No watermark pattern found: " + note + "."
	} else {
		decide(key, &bg, v, candidates(issued, func(l Layers) bool { return l.Background }))
		bg.Detail = note + " " + bitSummary(bg.Decision)
	}
	rep.Results = []Result{bg}
	for _, tech := range []string{"spacing", "text", "customxml", "metadata"} {
		r := newResult(tech)
		r.Status = "absent"
		r.Detail = "A rendering of the document carries none of the file structure, so this layer was lost."
		rep.Results = append(rep.Results, r)
	}
	rep.Verdict = combine(key, issued, rep.Results, map[string]codec.Votes{"background": v})
	return rep
}

// combine pools the evidence of every carrier that produced a signal. Each is
// normalised to equal weight so a dense carrier cannot drown a sparse one.
// Carriers that point at different recipients are reported as a conflict
// rather than outvoted: that is what a tampered or merged copy looks like.
func combine(key codec.Key, issued []Issued, results []Result, votes map[string]codec.Votes) Result {
	v := Result{Technique: "combined", Name: "Combined verdict"}
	var pooled codec.Votes
	var used []string
	for _, r := range results {
		lv, ok := votes[r.Technique]
		if !ok || r.Status == "absent" || lv.Total() == 0 {
			continue
		}
		w := codec.CodeLen / lv.Total()
		for i := range lv.One {
			pooled.One[i] += lv.One[i] * w
			pooled.Zero[i] += lv.Zero[i] * w
		}
		used = append(used, r.Name)
	}
	if len(used) > 0 {
		decide(key, &v, pooled, candidates(issued, func(Layers) bool { return true }))
		v.Detail = "Evidence pooled from " + strings.Join(used, ", ") + "."
	}
	fromBits := v.Status == "attributed"
	if !fromBits {
		for _, r := range results {
			if (r.Technique == "metadata" || r.Technique == "customxml") && r.Status == "attributed" {
				v.Status, v.MarkID, v.Recipient = "attributed", r.MarkID, r.Recipient
				v.Confidence = "Weak evidence"
				v.Weak = true
				v.Detail = "Only a stored tag identifies this file. A tag can be copied from one file to another, so confirm it another way."
			}
		}
	}
	if v.Status == "" {
		v.Status = "absent"
		v.Detail = "No carrier produced a signal."
	}

	var claims []codec.Claim
	for _, r := range results {
		switch {
		case r.Status == "attributed" && (r.Technique == "metadata" || r.Technique == "customxml"):
			claims = append(claims, codec.Claim{Layer: r.Name, MarkID: r.MarkID, Recipient: r.Recipient, Strength: codec.Stored})
		case r.Status == "attributed":
			claims = append(claims, codec.Claim{Layer: r.Name, MarkID: r.MarkID, Recipient: r.Recipient, Strength: codec.Attributed})
		case r.Decision != nil:
			if best, ok := r.Decision.Leaning(); ok {
				claims = append(claims, codec.Claim{Layer: r.Name, MarkID: best.MarkID, Recipient: best.Recipient, Strength: codec.Leaning})
			}
		}
		if r.Status == "invalid" {
			v.Warnings = append(v.Warnings, r.Name+": the stored tag fails its check value, so someone edited it.")
		}
	}
	groups, conflict := codec.Reconcile(v.MarkID, fromBits, claims)
	if conflict {
		v.Status, v.MarkID, v.Recipient, v.Weak = "conflict", "", "", false
		v.Candidates = groups
		v.Confidence = ""
		v.Detail = "The carriers point at different recipients. A tag was edited or copied from another copy, or copies were merged. Stored tags are easy to change; the bit carriers are much harder to forge."
		return v
	}
	if v.Status == "attributed" {
		v.ReadFrom = codec.Agreeing(v.MarkID, groups)
		if fromBits && len(v.ReadFrom) == 1 && v.ReadFrom[0] == layerInfo["spacing"][0] {
			v.Weak = true
			v.Confidence = "Weak evidence: " + v.Confidence
			v.Detail += " Only the spacing carrier survives, and it is lost as soon as the runs are retyped, so confirm the result another way."
		}
	}
	if len(v.Warnings) > 0 && v.Status != "attributed" {
		v.Detail += " A stored tag was tampered with."
	}
	return v
}
