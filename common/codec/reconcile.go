package codec

import "sort"

// Claim is one layer's view of whom an artifact was issued to.
type Claim struct {
	Layer     string
	MarkID    string
	Recipient string
	Strength  Strength
}

// Strength orders how much a claim is worth on its own.
type Strength int

const (
	// Leaning: the layer's best candidate is unique but falls short of the
	// attribution rule, so it can support a verdict but never make one.
	Leaning Strength = iota
	// Stored: a stored tag with a valid check value. It names one recipient
	// exactly, but it can be copied from another copy or planted.
	Stored
	// Attributed: codeword bits that meet the attribution rule on their own.
	Attributed
)

func (s Strength) String() string {
	switch s {
	case Attributed:
		return "attributed"
	case Stored:
		return "stored tag"
	}
	return "leaning"
}

// Support is one candidate recipient and the layers that point at them.
type Support struct {
	MarkID    string   `json:"markId"`
	Recipient string   `json:"recipient"`
	Layers    []string `json:"layers"`
	Strength  string   `json:"strength"` // the strongest claim among Layers
	strength  Strength
}

// Leaning reports the unique best candidate of a decision that did not reach
// the attribution rule but is better than chance. An absent or attributed
// decision has no leaning.
func (d Decision) Leaning() (Score, bool) {
	if d.Status != "inconclusive" || len(d.Ranking) == 0 {
		return Score{}, false
	}
	if len(d.Ranking) > 1 && d.Ranking[1].Errors <= d.Ranking[0].Errors {
		return Score{}, false
	}
	return d.Ranking[0], true
}

// Reconcile groups the claims by recipient and decides whether they conflict.
//
// Claims conflict when two recipients each have a claim that stands on its
// own (attributed bits or a valid stored tag), or when one recipient stands
// only on a stored tag and a bit layer leans towards someone else. A leaning
// layer that disagrees with an attribution made from bits is noise, not a
// conflict: the pooled decision already weighed it. Leanings with nothing
// standing are what an unrelated file produces by chance.
//
// named is the recipient's mark ID the verdict settled on, or "" if none.
// The result lists every candidate, strongest first, and whether they
// conflict.
func Reconcile(named string, verdictFromBits bool, claims []Claim) (groups []Support, conflict bool) {
	byMark := map[string]*Support{}
	for _, c := range claims {
		if c.MarkID == "" {
			continue
		}
		g := byMark[c.MarkID]
		if g == nil {
			g = &Support{MarkID: c.MarkID, Recipient: c.Recipient, strength: c.Strength}
			byMark[c.MarkID] = g
		}
		g.Layers = append(g.Layers, c.Layer)
		if c.Strength > g.strength {
			g.strength = c.Strength
		}
	}
	for _, g := range byMark {
		g.Strength = g.strength.String()
		groups = append(groups, *g)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].strength != groups[j].strength {
			return groups[i].strength > groups[j].strength
		}
		if len(groups[i].Layers) != len(groups[j].Layers) {
			return len(groups[i].Layers) > len(groups[j].Layers)
		}
		return groups[i].MarkID < groups[j].MarkID
	})
	if len(groups) < 2 {
		return groups, false
	}
	standing := 0
	for _, g := range groups {
		if g.strength >= Stored {
			standing++
		}
	}
	switch {
	case standing >= 2:
		return groups, true
	case standing == 0:
		// Leanings alone are what an unrelated file produces by chance.
		return groups, false
	case !verdictFromBits:
		return groups, true
	}
	// Attributed from bits, and every other candidate only leans: no conflict,
	// as long as the named recipient is the one standing.
	return groups, groups[0].MarkID != named
}

// Agreeing returns the layers that point at the named mark.
func Agreeing(named string, groups []Support) []string {
	for _, g := range groups {
		if g.MarkID == named {
			return g.Layers
		}
	}
	return nil
}
