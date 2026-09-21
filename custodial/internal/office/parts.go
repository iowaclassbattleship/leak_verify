package office

import (
	"fmt"
	"regexp"
	"strings"
)

// Two carriers that never touch the text and survive ordinary editing:
//
//   - A custom XML part, a small data part Office keeps across edits and
//     re-saves. Invisible in the application, and removed by the document
//     inspector along with the custom properties.
//   - A background image, the same frequency-domain watermark the PDF pipeline
//     uses, tiled behind the page (Word), the slide master (PowerPoint) or the
//     sheet (Excel). It stays put while the document is edited, and in Word and
//     PowerPoint it also prints and exports to PDF.

const (
	markNamespace = "urn:custodial:mark"
	customXMLItem = "customXml/item1.xml"
	customXMLProp = "customXml/itemProps1.xml"
	customXMLRels = "customXml/_rels/item1.xml.rels"
	mediaName     = "media/custodial-bg.png"
)

var reMarkXML = regexp.MustCompile(`(?s)<mark[^>]*>\s*<id>(.*?)</id>`)

// mainRels is the relationship part of the file's main document part, which is
// where a custom XML part is referenced from.
func (f *File) mainRels() string {
	switch f.Kind {
	case Docx:
		return "word/_rels/document.xml.rels"
	case Pptx:
		return "ppt/_rels/presentation.xml.rels"
	case Xlsx:
		return "xl/_rels/workbook.xml.rels"
	}
	return ""
}

// mediaPath is where the package keeps embedded images.
func (f *File) mediaPath() string {
	switch f.Kind {
	case Docx:
		return "word/" + mediaName
	case Pptx:
		return "ppt/" + mediaName
	case Xlsx:
		return "xl/" + mediaName
	}
	return mediaName
}

func (f *File) addRelationship(relsPart, id, relType, target string) {
	rels := f.parts[relsPart]
	if rels == nil {
		rels = []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"></Relationships>`)
	}
	s := string(rels)
	if strings.Contains(s, `Id="`+id+`"`) {
		return
	}
	entry := fmt.Sprintf(`<Relationship Id="%s" Type="%s" Target="%s"/>`, id, relType, target)
	f.set(relsPart, []byte(strings.Replace(s, "</Relationships>", entry+"</Relationships>", 1)))
}

func (f *File) addContentType(entry string) {
	ct := f.parts["[Content_Types].xml"]
	if ct == nil || strings.Contains(string(ct), entry) {
		return
	}
	f.set("[Content_Types].xml", []byte(strings.Replace(string(ct), "</Types>", entry+"</Types>", 1)))
}

// ---- custom XML part ----

func (f *File) setCustomXML(tag string) {
	f.set(customXMLItem, []byte(fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><mark xmlns="%s"><id>%s</id></mark>`, markNamespace, tag)))
	f.set(customXMLProp, []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<ds:datastoreItem ds:itemID="{4D3F7A21-6C58-4C0E-9B77-5E2B4F6A1C93}" `+
		`xmlns:ds="http://schemas.openxmlformats.org/officeDocument/2006/customXml">`+
		`<ds:schemaRefs><ds:schemaRef ds:uri="`+markNamespace+`"/></ds:schemaRefs></ds:datastoreItem>`))
	f.set(customXMLRels, []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`+
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/customXmlProps" Target="itemProps1.xml"/>`+
		`</Relationships>`))
	f.addContentType(`<Override PartName="/` + customXMLItem + `" ContentType="application/xml"/>`)
	f.addContentType(`<Override PartName="/` + customXMLProp + `" ContentType="application/vnd.openxmlformats-officedocument.customXmlProperties+xml"/>`)
	f.addRelationship(f.mainRels(), "rIdCustodialXml",
		"http://schemas.openxmlformats.org/officeDocument/2006/relationships/customXml", "../"+customXMLItem)
}

func (f *File) customXML() string {
	if m := reMarkXML.FindSubmatch(f.parts[customXMLItem]); m != nil {
		return string(m[1])
	}
	return ""
}

// dropCustomXML removes the part the way the document inspector does.
func (f *File) dropCustomXML() {
	for _, n := range []string{customXMLItem, customXMLProp, customXMLRels} {
		f.remove(n)
	}
	for _, n := range []string{"[Content_Types].xml", f.mainRels()} {
		if b := f.parts[n]; b != nil {
			s := regexp.MustCompile(`<(?:Override|Relationship)[^>]*customXml[^>]*/>`).ReplaceAllString(string(b), "")
			f.set(n, []byte(s))
		}
	}
}

// ---- background image ----

const (
	docxHeader = "word/header9.xml"
	docxHdRels = "word/_rels/header9.xml.rels"
)

// setBackground tiles the watermark behind the content of every page, slide or
// sheet. The image itself is one tile; the application repeats it.
func (f *File) setBackground(png []byte) {
	f.set(f.mediaPath(), png)
	f.addContentType(`<Default Extension="png" ContentType="image/png"/>`)
	switch f.Kind {
	case Docx:
		f.docxBackground()
	case Pptx:
		f.pptxBackground()
	case Xlsx:
		f.xlsxBackground()
	}
}

// docxBackground puts the tile in a header, which is what makes a Word
// watermark print and survive editing of the body text.
func (f *File) docxBackground() {
	f.set(docxHeader, []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" `+
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" `+
		`xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office">`+
		`<w:p><w:r><w:pict>`+
		`<v:rect id="CustodialBg" o:spid="_x0000_s2050" o:allowincell="f" `+
		`style="position:absolute;margin-left:0;margin-top:0;width:595.3pt;height:841.9pt;z-index:-251658752;`+
		`mso-position-horizontal:left;mso-position-horizontal-relative:page;`+
		`mso-position-vertical:top;mso-position-vertical-relative:page" stroked="f" strokeweight="0">`+
		`<v:fill r:id="rIdCustodialBg" o:title="" recolor="t" rotate="t" type="tile"/>`+
		`</v:rect></w:pict></w:r></w:p></w:hdr>`))
	f.addContentType(`<Override PartName="/` + docxHeader + `" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"/>`)
	f.set(docxHdRels, []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`+
		`<Relationship Id="rIdCustodialBg" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="`+mediaName+`"/>`+
		`</Relationships>`))
	f.addRelationship("word/_rels/document.xml.rels", "rIdCustodialHdr",
		"http://schemas.openxmlformats.org/officeDocument/2006/relationships/header", "header9.xml")

	// Reference the header from every section that does not already use one.
	doc := string(f.parts["word/document.xml"])
	ref := `<w:headerReference w:type="default" r:id="rIdCustodialHdr"/>`
	if !strings.Contains(doc, "rIdCustodialHdr") {
		doc = regexp.MustCompile(`<w:sectPr(\s[^>]*)?>`).ReplaceAllString(doc, "${0}"+ref)
		f.set("word/document.xml", []byte(doc))
	}
}

// pptxBackground fills the slide master background, so every slide carries it.
func (f *File) pptxBackground() {
	for _, name := range f.names {
		if !strings.HasPrefix(name, "ppt/slideMasters/slideMaster") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		s := string(f.parts[name])
		if strings.Contains(s, "rIdCustodialBg") {
			continue
		}
		bg := `<p:bg><p:bgPr><a:blipFill dpi="0" rotWithShape="1">` +
			`<a:blip r:embed="rIdCustodialBg"/><a:srcRect/><a:tile tx="0" ty="0" sx="100000" sy="100000" flip="none" algn="tl"/>` +
			`</a:blipFill><a:effectLst/></p:bgPr></p:bg>`
		if i := strings.Index(s, "<p:cSld>"); i >= 0 {
			s = s[:i+len("<p:cSld>")] + bg + s[i+len("<p:cSld>"):]
		} else {
			continue
		}
		f.set(name, []byte(s))
		rels := "ppt/slideMasters/_rels/" + strings.TrimPrefix(name, "ppt/slideMasters/") + ".rels"
		f.addRelationship(rels, "rIdCustodialBg",
			"http://schemas.openxmlformats.org/officeDocument/2006/relationships/image", "../"+mediaName)
	}
}

// xlsxBackground sets the sheet background, which Excel tiles automatically.
// It shows on screen but does not print, which the UI says.
func (f *File) xlsxBackground() {
	for _, name := range f.sheetParts() {
		s := string(f.parts[name])
		if strings.Contains(s, "rIdCustodialBg") {
			continue
		}
		pic := `<picture r:id="rIdCustodialBg"/>`
		if i := strings.LastIndex(s, "</worksheet>"); i > 0 {
			s = s[:i] + pic + s[i:]
		} else {
			continue
		}
		if !strings.Contains(s, `xmlns:r=`) {
			s = strings.Replace(s, "<worksheet ", `<worksheet xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" `, 1)
		}
		f.set(name, []byte(s))
		rels := "xl/worksheets/_rels/" + strings.TrimPrefix(name, "xl/worksheets/") + ".rels"
		f.addRelationship(rels, "rIdCustodialBg",
			"http://schemas.openxmlformats.org/officeDocument/2006/relationships/image", "../"+mediaName)
	}
}

// Backgrounds returns the embedded watermark images, for the detector.
func (f *File) Backgrounds() [][]byte {
	var out [][]byte
	for _, n := range f.names {
		if strings.HasSuffix(n, mediaName) {
			out = append(out, f.parts[n])
		}
	}
	return out
}

// dropBackground removes the watermark image and every reference to it.
func (f *File) dropBackground() {
	f.remove(f.mediaPath())
	f.remove(docxHeader)
	f.remove(docxHdRels)
	for _, n := range append([]string(nil), f.names...) {
		b := f.parts[n]
		if b == nil || !strings.Contains(string(b), "Custodial") {
			continue
		}
		s := regexp.MustCompile(`<(?:Relationship|Override)[^>]*Custodial[^>]*/>`).ReplaceAllString(string(b), "")
		s = regexp.MustCompile(`(?s)<w:pict>.*?</w:pict>`).ReplaceAllString(s, "")
		s = regexp.MustCompile(`(?s)<p:bg>.*?</p:bg>`).ReplaceAllString(s, "")
		s = regexp.MustCompile(`<picture[^>]*rIdCustodialBg[^>]*/>`).ReplaceAllString(s, "")
		s = regexp.MustCompile(`<w:headerReference[^>]*rIdCustodialHdr[^>]*/>`).ReplaceAllString(s, "")
		f.set(n, []byte(s))
	}
}
