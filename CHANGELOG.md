# Changelog — chat

`git log` stays the source of truth; this file is the readable summary.

## v0.1.0 — 2026-09-24

- First release: the neutral message store. One Postgres table of messages (migrated at
  start-up), the account core's friendship answer at the door, every send announced on the
  core's event stream.
- Internal HTTP on 20130 (`X-Internal-Key`) for the website and phone; gRPC `openpak.chat.v1.Chat`
  on 20131 for console adapters; metrics on 20132. `SendMessage` is idempotent per
  `(from_account, dedup_key)`. Bodies cap at 64 KiB.
