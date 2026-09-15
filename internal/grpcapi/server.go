// Package grpcapi is chat's gRPC surface for the console-side adapters
// (wiiu-chat, a 3DS bridge): the same rules as the HTTP surface, one wire.
package grpcapi

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"openpak/chat/internal/service"
	"openpak/chat/internal/store"
	chatv1 "openpak/chat/proto/openpak/chat/v1"
)

type Server struct {
	chatv1.UnimplementedChatServer
	svc *service.Service
	key string
}

func New(svc *service.Service, internalKey string) *Server {
	return &Server{svc: svc, key: internalKey}
}

func (s *Server) authorize(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || s.key == "" {
		return status.Error(codes.Unauthenticated, "missing internal credentials")
	}
	if vals := md.Get("x-internal-key"); len(vals) == 0 || vals[0] != s.key {
		return status.Error(codes.Unauthenticated, "missing internal credentials")
	}
	return nil
}

func pbMessage(m store.Message) *chatv1.Message {
	return &chatv1.Message{
		Id:          m.ID,
		FromAccount: m.FromAccount,
		ToAccount:   m.ToAccount,
		Kind:        m.Kind,
		Body:        m.Body,
		SentAtUnix:  m.SentAt.Unix(),
	}
}

func (s *Server) SendMessage(ctx context.Context, req *chatv1.SendMessageRequest) (*chatv1.SendMessageResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	m, dup, err := s.svc.Send(ctx, req.GetFromAccount(), req.GetToAccount(), req.GetKind(), req.GetBody(), req.GetDedupKey())
	if err != nil {
		return nil, sendErr(err)
	}
	return &chatv1.SendMessageResponse{Message: pbMessage(m), Duplicate: dup}, nil
}

func (s *Server) GetMessage(ctx context.Context, req *chatv1.GetMessageRequest) (*chatv1.GetMessageResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	m, err := s.svc.Message(ctx, req.GetId())
	if err != nil {
		return nil, sendErr(err)
	}
	return &chatv1.GetMessageResponse{Message: pbMessage(m)}, nil
}

func (s *Server) ListMessages(ctx context.Context, req *chatv1.ListMessagesRequest) (*chatv1.ListMessagesResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	ms, err := s.svc.Conversation(ctx, req.GetAccount(), req.GetPeer(), req.GetAfterId(), req.GetLimit())
	if err != nil {
		return nil, sendErr(err)
	}
	out := &chatv1.ListMessagesResponse{Messages: []*chatv1.Message{}}
	for _, m := range ms {
		out.Messages = append(out.Messages, pbMessage(m))
	}
	return out, nil
}

func (s *Server) ListConversations(ctx context.Context, req *chatv1.ListConversationsRequest) (*chatv1.ListConversationsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	cs, err := s.svc.Conversations(ctx, req.GetAccount())
	if err != nil {
		return nil, sendErr(err)
	}
	out := &chatv1.ListConversationsResponse{Conversations: []*chatv1.Conversation{}}
	for _, c := range cs {
		out.Conversations = append(out.Conversations, &chatv1.Conversation{Peer: c.Peer, Newest: pbMessage(c.Newest)})
	}
	return out, nil
}

func sendErr(err error) error {
	switch err {
	case service.ErrNotAllowed:
		return status.Error(codes.PermissionDenied, "only friends can talk")
	case service.ErrEmptyBody:
		return status.Error(codes.InvalidArgument, "empty message")
	case service.ErrTooLarge:
		return status.Error(codes.InvalidArgument, "message too large")
	case service.ErrSameTalker:
		return status.Error(codes.InvalidArgument, "a message needs two accounts")
	case store.ErrNotFound:
		return status.Error(codes.NotFound, "no such message")
	}
	if err == store.ErrUnknownKind {
		return status.Error(codes.InvalidArgument, "unknown kind")
	}
	return status.Error(codes.Internal, "chat service error")
}
