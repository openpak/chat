// Package core is the chat service's client of the account core. The core is
// consulted for exactly two things: whether two accounts may talk (the one
// friendship answer, the same for every surface) and where the "message"
// event is announced (the universal stream every adapter consumes). Chat
// never stores a friendship and never invents an event the core did not
// countersign.
package core

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	accountv1 "openpak/chat/internal/accountpb"
)

type Client struct {
	social accountv1.SocialClient
	events accountv1.EventsClient
	key    string
}

// Dial opens the core connection. Insecure on purpose: the internal network
// is the boundary, the same as every other internal caller.
func Dial(addr, internalKey string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("chat: core dial: %w", err)
	}
	return New(accountv1.NewSocialClient(conn), accountv1.NewEventsClient(conn), internalKey), nil
}

// New builds a client on existing stubs; tests hand it bufconn-backed ones.
func New(social accountv1.SocialClient, events accountv1.EventsClient, internalKey string) *Client {
	return &Client{
		social: social,
		events: events,
		key:    internalKey,
	}
}

func (c *Client) ctx(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "x-internal-key", c.key)
}

// Friends answers whether a and b are friends in the core's graph. Any other
// answer (pending, blocked, strangers, unknown accounts) is no.
func (c *Client) Friends(ctx context.Context, a, b string) (bool, error) {
	rctx, cancel := context.WithTimeout(c.ctx(ctx), 5*time.Second)
	defer cancel()
	rel, err := c.social.GetRelationship(rctx, &accountv1.GetRelationshipRequest{AccountId: a, OtherId: b})
	if err != nil {
		return false, err
	}
	return rel.GetRelationship() == accountv1.Relationship_RELATIONSHIP_FRIENDS, nil
}

// Announce places the message on the core's universal stream: type "message",
// account_id = who must hear it, subject_id = the chat message id. The body
// never rides the event log; receivers fetch it here.
func (c *Client) Announce(ctx context.Context, messageID int64, toAccount string) error {
	rctx, cancel := context.WithTimeout(c.ctx(ctx), 5*time.Second)
	defer cancel()
	_, err := c.events.EmitEvent(rctx, &accountv1.EmitEventRequest{
		Type:      "message",
		AccountId: toAccount,
		SubjectId: fmt.Sprint(messageID),
	})
	return err
}
