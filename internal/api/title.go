package api

// Title messages over internal HTTP (operators, smoke tests, a future
// adapter without gRPC); the console adapters use the gRPC surface. Bodies
// travel base64 ([]byte in JSON).
//
//	POST /internal/titles/{title}/messages          {"network","from_account","to_account"|"to_room","subject","body","body_kind","lifetime_s","flags","parent_id","sender_name","dedup_key"} -> 201
//	GET  /internal/titles/{title}/messages?network=&account=|room=&offset=&limit=&headers_only=1
//	POST /internal/titles/{title}/messages/delete   {"network","account"|"room","ids":[...]|"all":true} -> {"deleted":n}

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"openpak/chat/internal/service"
	"openpak/chat/internal/store"
)

type titleMessageJSON struct {
	ID              int64  `json:"id"`
	Title           string `json:"title"`
	Network         string `json:"network"`
	From            string `json:"from_account"`
	To              string `json:"to_account,omitempty"`
	Room            string `json:"to_room,omitempty"`
	Subject         string `json:"subject"`
	Body            []byte `json:"body"`
	BodyKind        string `json:"body_kind"`
	SenderName      string `json:"sender_name"`
	Flags           int64  `json:"flags"`
	ParentID        int64  `json:"parent_id"`
	SentAtUnixMs    int64  `json:"sent_at_unix_ms"`
	ExpiresAtUnixMs int64  `json:"expires_at_unix_ms"`
}

func titleOf(m store.TitleMessage) titleMessageJSON {
	return titleMessageJSON{ID: m.ID, Title: m.Title, Network: m.Network, From: m.FromAccount, To: m.ToAccount,
		Room: m.ToRoom, Subject: m.Subject, Body: m.Body, BodyKind: m.BodyKind, SenderName: m.SenderName,
		Flags: m.Flags, ParentID: m.ParentID, SentAtUnixMs: m.SentAt.UnixMilli(), ExpiresAtUnixMs: m.ExpiresAt.UnixMilli()}
}

func (s *Server) registerTitle(a func(string, http.HandlerFunc)) {
	a("POST /internal/titles/{title}/messages", s.titleSend)
	a("GET /internal/titles/{title}/messages", s.titleList)
	a("POST /internal/titles/{title}/messages/delete", s.titleDelete)
}

func titleFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrTitleArgs), errors.Is(err, service.ErrBadKind), errors.Is(err, store.ErrBadParent):
		jsonOut(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
	case errors.Is(err, service.ErrBlocked):
		jsonOut(w, http.StatusForbidden, map[string]any{"error": "blocked"})
	case errors.Is(err, service.ErrRateLimited):
		jsonOut(w, http.StatusTooManyRequests, map[string]any{"error": err.Error()})
	default:
		failWith(w, err)
	}
}

func (s *Server) titleSend(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Network    string `json:"network"`
		From       string `json:"from_account"`
		To         string `json:"to_account"`
		Room       string `json:"to_room"`
		Subject    string `json:"subject"`
		Body       []byte `json:"body"`
		BodyKind   string `json:"body_kind"`
		LifetimeS  int64  `json:"lifetime_s"`
		Flags      int64  `json:"flags"`
		ParentID   int64  `json:"parent_id"`
		SenderName string `json:"sender_name"`
		DedupKey   string `json:"dedup_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, service.MaxTitleBodyBytes*2+8192)).Decode(&in); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	m, dup, err := s.Svc.SendTitle(r.Context(), store.TitleMessage{
		Title: r.PathValue("title"), Network: in.Network, FromAccount: in.From, ToAccount: in.To, ToRoom: in.Room,
		Subject: in.Subject, Body: in.Body, BodyKind: in.BodyKind, SenderName: in.SenderName,
		Flags: in.Flags, ParentID: in.ParentID,
	}, in.LifetimeS, in.DedupKey)
	if err != nil {
		titleFail(w, err)
		return
	}
	code := http.StatusCreated
	if dup {
		code = 200
	}
	jsonOut(w, code, map[string]any{"message": titleOf(m), "duplicate": dup})
}

func (s *Server) titleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	in := store.Inbox{Title: r.PathValue("title"), Network: q.Get("network"), Account: q.Get("account"), Room: q.Get("room")}
	ms, total, err := s.Svc.TitleInbox(r.Context(), in, offset, int32(limit), nil, q.Get("headers_only") == "1")
	if err != nil {
		titleFail(w, err)
		return
	}
	out := []titleMessageJSON{}
	for _, m := range ms {
		out = append(out, titleOf(m))
	}
	jsonOut(w, 200, map[string]any{"messages": out, "total": total})
}

func (s *Server) titleDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Network string  `json:"network"`
		Account string  `json:"account"`
		Room    string  `json:"room"`
		IDs     []int64 `json:"ids"`
		All     bool    `json:"all"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	n, err := s.Svc.DeleteTitle(r.Context(), store.Inbox{Title: r.PathValue("title"), Network: in.Network, Account: in.Account, Room: in.Room}, in.IDs, in.All)
	if err != nil {
		titleFail(w, err)
		return
	}
	jsonOut(w, 200, map[string]any{"deleted": n})
}
