// Package api is chat's internal HTTP surface for the website (and through it
// the phone app). Same rules as the gRPC surface; JSON over it. The caller
// (the website) has already decided who the account is.
//
//	GET  /internal/accounts/{account}/conversations            one row per peer, newest first
//	GET  /internal/accounts/{account}/messages/{peer}?after=&limit=   history, oldest first
//	POST /internal/accounts/{account}/messages/{peer}          {"body","kind","dedup_key"} -> 201
//	GET  /internal/messages/{id}                               one message
//	GET  /healthz
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"openpak/chat/internal/service"
	"openpak/chat/internal/store"
)

type Server struct {
	Svc *service.Service
	Key string
}

func (s *Server) Register(mux *http.ServeMux) {
	a := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.auth(h)) }
	a("GET /internal/accounts/{account}/conversations", s.conversations)
	a("GET /internal/accounts/{account}/messages/{peer}", s.history)
	a("POST /internal/accounts/{account}/messages/{peer}", s.send)
	a("GET /internal/messages/{id}", s.message)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Key == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Key")), []byte(s.Key)) != 1 {
			jsonOut(w, http.StatusUnauthorized, map[string]any{"error": "internal key"})
			return
		}
		h(w, r)
	}
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type messageJSON struct {
	ID         int64  `json:"id"`
	From       string `json:"from_account"`
	To         string `json:"to_account"`
	Kind       string `json:"kind"`
	Body       string `json:"body"`
	SentAtUnix int64  `json:"sent_at_unix"`
}

func messageOf(m store.Message) messageJSON {
	return messageJSON{ID: m.ID, From: m.FromAccount, To: m.ToAccount, Kind: m.Kind, Body: m.Body, SentAtUnix: m.SentAt.Unix()}
}

func failWith(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrNotAllowed):
		jsonOut(w, http.StatusForbidden, map[string]any{"error": "only friends can talk"})
	case errors.Is(err, service.ErrEmptyBody):
		jsonOut(w, http.StatusBadRequest, map[string]any{"error": "empty message"})
	case errors.Is(err, service.ErrTooLarge):
		jsonOut(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "message too large"})
	case errors.Is(err, service.ErrSameTalker):
		jsonOut(w, http.StatusBadRequest, map[string]any{"error": "a message needs two accounts"})
	case errors.Is(err, store.ErrNotFound):
		jsonOut(w, http.StatusNotFound, map[string]any{"error": "not found"})
	case errors.Is(err, store.ErrUnknownKind):
		jsonOut(w, http.StatusBadRequest, map[string]any{"error": "unknown kind"})
	default:
		jsonOut(w, http.StatusBadGateway, map[string]any{"error": "chat service error"})
	}
}

func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Svc.Conversations(r.Context(), r.PathValue("account"))
	if err != nil {
		failWith(w, err)
		return
	}
	out := []map[string]any{}
	for _, c := range cs {
		out = append(out, map[string]any{"peer": c.Peer, "newest": messageOf(c.Newest)})
	}
	jsonOut(w, 200, map[string]any{"conversations": out})
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ms, err := s.Svc.Conversation(r.Context(), r.PathValue("account"), r.PathValue("peer"), after, int32(limit))
	if err != nil {
		failWith(w, err)
		return
	}
	out := []messageJSON{}
	for _, m := range ms {
		out = append(out, messageOf(m))
	}
	jsonOut(w, 200, map[string]any{"messages": out})
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body     string `json:"body"`
		Kind     string `json:"kind"`
		DedupKey string `json:"dedup_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, service.MaxBodyBytes+1024)).Decode(&in); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	m, dup, err := s.Svc.Send(r.Context(), r.PathValue("account"), r.PathValue("peer"), in.Kind, in.Body, in.DedupKey)
	if err != nil {
		failWith(w, err)
		return
	}
	code := http.StatusCreated
	if dup {
		code = 200
	}
	jsonOut(w, code, map[string]any{"message": messageOf(m), "duplicate": dup})
}

func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	m, err := s.Svc.Message(r.Context(), id)
	if err != nil {
		failWith(w, err)
		return
	}
	jsonOut(w, 200, messageOf(m))
}
