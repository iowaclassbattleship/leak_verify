package document

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Doc is the logical content that gets typeset for every recipient.
type Doc struct {
	Title      string
	Paragraphs []string
}

var asciiMap = map[rune]string{
	'‘': "'", '’': "'", '‚': "'", '“': "\"", '”': "\"", '„': "\"", '–': "-", '—': "-", '‐': "-", '‑': "-",
	'…': "...", '•': "-", '·': "-", ' ': " ", '€': "EUR", '«': "\"", '»': "\"", '×': "x",
	'ä': "ae", 'ö': "oe", 'ü': "ue", 'Ä': "Ae", 'Ö': "Oe", 'Ü': "Ue", 'ß': "ss",
	'é': "e", 'è': "e", 'ê': "e", 'ë': "e", 'É': "E", 'à': "a", 'â': "a", 'á': "a", 'ç': "c", 'ô': "o", 'ó': "o",
	'î': "i", 'ï': "i", 'í': "i", 'û': "u", 'ù': "u", 'ú': "u", 'ñ': "n",
}

// Normalize maps text onto the printable ASCII subset the embedded font
// encoding covers.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteRune(r)
		case r >= 32 && r < 127:
			b.WriteRune(r)
		case asciiMap[r] != "":
			b.WriteString(asciiMap[r])
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// DocFromText treats the first non-empty line as the title and blank lines as
// paragraph separators.
func DocFromText(text string) Doc {
	text = strings.ReplaceAll(Normalize(text), "\r\n", "\n")
	var d Doc
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			d.Paragraphs = append(d.Paragraphs, strings.Join(cur, " "))
			cur = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		switch {
		case line == "":
			flush()
		case d.Title == "":
			d.Title = line
		default:
			cur = append(cur, line)
		}
	}
	flush()
	return d
}

// ExtractedLine is a line of text recovered from a PDF content stream.
type ExtractedLine struct {
	Text string
	Y    float64
	Size float64
	Page int
	endX float64
}

// DocFromLines rebuilds title and paragraphs from positioned PDF text lines
// using vertical gaps, a best-effort heuristic for simple documents.
func DocFromLines(lines []ExtractedLine) Doc {
	var d Doc
	var clean []ExtractedLine
	for _, l := range lines {
		l.Text = strings.Join(strings.Fields(Normalize(l.Text)), " ")
		if l.Text != "" {
			clean = append(clean, l)
		}
	}
	if len(clean) == 0 {
		return d
	}
	var gaps, sizes []float64
	for i, l := range clean {
		sizes = append(sizes, l.Size)
		if i > 0 && clean[i-1].Page == l.Page {
			if g := clean[i-1].Y - l.Y; g > 0 {
				gaps = append(gaps, g)
			}
		}
	}
	medGap, medSize := median(gaps), median(sizes)
	i := 0
	var title []string
	for i < len(clean) && len(title) < 3 && (i == 0 || clean[i].Size > medSize*1.15 && clean[i].Page == clean[0].Page) {
		title = append(title, clean[i].Text)
		i++
		if clean[0].Size <= medSize*1.15 {
			break
		}
	}
	d.Title = strings.Join(title, " ")
	var cur string
	flush := func() {
		if cur != "" {
			d.Paragraphs = append(d.Paragraphs, cur)
			cur = ""
		}
	}
	for ; i < len(clean); i++ {
		l := clean[i]
		if i > 0 && cur != "" {
			prev := clean[i-1]
			gap := prev.Y - l.Y
			if (prev.Page == l.Page && medGap > 0 && gap > medGap*1.4) ||
				(prev.Page != l.Page && strings.ContainsAny(prev.Text[len(prev.Text)-1:], ".:!?")) {
				flush()
			}
		}
		switch {
		case cur == "":
			cur = l.Text
		case strings.HasSuffix(cur, "-") && len(l.Text) > 0 && unicode.IsLower(rune(l.Text[0])):
			cur = cur[:len(cur)-1] + l.Text
		default:
			cur += " " + l.Text
		}
	}
	flush()
	return d
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// TextQuality is the share of letters, digits, spaces and common punctuation;
// garbage from unreadable font encodings scores low.
func TextQuality(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	good, words := 0, 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || strings.ContainsRune(".,;:'\"()-%!?/&", r) {
			good++
		}
	}
	for _, w := range strings.Fields(s) {
		if len(w) >= 2 && len(w) <= 20 {
			words++
		}
	}
	q := float64(good) / float64(len([]rune(s)))
	if avg := float64(len(s)) / math.Max(1, float64(len(strings.Fields(s)))); avg > 14 {
		q *= 0.5 // words glued together or unreadable glyph codes
	}
	return q
}

const sampleTitle = "Project Halcyon - Board Briefing on the Proposed Acquisition of Meridian Logistics AG"

var sampleParagraphs = []string{
	"Prepared for the Board of Directors. Classification: Strictly Confidential. Distribution is limited to named recipients, and every copy of this briefing is individually registered.",
	"This briefing summarizes the current state of Project Halcyon, the proposed acquisition of Meridian Logistics AG, and sets out the decisions the Board is asked to take at its next meeting. The organization has been in exclusive discussions with Meridian for approximately four months. Negotiations began after an unsolicited approach from the principal shareholder of Meridian, and the steering committee analyzed three alternative structures before recommending the one described below. Moreover, management believes the timing is favorable: the logistics sector remains fragmented, while valuations have moved toward the lower end of their historical range.",
	"Strategic rationale. Meridian operates forty-two warehouses across Switzerland, southern Germany and northern Italy, and its route network overlaps with ours on approximately a third of volume. The combined organization would obtain scale in temperature-controlled freight, a segment in which neither company is currently able to fulfill demand from pharmaceutical customers. The steering committee has prioritized integration of the two dispatch platforms, since duplicated systems were recognized as the largest source of avoidable cost. Similarly, a centralized procurement function would minimize overlapping supplier contracts, which the finance team has labeled as a quick win for the first year.",
	"Valuation and financing. The indicative offer values Meridian at CHF 412 million on a cash-free, debt-free basis, which corresponds to 8.1 times normalized operating profit. This figure reflects the judgment of our advisers regarding comparable transactions among regional carriers. Financing would combine existing cash reserves with a new term facility; the treasury function has obtained indicative terms from two banks and expects to finalize the documentation within six weeks of Board approval. Therefore the transaction would not require an equity issue, and leverage would return toward the target range within twenty-four months.",
	"Risks. Three risks deserve particular attention. First, the largest customer contract of Meridian is due for renewal next spring, and our analysis assumes it is renewed on broadly similar terms. Second, the integration plan depends on retaining the Meridian operations team; retention packages have been authorized in principle but not yet finalized. Third, competition clearance in Germany may take longer than anticipated. While we consider a prohibition unlikely, the timetable assumes clearance within four months, and any remedy requirement could delay completion. The committee has modeled a downside case in which synergies arrive a year later; even in that case the acquisition remains value-accretive, although by a narrower margin.",
	"Confidentiality. Premature disclosure would almost certainly end the discussions and could expose the company to claims from Meridian. Any unauthorized disclosure of this briefing, in whole or in part, will be investigated. Directors who receive an inquiry from journalists, analysts or other third parties should not comment, and should forward the inquiry immediately to the Company Secretary. Copies of this document must not be forwarded, printed for circulation, or stored outside the board portal. Paper copies handed out at the meeting will be collected afterward and destroyed.",
	"Decisions requested. The Board is asked to authorize management to submit a binding offer on the terms summarized above, to approve the financing structure in principle, and to note that the final agreement will return to the Board before signing. Management will also begin preparatory work on the integration plan, so that detailed milestones can be presented at the following meeting. Furthermore, the Board is asked to confirm the membership of the steering committee, whose composition has been sufficient to manage the process so far, and to note that the engagement letters of the advisers have been canceled and reissued on revised fee terms.",
	"Next steps. Subject to the decision of the Board, the binding offer would be submitted within ten business days. Due diligence findings will be summarized in a supplementary paper, and management will emphasize any matter that could affect the offer price. The steering committee will meet weekly toward signing, and Directors will receive a short written update after each meeting. Questions regarding this briefing should be directed to the Chief Financial Officer.",
}

func SampleDoc() Doc {
	return Doc{Title: sampleTitle, Paragraphs: sampleParagraphs}
}
