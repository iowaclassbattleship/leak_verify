package server

import (
	"log"
	"sort"
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
	Unit       string             `json:"unit,omitempty"`
	Techniques tabular.Techniques `json:"techniques"`
	IssuedAt   time.Time          `json:"issuedAt"`
	FileName   string             `json:"fileName"`
	Source     string             `json:"source"`
	// Schema is the column roles the copy was marked under, so it can be
	// regenerated exactly after a restart.
	Schema *tabular.Schema `json:"schema,omitempty"`
}

func (s *Server) loadLog() {
	if err := s.store.Load(s.user, "provenance", &s.logged); err != nil {
		log.Printf("provenance: reading the issuance log for %s: %v", s.user, err)
	}
}

func (s *Server) saveLog() {
	// Entries for other tables are kept as they are: they are not loaded, but
	// they are still part of the record.
	var kept []loggedIssuance
	for _, e := range s.logged {
		if e.Source != s.tab.source {
			kept = append(kept, e)
		}
	}
	schema := s.tab.reg.Schema
	for _, is := range s.tab.reg.Issued {
		if is.MarkID == previewID {
			continue // the Mark tab's own copy, not an issuance to anyone
		}
		kept = append(kept, loggedIssuance{
			MarkID: is.MarkID, Mark: is.Mark, Recipient: is.Recipient, Org: is.Org, Unit: is.Unit,
			Techniques: is.Techniques, IssuedAt: is.IssuedAt, FileName: is.FileName,
			Source: s.tab.source, Schema: &schema,
		})
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].IssuedAt.Before(kept[j].IssuedAt) })
	s.logged = kept
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
		if e.Source != s.tab.source {
			continue // issued from another table; it cannot match this one
		}
		copyT, iss := tabular.MarkCopy(s.key, s.tab.reg.Master, s.tab.reg.Schema, e.MarkID, e.Techniques)
		iss.Recipient, iss.Org, iss.Unit, iss.IssuedAt, iss.FileName = e.Recipient, e.Org, e.Unit, e.IssuedAt, e.FileName
		if iss.Unit == "" {
			iss.Unit = "external" // logged before recipients had a unit
		}
		s.tab.reg.Issued = append(s.tab.reg.Issued, iss)
		s.tab.reg.Copies[e.MarkID] = copyT
	}
}

// loggedSchema returns the column roles copies of this source were issued
// under, if any were.
func (s *Server) loggedSchema(source string) *tabular.Schema {
	var out *tabular.Schema
	for _, e := range s.logged {
		if e.Source == source && e.Schema != nil {
			out = e.Schema
		}
	}
	return out
}
