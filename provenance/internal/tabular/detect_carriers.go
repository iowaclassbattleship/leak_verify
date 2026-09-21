package tabular

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"attribution/common/codec"
)

// Readers for the four carriers that do not write into a value's digits.
// Each needs the source row a leaked row came from, which resolveKeys has
// already worked out, so all four survive the file being cut down or shuffled
// to the same degree that row matching does.

// masterRows indexes the source by row key, so a leaked row can be compared
// with the row it came from.
func (r *Registry) masterRows() map[string]int {
	idx := make(map[string]int, len(r.Master.Rows))
	for i, row := range r.Master.Rows {
		idx[r.Schema.rowKey(r.Master, row)] = i
	}
	return idx
}

// detectAllocation asks which copy's withheld rows are missing. A recipient's
// own copy never contains the rows withheld from it, while every other copy
// contains its share of them.
func (r *Registry) detectAllocation(leaked *Table, pks []string) Result {
	res := Result{Technique: "allocation", Name: "Allocation", Stage: "Exact matching"}
	seen := map[string]bool{}
	for _, pk := range pks {
		if pk != "" {
			seen[pk] = true
		}
	}
	if len(seen) == 0 {
		res.Status = "absent"
		res.Detail = "No rows could be matched against the source, so the withheld rows cannot be looked up."
		return res
	}

	type score struct {
		id   uint16
		hits int
	}
	var scores []score
	expect := 0.0
	for _, is := range r.Issued {
		if !is.Techniques.Allocate {
			continue
		}
		rate := is.Techniques.Params.WithDefaults().AllocRate
		expect = float64(len(seen)) / float64(rate)
		hits := 0
		for pk := range seen {
			if withheld(r.Key, pk, is.MarkID, rate) {
				hits++
			}
		}
		scores = append(scores, score{is.MarkID, hits})
	}
	if len(scores) == 0 {
		res.Status = "absent"
		res.Detail = "No recipient was issued a withheld slice."
		return res
	}
	// Fewer than three expected hits cannot separate one copy from another.
	if expect < 3 {
		res.Status = "inconclusive"
		res.Detail = fmt.Sprintf("Only %d rows matched, so a wrong copy would be expected to show about %.1f withheld rows. That is too few to tell copies apart.", len(seen), expect)
		return res
	}
	best, second := score{hits: 1 << 30}, score{hits: 1 << 30}
	for _, s := range scores {
		if s.hits < best.hits {
			best, second = s, best
		} else if s.hits < second.hits {
			second = s
		}
	}
	res.Detail = fmt.Sprintf("%d of %d matched rows belong to the slice withheld from %s, against about %.0f expected for any other copy.",
		best.hits, len(seen), codec.FormatID(best.id), expect)
	if float64(best.hits) < expect/3 && (len(scores) == 1 || second.hits > best.hits) {
		res.Status = "attributed"
		res.MarkID = codec.FormatID(best.id)
		res.Recipient = r.label(best.id)
		res.Confidence = fmt.Sprintf("%d withheld rows present, %.0f expected if this were another copy", best.hits, expect)
	} else {
		res.Status = "inconclusive"
		res.Detail += " No copy stands out."
	}
	return res
}

// detectOrdering reads the swaps. Only pairs that are still neighbours and
// still come from a marked pair of the source can be read, so any resorting
// or heavy sampling erases this carrier.
func (r *Registry) detectOrdering(leaked *Table, pks []string) Result {
	res := Result{Technique: "ordering", Name: "Tuple ordering", Stage: "Fuzzy matching"}
	idx := r.masterRows()
	par := r.params()
	var votes codec.Votes
	pairs := 0
	for i := 0; i+1 < len(leaked.Rows); i++ {
		a, aok := idx[pks[i]]
		b, bok := idx[pks[i+1]]
		if pks[i] == "" || pks[i+1] == "" || !aok || !bok {
			continue
		}
		lo, bit := a, uint8(0)
		if b < a {
			lo, bit = b, 1
		}
		// Only pairs the marker could have used: adjacent in the source, and
		// starting on an even row.
		if abs(a-b) != 1 || lo%2 != 0 {
			continue
		}
		sel, slot, mask := orderSlot(r.Key, r.Schema.rowKey(r.Master, r.Master.Rows[lo]), par.OrderGamma)
		if !sel {
			continue
		}
		pairs++
		votes.Add(slot, bit^mask, 1)
		i++ // a row belongs to one pair only
	}
	d := r.Key.Decode(votes, r.candidates(func(is *Issuance) bool { return is.Techniques.Order }))
	res.Decision = &d
	res.Status = d.Status
	if pairs == 0 {
		res.Status = "absent"
		res.Detail = "No pair of rows is still next to the row it was issued beside, so the order carries nothing."
		return res
	}
	res.Detail = fmt.Sprintf("Read %d pairs still in their issued positions. Recovered %d of %d bits, decoded %s, %d bit errors.",
		pairs, d.BitsObserved, codec.CodeLen, d.DecodedID, d.BitErrors)
	res.Confidence = codec.FormatProb(d.FalseProb)
	if d.Status == "attributed" {
		res.MarkID, res.Recipient = d.MarkID, d.Recipient
	}
	return res
}

// detectNoise reads the direction each value was nudged in. The size of the
// nudge is keyed jitter, so only the sign carries the bit, and it is read by
// comparing with the source rather than from the value alone.
func (r *Registry) detectNoise(leaked *Table, pks []string) Result {
	res := Result{Technique: "noise", Name: "Noise as carrier", Stage: "Fuzzy matching"}
	idx := r.masterRows()
	par := r.params()
	var votes codec.Votes
	cells, lost := 0, 0
	for _, f := range r.Schema.Tolerant {
		c := leaked.Col(f.Name)
		sc := r.Master.Col(f.Name)
		if c < 0 || sc < 0 || f.Timestamp || f.Decimals <= 0 {
			continue
		}
		for i, row := range leaked.Rows {
			m, ok := idx[pks[i]]
			if pks[i] == "" || !ok {
				continue
			}
			sel, slot, mask, _ := noiseSlot(r.Key, pks[i], f.Name, par.NoiseRate, par.NoiseSpan)
			if !sel {
				continue
			}
			steps, ok := stepsBetween(f, r.Master.Rows[m][sc], row[c])
			if !ok || steps == 0 {
				lost++
				continue
			}
			// A nudge larger than the carrier uses is somebody else's edit.
			if steps > par.NoiseSpan+1 || steps < -(par.NoiseSpan+1) {
				lost++
				continue
			}
			bit := uint8(0)
			if steps > 0 {
				bit = 1
			}
			cells++
			votes.Add(slot, bit^mask, 1)
		}
	}
	d := r.Key.Decode(votes, r.candidates(func(is *Issuance) bool { return is.Techniques.Noise }))
	res.Decision = &d
	res.Status = d.Status
	if cells == 0 {
		res.Status = "absent"
		res.Detail = fmt.Sprintf("No value still sits within a nudge of the source (%d checked), so the direction cannot be read.", lost)
		return res
	}
	res.Detail = fmt.Sprintf("Read the direction of %d nudges against the source. Recovered %d of %d bits, decoded %s, %d bit errors.",
		cells, d.BitsObserved, codec.CodeLen, d.DecodedID, d.BitErrors)
	if lost > 0 {
		res.Detail += fmt.Sprintf(" %d values moved too far to read.", lost)
	}
	res.Confidence = codec.FormatProb(d.FalseProb)
	if d.Status == "attributed" {
		res.MarkID, res.Recipient = d.MarkID, d.Recipient
	}
	return res
}

// stepsBetween is how many units of the last declared digit separate two
// values of the same field.
func stepsBetween(f Field, before, after string) (int, bool) {
	a, err1 := strconv.ParseFloat(strings.TrimSpace(before), 64)
	b, err2 := strconv.ParseFloat(strings.TrimSpace(after), 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return int(math.Round((b - a) * math.Pow10(f.Decimals))), true
}

// detectFormat compares how each number is written with how the source wrote
// it. One extra trailing zero is a one bit.
func (r *Registry) detectFormat(leaked *Table, pks []string) Result {
	res := Result{Technique: "format", Name: "Free choices", Stage: "Fuzzy matching"}
	idx := r.masterRows()
	par := r.params()
	var votes codec.Votes
	cells := 0
	for i, row := range leaked.Rows {
		m, ok := idx[pks[i]]
		if pks[i] == "" || !ok {
			continue
		}
		src := r.Master.Rows[m]
		for c, name := range leaked.Columns {
			sc := r.Master.Col(name)
			if sc < 0 || name == r.Schema.Key {
				continue
			}
			sel, slot, mask := formatSlot(r.Key, pks[i], name, par.FormatRate)
			if !sel {
				continue
			}
			padded, ok := padDecimal(src[sc])
			if !ok {
				continue // the source value could not carry a zero anyway
			}
			var bit uint8
			switch row[c] {
			case padded:
				bit = 1
			case src[sc]:
				bit = 0
			default:
				continue // the value itself changed, so its spelling says nothing
			}
			cells++
			votes.Add(slot, bit^mask, 1)
		}
	}
	d := r.Key.Decode(votes, r.candidates(func(is *Issuance) bool { return is.Techniques.Format }))
	res.Decision = &d
	res.Status = d.Status
	if cells == 0 {
		res.Status = "absent"
		res.Detail = "No value is still written the way it was issued, so this carrier was lost."
		return res
	}
	res.Detail = fmt.Sprintf("Compared %d values with the way the source writes them. Recovered %d of %d bits, decoded %s, %d bit errors.",
		cells, d.BitsObserved, codec.CodeLen, d.DecodedID, d.BitErrors)
	res.Confidence = codec.FormatProb(d.FalseProb)
	if d.Status == "attributed" {
		res.MarkID, res.Recipient = d.MarkID, d.Recipient
	}
	return res
}

// detectRedaction decodes the recipient out of each pseudonym. The pseudonym
// is derived from the value it replaced, which the source still holds, so it
// can be decoded rather than merely compared.
func (r *Registry) detectRedaction(leaked *Table, pks []string) Result {
	res := Result{Technique: "redaction", Name: "Redaction", Stage: "Fuzzy matching"}
	names := r.Schema.redactable(r.Master)
	if len(names) == 0 {
		res.Status = "absent"
		res.Detail = "The source has no column that reads as personal text."
		return res
	}
	idx := r.masterRows()
	counts := map[uint16]int{}
	readable, valid := 0, 0
	for i, row := range leaked.Rows {
		m, ok := idx[pks[i]]
		if pks[i] == "" || !ok {
			continue
		}
		src := r.Master.Rows[m]
		for _, name := range names {
			c, sc := leaked.Col(name), r.Master.Col(name)
			if c < 0 || sc < 0 {
				continue
			}
			readable++
			if id, ok := decodeRedaction(r.Key, src[sc], row[c]); ok {
				valid++
				counts[id]++
			}
		}
	}
	if readable == 0 {
		res.Status = "absent"
		res.Detail = "No rows could be matched against the source, so the pseudonyms cannot be checked."
		return res
	}
	if valid == 0 {
		res.Status = "absent"
		res.Detail = fmt.Sprintf("None of the %d values checked is a pseudonym issued from this source.", readable)
		return res
	}
	var best uint16
	bestN := 0
	for id, n := range counts {
		if n > bestN {
			best, bestN = id, n
		}
	}
	res.Detail = fmt.Sprintf("%d of %d values carry a valid pseudonym, %d of them issued to %s.", valid, readable, bestN, codec.FormatID(best))
	if bestN >= 3 && bestN*10 >= valid*6 {
		res.Status = "attributed"
		res.MarkID = codec.FormatID(best)
		res.Recipient = r.label(best)
		res.Confidence = fmt.Sprintf("%d values agree, each with a 1-in-100 check value", bestN)
	} else {
		res.Status = "inconclusive"
	}
	return res
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
