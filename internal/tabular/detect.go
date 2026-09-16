package tabular

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"custodial/internal/codec"
)

type Result struct {
	Technique  string          `json:"technique"`
	Name       string          `json:"name"`
	Stage      string          `json:"stage"`
	Status     string          `json:"status"` // attributed | inconclusive | absent
	MarkID     string          `json:"markId,omitempty"`
	Recipient  string          `json:"recipient,omitempty"`
	Confidence string          `json:"confidence"`
	Detail     string          `json:"detail"`
	Decision   *codec.Decision `json:"decision,omitempty"`
}

type Report struct {
	Rows         int      `json:"rows"`
	Columns      []string `json:"columns"`
	ResolvedRows int      `json:"resolvedRows"`
	Results      []Result `json:"results"`
	Verdict      Result   `json:"verdict"`
}

// Registry is everything the issuer keeps: the key, the unmarked source and
// the issuance log with each recipient's regenerated copy.
type Registry struct {
	Key    codec.Key
	Master *Table
	Issued []*Issuance
	Copies map[uint16]*Table
}

func (r *Registry) label(id uint16) string {
	for _, is := range r.Issued {
		if is.MarkID == id {
			return is.Recipient + " (" + is.Org + ")"
		}
	}
	return ""
}

func (r *Registry) Detect(leaked *Table) Report {
	rep := Report{Rows: len(leaked.Rows), Columns: leaked.Columns}
	pks := r.resolveKeys(leaked)
	for _, pk := range pks {
		if pk != "" {
			rep.ResolvedRows++
		}
	}
	rep.Results = []Result{
		r.detectExact(leaked),
		r.detectCanaries(leaked),
		r.detectLowBit(leaked, pks),
		r.detectDummy(leaked, pks),
	}
	rep.Verdict = verdict(rep.Results)
	return rep
}

func verdict(results []Result) Result {
	v := Result{Technique: "verdict", Name: "Overall"}
	who := map[string][]string{}
	var order []string
	for _, res := range results {
		if res.Status == "attributed" {
			if _, seen := who[res.Recipient]; !seen {
				order = append(order, res.Recipient)
			}
			who[res.Recipient] = append(who[res.Recipient], res.Name)
		}
	}
	switch len(order) {
	case 0:
		v.Status = "inconclusive"
		v.Detail = "No technique could identify a recipient."
		for _, res := range results {
			if res.Status == "inconclusive" {
				return v
			}
		}
		v.Status = "absent"
		v.Detail = "No mark survives. Either this data was not issued here, or every layer was destroyed."
	case 1:
		v.Status = "attributed"
		v.Recipient = order[0]
		for _, res := range results {
			if res.Recipient == order[0] {
				v.MarkID = res.MarkID
				break
			}
		}
		v.Detail = "Identified by " + strings.Join(who[order[0]], ", ") + "."
	default:
		v.Status = "inconclusive"
		var parts []string
		for _, o := range order {
			parts = append(parts, o+": "+strings.Join(who[o], ", "))
		}
		v.Detail = "Techniques disagree, which suggests several copies were merged: " + strings.Join(parts, "; ")
	}
	return v
}

// resolveKeys maps each leaked row back to a source primary key, using
// account_id if present, else re-identifying the row against the master by
// email or by (full_name, city).
func (r *Registry) resolveKeys(leaked *Table) []string {
	m := r.Master
	mPK, mEmail, mName, mCity := m.Col("account_id"), m.Col("email"), m.Col("full_name"), m.Col("city")
	ids := map[string]bool{}
	byEmail := map[string]string{}
	byNameCity := map[string]string{}
	for _, row := range m.Rows {
		ids[row[mPK]] = true
		byEmail[strings.ToLower(row[mEmail])] = row[mPK]
		k := row[mName] + "|" + row[mCity]
		if _, dup := byNameCity[k]; dup {
			byNameCity[k] = "" // ambiguous
		} else {
			byNameCity[k] = row[mPK]
		}
	}
	lPK, lEmail, lName, lCity := leaked.Col("account_id"), leaked.Col("email"), leaked.Col("full_name"), leaked.Col("city")
	out := make([]string, len(leaked.Rows))
	for i, row := range leaked.Rows {
		switch {
		case lPK >= 0 && ids[row[lPK]]:
			out[i] = row[lPK]
		case lEmail >= 0 && byEmail[strings.ToLower(row[lEmail])] != "":
			out[i] = byEmail[strings.ToLower(row[lEmail])]
		case lName >= 0 && lCity >= 0:
			out[i] = byNameCity[row[lName]+"|"+row[lCity]]
		}
	}
	return out
}

// plural renders a count with the matching noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func rowHash(row []string, idx []int) [32]byte {
	h := sha256.New()
	for _, i := range idx {
		if i < 0 {
			h.Write([]byte{1})
		} else {
			h.Write([]byte(row[i]))
		}
		h.Write([]byte{0})
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// detectExact is stage 1: hash-fingerprint every leaked row and look for
// issued copies containing identical rows (projected onto the leaked columns).
func (r *Registry) detectExact(leaked *Table) Result {
	res := Result{Technique: "exact", Name: "Exact fingerprint", Stage: "Exact matching"}
	leakedIdx := make([]int, len(leaked.Columns))
	for i := range leakedIdx {
		leakedIdx[i] = i
	}
	leakHashes := make([][32]byte, len(leaked.Rows))
	for i, row := range leaked.Rows {
		leakHashes[i] = rowHash(row, leakedIdx)
	}
	count := func(t *Table) int {
		idx := make([]int, len(leaked.Columns))
		for i, c := range leaked.Columns {
			if idx[i] = t.Col(c); idx[i] < 0 {
				return 0 // copy never had this column
			}
		}
		set := make(map[[32]byte]struct{}, len(t.Rows))
		for _, row := range t.Rows {
			set[rowHash(row, idx)] = struct{}{}
		}
		n := 0
		for _, h := range leakHashes {
			if _, ok := set[h]; ok {
				n++
			}
		}
		return n
	}
	type hit struct {
		id uint16
		n  int
	}
	var hits []hit
	for _, is := range r.Issued {
		hits = append(hits, hit{is.MarkID, count(r.Copies[is.MarkID])})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].n > hits[j].n })
	n := len(leaked.Rows)
	master := count(r.Master)
	if len(hits) == 0 || hits[0].n == 0 {
		res.Status = "absent"
		res.Detail = fmt.Sprintf("No row is identical to an issued copy, and %d match the unmarked source. The data was changed, so see the fuzzy stages.", master)
		return res
	}
	second := 0
	if len(hits) > 1 {
		second = hits[1].n
	}
	best := hits[0]
	res.Detail = fmt.Sprintf("%d of %d rows are identical to the copy issued as %s. Next best copy: %d rows. Unmarked source: %d rows.", best.n, n, codec.FormatID(best.id), second, master)
	if best.n*2 >= n && best.n-second >= max(3, n/20) {
		res.Status = "attributed"
		res.MarkID = codec.FormatID(best.id)
		res.Recipient = r.label(best.id)
		res.Confidence = fmt.Sprintf("%.0f%% of rows match this copy alone", 100*float64(best.n-second)/float64(n))
	} else {
		res.Status = "inconclusive"
		if best.n*2 >= n {
			res.Detail += " The rows match issued content but are identical across copies, so the distinguishing cells are gone."
		}
	}
	return res
}

func (r *Registry) detectCanaries(leaked *Table) Result {
	res := Result{Technique: "canary", Name: "Canary rows", Stage: "Exact matching"}
	type ref struct {
		id    uint16
		index int
	}
	byKey := map[string]ref{}
	ambiguous := map[string]bool{}
	total := 0
	for _, is := range r.Issued {
		if !is.Techniques.Canary {
			continue
		}
		for i, c := range is.Canaries {
			total++
			ref := ref{is.MarkID, i}
			for _, k := range []string{"id:" + c.AccountID, "em:" + strings.ToLower(c.Email), "nc:" + c.Name + "|" + c.City + "|" + c.Balance} {
				if prev, dup := byKey[k]; dup && prev.id != ref.id {
					ambiguous[k] = true // two recipients drew the same synthetic identity
				}
				byKey[k] = ref
			}
		}
	}
	for k := range ambiguous {
		delete(byKey, k)
	}
	if total == 0 {
		res.Status = "absent"
		res.Detail = "No recipient was issued canary rows."
		return res
	}
	lPK, lEmail, lName, lCity, lBal := leaked.Col("account_id"), leaked.Col("email"), leaked.Col("full_name"), leaked.Col("city"), leaked.Col("balance_eur")
	found := map[uint16]map[int]bool{}
	for _, row := range leaked.Rows {
		var keys []string
		if lPK >= 0 {
			keys = append(keys, "id:"+row[lPK])
		}
		if lEmail >= 0 {
			keys = append(keys, "em:"+strings.ToLower(row[lEmail]))
		}
		if lName >= 0 && lCity >= 0 && lBal >= 0 {
			keys = append(keys, "nc:"+row[lName]+"|"+row[lCity]+"|"+row[lBal])
		}
		for _, k := range keys {
			if ref, ok := byKey[k]; ok {
				if found[ref.id] == nil {
					found[ref.id] = map[int]bool{}
				}
				found[ref.id][ref.index] = true
				break
			}
		}
	}
	switch len(found) {
	case 0:
		res.Status = "absent"
		res.Detail = fmt.Sprintf("None of the %d issued canary rows appear in these %d rows.", total, len(leaked.Rows))
		if lPK < 0 && lEmail < 0 && (lName < 0 || lCity < 0 || lBal < 0) {
			res.Detail = "No identifying columns are left to recognise canary rows."
		}
	case 1:
		for id, hits := range found {
			per := 0
			for _, is := range r.Issued {
				if is.MarkID == id {
					per = len(is.Canaries)
				}
			}
			res.Status = "attributed"
			res.MarkID = codec.FormatID(id)
			res.Recipient = r.label(id)
			res.Confidence = fmt.Sprintf("%d of %d canary rows present", len(hits), per)
			res.Detail = fmt.Sprintf("Found %s issued only to %s. A small sample may contain none.", plural(len(hits), "canary row", "canary rows"), codec.FormatID(id))
		}
	default:
		res.Status = "inconclusive"
		var parts []string
		for id, hits := range found {
			parts = append(parts, fmt.Sprintf("%s (%d rows)", codec.FormatID(id), len(hits)))
		}
		sort.Strings(parts)
		res.Detail = "Canary rows from several recipients are present, which suggests merged copies: " + strings.Join(parts, ", ")
	}
	return res
}

func (r *Registry) candidates(pred func(*Issuance) bool) []codec.Candidate {
	var c []codec.Candidate
	for _, is := range r.Issued {
		if pred(is) {
			c = append(c, codec.Candidate{ID: is.MarkID, Label: is.Recipient + " (" + is.Org + ")"})
		}
	}
	return c
}

// detectLowBit is the fuzzy stage: read the parity of every key-selected
// tolerant cell, majority-vote each codeword bit, then ECC-decode.
func (r *Registry) detectLowBit(leaked *Table, pks []string) Result {
	res := Result{Technique: "lowbit", Name: "Low-order-bit mark", Stage: "Fuzzy matching"}
	var votes codec.Votes
	present, cells, lostPrecision := 0, 0, 0
	for _, f := range Tolerant {
		c := leaked.Col(f.Name)
		if c < 0 {
			continue
		}
		present++
		for i, row := range leaked.Rows {
			if pks[i] == "" {
				continue
			}
			sel, idx, mask := lowBitSlot(r.Key, pks[i], f.Name)
			if !sel {
				continue
			}
			bit, ok := readParity(f, row[c])
			if !ok {
				lostPrecision++
				continue
			}
			cells++
			votes.Add(idx, bit^mask, 1)
		}
	}
	d := r.Key.Decode(votes, r.candidates(func(is *Issuance) bool { return is.Techniques.LowBit }))
	res.Decision = &d
	res.Status = d.Status
	switch {
	case present == 0:
		res.Detail = "All tolerant columns were dropped, so there is nothing to read."
	case cells == 0 && lostPrecision > 0:
		res.Detail = fmt.Sprintf("%d marked cells lost the precision that carried the mark.", lostPrecision)
	case cells == 0:
		res.Detail = "No rows could be matched against the source, so marked cells cannot be located."
	default:
		inLog := "not in the issuance log"
		if d.DecodedInLog {
			inLog = "in the issuance log"
		}
		res.Detail = fmt.Sprintf("Read %d marked cells. Recovered %d of %d bits, decoded %s (%s), %d bit errors against the closest copy.",
			cells, d.BitsObserved, codec.CodeLen, d.DecodedID, inLog, d.BitErrors)
		if lostPrecision > 0 {
			res.Detail += fmt.Sprintf(" %d cells had their precision removed.", lostPrecision)
		}
		res.Confidence = codec.FormatProb(d.FalseProb)
	}
	if d.Status == "attributed" {
		res.MarkID, res.Recipient = d.MarkID, d.Recipient
	}
	return res
}

func (r *Registry) detectDummy(leaked *Table, pks []string) Result {
	res := Result{Technique: "dummy", Name: "Dummy column", Stage: "Fuzzy matching"}
	c := leaked.Col(DummyColumn)
	if c < 0 {
		res.Status = "absent"
		res.Detail = "Column " + DummyColumn + " is not present."
		return res
	}
	counts := map[uint16]int{}
	valid, readable := 0, 0
	for i, row := range leaked.Rows {
		if pks[i] == "" {
			continue
		}
		readable++
		if id, ok := decodeDummy(r.Key, pks[i], row[c]); ok {
			counts[id]++
			valid++
		}
	}
	var best uint16
	bestN := 0
	for id, n := range counts {
		if n > bestN {
			best, bestN = id, n
		}
	}
	if valid == 0 {
		res.Status = "absent"
		res.Detail = fmt.Sprintf("Column %s is present, but no value verifies against the key across %d matched rows.", DummyColumn, readable)
		return res
	}
	res.Detail = fmt.Sprintf("%d of %d matched rows carry a valid code, %d of them decode to %s.", valid, readable, bestN, codec.FormatID(best))
	if lbl := r.label(best); lbl != "" && bestN >= 3 && bestN*10 >= valid*6 {
		res.Status = "attributed"
		res.MarkID = codec.FormatID(best)
		res.Recipient = lbl
		res.Confidence = fmt.Sprintf("%d rows agree, each with a 1-in-100 check value", bestN)
	} else {
		res.Status = "inconclusive"
	}
	return res
}
