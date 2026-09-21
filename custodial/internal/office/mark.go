package office

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"attribution/common/codec"
)

// Zero-width characters used by the text carrier. Both are legal in XML and
// invisible in every viewer, and they travel with copied text.
const (
	zwOne  = "​" // zero width space
	zwZero = "‌" // zero width non-joiner
)

// spacingStep is the character spacing applied per word: 1/20 pt in Word,
// 1/100 pt in PowerPoint. Both are far below what the eye resolves.
const (
	docxSpacing = 1
	pptxSpacing = 10
)

// zwMark is the only character the text carrier inserts: a zero-width space,
// placed after a word to mean a one bit and left out to mean a zero. Only a
// keyed third of the words are candidates, so a marked file carries the
// character after roughly one word in six.
const zwMark = "\u200b"

// textCandidateRate is the share of words that carry the text mark: one in
// this many. Sparser means fewer inserted characters, but fewer votes per bit.
const textCandidateRate = 3

type Layers struct {
	Spacing    bool `json:"spacing"`
	Text       bool `json:"text"`
	Metadata   bool `json:"metadata"`
	CustomXML  bool `json:"customxml"`
	Background bool `json:"background"`
}

// bitFunc returns the bit a carrier should store for a word (given the word
// before it), or false when the word cannot carry one.
type bitFunc func(prev, word string) (bit uint8, ok bool)

// core is the comparable form of a word: letters and digits only.
func core(token string) string {
	return strings.ToLower(strings.TrimFunc(strings.TrimSuffix(token, zwMark), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}))
}

// slotFor ties a slot to the words themselves rather than to their position,
// so a mark still reads back after text is copied out, reordered or partly
// deleted. The word is paired with the one before it, which spreads a small
// vocabulary over many more of the 32 codeword bits.
func slotFor(key codec.Key, prev, word string) (idx int, mask uint8, ok bool) {
	w := core(word)
	if w == "" {
		return 0, 0, false
	}
	h := key.Sum("office-slot", core(prev), w)
	return int(h[0]) % codec.CodeLen, h[1] & 1, true
}

// textSlot is like slotFor but only accepts a keyed subset of longer words, so
// the carrier touches as little of the text as possible.
func textSlot(key codec.Key, prev, word string) (idx int, mask uint8, ok bool) {
	w := core(word)
	if len([]rune(w)) < 3 {
		return 0, 0, false
	}
	h := key.Sum("office-text", core(prev), w)
	if h[2]%textCandidateRate != 0 {
		return 0, 0, false
	}
	return int(h[0]) % codec.CodeLen, h[1] & 1, true
}

func codeBits(key codec.Key, code codec.Codeword) bitFunc {
	return func(prev, word string) (uint8, bool) {
		idx, mask, ok := slotFor(key, prev, word)
		if !ok {
			return 0, false
		}
		return code[idx] ^ mask, true
	}
}

// voteFor records a bit read from a slot.
func voteFor(key codec.Key, v *codec.Votes, prev, word string, bit uint8) bool {
	idx, mask, ok := slotFor(key, prev, word)
	if !ok {
		return false
	}
	v.Add(idx, bit^mask, 1)
	return true
}

// Mark writes a recipient's copy. Layers not selected are left untouched.
// Mark writes one recipient's copy. tile is the watermark image the background
// layer tiles behind the content; it is ignored when that layer is off.
func (f *File) Mark(key codec.Key, code codec.Codeword, tag string, layers Layers, tile []byte) ([]byte, error) {
	c := f.clone()
	if layers.Spacing {
		c.applySpacing(codeBits(key, code))
	}
	if layers.Text {
		c.applyText(key, &code)
	}
	if layers.Metadata {
		c.setProperty(tag)
	}
	if layers.CustomXML {
		c.setCustomXML(tag)
	}
	if layers.Background && len(tile) > 0 {
		c.setBackground(tile)
	}
	return c.Bytes()
}

// ---- spacing carrier ----

var (
	reDocxRun  = regexp.MustCompile(`(?s)<w:r>(.*?)</w:r>`)
	reDocxRPr  = regexp.MustCompile(`(?s)^\s*(<w:rPr>.*?</w:rPr>)`)
	reDocxText = regexp.MustCompile(`(?s)<w:t(?:\s[^>]*)?>(.*?)</w:t>`)
	reDocxSpac = regexp.MustCompile(`<w:spacing w:val="(-?\d+)"/>`)
	rePptxRun  = regexp.MustCompile(`(?s)<a:r>(.*?)</a:r>`)
	rePptxRPr  = regexp.MustCompile(`(?s)^\s*(<a:rPr[^>]*?)(/>|>.*?</a:rPr>)`)
	rePptxText = regexp.MustCompile(`(?s)<a:t>(.*?)</a:t>`)
	rePptxSpc  = regexp.MustCompile(`\sspc="(-?\d+)"`)
	reXlsxRow  = regexp.MustCompile(`<row\s[^>]*>`)
	reXlsxCol  = regexp.MustCompile(`<col\s[^>]*/>`)
	reAttr     = func(name string) *regexp.Regexp { return regexp.MustCompile(name + `="([^"]*)"`) }
	reHt       = reAttr("ht")
	reWidth    = reAttr("width")
)

// applySpacing marks (or with a nil bits function, only counts) the spacing
// slots of a file, and returns how many slots it found.
func (f *File) applySpacing(bits bitFunc) int {
	slot := 0
	switch f.Kind {
	case Docx:
		for _, p := range f.contentParts() {
			prev := ""
			f.parts[p] = reDocxRun.ReplaceAllFunc(f.parts[p], func(run []byte) []byte {
				out, n, last := splitDocxRun(run, bits, prev)
				slot += n
				prev = last
				return out
			})
		}
	case Pptx:
		for _, p := range f.contentParts() {
			prev := ""
			f.parts[p] = rePptxRun.ReplaceAllFunc(f.parts[p], func(run []byte) []byte {
				out, n, last := splitPptxRun(run, bits, prev)
				slot += n
				prev = last
				return out
			})
		}
	case Xlsx:
		// Excel has no per-word spacing, so the carrier is the last digit of
		// row heights and column widths, keyed to the row or column itself.
		for _, p := range f.sheetParts() {
			data := reXlsxRow.ReplaceAllFunc(f.parts[p], func(tag []byte) []byte {
				slot++
				return markMeasure(tag, reHt, 12.8, "customHeight", measureToken(p, "row", tag), bits)
			})
			data = reXlsxCol.ReplaceAllFunc(data, func(tag []byte) []byte {
				slot++
				return markMeasure(tag, reWidth, 8.43, "customWidth", measureToken(p, "col", tag), bits)
			})
			f.parts[p] = data
		}
	}
	return slot
}

// splitDocxRun rewrites one Word run as one run per word, each carrying its own
// character spacing. Runs that hold anything but plain text are left alone.
func splitDocxRun(run []byte, bits bitFunc, prev string) ([]byte, int, string) {
	inner := run[len("<w:r>") : len(run)-len("</w:r>")]
	rPr := ""
	if m := reDocxRPr.FindSubmatch(inner); m != nil {
		rPr = string(m[1])
		inner = inner[len(m[0]):]
	}
	m := reDocxText.FindSubmatch(inner)
	if m == nil || len(bytesTrimSpace(inner)) != len(m[0]) || deliberateSpacing(rPr, reDocxSpac, 5) {
		return run, 0, lastWord(run, reDocxText, prev) // not plain text, or deliberate spacing
	}
	rPr = reDocxSpac.ReplaceAllString(rPr, "")
	words := splitWords(string(m[1]))
	if bits == nil {
		return run, len(words), lastOf(words, prev)
	}
	var b strings.Builder
	for _, w := range words {
		props := rPr
		if bit, ok := bits(prev, w); ok {
			val := docxSpacing
			if bit == 0 {
				val = -docxSpacing
			}
			props = withDocxSpacing(rPr, val)
		}
		if core(w) != "" {
			prev = w
		}
		b.WriteString("<w:r>" + props + `<w:t xml:space="preserve">` + w + "</w:t></w:r>")
	}
	return []byte(b.String()), len(words), prev
}

// lastOf returns the last word that can act as a predecessor.
func lastOf(words []string, prev string) string {
	for i := len(words) - 1; i >= 0; i-- {
		if core(words[i]) != "" {
			return words[i]
		}
	}
	return prev
}

func lastWord(run []byte, re *regexp.Regexp, prev string) string {
	if m := re.FindSubmatch(run); m != nil {
		return lastOf(splitWords(string(m[1])), prev)
	}
	return prev
}

// withDocxSpacing inserts a spacing element into a run's properties, keeping
// the order the schema expects.
func withDocxSpacing(rPr string, val int) string {
	el := fmt.Sprintf(`<w:spacing w:val="%d"/>`, val)
	if rPr == "" {
		return "<w:rPr>" + el + "</w:rPr>"
	}
	for _, after := range []string{"<w:w ", "<w:kern", "<w:position", "<w:sz", "<w:szCs", "<w:highlight", "<w:u ", "<w:bdr", "<w:shd", "<w:vertAlign", "<w:rtl", "<w:lang"} {
		if i := strings.Index(rPr, after); i >= 0 {
			return rPr[:i] + el + rPr[i:]
		}
	}
	return strings.TrimSuffix(rPr, "</w:rPr>") + el + "</w:rPr>"
}

// splitPptxRun does the same for a PowerPoint run, where spacing is an
// attribute of the run properties.
func splitPptxRun(run []byte, bits bitFunc, prev string) ([]byte, int, string) {
	inner := run[len("<a:r>") : len(run)-len("</a:r>")]
	rPr, rest := "", inner
	if m := rePptxRPr.FindSubmatch(inner); m != nil {
		rPr = string(m[0])
		rest = inner[len(m[0]):]
	}
	m := rePptxText.FindSubmatch(rest)
	if m == nil || len(bytesTrimSpace(rest)) != len(m[0]) || deliberateSpacing(rPr, rePptxSpc, 25) {
		return run, 0, lastWord(run, rePptxText, prev)
	}
	rPr = rePptxSpc.ReplaceAllString(rPr, "")
	words := splitWords(string(m[1]))
	if bits == nil {
		return run, len(words), lastOf(words, prev)
	}
	var b strings.Builder
	for _, w := range words {
		props := rPr
		if bit, ok := bits(prev, w); ok {
			val := pptxSpacing
			if bit == 0 {
				val = -pptxSpacing
			}
			props = withPptxSpacing(rPr, val)
		}
		if core(w) != "" {
			prev = w
		}
		b.WriteString("<a:r>" + props + "<a:t>" + w + "</a:t></a:r>")
	}
	return []byte(b.String()), len(words), prev
}

func withPptxSpacing(rPr string, val int) string {
	attr := fmt.Sprintf(` spc="%d"`, val)
	if rPr == "" {
		return `<a:rPr lang="en-US"` + attr + "/>"
	}
	if i := strings.Index(rPr, ">"); i > 0 {
		if rPr[i-1] == '/' {
			return rPr[:i-1] + attr + "/>"
		}
		return rPr[:i] + attr + rPr[i:]
	}
	return rPr
}

// markMeasure sets the last digit of a row height or column width, adding the
// attribute when the sheet does not carry one yet.
func markMeasure(tag []byte, attr *regexp.Regexp, fallback float64, customFlag, token string, bits bitFunc) []byte {
	if bits == nil {
		return tag
	}
	bit, ok := bits("", token)
	if !ok {
		return tag
	}
	value := fallback
	if m := attr.FindSubmatch(tag); m != nil {
		if v, err := strconv.ParseFloat(string(m[1]), 64); err == nil && v > 0 {
			value = v
		}
	}
	n := int64(math.Round(value * 100))
	if uint8(n&1) != bit {
		n++
	}
	set := fmt.Sprintf(`%s="%s"`, attr.String()[:strings.Index(attr.String(), "=")], strconv.FormatFloat(float64(n)/100, 'f', 2, 64))
	out := string(tag)
	if attr.MatchString(out) {
		out = attr.ReplaceAllString(out, set)
	} else {
		out = strings.TrimSuffix(strings.TrimSuffix(out, "/>"), ">") + " " + set
		if strings.HasSuffix(string(tag), "/>") {
			out += "/>"
		} else {
			out += ">"
		}
	}
	// Excel only keeps an explicit measurement when the custom flag is set.
	if strings.Contains(out, customFlag+`="false"`) {
		out = strings.ReplaceAll(out, customFlag+`="false"`, customFlag+`="true"`)
	} else if !strings.Contains(out, customFlag) {
		out = strings.TrimSuffix(strings.TrimSuffix(out, "/>"), ">") + " " + customFlag + `="true"`
		if strings.HasSuffix(string(tag), "/>") {
			out += "/>"
		} else {
			out += ">"
		}
	}
	return []byte(out)
}

// readSpacing recovers the votes carried by the spacing slots.
func (f *File) readSpacing(key codec.Key) (codec.Votes, int) {
	var v codec.Votes
	slots, read := 0, 0
	word := func(run []byte, re *regexp.Regexp) string {
		if m := re.FindSubmatch(run); m != nil {
			return string(m[1])
		}
		return ""
	}
	switch f.Kind {
	case Docx, Pptx:
		runRe, textRe, spacRe, step := reDocxRun, reDocxText, reDocxSpac, docxSpacing
		if f.Kind == Pptx {
			runRe, textRe, spacRe, step = rePptxRun, rePptxText, rePptxSpc, pptxSpacing
		}
		for _, p := range f.contentParts() {
			prev := ""
			for _, run := range runRe.FindAll(f.parts[p], -1) {
				if !textRe.Match(run) {
					continue
				}
				slots++
				text := word(run, textRe)
				if m := spacRe.FindSubmatch(run); m != nil {
					if n, err := strconv.Atoi(string(m[1])); err == nil && (n == step || n == -step) {
						bit := uint8(1)
						if n < 0 {
							bit = 0
						}
						if voteFor(key, &v, prev, text, bit) {
							read++
						}
					}
				}
				prev = lastOf(splitWords(text), prev)
			}
		}
	case Xlsx:
		for _, p := range f.sheetParts() {
			for _, tag := range reXlsxRow.FindAll(f.parts[p], -1) {
				slots++
				if readParity(key, &v, tag, reHt, measureToken(p, "row", tag)) {
					read++
				}
			}
			for _, tag := range reXlsxCol.FindAll(f.parts[p], -1) {
				slots++
				if readParity(key, &v, tag, reWidth, measureToken(p, "col", tag)) {
					read++
				}
			}
		}
	}
	return v, slots
}

func readParity(key codec.Key, v *codec.Votes, tag []byte, attr *regexp.Regexp, token string) bool {
	m := attr.FindSubmatch(tag)
	if m == nil {
		return false
	}
	x, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil {
		return false
	}
	return voteFor(key, v, "", token, uint8(int64(math.Round(x*100))&1))
}

// ---- text carrier ----

// applyText marks candidate words by appending a zero-width character for a
// one bit, and leaving the word alone for a zero. With bits nil it only counts
// the candidates.
func (f *File) applyText(key codec.Key, code *codec.Codeword) int {
	slots := 0
	for _, p := range f.contentParts() {
		prev := ""
		f.parts[p] = reTextEl.ReplaceAllFunc(f.parts[p], func(el []byte) []byte {
			m := reTextEl.FindSubmatch(el)
			text := string(m[3])
			if strings.TrimSpace(text) == "" {
				return el
			}
			var b strings.Builder
			for _, w := range splitWords(text) {
				idx, mask, ok := textSlot(key, prev, w)
				if core(w) != "" {
					prev = w
				}
				if !ok {
					b.WriteString(w)
					continue
				}
				slots++
				if code == nil || code[idx]^mask == 0 {
					b.WriteString(w)
					continue
				}
				trimmed := strings.TrimRight(w, " ")
				b.WriteString(trimmed + zwMark + w[len(trimmed):])
			}
			if code == nil {
				return el
			}
			return []byte("<" + string(m[1]) + string(m[2]) + ">" + b.String() + "</" + string(m[1]) + ">")
		})
	}
	return slots
}

func (f *File) readText(key codec.Key) (codec.Votes, int, int) {
	var text strings.Builder
	for _, p := range f.contentParts() {
		for _, m := range reTextEl.FindAllSubmatch(f.parts[p], -1) {
			text.Write(m[3])
			text.WriteString("\n")
		}
	}
	return ReadTextVotes(key, text.String())
}

// ReadTextVotes recovers the text carrier from plain text, for a copy that was
// pasted somewhere rather than forwarded as a file. It returns the votes, the
// number of candidate words and how many actually carry the character: with
// none, the carrier is simply not there.
func ReadTextVotes(key codec.Key, text string) (codec.Votes, int, int) {
	var v codec.Votes
	slots, marked := 0, 0
	prev := ""
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\r' || r == '\t'
	}) {
		idx, mask, ok := textSlot(key, prev, w)
		if core(w) != "" {
			prev = w
		}
		if !ok {
			continue
		}
		slots++
		var bit uint8
		if strings.HasSuffix(w, zwMark) {
			bit, marked = 1, marked+1
		}
		v.Add(idx, bit^mask, 1)
	}
	return v, slots, marked
}

// applyText appends a zero-width character to selected words, and returns the
// number of slots the file offers.
// deliberateSpacing reports whether a run already sets spacing large enough to
// be a typographic choice rather than noise, in which case it is left alone.
func deliberateSpacing(rPr string, re *regexp.Regexp, limit int) bool {
	m := re.FindStringSubmatch(rPr)
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return true
	}
	return n > limit || n < -limit
}

// measureToken names a row or column so its mark does not depend on position.
func measureToken(part, kind string, tag []byte) string {
	attr := "r"
	if kind == "col" {
		attr = "min"
	}
	id := ""
	if m := reAttr(attr).FindSubmatch(tag); m != nil {
		id = string(m[1])
	}
	return part + "|" + kind + id
}

// splitWords splits text into words, keeping the trailing spaces with each.
func splitWords(text string) []string {
	var out []string
	i := 0
	for i < len(text) {
		j := strings.IndexByte(text[i:], ' ')
		if j < 0 {
			out = append(out, text[i:])
			break
		}
		k := i + j
		for k < len(text) && text[k] == ' ' {
			k++
		}
		out = append(out, text[i:k])
		i = k
	}
	return out
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
