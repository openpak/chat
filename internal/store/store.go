// Package store holds the chat messages. One Postgres database (`chat`), one
// table: the neutral message is the whole model. Nothing console-shaped
// (PIDs, friend codes, Miiverse ids, NEX frames) is stored here; adapters map
// through the account core, never through this schema.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Message struct {
	ID          int64
	FromAccount string
	ToAccount   string
	Kind        string
	Body        string
	SentAt      time.Time
}

var (
	ErrNotFound    = errors.New("chat: not found")
	ErrUnknownKind = errors.New("chat: unknown kind")
)

// DB opens the pool and migrates. Chat's schema is one table; the migration
// runs here so a deployment never needs a second tool.
func DB(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

const schema = `
CREATE SEQUENCE IF NOT EXISTS messages_id_seq;

CREATE TABLE IF NOT EXISTS messages (
    id           BIGINT PRIMARY KEY DEFAULT nextval('messages_id_seq'),
    from_account UUID NOT NULL,
    to_account   UUID NOT NULL,
    kind         TEXT NOT NULL DEFAULT 'text'
                 CHECK (kind IN ('text','painting','screenshot','app_data')),
    body         TEXT NOT NULL,
    dedup_key    TEXT,
    sent_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (from_account, dedup_key)
);
CREATE INDEX IF NOT EXISTS idx_messages_pair ON messages
    (LEAST(from_account, to_account), GREATEST(from_account, to_account), id);
CREATE INDEX IF NOT EXISTS idx_messages_to ON messages (to_account, id);
`

// ValidKind says whether kind is one the neutral store carries. Empty means
// text everywhere; nothing console-specific is ever stored in kind or body.
func ValidKind(kind string) bool {
	switch kind {
	case "", "text", "painting", "screenshot", "app_data":
		return true
	}
	return false
}

// Insert stores one message and returns it. With a non-empty dedup key the
// first write wins; a later attempt with the same key returns the stored
// original and duplicate=true, and the caller does not re-announce it.
func Insert(ctx context.Context, q pgxQuerier, from, to, kind, body, dedupKey string) (Message, bool, error) {
	if !ValidKind(kind) {
		return Message{}, false, ErrUnknownKind
	}
	if kind == "" {
		kind = "text"
	}
	m := Message{FromAccount: from, ToAccount: to, Kind: kind, Body: body}
	err := q.QueryRow(ctx, `INSERT INTO messages (from_account, to_account, kind, body, dedup_key)
		VALUES ($1, $2, $3, $4, NULLIF($5,''))
		ON CONFLICT (from_account, dedup_key) DO NOTHING
		RETURNING id, sent_at`, from, to, kind, body, dedupKey).Scan(&m.ID, &m.SentAt)
	if err == nil {
		return m, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Message{}, false, err
	}
	// Conflict: only reachable with a dedup key (NULL keys never collide).
	if dedupKey == "" {
		return Message{}, false, errors.New("chat: insert failed without a conflict target")
	}
	if err := q.QueryRow(ctx, `SELECT id, kind, body, sent_at FROM messages
		WHERE from_account = $1 AND dedup_key = $2`, from, dedupKey).
		Scan(&m.ID, &m.Kind, &m.Body, &m.SentAt); err != nil {
		return Message{}, false, err
	}
	return m, true, nil
}

// ByID fetches one message.
func ByID(ctx context.Context, q pgxQuerier, id int64) (Message, error) {
	var m Message
	err := q.QueryRow(ctx, `SELECT id, from_account, to_account, kind, body, sent_at
		FROM messages WHERE id = $1`, id).
		Scan(&m.ID, &m.FromAccount, &m.ToAccount, &m.Kind, &m.Body, &m.SentAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	return m, err
}

// Conversation lists the exchange between account and peer, oldest first,
// strictly after afterID, capped at limit (server cap 200). The caller has
// already checked that account may read this conversation.
func Conversation(ctx context.Context, q pgxQuerier, account, peer string, afterID int64, limit int32) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := q.Query(ctx, `SELECT id, from_account, to_account, kind, body, sent_at FROM messages
		WHERE ((from_account = $1 AND to_account = $2) OR (from_account = $2 AND to_account = $1))
		AND id > $3 ORDER BY id LIMIT $4`, account, peer, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.FromAccount, &m.ToAccount, &m.Kind, &m.Body, &m.SentAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type ConversationHead struct {
	Peer   string
	Newest Message
}

// Conversations returns one row per peer with the newest message in that
// exchange, newest message first.
func Conversations(ctx context.Context, q pgxQuerier, account string) ([]ConversationHead, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (peer) peer, id, from_account, to_account, kind, body, sent_at
		FROM (
			SELECT CASE WHEN from_account = $1 THEN to_account ELSE from_account END AS peer,
			       id, from_account, to_account, kind, body, sent_at
			FROM messages WHERE from_account = $1 OR to_account = $1
		) t ORDER BY peer, id DESC`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byPeer := map[string]Message{}
	for rows.Next() {
		var peer string
		var m Message
		if err := rows.Scan(&peer, &m.ID, &m.FromAccount, &m.ToAccount, &m.Kind, &m.Body, &m.SentAt); err != nil {
			return nil, err
		}
		if prev, ok := byPeer[peer]; !ok || m.ID > prev.ID {
			byPeer[peer] = m
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []ConversationHead{}
	for peer, m := range byPeer {
		out = append(out, ConversationHead{Peer: peer, Newest: m})
	}
	// Newest message first, deterministically.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Newest.ID > out[j-1].Newest.ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// pgxQuerier is what pgxpool.Pool and pgx.Tx both satisfy.
// DeleteForAccount erases every message the account sent or received (account
// erasure): a conversation with an erased account goes from both sides.
func DeleteForAccount(ctx context.Context, pool *pgxpool.Pool, account string) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM messages WHERE from_account = $1 OR to_account = $1`, account)
	return tag.RowsAffected(), err
}

type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}
