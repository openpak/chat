// The chat flow against a real Postgres (CHAT_TEST_DATABASE_URL) and a fake
// core over bufconn: the friendship rule at the door, the announcement on
// every send, idempotent sends, and the two reading surfaces. Skipped without
// a database, like every integration surface here.
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	accountv1 "openpak/chat/internal/accountpb"
	api "openpak/chat/internal/api"
	"openpak/chat/internal/core"
	"openpak/chat/internal/service"
	"openpak/chat/internal/store"
)

// fakeCore answers friendship from a fixed set and records announcements.
type fakeCore struct {
	accountv1.UnimplementedSocialServer
	accountv1.UnimplementedEventsServer
	friends   [][2]string
	announced []string // "to:subjectID"
}

func (f *fakeCore) isFriends(a, b string) bool {
	for _, p := range f.friends {
		if (p[0] == a && p[1] == b) || (p[0] == b && p[1] == a) {
			return true
		}
	}
	return false
}

func (f *fakeCore) GetRelationship(_ context.Context, req *accountv1.GetRelationshipRequest) (*accountv1.GetRelationshipResponse, error) {
	if f.isFriends(req.GetAccountId(), req.GetOtherId()) {
		return &accountv1.GetRelationshipResponse{Relationship: accountv1.Relationship_RELATIONSHIP_FRIENDS}, nil
	}
	return &accountv1.GetRelationshipResponse{Relationship: accountv1.Relationship_RELATIONSHIP_NONE}, nil
}

func (f *fakeCore) EmitEvent(_ context.Context, req *accountv1.EmitEventRequest) (*accountv1.EmitEventResponse, error) {
	f.announced = append(f.announced, req.GetType()+"|"+req.GetAccountId()+"|"+req.GetSubjectId())
	return &accountv1.EmitEventResponse{Event: &accountv1.InvalidationEvent{Type: req.GetType(), AccountId: req.GetAccountId(), SubjectId: req.GetSubjectId()}}, nil
}

func setup(t *testing.T) (*api.Server, *fakeCore, string) {
	t.Helper()
	dbURL := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("CHAT_TEST_DATABASE_URL not set")
	}
	pool, err := store.DB(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE messages`); err != nil {
		t.Fatal(err)
	}

	fake := &fakeCore{friends: [][2]string{{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"}}}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	accountv1.RegisterSocialServer(gs, fake)
	accountv1.RegisterEventsServer(gs, fake)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough://chat-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	svc := &service.Service{Store: pool, Core: core.New(accountv1.NewSocialClient(conn), accountv1.NewEventsClient(conn), "core-key")}
	srv := &api.Server{Svc: svc, Key: "internal"}
	return srv, fake, "internal"
}

func TestChatFlow(t *testing.T) {
	srv, fake, key := setup(t)
	mux := http.NewServeMux()
	srv.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a := "11111111-1111-1111-1111-111111111111"
	b := "22222222-2222-2222-2222-222222222222"
	c := "33333333-3333-3333-3333-333333333333"

	do := func(method, path string, body io.Reader) *http.Response {
		req, _ := http.NewRequest(method, ts.URL+path, body)
		req.Header.Set("X-Internal-Key", key)
		if b, ok := body.(*bytes.Reader); ok {
			req.ContentLength = int64(b.Len())
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	send := func(from, to, body, dedup string) *http.Response {
		payload, _ := json.Marshal(map[string]string{"body": body, "dedup_key": dedup})
		return do("POST", fmt.Sprintf("/internal/accounts/%s/messages/%s", from, to), bytes.NewReader(payload))
	}

	// Strangers cannot talk, and nothing is announced.
	if resp := send(c, a, "hello?", ""); resp.StatusCode != 403 {
		t.Fatalf("stranger send: %d", resp.StatusCode)
	}
	if len(fake.announced) != 0 {
		t.Fatalf("a refused send announced something: %v", fake.announced)
	}

	// Friends can. Every send is announced to the recipient with the message id.
	resp := send(a, b, "hello from the phone", "")
	if resp.StatusCode != 201 {
		t.Fatalf("send: %d", resp.StatusCode)
	}
	var sent struct {
		Message struct {
			ID   int64  `json:"id"`
			From string `json:"from_account"`
			To   string `json:"to_account"`
			Kind string `json:"kind"`
			Body string `json:"body"`
		} `json:"message"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&sent)
	resp.Body.Close()
	if sent.Message.Kind != "text" || sent.Message.Body != "hello from the phone" || sent.Message.ID == 0 {
		t.Fatalf("sent: %+v", sent.Message)
	}
	if len(fake.announced) != 1 || !strings.HasPrefix(fake.announced[0], "message|"+b+"|"+fmt.Sprint(sent.Message.ID)) {
		t.Fatalf("announcement: %v (want message|%s|%d)", fake.announced, b, sent.Message.ID)
	}

	// Reply, so the conversation has both directions.
	resp = send(b, a, "hello from the console", "")
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("reply: %d", resp.StatusCode)
	}

	// Idempotent send: same dedup key stores once, announces once.
	resp = send(a, b, "hello from the phone", "client-1")
	if resp.StatusCode != 201 {
		t.Fatalf("first keyed send must create: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = send(a, b, "hello from the phone", "client-1")
	if resp.StatusCode != 200 {
		t.Fatalf("duplicate send must answer 200, got %d", resp.StatusCode)
	}
	var dup struct {
		Duplicate bool `json:"duplicate"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&dup)
	resp.Body.Close()
	if !dup.Duplicate {
		t.Fatal("same dedup key must be reported duplicate")
	}
	if len(fake.announced) != 3 { // hello + reply + the first keyed send; the replay is silent
		t.Fatalf("announcement count wrong: %v", fake.announced)
	}

	// History, with after-pagination.
	resp = do("GET", fmt.Sprintf("/internal/accounts/%s/messages/%s", a, b), nil)
	var hist struct {
		Messages []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&hist)
	resp.Body.Close()
	if len(hist.Messages) != 3 || hist.Messages[0].Body != "hello from the phone" || hist.Messages[1].Body != "hello from the console" || hist.Messages[2].Body != "hello from the phone" {
		t.Fatalf("history: %+v", hist.Messages)
	}
	resp = do("GET", fmt.Sprintf("/internal/accounts/%s/messages/%s?after=%d", a, b, hist.Messages[0].ID), nil)
	var after struct {
		Messages []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&after)
	resp.Body.Close()
	if len(after.Messages) != 2 || after.Messages[0].Body != "hello from the console" {
		t.Fatalf("after-pagination: %+v", after.Messages)
	}

	// Conversations: one row per peer, newest message first; the third
	// account has none.
	resp = do("GET", "/internal/accounts/"+a+"/conversations", nil)
	var convs struct {
		Conversations []struct {
			Peer   string `json:"peer"`
			Newest struct {
				Body string `json:"body"`
			} `json:"newest"`
		} `json:"conversations"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&convs)
	resp.Body.Close()
	if len(convs.Conversations) != 1 || convs.Conversations[0].Peer != b || convs.Conversations[0].Newest.Body != "hello from the phone" {
		t.Fatalf("conversations: %+v", convs.Conversations)
	}
	resp = do("GET", "/internal/accounts/"+c+"/conversations", nil)
	_ = json.NewDecoder(resp.Body).Decode(&convs)
	resp.Body.Close()
	if len(convs.Conversations) != 0 {
		t.Fatalf("a stranger's conversation list must be empty: %+v", convs.Conversations)
	}

	// Rule errors.
	if resp := send(a, b, "   ", ""); resp.StatusCode != 400 {
		t.Fatalf("blank body: %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	payload, _ := json.Marshal(map[string]string{"body": "x", "kind": "sticker"})
	if resp := do("POST", fmt.Sprintf("/internal/accounts/%s/messages/%s", a, b), bytes.NewReader(payload)); resp.StatusCode != 400 {
		t.Fatalf("unknown kind: %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := do("POST", fmt.Sprintf("/internal/accounts/%s/messages/%s", a, a), bytes.NewReader([]byte(`{"body":"note to self"}`))); resp.StatusCode != 400 {
		t.Fatalf("self-send: %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := do("GET", "/internal/accounts/"+a+"/messages/"+a, nil); resp.StatusCode != 400 {
		t.Fatalf("self-history: %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	// Without the internal key, nothing answers.
	req, _ := http.NewRequest("GET", ts.URL+"/internal/accounts/"+a+"/conversations", nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 401 {
		t.Fatalf("unauthenticated: %d", resp2.StatusCode)
	}
}
