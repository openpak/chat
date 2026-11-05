package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"openpak/chat/internal/service"
	"openpak/chat/internal/store"
	chatv1 "openpak/chat/proto/openpak/chat/v1"
)

func pbTitle(m store.TitleMessage) *chatv1.TitleMessage {
	return &chatv1.TitleMessage{
		Id: m.ID, Title: m.Title, Network: m.Network,
		FromAccount: m.FromAccount, ToAccount: m.ToAccount, ToRoom: m.ToRoom,
		Subject: m.Subject, Body: m.Body, BodyKind: m.BodyKind, SenderName: m.SenderName,
		Flags: m.Flags, ParentId: m.ParentID,
		SentAtUnixMs: m.SentAt.UnixMilli(), ExpiresAtUnixMs: m.ExpiresAt.UnixMilli(),
	}
}

func (s *Server) SendTitleMessage(ctx context.Context, req *chatv1.SendTitleMessageRequest) (*chatv1.SendTitleMessageResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	m, dup, err := s.svc.SendTitle(ctx, store.TitleMessage{
		Title: req.GetTitle(), Network: req.GetNetwork(),
		FromAccount: req.GetFromAccount(), ToAccount: req.GetToAccount(), ToRoom: req.GetToRoom(),
		Subject: req.GetSubject(), Body: req.GetBody(), BodyKind: req.GetBodyKind(),
		SenderName: req.GetSenderName(), Flags: req.GetFlags(), ParentID: req.GetParentId(),
	}, req.GetLifetimeS(), req.GetDedupKey())
	if err != nil {
		return nil, titleErr(err)
	}
	return &chatv1.SendTitleMessageResponse{Message: pbTitle(m), Duplicate: dup}, nil
}

func (s *Server) ListTitleMessages(ctx context.Context, req *chatv1.ListTitleMessagesRequest) (*chatv1.ListTitleMessagesResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	in := store.Inbox{Title: req.GetTitle(), Network: req.GetNetwork(), Account: req.GetAccount(), Room: req.GetRoom()}
	ms, total, err := s.svc.TitleInbox(ctx, in, req.GetOffset(), req.GetLimit(), req.GetIds(), req.GetHeadersOnly())
	if err != nil {
		return nil, titleErr(err)
	}
	out := &chatv1.ListTitleMessagesResponse{Messages: []*chatv1.TitleMessage{}, Total: total}
	for _, m := range ms {
		out.Messages = append(out.Messages, pbTitle(m))
	}
	return out, nil
}

func (s *Server) DeleteTitleMessages(ctx context.Context, req *chatv1.DeleteTitleMessagesRequest) (*chatv1.DeleteTitleMessagesResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	in := store.Inbox{Title: req.GetTitle(), Network: req.GetNetwork(), Account: req.GetAccount(), Room: req.GetRoom()}
	n, err := s.svc.DeleteTitle(ctx, in, req.GetIds(), req.GetAll())
	if err != nil {
		return nil, titleErr(err)
	}
	return &chatv1.DeleteTitleMessagesResponse{Deleted: n}, nil
}

func titleErr(err error) error {
	switch {
	case errors.Is(err, service.ErrTitleArgs), errors.Is(err, service.ErrBadAccount),
		errors.Is(err, service.ErrSameTalker), errors.Is(err, service.ErrBadKind):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, service.ErrTooLarge):
		return status.Error(codes.InvalidArgument, "title message too large")
	case errors.Is(err, store.ErrBadParent):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, service.ErrBlocked):
		return status.Error(codes.PermissionDenied, "blocked")
	case errors.Is(err, service.ErrRateLimited):
		return status.Error(codes.ResourceExhausted, err.Error())
	}
	return status.Error(codes.Internal, "chat service error")
}
