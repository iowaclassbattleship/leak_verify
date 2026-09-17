package office

import (
	"fmt"
	"strings"
)

type AttackInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Attacks are the transformations a leaked Open XML file plausibly goes
// through. Exporting to PDF and printing are not simulated: the spacing marks
// survive both, but reading them back needs a rendering engine this demo does
// not include.
var Attacks = []AttackInfo{
	{"none", "Forward the file", "The recipient's file leaks unchanged."},
	{"resave", "Re-save", "Opened and saved again: the archive is rebuilt, and formatting and properties are kept."},
	{"edit", "Edit the document", "The recipient works on the file: text is rewritten and run formatting is replaced."},
	{"inspect", "Run the document inspector", "Strips custom properties and custom XML parts, which is what the inspector removes."},
	{"cleanup", "Strip invisible characters", "Passed through a tool that removes zero-width characters from the text."},
	{"strip", "Remove the watermark image", "The package is unpacked and the background image deleted by hand."},
	{"copypaste", "Copy-paste text", "All text selected and pasted elsewhere. Only what travels with the words survives."},
}

// Artifact is the file a simulated leak produced.
type Artifact struct {
	Kind string // office | txt
	Name string
	Data []byte
}

func Attack(data []byte, kind string, base string) (*Artifact, error) {
	f, err := Parse(data)
	if err != nil {
		return nil, err
	}
	ext := "." + string(f.Kind)
	switch kind {
	case "none":
		return &Artifact{Kind: "office", Name: base + ext, Data: data}, nil
	case "resave":
		out, err := f.Bytes()
		return &Artifact{Kind: "office", Name: base + "-resaved" + ext, Data: out}, err
	case "edit":
		f.editText()
		out, err := f.Bytes()
		return &Artifact{Kind: "office", Name: base + "-edited" + ext, Data: out}, err
	case "inspect":
		f.dropProperty()
		f.dropCustomXML()
		out, err := f.Bytes()
		return &Artifact{Kind: "office", Name: base + "-inspected" + ext, Data: out}, err
	case "strip":
		f.dropBackground()
		out, err := f.Bytes()
		return &Artifact{Kind: "office", Name: base + "-stripped" + ext, Data: out}, err
	case "cleanup":
		for _, p := range f.contentParts() {
			f.parts[p] = []byte(strings.ReplaceAll(string(f.parts[p]), zwMark, ""))
		}
		out, err := f.Bytes()
		return &Artifact{Kind: "office", Name: base + "-cleaned" + ext, Data: out}, err
	case "copypaste":
		return &Artifact{Kind: "txt", Name: base + "-pasted.txt", Data: []byte(f.rawText())}, nil
	}
	return nil, fmt.Errorf("unknown attack %q", kind)
}

// editText stands in for a recipient working on the file: the run formatting
// that carried the spacing mark is replaced and the invisible characters go
// with the retyped text. Carriers that never touched the text are untouched.
func (f *File) editText() {
	for _, p := range f.contentParts() {
		s := strings.ReplaceAll(string(f.parts[p]), zwMark, "")
		s = reDocxSpac.ReplaceAllString(s, "")
		s = rePptxSpc.ReplaceAllString(s, "")
		f.parts[p] = []byte(s)
	}
	for _, p := range f.sheetParts() {
		s := reHt.ReplaceAllString(string(f.parts[p]), `ht="15"`)
		s = reWidth.ReplaceAllString(s, `width="11"`)
		f.parts[p] = []byte(s)
	}
}

// rawText keeps the invisible characters, the way a clipboard would.
func (f *File) rawText() string {
	var b strings.Builder
	for _, p := range f.contentParts() {
		for _, m := range reTextEl.FindAllSubmatch(f.parts[p], -1) {
			b.Write(m[3])
			b.WriteString("\n")
		}
	}
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&apos;", "'").Replace(b.String())
}
