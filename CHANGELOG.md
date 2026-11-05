# Changelog — chat

`git log` stays the source of truth; this file is the readable summary.

## v0.3.0 — 2026-11-05

- Title messages (gap M1-M5, card openpak/prds#138): `SendTitleMessage`, `ListTitleMessages`,
  `DeleteTitleMessages` on gRPC and `/internal/titles/{title}/messages` on internal HTTP. A
  separate `title_messages` table (migrated at start-up, additive): scoped by title and network,
  to an account or to a room (`gathering:<id>`), binary or text bodies up to 64 KiB, subject,
  sender name, flags, `parent_id` threads within a title, expiry by `lifetime_s` (capped at 30
  days) with a garbage collector every 10 minutes. Not friendship-gated; a block between the two
  accounts refuses. Never announced and never visible to the social chat. Per-sender rate limit
  (60 a minute, `CHAT_TITLE_RATE_PER_MIN`).
- Account erasure also deletes every title message the account sent or that was addressed to it;
  the response adds `title_messages_deleted` (`deleted` still counts social messages).

## v0.2.0 — 2026-11-03
- Per-request logging: one slog line per HTTP request (method, path, status, ms) and per gRPC unary call (method, code, ms) on stderr via internal/httplog. Query strings, headers and bodies are never logged; /healthz and /metrics are skipped.

## v0.1.1 — 2026-11-03

- `DELETE /internal/accounts/{account}/messages` for account erasure: every message the account
  sent or received goes (both sides of each conversation). The website's erasure finaliser calls
  it once the deletion grace period is over; a repeat is a no-op.

## v0.1.0 — 2026-09-24

- First release: the neutral message store. One Postgres table of messages (migrated at
  start-up), the account core's friendship answer at the door, every send announced on the
  core's event stream.
- Internal HTTP on 20130 (`X-Internal-Key`) for the website and phone; gRPC `openpak.chat.v1.Chat`
  on 20131 for console adapters; metrics on 20132. `SendMessage` is idempotent per
  `(from_account, dedup_key)`. Bodies cap at 64 KiB.
