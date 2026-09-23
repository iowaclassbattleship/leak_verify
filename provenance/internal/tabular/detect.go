package tabular

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"attribution/common/codec"
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
	// Candidates lists every copy a technique found rows of, when there is
	// more than one; on the verdict, the recipients of merged copies.
	Candidates []codec.Support `json:"candidates,omitempty"`
	ReadFrom   []string        `json:"readFrom,omitempty"` // verdict: techniques that agree
}

type Report struct {
	Rows         int      `json:"rows"`
	Columns      []string `json:"columns"`
	ResolvedRows int      `json:"resolvedRows"`
	// MatchesSource is set when every row is identical to the unmarked source.
	MatchesSource bool     `json:"matchesSource"`
	Results       []Result `json:"results"`
	Verdict       Result   `json:"verdict"`
}

// Registry is everything the issuer keeps: the key, the unmarked source and
// the issuance log with each recipient's regenerated copy.
type Registry struct {
	Key    codec.Key
	Master *Table
	Schema Schema
	Issued []*Issuance
	Copies map[uint16]*Table
}

func (r *Registry) label(id uint16) string {
	for _, is := range r.Issued {
		if is.MarkID == id {
			if is.Org == "" {
				return is.Recipient
			}
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
		r.detectAllocation(leaked, pks),
		r.detectLowBit(leaked, pks),
		r.detectNoise(leaked, pks),
		r.detectFormat(leaked, pks),
		r.detectOrdering(leaked, pks),
		r.detectRedaction(leaked, pks),
		r.detectDummy(leaked, pks),
	}
	rep.MatchesSource = len(leaked.Rows) > 0 && r.sourceRows(leaked) == len(leaked.Rows)
	rep.Verdict = verdict(rep.Results, rep.MatchesSource)
	return rep
}

// verdict pools the techniques. One recipient named by any technique is an
// attribution; several recipients named by techniques that each stand on
// their own is what merged or colluding copies look like, and is reported as
// such rather than as "inconclusive".
func verdict(results []Result, matchesSource bool) Result {
	v := Result{Technique: "verdict", Name: "Overall"}
	var claims []codec.Claim
	for _, res := range results {
		switch {
		case res.Status == "attributed":
			claims = append(claims, codec.Claim{Layer: res.Name, MarkID: res.MarkID, Recipient: res.Recipient, Strength: codec.Attributed})
		case len(res.Candidates) > 0:
			for _, c := range res.Candidates {
				claims = append(claims, codec.Claim{Layer: res.Name, MarkID: c.MarkID, Recipient: c.Recipient, Strength: codec.Attributed})
			}
		case res.Decision != nil:
			if best, ok := res.Decision.Leaning(); ok {
				claims = append(claims, codec.Claim{Layer: res.Name, MarkID: best.MarkID, Recipient: best.Recipient, Strength: codec.Leaning})
			}
		}
	}
	named := ""
	for _, c := range claims {
		if c.Strength == codec.Attributed {
			named = c.MarkID
			break
		}
	}
	groups, conflict := codec.Reconcile(named, true, claims)
	standing := 0
	for _, g := range groups {
		if g.Strength == codec.Attributed.String() {
			standing++
		}
	}
	switch {
	case conflict && standing >= 2:
		v.Status = "merged"
		v.Candidates = nil
		var parts []string
		for _, g := range groups {
			if g.Strength == codec.Attributed.String() {
				v.Candidates = append(v.Candidates, g)
				parts = append(parts, fmt.Sprintf("%s (%s)", g.MarkID, g.Recipient))
			}
		}
		v.Detail = "Rows from several recipients' copies are present: " + strings.Join(parts, ", ") + ". The copies were merged, or the recipients shared data with each other."
	case standing == 1:
		g := groups[0]
		v.Status, v.MarkID, v.Recipient = "attributed", g.MarkID, g.Recipient
		v.ReadFrom = codec.Agreeing(g.MarkID, groups)
		v.Detail = "Identified by " + strings.Join(v.ReadFrom, ", ") + "."
	case matchesSource:
		v.Status = "absent"
		v.Detail = "Every row is identical to the unmarked source, so this is the source itself or a part of it, not an issued copy."
	default:
		v.Status = "absent"
		v.Detail = "No mark survives. Either this data was not issued here, or every layer was destroyed."
		for _, res := range results {
			if res.Status == "inconclusive" {
				v.Status = "inconclusive"
				v.Detail = "No technique could identify a recipient."
			}
		}
	}
	return v
}

// sourceRows counts the leaked rows identical to a row of the unmarked
// source, on the columns the file has.
func (r *Registry) sourceRows(leaked *Table) int {
	idx := make([]int, len(leaked.Columns))
	for i, c := range leaked.Columns {
		if idx[i] = r.Master.Col(c); idx[i] < 0 {
			return 0
		}
	}
	all := make([]int, len(leaked.Columns))
	for i := range all {
		all[i] = i
	}
	set := make(map[[32]byte]struct{}, len(r.Master.Rows))
	for _, row := range r.Master.Rows {
		set[rowHash(row, idx)] = struct{}{}
	}
	n := 0
	for _, row := range leaked.Rows {
		if _, ok := set[rowHash(row, all)]; ok {
			n++
		}
	}
	return n
}

// resolveKeys maps each leaked row back to the value its marks were derived
// from: the identifying column if it survived, otherwise another unique column
// or the columns that marking never alters.
func (r *Registry) resolveKeys(leaked *Table) []string {
	m, sc := r.Master, r.Schema
	out := make([]string, len(leaked.Rows))

	// With no identifying column the key is derived from the row itself, so no
	// lookup against the source is needed: it works as long as the columns
	// marking leaves alone are still present.
	if sc.Key == "" {
		ok := true
		for _, name := range sc.stable(m) {
			if leaked.Col(name) < 0 {
				ok = false
			}
		}
		if ok {
			for i, row := range leaked.Rows {
				out[i] = sc.rowKey(leaked, row)
			}
			return out
		}
	}
	keyOf := make([]string, len(m.Rows))
	for i, row := range m.Rows {
		keyOf[i] = sc.rowKey(m, row)
	}

	// Direct: the identifying column is still present.
	if c := leaked.Col(sc.Key); sc.Key != "" && c >= 0 {
		known := map[string]bool{}
		for _, k := range keyOf {
			known[k] = true
		}
		for i, row := range leaked.Rows {
			if known[row[c]] {
				out[i] = row[c]
			}
		}
	}

	// Fallback: another unique column, then the stable columns as a whole.
	lookups := make([]map[string]string, 0, len(sc.Match)+1)
	cols := make([][2]int, 0, len(sc.Match)+1) // master column, leaked column
	for _, name := range sc.Match {
		mc, lc := m.Col(name), leaked.Col(name)
		if mc >= 0 && lc >= 0 {
			cols = append(cols, [2]int{mc, lc})
			lookups = append(lookups, map[string]string{})
		}
	}
	var stable []string
	for _, name := range sc.stable(m) {
		if leaked.Col(name) >= 0 {
			stable = append(stable, name)
		}
	}
	var stableLookup map[string]string
	if len(stable) > 0 {
		stableLookup = map[string]string{}
	}
	for i, row := range m.Rows {
		for j, cc := range cols {
			v := row[cc[0]]
			if _, dup := lookups[j][v]; dup {
				lookups[j][v] = "" // ambiguous
			} else {
				lookups[j][v] = keyOf[i]
			}
		}
		if stableLookup != nil {
			h := project(row, m, stable)
			if _, dup := stableLookup[h]; dup {
				stableLookup[h] = ""
			} else {
				stableLookup[h] = keyOf[i]
			}
		}
	}
	for i, row := range leaked.Rows {
		if out[i] != "" {
			continue
		}
		for j, cc := range cols {
			if k := lookups[j][row[cc[1]]]; k != "" {
				out[i] = k
				break
			}
		}
		if out[i] == "" && stableLookup != nil {
			out[i] = stableLookup[project(row, leaked, stable)]
		}
	}
	return out
}

// project joins the named columns of a row into a comparable string.
func project(row []string, t *Table, names []string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = row[t.Col(n)]
	}
	return strings.Join(parts, "\x00")
}

// plural renders a count with the matching noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return thousands(n) + " " + many
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
	switch {
	case master == n && n > 0:
		res.Status = "absent"
		res.Detail = fmt.Sprintf("Every one of the %s is identical to the unmarked source, and no row is unique to an issued copy.", countNoun(n, "row", "rows"))
		return res
	case len(hits) == 0 || hits[0].n == 0:
		res.Status = "absent"
		res.Detail = fmt.Sprintf("No row is identical to an issued copy, and %s of %s match the unmarked source.", thousands(master), countNoun(n, "row", "rows"))
		if master < n {
			res.Detail += " The rest were changed, so see the fuzzy stages."
		}
		return res
	}
	second := 0
	if len(hits) > 1 {
		second = hits[1].n
	}
	best := hits[0]
	res.Detail = fmt.Sprintf("%s of %s are identical to the copy issued as %s. Next best copy: %s. Unmarked source: %s.",
		thousands(best.n), countNoun(n, "row", "rows"), codec.FormatID(best.id), countNoun(second, "row", "rows"), countNoun(master, "row", "rows"))
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
	sc := r.Master
	// Identify canaries by any column that identifies a row, and by the whole
	// set of columns marking leaves alone.
	var probes []string
	for _, name := range r.Schema.identifying(r.Master) {
		if leaked.Col(name) >= 0 {
			probes = append(probes, name)
		}
	}
	var stable []string
	for _, name := range r.Schema.stable(r.Master) {
		if leaked.Col(name) >= 0 {
			stable = append(stable, name)
		}
	}
	type ref struct {
		id    uint16
		index int
	}
	byKey := map[string]ref{}
	ambiguous := map[string]bool{}
	add := func(k string, rf ref) {
		if prev, dup := byKey[k]; dup && prev.id != rf.id {
			ambiguous[k] = true
		}
		byKey[k] = rf
	}
	realRows := map[string]bool{}
	if len(stable) > 0 {
		for _, row := range sc.Rows {
			realRows[project(row, sc, stable)] = true
		}
	}
	total := 0
	for _, is := range r.Issued {
		if !is.Techniques.Canary {
			continue
		}
		for i, c := range is.Canaries {
			total++
			rf := ref{is.MarkID, i}
			for _, name := range probes {
				add(name+"="+c.Row[sc.Col(name)], rf)
			}
			if h := project(c.Row, sc, stable); len(stable) > 0 && !realRows[h] {
				add("row="+h, rf)
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
	if len(probes) == 0 && len(stable) == 0 {
		res.Status = "absent"
		res.Detail = "No columns are left to recognise canary rows."
		return res
	}
	found := map[uint16]map[int]bool{}
	for _, row := range leaked.Rows {
		var keys []string
		for _, name := range probes {
			keys = append(keys, name+"="+row[leaked.Col(name)])
		}
		if len(stable) > 0 {
			keys = append(keys, "row="+project(row, leaked, stable))
		}
		for _, k := range keys {
			if rf, ok := byKey[k]; ok {
				if found[rf.id] == nil {
					found[rf.id] = map[int]bool{}
				}
				found[rf.id][rf.index] = true
				break
			}
		}
	}
	switch len(found) {
	case 0:
		res.Status = "absent"
		res.Detail = sayf("None of the %d issued canary rows appear in these %d rows.", total, len(leaked.Rows))
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
			res.Confidence = sayf("%d of %d canary rows present", len(hits), per)
			res.Detail = fmt.Sprintf("Found %s issued only to %s. A small sample may contain none.", plural(len(hits), "canary row", "canary rows"), codec.FormatID(id))
		}
	default:
		// Each canary row was issued to one recipient only, so every one found
		// names its recipient on its own: several means merged copies.
		res.Status = "inconclusive"
		var parts []string
		ids := make([]uint16, 0, len(found))
		for id := range found {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			return len(found[ids[i]]) > len(found[ids[j]]) || len(found[ids[i]]) == len(found[ids[j]]) && ids[i] < ids[j]
		})
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf("%s, %s (%s)", codec.FormatID(id), r.label(id), countNoun(len(found[id]), "row", "rows")))
			res.Candidates = append(res.Candidates, codec.Support{MarkID: codec.FormatID(id), Recipient: r.label(id), Layers: []string{res.Name}, Strength: codec.Attributed.String()})
		}
		res.Detail = "Canary rows from several recipients are present, which means merged copies: " + strings.Join(parts, "; ") + "."
	}
	return res
}

func (r *Registry) candidates(pred func(*Issuance) bool) []codec.Candidate {
	var c []codec.Candidate
	for _, is := range r.Issued {
		if pred(is) {
			c = append(c, codec.Candidate{ID: is.MarkID, Label: r.label(is.MarkID)})
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
	for _, f := range r.Schema.Tolerant {
		c := leaked.Col(f.Name)
		if c < 0 {
			continue
		}
		present++
		for i, row := range leaked.Rows {
			if pks[i] == "" {
				continue
			}
			df, deep := shallower(f, r.params().LowBitDepth)
			if !deep {
				continue
			}
			sel, idx, mask := lowBitSlot(r.Key, pks[i], f.Name, r.params().LowBitGamma)
			if !sel {
				continue
			}
			bit, ok := readParity(df, row[c])
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
		res.Detail = sayf("%d marked cells lost the precision that carried the mark.", lostPrecision)
	case cells == 0:
		res.Detail = "No rows could be matched against the source, so marked cells cannot be located."
	default:
		inLog := "not in the issuance log"
		if d.DecodedInLog {
			inLog = "in the issuance log"
		}
		res.Detail = sayf("Read %d marked cells. Recovered %d of %d bits, decoded %s (%s), %d bit errors against the closest copy.",
			cells, d.BitsObserved, codec.CodeLen, d.DecodedID, inLog, d.BitErrors)
		if lostPrecision > 0 {
			res.Detail += sayf(" %d cells had their precision removed.", lostPrecision)
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
	c := leaked.Col(r.Schema.Dummy)
	if r.Schema.Dummy == "" || c < 0 {
		res.Status = "absent"
		res.Detail = "The dummy column is not present."
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
		res.Detail = sayf("Column %s is present, but no value verifies against the key across %d matched rows.", r.Schema.Dummy, readable)
		return res
	}
	res.Detail = sayf("%d of %d matched rows carry a valid code, %d of them decode to %s.", valid, readable, bestN, codec.FormatID(best))
	if lbl := r.label(best); lbl != "" && bestN >= 3 && bestN*10 >= valid*6 {
		res.Status = "attributed"
		res.MarkID = codec.FormatID(best)
		res.Recipient = lbl
		res.Confidence = sayf("%d rows agree, each with a 1-in-100 check value", bestN)
	} else {
		res.Status = "inconclusive"
	}
	return res
}
