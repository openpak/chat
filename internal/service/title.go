package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"openpak/chat/internal/store"
)

// Title message limits (docs/protocol.md "Title messages"). NEX's own caps are
// far lower (256 characters of text, 512 bytes of binary); these bound the
// neutral store, not one game.
const (
	MaxTitleBodyBytes    = 64 << 10
	MaxTitleSubjectBytes = 1024
	MaxTitleNameBytes    = 256
	MaxTitleKeyBytes     = 128 // title, network, room, dedup key
	MaxTitleLifetime     = 30 * 24 * time.Hour
	MaxTitleDeleteIDs    = 1000
	// DefaultTitleRatePerMinute is how many title messages one sender may store
	// per minute (CHAT_TITLE_RATE_PER_MIN overrides; a retried send counts too).
	DefaultTitleRatePerMinute = 60
)

var (
	ErrTitleArgs   = errors.New("chat: title, network and sender are required, with exactly one of to_account / to_room")
	ErrBlocked     = errors.New("chat: blocked")
	ErrRateLimited = errors.New("chat: too many title messages, slow down")
	ErrBadKind     = errors.New("chat: body_kind is binary or text")
)

// rateLimiter is a fixed one-minute window per sender. In memory on purpose:
// it guards the store, it is not player data, and a restart forgiving one
// minute is harmless.
type rateLimiter struct {
	mu     sync.Mutex
	window time.Time
	counts map[string]int
}

func (r *rateLimiter) allow(sender string, perMinute int, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := now.Truncate(time.Minute)
	if !w.Equal(r.window) || r.counts == nil {
		r.window, r.counts = w, map[string]int{}
	}
	if r.counts[sender] >= perMinute {
		return false
	}
	r.counts[sender]++
	return true
}

func validKey(s string) bool { return s != "" && len(s) <= MaxTitleKeyBytes }

func validInbox(in store.Inbox) error {
	if !validKey(in.Title) || !validKey(in.Network) {
		return ErrTitleArgs
	}
	if (in.Account == "") == (in.Room == "") {
		return ErrTitleArgs
	}
	if in.Account != "" && !uuidRE.MatchString(in.Account) {
		return ErrBadAccount
	}
	if in.Room != "" && len(in.Room) > MaxTitleKeyBytes {
		return ErrTitleArgs
	}
	return nil
}

// SendTitle stores one title message. Not friendship-gated: a game messages
// players its players met in a gathering. A block between the two accounts
// (either direction, the core's answer) refuses it; a room message has no one
// account to ask. Nothing is announced on the event stream: instant delivery
// is the adapter's, and the social chat must never hear of these.
func (s *Service) SendTitle(ctx context.Context, m store.TitleMessage, lifetimeS int64, dedupKey string) (store.TitleMessage, bool, error) {
	if err := validInbox(store.Inbox{Title: m.Title, Network: m.Network, Account: m.ToAccount, Room: m.ToRoom}); err != nil {
		return store.TitleMessage{}, false, err
	}
	if !uuidRE.MatchString(m.FromAccount) {
		return store.TitleMessage{}, false, ErrBadAccount
	}
	if m.FromAccount == m.ToAccount {
		return store.TitleMessage{}, false, ErrSameTalker
	}
	switch m.BodyKind {
	case "", "binary":
		m.BodyKind = "binary"
	case "text":
		if !utf8.Valid(m.Body) {
			return store.TitleMessage{}, false, ErrBadKind
		}
	default:
		return store.TitleMessage{}, false, ErrBadKind
	}
	if len(m.Body) > MaxTitleBodyBytes || len(m.Subject) > MaxTitleSubjectBytes || len(m.SenderName) > MaxTitleNameBytes {
		return store.TitleMessage{}, false, ErrTooLarge
	}
	if !utf8.ValidString(m.Subject) || !utf8.ValidString(m.SenderName) || strings.ContainsRune(m.Subject+m.SenderName, 0) {
		return store.TitleMessage{}, false, ErrTitleArgs
	}
	if len(dedupKey) > MaxTitleKeyBytes || m.ParentID < 0 {
		return store.TitleMessage{}, false, ErrTitleArgs
	}
	lifetime := time.Duration(lifetimeS) * time.Second
	if lifetimeS <= 0 || lifetime > MaxTitleLifetime {
		lifetime = MaxTitleLifetime
	}
	if m.ToAccount != "" {
		blocked, err := s.Core.Blocked(ctx, m.FromAccount, m.ToAccount)
		if err != nil {
			return store.TitleMessage{}, false, fmt.Errorf("chat: block check: %w", err)
		}
		if blocked {
			return store.TitleMessage{}, false, ErrBlocked
		}
	}
	rate := s.TitleRatePerMinute
	if rate <= 0 {
		rate = DefaultTitleRatePerMinute
	}
	if !s.limiter.allow(m.FromAccount, rate, time.Now()) {
		return store.TitleMessage{}, false, ErrRateLimited
	}
	return store.InsertTitle(ctx, s.Store, m, lifetime, dedupKey)
}

// TitleInbox lists a recipient's unexpired messages in one title.
func (s *Service) TitleInbox(ctx context.Context, in store.Inbox, offset int64, limit int32, ids []int64, headersOnly bool) ([]store.TitleMessage, int64, error) {
	if err := validInbox(in); err != nil {
		return nil, 0, err
	}
	return store.ListTitle(ctx, s.Store, in, offset, limit, ids, headersOnly)
}

// DeleteTitle removes messages from a recipient's inbox: ids, or all of it.
func (s *Service) DeleteTitle(ctx context.Context, in store.Inbox, ids []int64, all bool) (int64, error) {
	if err := validInbox(in); err != nil {
		return 0, err
	}
	if all == (len(ids) > 0) || len(ids) > MaxTitleDeleteIDs {
		return 0, ErrTitleArgs
	}
	return store.DeleteTitle(ctx, s.Store, in, ids, all)
}

// CollectTitleGarbage deletes expired title messages every interval until ctx
// ends. Reads already skip expired rows; this keeps the table small.
func (s *Service) CollectTitleGarbage(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := store.DeleteExpiredTitleMessages(ctx, s.Store, 5000); err != nil {
			log.Printf("chat: title message gc: %v", err)
		} else if n > 0 {
			log.Printf("chat: title message gc: %d expired", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
