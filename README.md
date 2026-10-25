# Chat Server

OpenPak's Chat Server.

| | |
| --- | --- |
| Network | Neutral: website, phone and console adapters |
| Role | Neutral message store: one Postgres table of messages, the account core's friendship check, and an event for each send |
| Depends on | `account` (account core), PostgreSQL |
| Used by | Website and phone (internal HTTP), console adapters (gRPC) |
| Image | `ghcr.io/openpak/chat` |
| Licence | AGPL-3.0-only |

## What it serves

| Endpoint | What |
| --- | --- |
| Internal HTTP | Website and phone: conversations, history, send, account erasure (`X-Internal-Key`) |
| gRPC `openpak.chat.v1.Chat` | Console adapters: `SendMessage`, `GetMessage`, `ListMessages`, `ListConversations` |

## Run

```sh
set -a; . ./example.env; set +a   # the service reads its settings from the environment
go run ./cmd/chat
```

## Docs

- [Protocol](docs/protocol.md)
- [Evidence](docs/evidence.md)
- [Deploy](docs/deploy.md)
- [Notes](docs/notes.md)
- [Docs index](docs/README.md)

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).
