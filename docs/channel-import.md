# Channel import (cloner integration)

`telesrv` can hold a copy of a broadcast channel that lives somewhere else,
typically on official Telegram: the full history with the original message ids
and dates, and then every new post, edit and deletion as they happen.

The program that reads the source (the *cloner*) is not part of this
repository. It logs in to the source, downloads messages and files, and writes
them here through the Admin API. This document is the contract it implements.
The server side is `internal/app/channelimport`; the routes are
`internal/adminapi/channelimport.go`.

## Access

The routes live on the Admin API listener (port 2599) under
`/v1/channel-imports` and require the `channels.import` permission. Give the
cloner a scoped token that carries only that:

```
TELESRV_ADMIN_SCOPED_TOKENS=cloner:<token>:channels.import
```

Send it as `Authorization: Bearer <token>`. The token cannot call any other
Admin API route.

These routes are not admin commands: there is no `command_id` and no command
audit row. Every write is idempotent by its own key instead, so any request can
be retried:

| Write | Key |
|---|---|
| create an import | `source.channel_id` |
| upload a file | `source_key` |
| write a message | the message `id` at the source |

## Life of an import

```
importing ──finalize──▶ mirroring ──detach──▶ detached
```

- **importing** — backfill. Batches are written without per-message updates
  and without notifications. The channel has no username, so it cannot be found
  or joined; only its owner sees it.
- **mirroring** — the channel is public. New source posts and edits are
  delivered to members like ordinary posts, with push notifications.
- **detached** — the import no longer accepts writes. The channel is an
  ordinary channel; the same source may be imported again into a new one.

### Message ids

A source message keeps its id while that id is still free locally, so replies,
albums and `t.me/<channel>/<id>` numbering line up, and gaps left by deleted
source messages stay gaps.

The channel owner may post locally at any time. Local posts take the next free
id, and from then on a new source message whose id is already taken gets the
next local id instead. The server keeps the mapping: later edits, deletions and
`reply_to_msg_id` are always given in **source** ids, and `local_ids` in the
batch response says where each message landed.

### Dates

A message keeps the `date` it had at the source, however old. Only the update
that announces a mirrored post to members is stamped with the server's time.
Imported posts never expire, even if the channel has an auto-delete timer.

## Errors

Failures carry `{"error": "...", "code": "..."}`.

| Status | `code` | Meaning |
|---|---|---|
| 401 | — | unknown token |
| 403 | `FORBIDDEN` | the token lacks `channels.import` |
| 404 | `channel_import_not_found` | no such import |
| 409 | `channel_import_state` | the import is detached |
| 409 | `channel_import_order` | ids are not ascending, or a never-written id is below `max_source_id` |
| 409 | `channel_import_target` | the target is not an empty broadcast channel |
| 409 | `channel_import_exists` | the target channel already has an import |
| 409 | `channel_import_username` | the username is taken or invalid; the import stays `importing` |
| 400 | `channel_import_owner` | the owner or sender account cannot own a channel |
| 400 | `channel_import_media` | bad upload |
| 400 | `channel_import_invalid` | malformed request |
| 413 | `channel_import_media` | upload over the size limit |

Unknown JSON fields are rejected with 400 rather than ignored.

## Routes

### `POST /v1/channel-imports` — create

Creates the import and, unless `target_channel_id` is given, a new broadcast
channel owned by `channel.owner_user_id`.

```json
{
  "source": {"channel_id": 1234567890, "username": "durov"},
  "channel": {
    "owner_user_id": 10001,
    "title": "Current title",
    "about": "…",
    "created_date": 1420070400,
    "create_title": "Title when the channel was created",
    "create_message_missing": false
  },
  "sender_user_id": 0
}
```

- `owner_user_id` must be an existing ordinary account (not a bot, not deleted).
  It becomes the channel creator and manages the channel from a client.
- `created_date` and `create_title` come from the source's message 1, the
  "channel created" service message. Local message 1 is that same service
  message, so the ids line up from the start. Set `create_message_missing` when
  the source no longer has it; local message 1 is then a gap.
- `sender_user_id` is the account imported posts are stored under. Broadcast
  posts are shown as sent by the channel, so it is never visible; it defaults
  to the owner.
- To import into a channel that already exists, send `target_channel_id`
  instead of `owner_user_id`/`title`. It must be a broadcast channel with
  nothing in it but its own "channel created" message.

Returns 201 with the status object below. If `source.channel_id` already has an
import that is not detached, that import is returned with 200 and
`"existing": true`, and nothing is created.

### `GET /v1/channel-imports/{id}` — status

```json
{
  "import_id": 7,
  "channel_id": 4001,
  "state": "importing",
  "source_channel_id": 1234567890,
  "source_username": "durov",
  "sender_user_id": 10001,
  "max_source_id": 5120,
  "created_at": 1794000000,
  "finalized_at": 0,
  "channel": {"title": "…", "about": "…", "username": "", "owner_user_id": 10001, "date": 1420070400, "top_message_id": 5120},
  "stats": {"messages": 4987, "media": 1203}
}
```

`max_source_id` is the highest source id written so far. A cloner that was
interrupted resumes from the message after it.

### `POST /v1/channel-imports/{id}/media` — upload a file

The request body is the file itself (`application/octet-stream`, not
multipart). Metadata goes in the query string:

| Parameter | |
|---|---|
| `source_key` | required; a stable id of the file at the source, e.g. its `file_unique_id` or `<document id>`. At most 256 characters. |
| `kind` | required; `photo` or `document` |
| `date` | optional unix time of the file at the source |
| `mime_type` | documents only |
| `attributes` | documents only; URL-encoded JSON array |

Attributes use these shapes:

```json
[
  {"kind": "video", "w": 1280, "h": 720, "duration": 12.5, "supports_streaming": true, "round_message": false},
  {"kind": "audio", "audio_duration": 31, "voice": true, "title": "…", "performer": "…"},
  {"kind": "filename", "file_name": "clip.mp4"},
  {"kind": "animated"},
  {"kind": "image_size", "w": 512, "h": 512},
  {"kind": "sticker", "alt": "🙂"}
]
```

A GIF at the source is already an MP4: upload it as `video/mp4` with a `video`
attribute and `animated`. Sticker-set references and custom-emoji attributes
are dropped, because the set does not exist on this server.

Photos are limited to 20 MiB and are decoded to produce the usual sizes.
Documents are limited to 4 GiB and are streamed to storage; a video gets a
generated thumbnail when the server has a thumbnailer and the file is small
enough for it.

Returns 201:

```json
{"source_key": "AgADBAAD", "kind": "document", "media_id": "5870402113990211841", "existing": false}
```

Uploading a `source_key` that is already known returns 200 with
`"existing": true` without reading the body, so a resumed cloner can simply
upload everything again.

### `POST /v1/channel-imports/{id}/messages` — write a batch

```json
{"messages": [
  {
    "id": 2,
    "date": 1420070500,
    "edit_date": 0,
    "text": "Hello",
    "entities": [{"type": "bold", "offset": 0, "length": 5}],
    "media": {"source_key": "AgADBAAD", "spoiler": false},
    "grouped_id": "13000000000000001",
    "reply_to_msg_id": 0,
    "fwd_from": {"from_name": "Some Channel", "date": 1419990000, "from_source_channel_id": 0, "channel_post": 0},
    "post_author": "Editor",
    "views": 12345,
    "forwards": 67,
    "silent": false,
    "noforwards": false,
    "pinned": false,
    "buttons": [[{"text": "Open", "url": "https://example.com"}]]
  },
  {"id": 3, "date": 1420070600, "reply_to_msg_id": 2, "action": {"type": "pin_message"}}
]}
```

Rules:

- At most 500 messages, at most 16 MiB of JSON.
- `id` must be strictly ascending within a batch. A message that was never
  written may not be below `max_source_id`: history is backfilled oldest first.
- A batch is all or nothing.
- Sending a message that is already written is how it is **edited**: if the
  content differs it is updated (and, when mirroring, members get an edit
  update); if it is identical nothing happens. `views` and `forwards` are
  refreshed either way and only ever go up, so re-sending recent posts is the
  way to keep counters current.
- `grouped_id` may be a JSON string, because source values do not fit a JSON
  number exactly. Keep the messages of one album in one batch.
- `reply_to_msg_id` is a source id. If that message was never imported (it was
  deleted at the source) the id is kept and clients show a deleted message.
- Links in `text` such as `t.me/<source>/123` are stored as they are. Rewrite
  them in the cloner if they should point at the copy.

Entities: `bold`, `italic`, `underline`, `strike`, `code`, `pre` (`language`),
`text_url` (`url`), `spoiler`, `blockquote` (`collapsed`), `mention`, `hashtag`,
`cashtag`, `bot_command`, `url`, `email`, `phone`, `bank_card`. Offsets and
lengths are in UTF-16 code units, as in MTProto. Any other type, notably
`custom_emoji` and `mention_name`, is dropped and the text is kept.

Buttons: only URL buttons are kept.

Forwards: with `from_source_channel_id` set to a source channel that has its
own import here, the forward header points at the local copy and `channel_post`
is translated to the local id. Otherwise the header shows `from_name`.

Service messages carry `action` and no text or media:

| `type` | Fields | Notes |
|---|---|---|
| `edit_title` | `title` | |
| `edit_photo` | `photo_source_key` | upload the photo first, as `kind=photo` |
| `delete_photo` | | |
| `pin_message` | | the pinned message is `reply_to_msg_id` |
| `group_call` | `duration` | a finished live stream |
| `group_call_scheduled` | `schedule_date` | |

An imported service message is history only. It does not rename the channel,
change its photo or pin anything: the channel's current state comes from
`create`, `meta`, `photo`, and each message's own `pinned` flag.

Response:

```json
{
  "written": 2,
  "edited": 0,
  "unchanged": 0,
  "skipped": [{"id": 9, "reason": "unsupported action \"giveaway_launch\""}],
  "local_ids": {"2": 2, "3": 3},
  "max_source_id": 3,
  "state": "importing"
}
```

A message is skipped, not rejected, when it has an unsupported `action`, has
neither text nor importable media (a poll, a location, a contact), or refers to
a `source_key` that was never uploaded. Its id stays a gap. Fix the cause and
send it again only if it is still above `max_source_id`.

### `POST /v1/channel-imports/{id}/messages/delete` — delete

```json
{"ids": [17, 18, 99]}
```

Deletes messages that were deleted at the source; at most 1000 source ids.

```json
{"deleted_local_ids": [17, 18], "unknown_ids": [99]}
```

A deleted message does not come back if the cloner sends it again.

### `POST /v1/channel-imports/{id}/finalize` — end the backfill

```json
{"username": "durov_mirror", "reserve_through": 5130}
```

Moves the import to `mirroring`: sets the public username, marks the backfilled
history as read for existing members, and tells connected clients to reload the
channel. Both fields are optional.

`reserve_through` is the highest id the source has ever used, including
messages deleted at its end. Local posts are then numbered above it, so the
next source post can still keep its own id.

If the username is taken the call fails with `channel_import_username` and the
import stays `importing`; call again with another name. Calling it on an import
that is already mirroring changes nothing.

### `POST /v1/channel-imports/{id}/meta` and `/photo` — profile changes

```json
{"title": "New title", "about": "New description"}
```

Omit a field to leave it alone; `"about": ""` clears the description. The
photo route takes the image bytes as the request body. Neither writes a service
message: the source's own "title changed" message arrives through the message
batch as history.

### `POST /v1/channel-imports/{id}/detach` — stop mirroring

No body. The import stops accepting writes; the channel and its history stay.

## A cloner in outline

1. Read the source channel and its message 1. `POST /v1/channel-imports`.
2. Walk the source history upward from `max_source_id + 1`. For each page:
   upload the files, then send the messages. Back off on the source's flood
   limits; nothing here needs to be undone on a retry.
3. `finalize` with the username and the source's highest id.
4. Subscribe to the source's updates: send new and edited messages to
   `/messages`, deletions to `/messages/delete`, profile changes to `/meta` and
   `/photo`. Re-send the most recent posts now and then to refresh `views`.

## Not imported

Reactions, comments (the linked discussion group), polls, and any media that is
not a photo or a document. Custom emoji are shown as their fallback character.
