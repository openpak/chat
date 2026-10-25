# Deploy: chat

Ports: 20130 HTTP, 20131 gRPC, 20132 metrics — block 20130 of `ports.md`.
Health: `GET /healthz` on 20130.

## Tests

```sh
podman run -d --name openpak-chat-test -e POSTGRES_PASSWORD=test -e POSTGRES_DB=chat \
  -p 54329:5432 docker.io/library/postgres:18-alpine
CHAT_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:54329/chat?sslmode=disable' \
  go test -race ./...
```

## Deploying

`ghcr.io/openpak/chat` on the `openpak-v*` tags, like every service. Needs
Postgres (`chat` database) and the core's address + internal key. Nothing
else — no object storage, no MongoDB.
