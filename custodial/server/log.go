package server

import (
	"log"
	"time"
)

// The issuance log outlives the process, so a copy issued last week can still
// be named when it turns up. The marked bytes are not kept: they are
// regenerable from the key, the source and the entry below, and a restored
// entry therefore has no download.

type loggedIssuance struct {
	ID       uint16          `json:"id"`
	MarkID   string          `json:"markId"`
	Tag      string          `json:"tag"`
	Name     string          `json:"name"`
	Role     string          `json:"role"`
	Unit     string          `json:"unit"`
	IssuedAt time.Time       `json:"issuedAt"`
	IssuedBy string          `json:"issuedBy"`
	Layers   map[string]bool `json:"layers"`
	FileName string          `json:"fileName"`
	Source   string          `json:"source"`
	Document string          `json:"document,omitempty"`
}

func (s *Server) loadLog() {
	var entries []loggedIssuance
	if err := s.store.Load(s.user, "custodial", &entries); err != nil {
		log.Printf("custodial: reading the issuance log for %s: %v", s.user, err)
		return
	}
	for _, e := range entries {
		s.doc.issued = append(s.doc.issued, &docIssuance{
			id: e.ID, tag: e.Tag, Restored: true,
			MarkID: e.MarkID, Name: e.Name, Role: e.Role, Unit: e.Unit,
			IssuedAt: e.IssuedAt, IssuedBy: e.IssuedBy, Layers: e.Layers,
			FileName: e.FileName, Source: e.Source, Document: e.Document,
		})
	}
}

func (s *Server) saveLog() {
	var entries []loggedIssuance
	for _, is := range s.doc.issued {
		entries = append(entries, loggedIssuance{
			ID: is.id, MarkID: is.MarkID, Tag: is.tag,
			Name: is.Name, Role: is.Role, Unit: is.Unit,
			IssuedAt: is.IssuedAt, IssuedBy: is.IssuedBy, Layers: is.Layers,
			FileName: is.FileName, Source: is.Source, Document: is.Document,
		})
	}
	if err := s.store.Save(s.user, "custodial", entries); err != nil {
		log.Printf("custodial: writing the issuance log for %s: %v", s.user, err)
	}
}
