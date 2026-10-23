# OpenPak chat

The neutral message store (universal-social-prd §4d, platform-wiiu-prd WU-4).
One Postgres table of `{from_account, to_account, kind, body, sent_at}`; the
account core's friendship answer at the door; every send announced on the
core's universal event stream (`type = "message"`, `subject_id` = the message
id) so each family's adapter delivers it in its own transport. A message sent
on the website is the same row a Wii U surface reads — that is the whole
point; nothing console-shaped is stored here.

Surfaces and their owners:

| Client | Surface |
| --- | --- |
| website / phone | internal HTTP (below), proxied by the website under `/api/v1/me/chat` |
| console adapters (`wiiu-chat`, later a 3DS bridge) | gRPC `Chat` |

Ports: 20130 HTTP, 20131 gRPC, 20132 metrics — block 20130 of `ports.md`.
Health: `GET /healthz` on 20130.

## Internal HTTP (X-Internal-Key, the caller decided who the account is)

```
GET  /internal/accounts/{account}/conversations                   one row per peer, newest first
GET  /internal/accounts/{account}/messages/{peer}?after=&limit=   history, oldest first
POST /internal/accounts/{account}/messages/{peer}                 {"body","kind","dedup_key"} -> 201
GET  /internal/messages/{id}
DELETE /internal/accounts/{account}/messages                     account erasure: every message to or from it -> {"deleted":n}
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

## Tests

```sh
podman run -d --name openpak-chat-test -e POSTGRES_PASSWORD=test -e POSTGRES_DB=chat \
  -p 54329:5432 docker.io/library/postgres:18-alpine
CHAT_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:54329/chat?sslmode=disable' \
  go test -race ./...
```

The flow test covers the whole surface against a fake core over bufconn:
strangers refused and unannounced, friends announced exactly once, dedup
replays silent, pagination, and the conversation list.

## Deploying

`ghcr.io/openpak/chat` on the `openpak-v*` tags, like every service. Needs
Postgres (`chat` database) and the core's address + internal key. Nothing
else — no object storage, no MongoDB.
