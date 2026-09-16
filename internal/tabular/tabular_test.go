package tabular

import (
	"testing"

	"custodial/internal/codec"
)

func TestBattery(t *testing.T) {
	key := codec.NewKey()
	master := Generate(2000, 7)
	reg := &Registry{Key: key, Master: master, Copies: map[uint16]*Table{}}
	all := Techniques{Canary: true, LowBit: true, Dummy: true}
	names := []string{"Alice", "Bob", "Carol", "Dan", "Eve", "Frank"}
	for _, n := range names {
		var ids []uint16
		for _, is := range reg.Issued {
			ids = append(ids, is.MarkID)
		}
		id := key.AllocateID(ids)
		cp, is := MarkCopy(key, master, id, all)
		is.Recipient, is.Org = n, "Org"
		reg.Issued = append(reg.Issued, is)
		reg.Copies[id] = cp
	}
	leaker := reg.Issued[2]
	for _, b := range Battery() {
		rep := reg.Detect(Apply(reg.Copies[leaker.MarkID], b.Attack))
		line := ""
		for _, r := range rep.Results {
			mark := "-"
			switch {
			case r.Status == "attributed" && r.MarkID == leaker.Mark:
				mark = "OK"
			case r.Status == "attributed":
				mark = "WRONG"
			case r.Status == "inconclusive":
				mark = "~"
			}
			line += r.Technique + "=" + mark + " "
		}
		t.Logf("%-45s %s | verdict=%s %s", b.Name, line, rep.Verdict.Status, rep.Verdict.Recipient)
		for _, r := range rep.Results {
			if r.Status == "attributed" && r.MarkID != leaker.Mark {
				t.Errorf("%s: %s wrongly attributed: %s", b.Name, r.Technique, r.Detail)
			}
		}
	}
	// An unmarked source must never be attributed.
	rep := reg.Detect(master)
	if rep.Verdict.Status == "attributed" {
		t.Errorf("unmarked master attributed: %+v", rep.Verdict)
	}
	t.Logf("master: %s / %s", rep.Verdict.Status, rep.Results[2].Detail)
}
