package document

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"golang.org/x/text/encoding/charmap"
)

// pdfFont turns the codes a content stream shows into text and advance
// widths. It covers what ordinary producers emit: simple fonts with a base
// encoding, /Differences and an optional ToUnicode CMap (ReportLab, TeX, our
// own files), and Type0 fonts with two-byte Identity-H codes and a ToUnicode
// CMap (LibreOffice, Word, Chromium).
type pdfFont struct {
	twoByte bool
	first   int
	widths  []float64       // simple font, 1/1000 em, from FirstChar
	cidW    map[int]float64 // Type0, from /W
	dw      float64         // Type0 default width
	toUni   map[int]string  // ToUnicode CMap, preferred when present
	enc     *[256]string    // simple font: code to text, from the encoding
}

// codes splits a shown string into character codes.
func (f *pdfFont) codes(s string) []int {
	if f != nil && f.twoByte {
		out := make([]int, 0, len(s)/2)
		for i := 0; i+1 < len(s); i += 2 {
			out = append(out, int(s[i])<<8|int(s[i+1]))
		}
		return out
	}
	out := make([]int, len(s))
	for i := 0; i < len(s); i++ {
		out[i] = int(s[i])
	}
	return out
}

func (f *pdfFont) width(code int) float64 {
	switch {
	case f == nil:
		return 500 // unknown font: assume half an em
	case f.twoByte:
		if w, ok := f.cidW[code]; ok {
			return w
		}
		if f.dw > 0 {
			return f.dw
		}
		return 1000
	}
	if i := code - f.first; i >= 0 && i < len(f.widths) && f.widths[i] > 0 {
		return f.widths[i]
	}
	return 500
}

func (f *pdfFont) text(code int) string {
	if f == nil {
		return decodeSimpleText(string([]byte{byte(code)}))
	}
	if t, ok := f.toUni[code]; ok {
		return t
	}
	if f.twoByte {
		return "" // a CID without a Unicode mapping has no recoverable text
	}
	if f.enc != nil && code < 256 {
		if t := f.enc[code]; t != "" {
			return t
		}
	}
	return decodeSimpleText(string([]byte{byte(code)}))
}

var (
	reSubtypeType0   = regexp.MustCompile(`/Subtype\s*/Type0\b`)
	reDescendant     = regexp.MustCompile(`/DescendantFonts\s*(\[\s*(\d+)\s+\d+\s+R\s*\]|(\d+)\s+\d+\s+R|\[\s*<<)`)
	reToUnicode      = regexp.MustCompile(`/ToUnicode\s+(\d+)\s+\d+\s+R`)
	reEncodingName   = regexp.MustCompile(`/Encoding\s*/([A-Za-z0-9-]+)`)
	reEncodingRef    = regexp.MustCompile(`/Encoding\s+(\d+)\s+\d+\s+R`)
	reBaseEncoding   = regexp.MustCompile(`/BaseEncoding\s*/(\w+)`)
	reDW             = regexp.MustCompile(`/DW\s+(\d+(?:\.\d+)?)`)
	reWArrayStart    = regexp.MustCompile(`/W\s*\[`)
	reWArrayRef      = regexp.MustCompile(`/W\s+(\d+)\s+\d+\s+R`)
	reBaseFont       = regexp.MustCompile(`/BaseFont\s*/([^\s/<>\[\]()]+)`)
	reCMapHex        = regexp.MustCompile(`<([0-9A-Fa-f\s]*)>`)
	reCodespaceBlock = regexp.MustCompile(`(?s)begincodespacerange(.*?)endcodespacerange`)
	reBfcharBlock    = regexp.MustCompile(`(?s)beginbfchar(.*?)endbfchar`)
	reBfrangeBlock   = regexp.MustCompile(`(?s)beginbfrange(.*?)endbfrange`)
	reBfrangeEntry   = regexp.MustCompile(`<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>\s*(<[0-9A-Fa-f]*>|\[[^\]]*\])`)
)

// loadFont builds the decoder for one font dictionary.
func loadFont(objs map[int]*pdfObject, dict []byte) *pdfFont {
	f := &pdfFont{}
	if m := reToUnicode.FindSubmatch(dict); m != nil {
		if o, ok := objs[atoi(m[1])]; ok && o.stream != nil {
			f.toUni = parseToUnicode(o.stream)
		}
	}
	if reSubtypeType0.Match(dict) {
		f.twoByte = true
		f.dw = 1000
		desc := descendantDict(objs, dict)
		if m := reDW.FindSubmatch(desc); m != nil {
			f.dw, _ = strconv.ParseFloat(string(m[1]), 64)
		}
		f.cidW = parseCIDWidths(objs, desc)
		return f
	}
	if fc := reFirstChar.FindSubmatch(dict); fc != nil {
		f.first = atoi(fc[1])
	}
	var arr []byte
	if w := reWidthsInline.FindSubmatch(dict); w != nil {
		arr = w[1]
	} else if w := reWidthsRef.FindSubmatch(dict); w != nil {
		if ao, ok := objs[atoi(w[1])]; ok {
			arr = bytes.Trim(bytes.TrimSpace(ao.dict), "[]")
		}
	}
	for _, num := range reNumber.FindAll(arr, -1) {
		v, _ := strconv.ParseFloat(string(num), 64)
		f.widths = append(f.widths, v)
	}
	f.enc = simpleEncoding(objs, dict)
	return f
}

// descendantDict returns the CIDFont dictionary under a Type0 font.
func descendantDict(objs map[int]*pdfObject, dict []byte) []byte {
	m := reDescendant.FindSubmatchIndex(dict)
	if m == nil {
		return nil
	}
	for _, g := range []int{2, 3} {
		if m[2*g] >= 0 {
			if o, ok := objs[atoi(dict[m[2*g]:m[2*g+1]])]; ok {
				return o.dict
			}
		}
	}
	return dict[m[0]:] // inline dictionary
}

// parseCIDWidths reads a /W array: "c [w1 w2 ...]" and "cfirst clast w".
func parseCIDWidths(objs map[int]*pdfObject, desc []byte) map[int]float64 {
	out := map[int]float64{}
	var body []byte
	if m := reWArrayRef.FindSubmatch(desc); m != nil {
		if o, ok := objs[atoi(m[1])]; ok {
			body = bytes.TrimSpace(o.dict)
			body = bytes.TrimPrefix(body, []byte("["))
		}
	} else if loc := reWArrayStart.FindIndex(desc); loc != nil {
		body = desc[loc[1]:]
	}
	var nums []float64
	depth := 0
	for i := 0; i < len(body); {
		c := body[i]
		switch {
		case c == '[':
			depth++
			i++
		case c == ']':
			if depth == 0 {
				return out // end of /W
			}
			depth--
			i++
			// "c [w1 w2 ...]": the array just closed.
			if len(nums) >= 2 {
				start := int(nums[0])
				for k, w := range nums[1:] {
					out[start+k] = w
				}
			}
			nums = nums[:0]
		case c == '-' || c == '.' || (c >= '0' && c <= '9'):
			j := i + 1
			for j < len(body) && (body[j] == '.' || (body[j] >= '0' && body[j] <= '9')) {
				j++
			}
			v, _ := strconv.ParseFloat(string(body[i:j]), 64)
			nums = append(nums, v)
			i = j
			if depth == 0 && len(nums) == 3 { // "cfirst clast w"
				for c := int(nums[0]); c <= int(nums[1]) && c-int(nums[0]) < 65536; c++ {
					out[c] = nums[2]
				}
				nums = nums[:0]
			}
		default:
			i++
		}
	}
	return out
}

// parseToUnicode reads the bfchar and bfrange sections of a ToUnicode CMap.
func parseToUnicode(cmap []byte) map[int]string {
	out := map[int]string{}
	for _, blk := range reBfcharBlock.FindAllSubmatch(cmap, -1) {
		hexes := reCMapHex.FindAllSubmatch(blk[1], -1)
		for k := 0; k+1 < len(hexes); k += 2 {
			if src, ok := hexInt(hexes[k][1]); ok {
				out[src] = utf16Hex(hexes[k+1][1])
			}
		}
	}
	for _, blk := range reBfrangeBlock.FindAllSubmatch(cmap, -1) {
		for _, e := range reBfrangeEntry.FindAllSubmatch(blk[1], -1) {
			lo, ok1 := hexInt(e[1])
			hi, ok2 := hexInt(e[2])
			if !ok1 || !ok2 || hi < lo || hi-lo > 65535 {
				continue
			}
			if e[3][0] == '[' {
				for k, h := range reCMapHex.FindAllSubmatch(e[3], -1) {
					if lo+k <= hi {
						out[lo+k] = utf16Hex(h[1])
					}
				}
				continue
			}
			// The destination's last unit increments across the range.
			base := []rune(utf16Hex(bytes.Trim(e[3], "<>")))
			if len(base) == 0 {
				continue
			}
			for c := lo; c <= hi; c++ {
				r := append([]rune(nil), base...)
				r[len(r)-1] += rune(c - lo)
				out[c] = string(r)
			}
		}
	}
	return out
}

func hexInt(h []byte) (int, bool) {
	h = bytes.Join(bytes.Fields(h), nil)
	if len(h) == 0 || len(h) > 8 {
		return 0, false
	}
	v, err := strconv.ParseUint(string(h), 16, 32)
	return int(v), err == nil
}

// utf16Hex decodes a CMap destination, UTF-16BE in hex.
func utf16Hex(h []byte) string {
	h = bytes.Join(bytes.Fields(h), nil)
	if len(h) == 2 { // a one-byte destination, seen in some producers
		v, _ := strconv.ParseUint(string(h), 16, 8)
		return string(rune(v))
	}
	var units []uint16
	for i := 0; i+4 <= len(h); i += 4 {
		v, err := strconv.ParseUint(string(h[i:i+4]), 16, 16)
		if err != nil {
			return ""
		}
		units = append(units, uint16(v))
	}
	s := string(utf16.Decode(units))
	if s == " " {
		return " "
	}
	return s
}

// simpleEncoding resolves a simple font's code-to-text table: the base
// encoding, then any /Differences on top.
func simpleEncoding(objs map[int]*pdfObject, dict []byte) *[256]string {
	var enc [256]string
	base := "StandardEncoding"
	if m := reBaseFont.FindSubmatch(dict); m != nil {
		name := string(m[1])
		if i := strings.IndexByte(name, '+'); i == 6 {
			name = name[7:] // subset prefix
		}
		if strings.HasPrefix(name, "Symbol") || strings.HasPrefix(name, "ZapfDingbats") {
			base = ""
		}
	}
	encDict := []byte(nil)
	if m := reEncodingName.FindSubmatch(dict); m != nil {
		base = string(m[1])
	} else if m := reEncodingRef.FindSubmatch(dict); m != nil {
		if o, ok := objs[atoi(m[1])]; ok {
			encDict = o.dict
		}
	} else if i := bytes.Index(dict, []byte("/Encoding")); i >= 0 {
		encDict = dict[i:]
	}
	if encDict != nil {
		if m := reBaseEncoding.FindSubmatch(encDict); m != nil {
			base = string(m[1])
		}
	}
	fillBaseEncoding(&enc, base)
	if encDict != nil {
		if m := reDifferences.FindSubmatch(encDict); m != nil {
			code := 0
			for _, tok := range reDiffToken.FindAll(m[1], -1) {
				if tok[0] != '/' {
					code, _ = strconv.Atoi(string(tok))
					continue
				}
				if code >= 0 && code < 256 {
					if t := glyphNameText(string(tok[1:])); t != "" {
						enc[code] = t
					}
				}
				code++
			}
		}
	}
	return &enc
}

func fillBaseEncoding(enc *[256]string, base string) {
	var cm *charmap.Charmap
	switch base {
	case "WinAnsiEncoding":
		cm = charmap.Windows1252
	case "MacRomanEncoding":
		cm = charmap.Macintosh
	case "StandardEncoding", "PDFDocEncoding":
		cm = charmap.Windows1252 // close enough outside a handful of codes
	default:
		return
	}
	for c := 32; c < 256; c++ {
		if r := cm.DecodeByte(byte(c)); r != '�' {
			enc[c] = string(r)
		}
	}
	if base == "StandardEncoding" {
		enc[0x27], enc[0x60] = "'", "'"
	}
	enc[0xA0] = " "
}

// glyphNames covers the Adobe glyph names that show up in /Differences for
// Latin text. Single letters and uniXXXX names are handled in glyphNameText.
var glyphNames = map[string]string{
	"space": " ", "exclam": "!", "quotedbl": "\"", "numbersign": "#", "dollar": "$", "percent": "%",
	"ampersand": "&", "quotesingle": "'", "parenleft": "(", "parenright": ")", "asterisk": "*", "plus": "+",
	"comma": ",", "hyphen": "-", "period": ".", "slash": "/", "colon": ":", "semicolon": ";", "less": "<",
	"equal": "=", "greater": ">", "question": "?", "at": "@", "bracketleft": "[", "backslash": "\\",
	"bracketright": "]", "asciicircum": "^", "underscore": "_", "grave": "`", "braceleft": "{", "bar": "|",
	"braceright": "}", "asciitilde": "~", "zero": "0", "one": "1", "two": "2", "three": "3", "four": "4",
	"five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9",
	"fi": "fi", "fl": "fl", "ff": "ff", "ffi": "ffi", "ffl": "ffl", "dotlessi": "ı",
	"quoteright": "’", "quoteleft": "‘", "quotedblleft": "“", "quotedblright": "”", "quotesinglbase": "‚",
	"quotedblbase": "„", "endash": "–", "emdash": "—", "bullet": "•", "minus": "-", "ellipsis": "…",
	"guillemotleft": "«", "guillemotright": "»", "periodcentered": "·", "degree": "°", "section": "§",
	"paragraph": "¶", "copyright": "©", "registered": "®", "trademark": "™", "Euro": "€", "sterling": "£",
	"yen": "¥", "cent": "¢", "multiply": "×", "divide": "÷", "plusminus": "±", "nbspace": " ",
	"adieresis": "ä", "odieresis": "ö", "udieresis": "ü", "Adieresis": "Ä", "Odieresis": "Ö", "Udieresis": "Ü",
	"germandbls": "ß", "eacute": "é", "egrave": "è", "ecircumflex": "ê", "edieresis": "ë", "Eacute": "É",
	"agrave": "à", "acircumflex": "â", "aacute": "á", "ccedilla": "ç", "Ccedilla": "Ç", "ocircumflex": "ô",
	"oacute": "ó", "icircumflex": "î", "idieresis": "ï", "iacute": "í", "ucircumflex": "û", "ugrave": "ù",
	"uacute": "ú", "ntilde": "ñ", "oslash": "ø", "aring": "å", "ae": "æ", "oe": "œ",
}

// glyphNameText maps a glyph name to its text, following the Adobe glyph list
// conventions: "a", "uni00E9", "u1F600", "f_f_i", "a.sc".
func glyphNameText(name string) string {
	if i := strings.IndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	if strings.Contains(name, "_") {
		var b strings.Builder
		for _, part := range strings.Split(name, "_") {
			b.WriteString(glyphNameText(part))
		}
		return b.String()
	}
	if t, ok := glyphNames[name]; ok {
		return t
	}
	if len(name) == 1 && (name[0] >= 'a' && name[0] <= 'z' || name[0] >= 'A' && name[0] <= 'Z') {
		return name
	}
	if strings.HasPrefix(name, "uni") && len(name) >= 7 {
		var b strings.Builder
		for i := 3; i+4 <= len(name); i += 4 {
			v, err := strconv.ParseUint(name[i:i+4], 16, 16)
			if err != nil {
				return ""
			}
			b.WriteRune(rune(v))
		}
		return b.String()
	}
	if strings.HasPrefix(name, "u") && len(name) >= 5 && len(name) <= 7 {
		if v, err := strconv.ParseUint(name[1:], 16, 32); err == nil {
			return string(rune(v))
		}
	}
	return ""
}

func (f *pdfFont) isTwoByte() bool { return f != nil && f.twoByte }
