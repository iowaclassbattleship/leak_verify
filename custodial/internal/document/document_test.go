package document

import (
	"fmt"
	"os"
	"testing"
	"time"

	"attribution/common/codec"
)

func TestPipeline(t *testing.T) {
	key := codec.NewKey()
	e := NewEngine(key)
	m := e.NewMaster(SampleDoc(), "sample")
	t.Logf("master: pages=%d bodyLines=%d tiles=%d words=%d warnings=%v", m.Pages, m.BodyLines, m.Tiles, m.Words, m.Warnings)
	all := Layers{Layout: true, Spectral: true, Object: true, Metadata: true}
	var issued []Issued
	copies := map[uint16][]byte{}
	for i := 0; i < 8; i++ {
		var ids []uint16
		for _, is := range issued {
			ids = append(ids, is.ID)
		}
		id := key.AllocateID(ids)
		issued = append(issued, Issued{ID: id, Label: fmt.Sprintf("R%d", i), Layers: all})
		start := time.Now()
		copies[id] = e.IssueCopy(m, id, all)
		if i == 0 {
			t.Logf("issue one copy: %dms, %d KB", time.Since(start).Milliseconds(), len(copies[id])/1024)
		}
	}
	leaker := issued[3]
	dir := os.Getenv("DOC_OUT")
	if dir != "" {
		os.WriteFile(dir+"/copy.pdf", copies[leaker.ID], 0o644)
		os.WriteFile(dir+"/master.pdf", m.PDF, 0o644)
	}

	status := func(rep Report) map[string]string {
		out := map[string]string{}
		for _, r := range append(rep.Results, rep.Verdict) {
			s := r.Status
			switch {
			case r.Status == "attributed" && r.Recipient == leaker.Label:
				s = "OK"
			case r.Status == "attributed":
				s = "WRONG"
			}
			out[r.Technique] = s
		}
		return out
	}
	run := func(pdf []byte, attack string) (Report, time.Duration) {
		t.Helper()
		start := time.Now()
		art, err := e.Attack(pdf, attack)
		if err != nil {
			t.Fatal(err)
		}
		if dir != "" && attack != "none" {
			os.WriteFile(dir+"/"+art.Name, art.Data, 0o644)
		}
		rep, err := e.Detect(m, issued, art.Data)
		if err != nil {
			t.Fatal(err)
		}
		return rep, time.Since(start)
	}

	// Expected per attack: "OK" must attribute correctly, "-" must not attribute.
	want := map[string]map[string]string{
		"none":       {"layout": "OK", "spectral": "OK", "object": "OK", "metadata": "OK", "combined": "OK"},
		"resave":     {"layout": "OK", "spectral": "OK", "object": "OK", "metadata": "-", "combined": "OK"},
		"strip":      {"layout": "OK", "spectral": "-", "object": "OK", "metadata": "-", "combined": "OK"},
		"copypaste":  {"layout": "-", "spectral": "-", "object": "-", "metadata": "-", "combined": "-"},
		"screenshot": {"layout": "OK", "spectral": "OK", "object": "OK", "combined": "OK"},
		"cropshot":   {"spectral": "OK", "object": "-", "combined": "OK"},
		"printscan":  {"layout": "OK", "spectral": "-", "object": "-", "combined": "OK"},
	}
	for _, a := range Attacks {
		rep, took := run(copies[leaker.ID], a.ID)
		got := status(rep)
		t.Logf("%-10s %4dms layout=%-12s spectral=%-12s object=%-12s metadata=%-12s combined=%s",
			a.ID, took.Milliseconds(), got["layout"], got["spectral"], got["object"], got["metadata"], got["combined"])
		for _, r := range rep.Results {
			if r.Technique == "spectral" || (a.ID == "cropshot" && r.Technique == "layout") {
				t.Logf("           %s: %s", r.Technique, r.Detail)
			}
		}
		for tech, st := range got {
			if st == "WRONG" {
				t.Errorf("%s: %s attributed to the wrong recipient", a.ID, tech)
			}
			switch w := want[a.ID][tech]; {
			case w == "OK" && st != "OK":
				t.Errorf("%s: expected %s to attribute, got %s", a.ID, tech, st)
			case w == "-" && st == "OK":
				t.Errorf("%s: expected %s not to attribute", a.ID, tech)
			}
		}
	}

	// The unmarked master must never be attributed, in any form.
	for _, a := range []string{"none", "screenshot", "cropshot", "printscan", "copypaste"} {
		rep, _ := run(m.PDF, a)
		for tech, st := range status(rep) {
			if st == "OK" || st == "WRONG" {
				t.Errorf("master-%s: %s attributed an unmarked document", a, tech)
			}
		}
	}
}
