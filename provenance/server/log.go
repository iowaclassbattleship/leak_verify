package server

import (
	"log"
	"time"

	"attribution/provenance/internal/tabular"
)

// The issuance log outlives the process. Marked tables are not stored, because
// marking is deterministic: given the same source and schema, every copy and
// its canary rows regenerate from the log entry below.

type loggedIssuance struct {
	MarkID     uint16             `json:"id"`
	Mark       string             `json:"markId"`
	Recipient  string             `json:"recipient"`
	Org        string             `json:"org"`
	Techniques tabular.Techniques `json:"techniques"`
	IssuedAt   time.Time          `json:"issuedAt"`
	FileName   string             `json:"fileName"`
	Source     string             `json:"source"`
}

func (s *Server) loadLog() {
	if err := s.store.Load(s.user, "provenance", &s.logged); err != nil {
		log.Printf("provenance: reading the issuance log for %s: %v", s.user, err)
	}
}

func (s *Server) saveLog() {
	s.logged = s.logged[:0]
	for _, is := range s.tab.reg.Issued {
		if is.MarkID == previewID {
			continue // the Mark tab's own copy, not an issuance to anyone
		}
		s.logged = append(s.logged, loggedIssuance{
			MarkID: is.MarkID, Mark: is.Mark, Recipient: is.Recipient, Org: is.Org,
			Techniques: is.Techniques, IssuedAt: is.IssuedAt, FileName: is.FileName,
			Source: s.tab.source,
		})
	}
	if err := s.store.Save(s.user, "provenance", s.logged); err != nil {
		log.Printf("provenance: writing the issuance log for %s: %v", s.user, err)
	}
}

// restore replays the log against whatever table is loaded now, regenerating
// each copy and its canary rows so detection works after a restart. Entries
// issued from a different table simply will not match anything, which is the
// right answer rather than a wrong one.
func (s *Server) restore() {
	for _, e := range s.logged {
		copyT, iss := tabular.MarkCopy(s.key, s.tab.reg.Master, s.tab.reg.Schema, e.MarkID, e.Techniques)
		iss.Recipient, iss.Org, iss.IssuedAt, iss.FileName = e.Recipient, e.Org, e.IssuedAt, e.FileName
		s.tab.reg.Issued = append(s.tab.reg.Issued, iss)
		s.tab.reg.Copies[e.MarkID] = copyT
	}
}
