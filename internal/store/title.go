package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Title messages (gap M1-M5): what a game sends between its own players. A
// separate table, so nothing that reads `messages` (the social chat, its
// conversations, the website) can ever see one. Bodies are bytes; every row
// expires (expires_at, swept by DeleteExpiredTitleMessages).
const titleSchema = `
CREATE TABLE IF NOT EXISTS title_messages (
    id           BIGSERIAL PRIMARY KEY,
    title        TEXT NOT NULL,
    network      TEXT NOT NULL,
    from_account UUID NOT NULL,
    to_account   UUID,
    to_room      TEXT,
    subject      TEXT NOT NULL DEFAULT '',
    body         BYTEA NOT NULL,
    body_kind    TEXT NOT NULL DEFAULT 'binary' CHECK (body_kind IN ('binary','text')),
    sender_name  TEXT NOT NULL DEFAULT '',
    flags        BIGINT NOT NULL DEFAULT 0,
    parent_id    BIGINT,
    dedup_key    TEXT,
    sent_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    CHECK ((to_account IS NULL) <> (to_room IS NULL)),
    UNIQUE (from_account, dedup_key)
);
CREATE INDEX IF NOT EXISTS idx_title_messages_to ON title_messages (title, network, to_account, id)
    WHERE to_account IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_title_messages_room ON title_messages (title, network, to_room, id)
    WHERE to_room IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_title_messages_from ON title_messages (from_account);
CREATE INDEX IF NOT EXISTS idx_title_messages_expiry ON title_messages (expires_at);
`

type TitleMessage struct {
	ID          int64
	Title       string
	Network     string
	FromAccount string
	ToAccount   string // "" when ToRoom is set
	ToRoom      string
	Subject     string
	Body        []byte
	BodyKind    string
	SenderName  string
	Flags       int64
	ParentID    int64
	SentAt      time.Time
	ExpiresAt   time.Time
}

// Inbox names one recipient's messages in one title: an account or a room.
type Inbox struct {
	Title, Network, Account, Room string
}

const titleCols = `id, title, network, from_account, COALESCE(to_account::text, ''), COALESCE(to_room, ''),
	subject, body, body_kind, sender_name, flags, COALESCE(parent_id, 0), sent_at, expires_at`

func scanTitle(row pgx.Row) (TitleMessage, error) {
	var m TitleMessage
	err := row.Scan(&m.ID, &m.Title, &m.Network, &m.FromAccount, &m.ToAccount, &m.ToRoom,
		&m.Subject, &m.Body, &m.BodyKind, &m.SenderName, &m.Flags, &m.ParentID, &m.SentAt, &m.ExpiresAt)
	return m, err
}

// ErrBadParent: parent_id names no live message of the same title and network.
var ErrBadParent = errors.New("chat: parent message not found in this title")

// InsertTitle stores one title message. With a dedup key the first write wins
// and a repeat returns the original with duplicate=true.
func InsertTitle(ctx context.Context, q pgxQuerier, m TitleMessage, lifetime time.Duration, dedupKey string) (TitleMessage, bool, error) {
	if m.BodyKind == "" {
		m.BodyKind = "binary"
	}
	if m.ParentID != 0 {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM title_messages
			WHERE id = $1 AND title = $2 AND network = $3 AND expires_at > now())`,
			m.ParentID, m.Title, m.Network).Scan(&ok); err != nil {
			return TitleMessage{}, false, err
		}
		if !ok {
			return TitleMessage{}, false, ErrBadParent
		}
	}
	row := q.QueryRow(ctx, `INSERT INTO title_messages (title, network, from_account, to_account, to_room,
			subject, body, body_kind, sender_name, flags, parent_id, dedup_key, expires_at)
		VALUES ($1, $2, $3, NULLIF($4,'')::uuid, NULLIF($5,''), $6, $7, $8, $9, $10, NULLIF($11,0), NULLIF($12,''),
			now() + make_interval(secs => $13))
		ON CONFLICT (from_account, dedup_key) DO NOTHING
		RETURNING `+titleCols,
		m.Title, m.Network, m.FromAccount, m.ToAccount, m.ToRoom, m.Subject, m.Body, m.BodyKind,
		m.SenderName, m.Flags, m.ParentID, dedupKey, lifetime.Seconds())
	out, err := scanTitle(row)
	if err == nil {
		return out, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) || dedupKey == "" {
		return TitleMessage{}, false, err
	}
	out, err = scanTitle(q.QueryRow(ctx, `SELECT `+titleCols+` FROM title_messages
		WHERE from_account = $1 AND dedup_key = $2`, m.FromAccount, dedupKey))
	if err != nil {
		return TitleMessage{}, false, err
	}
	return out, true, nil
}

// inboxWhere is the WHERE clause of one unexpired inbox, args $1..$3.
func inboxWhere(in Inbox) (string, []any) {
	if in.Room != "" {
		return `title = $1 AND network = $2 AND to_room = $3 AND expires_at > now()`, []any{in.Title, in.Network, in.Room}
	}
	return `title = $1 AND network = $2 AND to_account = $3::uuid AND expires_at > now()`, []any{in.Title, in.Network, in.Account}
}

// ListTitle returns an inbox page, oldest first, and the inbox's total. ids,
// when given, narrows the page to those messages (still inside the inbox).
func ListTitle(ctx context.Context, q pgxQuerier, in Inbox, offset int64, limit int32, ids []int64, headersOnly bool) ([]TitleMessage, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	where, args := inboxWhere(in)
	var total int64
	if err := q.QueryRow(ctx, `SELECT count(*) FROM title_messages WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	cols := titleCols
	if headersOnly {
		cols = `id, title, network, from_account, COALESCE(to_account::text, ''), COALESCE(to_room, ''),
	subject, ''::bytea, body_kind, sender_name, flags, COALESCE(parent_id, 0), sent_at, expires_at`
	}
	sql := `SELECT ` + cols + ` FROM title_messages WHERE ` + where
	if len(ids) > 0 {
		args = append(args, ids)
		sql += ` AND id = ANY($4)`
	}
	args = append(args, offset, limit)
	sql += ` ORDER BY id OFFSET $` + strconv.Itoa(len(args)-1) + ` LIMIT $` + strconv.Itoa(len(args))
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []TitleMessage{}
	for rows.Next() {
		m, err := scanTitle(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

// DeleteTitle deletes messages from an inbox: the given ids, or all of it.
func DeleteTitle(ctx context.Context, q pgxExecer, in Inbox, ids []int64, all bool) (int64, error) {
	where, args := inboxWhere(in)
	where = where[:len(where)-len(` AND expires_at > now()`)] // expired rows may go too
	if !all {
		args = append(args, ids)
		where += ` AND id = ANY($4)`
	}
	tag, err := q.Exec(ctx, `DELETE FROM title_messages WHERE `+where, args...)
	return tag.RowsAffected(), err
}

// DeleteExpiredTitleMessages is the garbage collector: expired rows, in
// batches so one sweep never holds a long lock.
func DeleteExpiredTitleMessages(ctx context.Context, q pgxExecer, batch int) (int64, error) {
	var total int64
	for {
		tag, err := q.Exec(ctx, `DELETE FROM title_messages WHERE id IN (
			SELECT id FROM title_messages WHERE expires_at <= now() LIMIT $1)`, batch)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < int64(batch) {
			return total, nil
		}
	}
}
