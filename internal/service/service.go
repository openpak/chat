// Package service is chat's rules on top of the store: who may talk, what a
// message may carry, and the announcement every send produces. The service is
// the only writer; the gRPC and HTTP surfaces are thin maps onto it.
package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"openpak/chat/internal/core"
	"openpak/chat/internal/store"
)

// MaxBodyBytes is what a message may carry. A painting or screenshot is its
// console's encoding, base64: the store is neutral about the bytes, not about
// the size.
const MaxBodyBytes = 64 << 10

var (
	ErrNotAllowed = errors.New("chat: only friends can talk")
	ErrEmptyBody  = errors.New("chat: empty message")
	ErrTooLarge   = errors.New("chat: message too large")
	ErrSameTalker = errors.New("chat: a message needs two accounts")
	ErrBadAccount = errors.New("chat: account ids are UUIDs")
)

type Service struct {
	Store *pgxpool.Pool
	Core  *core.Client
}

// Send applies the rules, stores, and announces. duplicate is true when the
// dedup key had already stored this exact message; nothing is re-announced.
func (s *Service) Send(ctx context.Context, from, to, kind, body, dedupKey string) (store.Message, bool, error) {
	if from == "" || to == "" {
		return store.Message{}, false, ErrSameTalker
	}
	if from == to {
		return store.Message{}, false, ErrSameTalker
	}
	if strings.TrimSpace(body) == "" {
		return store.Message{}, false, ErrEmptyBody
	}
	if len(body) > MaxBodyBytes {
		return store.Message{}, false, ErrTooLarge
	}
	ok, err := s.Core.Friends(ctx, from, to)
	if err != nil {
		return store.Message{}, false, fmt.Errorf("chat: friendship: %w", err)
	}
	if !ok {
		return store.Message{}, false, ErrNotAllowed
	}
	m, duplicate, err := store.Insert(ctx, s.Store, from, to, kind, body, dedupKey)
	if err != nil {
		return store.Message{}, false, err
	}
	if !duplicate {
		// The message exists the moment Insert returns; the announcement is
		// how consoles hear about it now instead of at their next sync. A
		// failed announcement is logged, never fatal: every surface's
		// conversation view still finds the message here.
		acx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := s.Core.Announce(acx, m.ID, to); err != nil {
			log.Printf("chat: announce message %d to %s: %v (visible on next sync)", m.ID, to, err)
		}
		cancel()
	}
	return m, duplicate, nil
}

func (s *Service) Message(ctx context.Context, id int64) (store.Message, error) {
	return store.ByID(ctx, s.Store, id)
}

// Conversation returns the exchange between account and peer. The reader may
// always read their own side; strangers' conversations are unreachable by
// construction (one of the two must be the caller).
func (s *Service) Conversation(ctx context.Context, account, peer string, afterID int64, limit int32) ([]store.Message, error) {
	if account == "" || peer == "" || account == peer {
		return nil, ErrSameTalker
	}
	return store.Conversation(ctx, s.Store, account, peer, afterID, limit)
}

func (s *Service) Conversations(ctx context.Context, account string) ([]store.ConversationHead, error) {
	return store.Conversations(ctx, s.Store, account)
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// EraseAccount deletes every message to or from the account. Idempotent.
func (s *Service) EraseAccount(ctx context.Context, account string) (int64, error) {
	if !uuidRE.MatchString(account) {
		return 0, ErrBadAccount
	}
	return store.DeleteForAccount(ctx, s.Store, account)
}
