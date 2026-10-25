# Notes: chat

## Purpose

The neutral message store (universal-social-prd §4d, platform-wiiu-prd WU-4).
One Postgres table of `{from_account, to_account, kind, body, sent_at}`; the
account core's friendship answer at the door; every send announced on the
core's universal event stream (`type = "message"`, `subject_id` = the message
id) so each family's adapter delivers it in its own transport. A message sent
on the website is the same row a Wii U surface reads — that is the whole
point; nothing console-shaped is stored here.
