package tabular

import (
	"fmt"
	"strconv"
	"strings"

	"attribution/common/codec"
)

// Preview shows what the selected measures do to real rows, so a data owner
// can judge the cost before anything is issued. The rows are the head of the
// table; the counts are for the whole copy.

type PreviewCell struct {
	Value  string `json:"value"`
	Before string `json:"before,omitempty"` // set only when a measure changed it
	Kind   string `json:"kind,omitempty"`   // changed | noise | format | added
}

type PreviewRow struct {
	Cells    []PreviewCell `json:"cells"`
	Canary   bool          `json:"canary,omitempty"`
	Withheld bool          `json:"withheld,omitempty"`
	Moved    bool          `json:"moved,omitempty"`
}

// PreviewColumn carries the column name and the role that decides whether a
// measure may touch it.
type PreviewColumn struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	Added bool   `json:"added,omitempty"`
}

type PreviewImpact struct {
	Rows          int      `json:"rows"`
	RowsAdded     int      `json:"rowsAdded"`
	RowsWithheld  int      `json:"rowsWithheld"`
	ColumnsAdded  int      `json:"columnsAdded"`
	CellsTotal    int      `json:"cellsTotal"`
	CellsChanged  int      `json:"cellsChanged"`
	CellsPercent  float64  `json:"cellsPercent"`
	CellsReformat int      `json:"cellsReformatted"`
	PairsSwapped  int      `json:"pairsSwapped"`
	CellsRedacted int      `json:"cellsRedacted"`
	CellsNoised   int      `json:"cellsNoised"`
	LargestShift  string   `json:"largestShift,omitempty"`
	Notes         []string `json:"notes"`
}

type PreviewResult struct {
	Columns []PreviewColumn `json:"columns"`
	Rows    []PreviewRow    `json:"rows"`
	Impact  PreviewImpact   `json:"impact"`
}

// PreviewMark marks the table for one recipient and returns the head of the
// result with every change marked up.
//
// Low-order-bit and dummy values depend only on the key and the row's own
// identifying value, so a row marks the same whether it sits in a preview or
// in the full copy. Canary rows are scattered across the whole table, so the
// head of a large copy would usually show none; one is placed here so the
// measure can be seen, and the impact counts state the real number.
func PreviewMark(key codec.Key, master *Table, sc Schema, id uint16, tech Techniques, n int) PreviewResult {
	marked, iss := MarkCopy(key, master, sc, id, tech)

	out := PreviewResult{Impact: PreviewImpact{
		Rows:          len(marked.Rows),
		RowsAdded:     len(iss.Canaries),
		RowsWithheld:  iss.Withheld,
		ColumnsAdded:  len(marked.Columns) - len(master.Columns),
		CellsTotal:    len(master.Rows) * len(master.Columns),
		CellsChanged:  iss.Changed,
		CellsNoised:   iss.Noised,
		CellsReformat: iss.Reformat,
		PairsSwapped:  iss.Swapped,
		CellsRedacted: iss.Redacted,
	}}
	if out.Impact.CellsTotal > 0 {
		out.Impact.CellsPercent = 100 * float64(out.Impact.CellsChanged) / float64(out.Impact.CellsTotal)
	}

	for _, name := range master.Columns {
		out.Columns = append(out.Columns, PreviewColumn{Name: name, Role: sc.roleOf(name)})
	}
	if tech.Dummy && sc.Dummy != "" {
		out.Columns = append(out.Columns, PreviewColumn{Name: sc.Dummy, Role: "added", Added: true})
	}

	// The head of the table, each row marked exactly as the full copy marks it.
	head := min(n, len(master.Rows))
	code := key.Encode(id)
	par := tech.Params.WithDefaults()
	redact := map[string]bool{}
	if tech.Redact {
		for _, name := range sc.redactable(master) {
			redact[name] = true
		}
	}
	var largest float64
	for i := 0; i < head; i++ {
		src := master.Rows[i]
		pk := sc.rowKey(master, src)
		row := PreviewRow{Withheld: tech.Allocate && withheld(key, pk, id, par.AllocRate)}
		for c, name := range master.Columns {
			cell := PreviewCell{Value: src[c]}
			if redact[name] {
				cell.Before, cell.Value, cell.Kind = cell.Value, redactValue(key, cell.Value, id), "redact"
			}
			f, tolerant := sc.tolerantField(name)
			if tech.Noise && tolerant {
				if sel, idx, mask, jitter := noiseSlot(key, pk, name, par.NoiseRate, par.NoiseSpan); sel {
					if nv, ok := applyNoise(f, cell.Value, code[idx]^mask, jitter); ok {
						cell.Before, cell.Value, cell.Kind = cell.Value, nv, "noise"
					}
				}
			}
			if df, deep := shallower(f, par.LowBitDepth); tech.LowBit && tolerant && deep {
				if sel, idx, mask := lowBitSlot(key, pk, name, par.LowBitGamma); sel {
					if nv, changed := writeParity(df, cell.Value, code[idx]^mask); changed {
						if cell.Before == "" {
							cell.Before = cell.Value
						}
						cell.Value, cell.Kind = nv, "changed"
					}
				}
			}
			if cell.Before != "" {
				if d := numericShift(cell.Before, cell.Value); d > largest {
					largest = d
				}
			}
			if tech.Format && name != sc.Key {
				if sel, idx, mask := formatSlot(key, pk, name, par.FormatRate); sel && code[idx]^mask == 1 {
					if nv, ok := padDecimal(cell.Value); ok {
						if cell.Before == "" {
							cell.Before = cell.Value
						}
						cell.Value, cell.Kind = nv, "format"
					}
				}
			}
			row.Cells = append(row.Cells, cell)
		}
		if tech.Dummy && sc.Dummy != "" {
			row.Cells = append(row.Cells, PreviewCell{Value: dummyValue(key, pk, id), Kind: "added"})
		}
		out.Rows = append(out.Rows, row)
	}
	if largest > 0 {
		out.Impact.LargestShift = strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.10f", largest), "0"), ".")
	}

	// If allocation withheld nothing inside the head, pull in the first row it
	// did withhold, so the measure is visible rather than described.
	if tech.Allocate && len(out.Rows) > 0 {
		shown := false
		for _, r := range out.Rows {
			shown = shown || r.Withheld
		}
		for i := head; i < len(master.Rows) && !shown; i++ {
			src := master.Rows[i]
			if !withheld(key, sc.rowKey(master, src), id, par.AllocRate) {
				continue
			}
			row := PreviewRow{Withheld: true}
			for c, name := range master.Columns {
				v := src[c]
				if redact[name] {
					v = redactValue(key, v, id)
				}
				row.Cells = append(row.Cells, PreviewCell{Value: v})
			}
			if tech.Dummy && sc.Dummy != "" {
				row.Cells = append(row.Cells, PreviewCell{Value: "", Kind: "added"})
			}
			at := min(6, len(out.Rows))
			out.Rows = append(out.Rows[:at], append([]PreviewRow{row}, out.Rows[at:]...)...)
			shown = true
		}
	}

	// One canary, so the measure is visible rather than described.
	if tech.Canary && len(iss.Canaries) > 0 && len(out.Rows) > 0 {
		c := iss.Canaries[0]
		row := PreviewRow{Canary: true}
		for i := range out.Columns {
			v := ""
			if i < len(c.Row) {
				v = c.Row[i]
			}
			kind := ""
			if out.Columns[i].Added {
				v, kind = dummyValue(key, sc.rowKey(master, c.Row), id), "added"
			}
			row.Cells = append(row.Cells, PreviewCell{Value: v, Kind: kind})
		}
		at := min(3, len(out.Rows))
		out.Rows = append(out.Rows[:at], append([]PreviewRow{row}, out.Rows[at:]...)...)
	}

	// Ordering is applied last, as it is on the full copy.
	if tech.Order {
		for i := 0; i+1 < len(out.Rows); i += 2 {
			if out.Rows[i].Canary || out.Rows[i+1].Canary || out.Rows[i].Withheld || out.Rows[i+1].Withheld {
				continue
			}
			src := master.Rows[min(i, head-1)]
			if sel, idx, mask := orderSlot(key, sc.rowKey(master, src), par.OrderGamma); sel && code[idx]^mask == 1 {
				out.Rows[i], out.Rows[i+1] = out.Rows[i+1], out.Rows[i]
				out.Rows[i].Moved, out.Rows[i+1].Moved = true, true
			}
		}
	}

	out.Impact.Notes = previewNotes(master, sc, tech, iss, out.Impact, par)
	return out
}

// previewNotes states plainly what each selected measure cost, including where
// it could not be applied.
func previewNotes(master *Table, sc Schema, tech Techniques, iss *Issuance, im PreviewImpact, par Params) []string {
	var notes []string
	if tech.Canary {
		if n := len(iss.Canaries); n > 0 {
			notes = append(notes, fmt.Sprintf("%s added across the copy, about 1 in %s. No existing value is altered. One is shown above.",
				countNoun(n, "synthetic row", "synthetic rows"), thousands(max(1, im.Rows/max(1, n)))))
			if sc.DenseKey(master) {
				notes = append(notes, fmt.Sprintf("%s has no gaps, so each canary takes a key past the highest one. Anyone sorting the copy by %s can spot them.", sc.Key, sc.Key))
			}
		} else {
			notes = append(notes, "No synthetic rows could be built: the table has no column that identifies a row.")
		}
	}
	if tech.LowBit {
		switch {
		case len(sc.Tolerant) == 0:
			notes = append(notes, "No column is marked tolerant, so there is nowhere to carry this mark. Nothing was changed.")
		case iss.Changed == 0:
			notes = append(notes, "The tolerant columns hold no spare precision, so nothing was changed.")
		default:
			notes = append(notes, fmt.Sprintf("%s of %s cells changed (%.2f%%), at %s of %s. Largest change %s.",
				thousands(im.CellsChanged), thousands(im.CellsTotal), im.CellsPercent, depthName(par.LowBitDepth), tolerantList(sc), im.LargestShift))
		}
	}
	if tech.Allocate {
		notes = append(notes, fmt.Sprintf("%s withheld from this copy, about 1 in %s. Every remaining value is untouched, but the copy is no longer complete, which a licence promising the full set may not allow.",
			countNoun(im.RowsWithheld, "row", "rows"), thousands(par.AllocRate))+withheldShownNote(im))
	}
	if tech.Order {
		if im.PairsSwapped == 0 {
			notes = append(notes, "No pairs were reordered.")
		} else {
			notes = append(notes, fmt.Sprintf("%s reordered. No value, row or column differs from the source, only the sequence. Lost the moment anyone sorts the file.", countNoun(im.PairsSwapped, "adjacent pair", "adjacent pairs")))
		}
	}
	if tech.Format {
		if im.CellsReformat == 0 {
			notes = append(notes, "No cell could be rewritten: the table has no decimal values to pad.")
		} else {
			notes = append(notes, fmt.Sprintf("%s rewritten with one more decimal place. The numbers are identical, only their spelling differs. Lost as soon as any tool parses and re-saves the file.", countNoun(im.CellsReformat, "cell", "cells")))
		}
	}
	if tech.Noise {
		switch {
		case len(sc.Tolerant) == 0:
			notes = append(notes, "No column is marked tolerant, so there is no noise to carry a mark.")
		default:
			notes = append(notes, fmt.Sprintf("%s carry the mark in a perturbation of up to %d units of their last declared digit, in %s. A recipient never sees the unperturbed values, so this reads the same as an unmarked release. That holds only where it replaces the pipeline's own noise step; run after it, the two perturbations compound.",
				countNoun(iss.Noised, "cell", "cells"), par.NoiseSpan, tolerantList(sc)))
		}
	}
	if tech.Redact {
		names := sc.redactable(master)
		if len(names) == 0 {
			notes = append(notes, "No column is set to be redacted, so nothing was replaced. Set a column's role to redact in the table header.")
		} else {
			notes = append(notes, fmt.Sprintf("%s in %s replaced by a keyed pseudonym. Free only if you are redacting these columns anyway. The pseudonym differs per recipient, so two recipients comparing copies can see which column carries the mark and can strip it by renumbering, and neither can link a person across copies. Do not reuse the re-identification key for this.",
				countNoun(im.CellsRedacted, "value", "values"), joinAnd(names)))
		}
	}
	if tech.Dummy {
		if sc.Dummy == "" {
			notes = append(notes, "No column was added: the table already uses the reference column name.")
		} else {
			notes = append(notes, fmt.Sprintf("One column added, %q. Every existing value is untouched, and the column is trivially dropped.", sc.Dummy))
		}
	}
	if !tech.Canary && !tech.LowBit && !tech.Dummy && !tech.Allocate && !tech.Order && !tech.Format && !tech.Noise && !tech.Redact {
		notes = append(notes, "No measure selected. The copy is identical to the source, and only exact matching against the source could identify it.")
	}
	return notes
}

// thousands writes n with a comma between each group of three digits.
func thousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

// sayf is fmt.Sprintf for sentences a person reads: every %d is written
// with thousands separators, so counts read the same everywhere.
func sayf(format string, args ...any) string {
	var b strings.Builder
	argi := 0
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			b.WriteByte(format[i])
			continue
		}
		j := i + 1
		for j < len(format) && strings.IndexByte("+-# 0123456789.", format[j]) >= 0 {
			j++
		}
		if j >= len(format) {
			b.WriteString(format[i:])
			break
		}
		verb := format[i : j+1]
		switch {
		case verb == "%%":
			b.WriteByte('%')
		case verb == "%d" && argi < len(args):
			if n, ok := args[argi].(int); ok {
				b.WriteString(thousands(n))
			} else {
				fmt.Fprintf(&b, verb, args[argi])
			}
			argi++
		case argi < len(args):
			fmt.Fprintf(&b, verb, args[argi])
			argi++
		default:
			b.WriteString(verb)
		}
		i = j
	}
	return b.String()
}

// countNoun renders a count with the matching noun: "1 row", "2,000 rows".
func countNoun(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return thousands(n) + " " + many
}

// joinAnd lists names as "a", "a and b", "a, b and c".
func joinAnd(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// withheldShownNote says so when the illustrated row came from further down.
func withheldShownNote(im PreviewImpact) string {
	if im.RowsWithheld > 0 {
		return " One is shown above, struck through; it is not in the copy."
	}
	return ""
}

// depthName names the digit the mark is written into.
func depthName(depth int) string {
	switch depth {
	case 0:
		return "the last decimal place"
	case 1:
		return "one place before the last"
	default:
		return fmt.Sprintf("%d places before the last", depth)
	}
}

func tolerantList(sc Schema) string {
	var names []string
	for _, f := range sc.Tolerant {
		names = append(names, f.Name)
	}
	return strings.Join(names, ", ")
}

// tolerantField returns the tolerant field of that name, if it is one.
func (s Schema) tolerantField(name string) (Field, bool) {
	for _, f := range s.Tolerant {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// roleOf labels a column for the preview header.
func (s Schema) roleOf(name string) string {
	if name == s.Key {
		return "identifier"
	}
	for _, m := range s.Match {
		if m == name {
			return "match"
		}
	}
	if _, ok := s.tolerantField(name); ok {
		return "tolerant"
	}
	if s.redacted(name) {
		return "redact"
	}
	return ""
}

// numericShift is how far a value moved, for the largest-change figure.
func numericShift(before, after string) float64 {
	b, err1 := strconv.ParseFloat(strings.TrimSpace(before), 64)
	a, err2 := strconv.ParseFloat(strings.TrimSpace(after), 64)
	if err1 != nil || err2 != nil {
		return 0
	}
	if d := a - b; d > 0 {
		return d
	} else {
		return -d
	}
}
