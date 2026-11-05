# Protocol: chat

Surfaces and their owners:

| Client | Surface |
| --- | --- |
| website / phone | internal HTTP (below), proxied by the website under `/api/v1/me/chat` |
| console adapters (`wiiu-chat`, later a 3DS bridge) | gRPC `Chat` |

## Internal HTTP (X-Internal-Key, the caller decided who the account is)

```
GET  /internal/accounts/{account}/conversations                   one row per peer, newest first
GET  /internal/accounts/{account}/messages/{peer}?after=&limit=   history, oldest first
POST /internal/accounts/{account}/messages/{peer}                 {"body","kind","dedup_key"} -> 201
GET  /internal/messages/{id}
DELETE /internal/accounts/{account}/messages                     account erasure: every message and title message to or from it -> {"deleted":n,"title_messages_deleted":m}
POST /internal/titles/{title}/messages                            title message (below) -> 201
GET  /internal/titles/{title}/messages?network=&account=|room=&offset=&limit=&headers_only=1
POST /internal/titles/{title}/messages/delete                     {"network","account"|"room","ids":[..]|"all":true} -> {"deleted":n}
```

`kind` is `text` (default), `painting`, `screenshot` or `app_data` — the four
payloads the real surfaces carry (Miiverse messages distinguish them; Wii U
Chat sends paintings). The store is neutral about the bytes, not about the
shape: no console formatting, no markup. Bodies cap at 64 KiB.

## gRPC (`openpak.chat.v1.Chat`)

`SendMessage`, `GetMessage`, `ListMessages`, `ListConversations`. Same
credentials as everything internal (`x-internal-key`). `SendMessage` is
idempotent per `(from_account, dedup_key)`: an adapter retrying after a
dropped reply gets `duplicate: true` and the stored message, and the recipient
is not announced twice.

## Rules

- **Only friends talk.** The core's `GetRelationship` is the one answer,
  applied before anything is stored, for every surface alike.
- **Announcement is best effort, storage is not.** The message exists the
  moment `Insert` returns; if the core is unreachable the announcement is
  logged and the message is still visible in every surface's history —
  consoles learn of it at their next sync, as they always have.
- **Event log holds no bodies.** The core's event says "account X has message
  123"; receivers fetch the content here.

## Store

One Postgres table of `{from_account, to_account, kind, body, sent_at}`. Every send is announced on the account core's event stream (`type = "message"`, `subject_id` = the message id) so each family's adapter delivers it in its own transport; a message sent on the website is the same row a Wii U surface reads. Nothing console-shaped is stored.

## Title messages

What a game sends between its own players (NEX Messaging 0x17 and MessageDelivery 0x1B; gap
M1-M5 of `nex/docs/core-backed-storage.md`). gRPC `SendTitleMessage`, `ListTitleMessages`,
`DeleteTitleMessages`; the same on internal HTTP (bodies base64 in JSON).

| Field | Meaning |
| --- | --- |
| `title`, `network` | the game's neutral key (`monster-hunter-4`) and network (`3ds`); every read and delete is scoped by both |
| `from_account` | the sender's core account |
| `to_account` or `to_room` | exactly one: an account's inbox, or a room the game defines (NEX: `gathering:<gathering id>`, recipient type 2). Who may read a room is the adapter's call (NEX: the gathering's participants) |
| `subject`, `sender_name` | UTF-8, at most 1 KiB and 256 bytes |
| `body`, `body_kind` | bytes, at most 64 KiB; `binary` (default) or `text` (must be UTF-8) |
| `flags` | the game's flag word, stored as given |
| `parent_id` | 0, or a live title message of the same title and network (else `FailedPrecondition`) |
| `lifetime_s` | expiry; 0 or more than 30 days means 30 days |
| `dedup_key` | optional, idempotent per `(from_account, dedup_key)` like `SendMessage` |

Rules:

- **Not friendship-gated.** A game messages players met in a gathering. The core's
  `GetRelationship` is still asked for account messages: a block either way refuses
  (`PermissionDenied` / 403). Room messages have no single account to ask.
- **Never in the social chat.** A separate table: `GetMessage`, `ListMessages`,
  `ListConversations` and the website never see a title message, and nothing is announced on the
  event stream (instant delivery to a connected console is the adapter's job).
- **Expiry.** Reads skip expired rows; a collector deletes them every 10 minutes, 5000 at a time.
- **Limits.** 64 KiB body, per-sender rate limit (60 stores a minute, `CHAT_TITLE_RATE_PER_MIN`;
  `ResourceExhausted` / 429), at most 1000 ids per delete, list pages capped at 200.
- **Inbox scope.** `ListTitleMessages(title, network, account | room, offset, limit, ids,
  headers_only)` returns oldest first plus the inbox's unexpired `total`; `ids` narrow the page but
  never reach outside the inbox. `DeleteTitleMessages(..., ids | all)` likewise.
- **Erasure.** The account-erasure call deletes title messages sent by or addressed to the
  account, in the same transaction as its social messages.

Store: table `title_messages` (`id, title, network, from_account, to_account, to_room, subject,
body bytea, body_kind, sender_name, flags, parent_id, dedup_key, sent_at, expires_at`), created at
start-up next to `messages`.
