package api_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"openpak/chat/internal/grpcapi"
	"openpak/chat/internal/store"
	chatv1 "openpak/chat/proto/openpak/chat/v1"
)

// Title messages over gRPC, end to end against Postgres: not friendship-gated,
// blocks refuse, binary bodies, rooms, threads, expiry, delete, rate limit, and
// none of it ever reaches the social chat.
func TestTitleMessagesGRPC(t *testing.T) {
	srv, fake, key := setup(t)
	a := "11111111-1111-1111-1111-111111111111"
	b := "22222222-2222-2222-2222-222222222222"
	c := "33333333-3333-3333-3333-333333333333"
	d := "44444444-4444-4444-4444-444444444444"
	fake.blocked = [][2]string{{d, a}} // d blocked a

	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	chatv1.RegisterChatServer(gs, grpcapi.New(srv.Svc, key))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough://chat",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cl := chatv1.NewChatClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-internal-key", key)

	send := func(r *chatv1.SendTitleMessageRequest) (*chatv1.SendTitleMessageResponse, error) {
		if r.Title == "" {
			r.Title = "monster-hunter-4"
		}
		if r.Network == "" {
			r.Network = "3ds"
		}
		return cl.SendTitleMessage(ctx, r)
	}
	code := func(err error) codes.Code { return status.Code(err) }

	// Strangers (a and c are not friends) may message; the body is binary.
	body := []byte{0, 1, 2, 0xff, 0xfe}
	r1, err := send(&chatv1.SendTitleMessageRequest{FromAccount: a, ToAccount: c, Subject: "hunt?", Body: body, Flags: 2, SenderName: "Ana", LifetimeS: 3600})
	if err != nil {
		t.Fatalf("stranger title send: %v", err)
	}
	m1 := r1.GetMessage()
	if m1.GetBodyKind() != "binary" || string(m1.GetBody()) != string(body) || m1.GetFlags() != 2 || m1.GetSenderName() != "Ana" {
		t.Fatalf("stored message: %+v", m1)
	}
	if exp := time.UnixMilli(m1.GetExpiresAtUnixMs()).Sub(time.UnixMilli(m1.GetSentAtUnixMs())); exp < 3599*time.Second || exp > 3601*time.Second {
		t.Fatalf("lifetime: %v", exp)
	}
	if len(fake.announced) != 0 {
		t.Fatalf("a title message must never be announced: %v", fake.announced)
	}

	// Threading: a reply names its parent; an unknown parent is refused.
	r2, err := send(&chatv1.SendTitleMessageRequest{FromAccount: c, ToAccount: a, Body: []byte("ok"), BodyKind: "text", ParentId: m1.GetId()})
	if err != nil || r2.GetMessage().GetParentId() != m1.GetId() {
		t.Fatalf("reply: %v %+v", err, r2)
	}
	if _, err := send(&chatv1.SendTitleMessageRequest{FromAccount: c, ToAccount: a, Body: []byte("x"), ParentId: 999999}); code(err) != codes.FailedPrecondition {
		t.Fatalf("unknown parent: %v", err)
	}
	// Another title's parent does not count either.
	if _, err := send(&chatv1.SendTitleMessageRequest{Title: "yo-kai-watch-2", FromAccount: c, ToAccount: a, Body: []byte("x"), ParentId: m1.GetId()}); code(err) != codes.FailedPrecondition {
		t.Fatalf("cross-title parent: %v", err)
	}

	// Blocks refuse, either direction.
	if _, err := send(&chatv1.SendTitleMessageRequest{FromAccount: a, ToAccount: d, Body: []byte("x")}); code(err) != codes.PermissionDenied {
		t.Fatalf("blocked (by other): %v", err)
	}
	if _, err := send(&chatv1.SendTitleMessageRequest{FromAccount: d, ToAccount: a, Body: []byte("x")}); code(err) != codes.PermissionDenied {
		t.Fatalf("blocked (by self): %v", err)
	}

	// Argument rules.
	bad := []*chatv1.SendTitleMessageRequest{
		{FromAccount: a, Body: []byte("x")},                                      // no recipient
		{FromAccount: a, ToAccount: c, ToRoom: "gathering:1", Body: []byte("x")}, // both
		{FromAccount: a, ToAccount: a, Body: []byte("x")},                        // self
		{FromAccount: "pid-1", ToAccount: c, Body: []byte("x")},                  // not a uuid
		{FromAccount: a, ToAccount: c, Body: []byte{0xff}, BodyKind: "text"},     // text must be UTF-8
		{FromAccount: a, ToAccount: c, Body: []byte("x"), BodyKind: "sticker"},
		{FromAccount: a, ToAccount: c, Body: make([]byte, 64<<10+1)},
		{FromAccount: a, ToAccount: c, Body: []byte("x"), Subject: strings.Repeat("s", 1025)},
	}
	for i, r := range bad {
		if _, err := send(r); code(err) != codes.InvalidArgument {
			t.Fatalf("bad request %d: %v", i, err)
		}
	}

	// Rooms: a gathering's messages are its own inbox.
	for i := 0; i < 3; i++ {
		if _, err := send(&chatv1.SendTitleMessageRequest{FromAccount: b, ToRoom: "gathering:42", Body: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	room, err := cl.ListTitleMessages(ctx, &chatv1.ListTitleMessagesRequest{Title: "monster-hunter-4", Network: "3ds", Room: "gathering:42", Offset: 1, Limit: 5})
	if err != nil || room.GetTotal() != 3 || len(room.GetMessages()) != 2 || room.GetMessages()[0].GetBody()[0] != 1 {
		t.Fatalf("room page: %v %+v", err, room)
	}

	// Inbox scope: c's inbox in this title and network only; headers only.
	inbox, err := cl.ListTitleMessages(ctx, &chatv1.ListTitleMessagesRequest{Title: "monster-hunter-4", Network: "3ds", Account: c, HeadersOnly: true})
	if err != nil || inbox.GetTotal() != 1 || len(inbox.GetMessages()) != 1 || len(inbox.GetMessages()[0].GetBody()) != 0 || inbox.GetMessages()[0].GetSubject() != "hunt?" {
		t.Fatalf("inbox: %v %+v", err, inbox)
	}
	other, _ := cl.ListTitleMessages(ctx, &chatv1.ListTitleMessagesRequest{Title: "monster-hunter-4", Network: "wiiu", Account: c})
	if other.GetTotal() != 0 {
		t.Fatalf("another network's inbox: %+v", other)
	}
	byID, _ := cl.ListTitleMessages(ctx, &chatv1.ListTitleMessagesRequest{Title: "monster-hunter-4", Network: "3ds", Account: a, Ids: []int64{r2.GetMessage().GetId(), m1.GetId()}})
	if len(byID.GetMessages()) != 1 || byID.GetMessages()[0].GetId() != r2.GetMessage().GetId() {
		t.Fatalf("ids outside the inbox must not be returned: %+v", byID)
	}

	// Never in the social chat.
	h := http.NewServeMux()
	srv.Register(h)
	ts := httptest.NewServer(h)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/internal/accounts/"+a+"/conversations", nil)
	req.Header.Set("X-Internal-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var convs struct {
		Conversations []any `json:"conversations"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&convs)
	resp.Body.Close()
	if len(convs.Conversations) != 0 {
		t.Fatalf("title messages leaked into the social chat: %+v", convs)
	}

	// Expiry: an expired message is invisible, then garbage-collected.
	if _, err := srv.Svc.Store.Exec(context.Background(), `UPDATE title_messages SET expires_at = now() - interval '1 second' WHERE id = $1`, r2.GetMessage().GetId()); err != nil {
		t.Fatal(err)
	}
	gone, _ := cl.ListTitleMessages(ctx, &chatv1.ListTitleMessagesRequest{Title: "monster-hunter-4", Network: "3ds", Account: a})
	if gone.GetTotal() != 0 {
		t.Fatalf("expired message still listed: %+v", gone)
	}
	if n, err := store.DeleteExpiredTitleMessages(context.Background(), srv.Svc.Store, 1); err != nil || n != 1 {
		t.Fatalf("gc: %d %v", n, err)
	}

	// Delete: ids in the inbox only; then all.
	if _, err := cl.DeleteTitleMessages(ctx, &chatv1.DeleteTitleMessagesRequest{Title: "monster-hunter-4", Network: "3ds", Account: c, Ids: []int64{m1.GetId()}, All: true}); code(err) != codes.InvalidArgument {
		t.Fatalf("ids and all: %v", err)
	}
	del, err := cl.DeleteTitleMessages(ctx, &chatv1.DeleteTitleMessagesRequest{Title: "monster-hunter-4", Network: "3ds", Account: a, Ids: []int64{m1.GetId()}})
	if err != nil || del.GetDeleted() != 0 {
		t.Fatalf("deleting someone else's message from my inbox: %v %+v", err, del)
	}
	del, err = cl.DeleteTitleMessages(ctx, &chatv1.DeleteTitleMessagesRequest{Title: "monster-hunter-4", Network: "3ds", Account: c, Ids: []int64{m1.GetId()}})
	if err != nil || del.GetDeleted() != 1 {
		t.Fatalf("delete: %v %+v", err, del)
	}
	del, err = cl.DeleteTitleMessages(ctx, &chatv1.DeleteTitleMessagesRequest{Title: "monster-hunter-4", Network: "3ds", Room: "gathering:42", All: true})
	if err != nil || del.GetDeleted() != 3 {
		t.Fatalf("delete all: %v %+v", err, del)
	}

	// Dedup: a retried send stores once.
	k1, err := send(&chatv1.SendTitleMessageRequest{FromAccount: a, ToAccount: c, Body: []byte("x"), DedupKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	k2, err := send(&chatv1.SendTitleMessageRequest{FromAccount: a, ToAccount: c, Body: []byte("x"), DedupKey: "k1"})
	if err != nil || !k2.GetDuplicate() || k2.GetMessage().GetId() != k1.GetMessage().GetId() {
		t.Fatalf("dedup: %v %+v", err, k2)
	}

	// Rate limit per sender.
	srv.Svc.TitleRatePerMinute = 3
	limited := false
	for i := 0; i < 10; i++ {
		if _, err := send(&chatv1.SendTitleMessageRequest{FromAccount: b, ToAccount: c, Body: []byte("x")}); code(err) == codes.ResourceExhausted {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("the per-sender rate limit never answered")
	}
	if _, err := send(&chatv1.SendTitleMessageRequest{FromAccount: d, ToAccount: b, Body: []byte("x")}); err != nil {
		t.Fatalf("another sender is not limited: %v", err)
	}
}
