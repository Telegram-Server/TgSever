# HTTP Bot API: supported methods

This document lists every Bot API method that the telesrv HTTP gateway
(`internal/botapi`, default port `8081`) actually implements, together with
the parameters it accepts, the extensions it adds, and the official methods it
deliberately does not support.

Russian version of this document: [bot-api.ru.md](./bot-api.ru.md).

## How to use this document

- [Transport](#transport)
- [Authentication](#authentication)
- [Request and response conventions](#request-and-response-conventions)
- [Errors](#errors)
- [Method summary](#method-summary)
- [Updates](#updates)
- [Methods: bot identity and commands](#methods-bot-identity-and-commands)
- [Methods: bot profile](#methods-bot-profile)
- [Methods: sending](#methods-sending)
- [Methods: media groups, polls, dice, chat actions](#methods-media-groups-polls-dice-chat-actions)
- [Methods: rich messages](#methods-rich-messages)
- [Methods: ephemeral messages](#methods-ephemeral-messages)
- [Methods: forwarding and copying](#methods-forwarding-and-copying)
- [Methods: editing and deleting](#methods-editing-and-deleting)
- [Methods: chat queries](#methods-chat-queries)
- [Methods: chat management](#methods-chat-management)
- [Methods: files](#methods-files)
- [Methods: webhooks](#methods-webhooks)
- [Methods: menus, emoji status, web apps](#methods-menus-emoji-status-web-apps)
- [Methods: payments](#methods-payments)
- [Markup and entities](#markup-and-entities)
- [Limits](#limits)
- [Additional Bot API 10.3 methods](#additional-bot-api-103-methods)
- [Compatibility boundaries](#compatibility-boundaries)
- [Demos and self-tests](#demos-and-self-tests)

## Transport

| Item | Value |
|---|---|
| Method endpoint | `http(s)://<host>/bot<TOKEN>/<METHOD>` |
| File download | `http(s)://<host>/file/bot<TOKEN>/<file_path>` |
| Listener setting | `TELESRV_BOT_API_ADDR`; an empty value disables the gateway |
| Request body limit | 25 MiB upload + 1 MiB overhead (`FILE_TOO_BIG`) |
| Accepted content types | `application/json`, `multipart/form-data`, `application/x-www-form-urlencoded` |

`<METHOD>` is matched case-insensitively. Both `GET` and `POST` reach the
dispatcher, so `urlencoded` GET requests work.

Client configuration:

- `python-telegram-bot`: pass `--base-url http://host:8081/bot` and
  `--base-file-url http://host:8081/file/bot`.
- `aiogram`: `TelegramAPIServer.from_base("http://host:8081")`.
- grammY: `new Api(token, { apiRoot: "http://host:8081" })`.

## Authentication

The path token must be the bot's `<bot_id>:<secret>` pair and its secret must
match the value stored for that bot. Anything else returns HTTP `401` with
`ACCESS_TOKEN_INVALID`. There is no separate auth header; the token in the URL
is the credential, exactly like the official gateway.

File downloads (`/file/bot...`) are authenticated the same way.

## Request and response conventions

Success:

```json
{"ok": true, "result": {}}
```

Failure:

```json
{"ok": false, "error_code": 400, "description": "CHAT_ID_INVALID"}
```

`error_code` is the HTTP status code. `description` is an upper-case
`SCREAMING_SNAKE` error marker, not always the official Telegram wording —
match on the marker, not on the whole string.

Field decoding is generic: JSON bodies are flattened to strings, non-string
JSON values are re-marshalled to their JSON form, and `multipart` uses the
first value of each field. This is why several methods accept both the
`message_text` shorthand and the full `InputTextMessageContent` object, and
why `setChatMenuButton` accepts both a nested `menu_button` object and a flat
body carrying `type`.

`chat_id` as `@username`: the media group / poll / dice / chat action
methods, the edit methods `editMessageCaption`, `editMessageMedia`,
`editMessageReplyMarkup` and `stopPoll`, the forwarding methods (both
`chat_id` and `from_chat_id`), the chat queries and the chat management
methods accept either a numeric id or `@username`. A username resolves only
to a **public supergroup or channel** — users are addressed by numeric id,
as in the official gateway. An unknown username is `USERNAME_NOT_OCCUPIED`,
a malformed one is `CHAT_ID_INVALID` or `USERNAME_INVALID`. The older
methods (`sendMessage`, the `send<Media>` family, `sendRichMessage`,
`editMessageText`, `deleteMessage`, the ephemeral methods, `sendInvoice`)
still take a numeric `chat_id` only.

Chat ids follow the official numbering: positive for users,
`-100<channel_id>` for supergroups and channels. telesrv has **no legacy basic
groups**, so every group is a supergroup.

## Errors

Markers the gateway can return directly:

| Marker | HTTP | Notes |
|---|---:|---|
| `ACCESS_TOKEN_INVALID` | 401 | bad or unknown token |
| `METHOD_NOT_FOUND` | 404 / 501 | unknown method, or the feature is not wired in this deployment |
| `BAD_REQUEST` | 400 | unclassified failure, or an unparsable object parameter (for example `permissions`) |
| `BOT_INVALID` | 400 | the call is not valid for this bot (also returned for `setMyName` with an empty name) |
| `CHAT_ID_INVALID` | 400 | `chat_id` missing, zero, unparsable, or of the wrong kind for the method (for example a private chat passed to a group-only method) |
| `USERNAME_NOT_OCCUPIED`, `USERNAME_INVALID` | 400 | `@username` in `chat_id` does not resolve |
| `PEER_ID_INVALID` | 400 | the bot cannot reach the target chat |
| `MESSAGE_ID_INVALID` | 400 | `message_id` missing or `<= 0`, or no usable message at that id |
| `MESSAGE_IDS_EMPTY` | 400 | `message_ids` missing or an empty array |
| `MESSAGE_IDENTIFIER_INVALID` | 400 | `inline_message_id` mixed with `chat_id`/`message_id` |
| `MESSAGE_EMPTY` | 400 | no `text` given |
| `MESSAGE_TOO_LONG` | 400 | over the text limit |
| `MESSAGE_NOT_MODIFIED` | 400 | edit that changes nothing |
| `MESSAGE_AUTHOR_REQUIRED`, `EDIT_MESSAGES_FORBIDDEN` | 400 | the bot may not edit that message |
| `MESSAGE_POLL_CLOSED` | 400 | `stopPoll` on a poll that is already closed |
| `MEDIA_PREV_INVALID` | 400 | `editMessageMedia` on a message that is not a photo/document message |
| `CHAT_FORWARDS_RESTRICTED` | 400 | forwarding or copying protected content |
| `REPLY_PARAMETERS_INVALID` | 400 | conflicting reply selectors |
| `REPLY_MESSAGE_ID_INVALID` | 400 | negative or malformed reply target |
| `MESSAGE_THREAD_ID_INVALID` | 400 | ephemeral top-message id out of range |
| `ENTITY_INVALID`, `ENTITY_BOUNDS_INVALID`, `ENTITY_TYPE_UNSUPPORTED`, `ENTITIES_TOO_LONG` | 400 | entity list problems |
| `FILE_ID_INVALID`, `FILE_TOO_BIG`, `MEDIA_INVALID`, `MEDIA_EMPTY`, `PHOTO_INVALID`, `MEDIA_CAPTION_TOO_LONG` | 400 | file/media problems |
| `MEDIA_GROUP_INVALID` | 400 | `media` is not 2–10 items, or mixes types that cannot share an album |
| `POLL_QUESTION_INVALID`, `POLL_ANSWERS_INVALID`, `POLL_ANSWER_INVALID`, `POLL_OPTION_INVALID`, `OPTIONS_TOO_MUCH`, `OPTION_INVALID`, `QUIZ_CORRECT_ANSWERS_INVALID`, `POLL_TYPE_INVALID`, `POLL_CLOSE_INVALID` | 400 | `sendPoll` problems |
| `EMOTICON_INVALID` | 400 | `sendDice` with an unsupported emoji |
| `ACTION_INVALID` | 400 | `sendChatAction` with an unknown action |
| `BUTTON_INVALID`, `BUTTON_TYPE_INVALID`, `BUTTON_DATA_INVALID`, `BUTTON_URL_INVALID` | 400 | markup problems |
| `RICH_MESSAGE_INVALID`, `RICH_MESSAGE_TOO_LONG`, `RICH_MESSAGE_DATE_INVALID`, `RICH_MESSAGE_BLOCKS_UNSUPPORTED`, `RICH_MESSAGE_MEDIA_UNSUPPORTED`, `RICH_MESSAGE_OPTION_UNSUPPORTED` | 400 | rich message problems |
| `WEBPAGE_MEDIA_EMPTY` | 400 | rich HTML referenced remote media the local blob backend cannot materialize |
| `EFFECT_ID_INVALID`, `VALUE_INVALID`, `LIMIT_INVALID` | 400 | bad numeric parameter |
| `BOT_COMMAND_INVALID` | 400 | malformed or oversized `commands` |
| `BOT_COMMAND_SCOPE_UNSUPPORTED` | 400 | non-default scope, or any `language_code` |
| `LANG_CODE_NOT_SUPPORTED` | 400 | `setMyName` / `setMyDescription` / `setMyShortDescription` with a non-empty `language_code` |
| `NAME_TOO_LONG`, `DESCRIPTION_TOO_LONG`, `ABOUT_TOO_LONG` | 400 | bot profile text over its limit |
| `EPHEMERAL_MESSAGE_ID_INVALID`, `EPHEMERAL_TARGET_REQUIRED`, `EPHEMERAL_ACTION_EXPIRED` | 400 | ephemeral addressing problems |
| `QUERY_ID_INVALID`, `USER_ID_INVALID`, `RESULT_ID_INVALID`, `RESULT_ID_EMPTY`, `RESULT_TYPE_INVALID` | 400 | query/user/result problems |
| `USER_NOT_PARTICIPANT`, `PARTICIPANT_ID_INVALID` | 400 | the target user is not (or cannot be) a member |
| `USER_ADMIN_INVALID`, `USER_NOT_ADMIN`, `RIGHT_FORBIDDEN`, `USER_CREATOR`, `ADMINS_TOO_MUCH`, `ADMIN_RANK_INVALID` | 400 | promotion / custom title problems |
| `CHAT_NOT_MODIFIED`, `CHAT_TITLE_EMPTY`, `TITLE_INVALID`, `CHAT_ABOUT_TOO_LONG`, `CHAT_ABOUT_NOT_MODIFIED` | 400 | chat title / description / permissions problems |
| `CHANNEL_INVALID`, `CHANNEL_PRIVATE`, `USER_BANNED_IN_CHANNEL`, `MEGAGROUP_ID_INVALID`, `FROZEN_METHOD_INVALID` | 400 | the chat cannot be managed by this bot |
| `OFFSET_INVALID` | 400 | `getUpdates` offset below `-10000` |
| `ALLOWED_UPDATES_INVALID` | 400 | not a JSON array of up to 100 strings |
| `ALLOWED_UPDATES_UNSUPPORTED` | 501 | gateway without the update-control service |
| `MAX_CONNECTIONS_INVALID` | 400 | outside `1..100` |
| `SECRET_TOKEN_INVALID` | 400 | characters outside `[A-Za-z0-9_-]`, or longer than 256 |
| `WEBHOOK_URL_INVALID` | 400 | not an absolute `http(s)` URL, longer than 2048, has userinfo/fragment, or a port outside `1..65535` |
| `CERTIFICATE_PINNING_UNSUPPORTED` | 400 | `certificate` / uploaded certificate supplied |
| `IP_ADDRESS_UNSUPPORTED` | 400 | `ip_address` supplied |
| `WEBHOOK_UNSUPPORTED` | 501 | gateway without the webhook service |
| `METHOD_NOT_FOUND` | 501 | `answerShippingQuery` |
| `BLOCKED_USER_EMOJI_STATUS_SERVICE_MISSING`, `BLOCKED_WEBAPP_QUERY_SERVICE_MISSING`, `BLOCKED_PREPARED_INLINE_SERVICE_MISSING` | 501 | optional service not wired |
| `USER_PERMISSION_DENIED` | 403 | emoji status without the required user grant |
| `PREMIUM_ACCOUNT_REQUIRED` | 400 | emoji status without Premium |
| `PREMIUM_GIFT_SELF_INVALID`, `PREMIUM_GIFT_CODE_INVALID`, `BALANCE_TOO_LOW`, `STAR_COUNT_INVALID`, `MONTH_COUNT_INVALID` | 400 | premium gift problems |
| `GIFT_ID_INVALID`, `GIFT_NOT_AVAILABLE`, `GIFT_UPGRADE_UNAVAILABLE` | 400 | `sendGift` problems |
| `STAR_GIFT_UNAVAILABLE` | 500 | the gift subsystem itself is unavailable |
| `CURRENCY_INVALID`, `INVOICE_AMOUNT_INVALID`, `INVOICE_INVALID`, `INVOICE_ALREADY_PAID`, `INVOICE_REFUNDED` | 400 | `sendInvoice` / `refundStarPayment` problems |
| `IDEMPOTENCY_KEY_INVALID` | 400 | conflicting `request_id`/`Idempotency-Key`, or bad charset/length |
| `PAYMENT_FORM_INVALID` | 400 | payment form rejected |
| `CHAT_WRITE_FORBIDDEN`, `CHAT_ADMIN_REQUIRED` | 400 | rights missing |
| `USER_BOT_REQUIRED` | 400 | target is not a bot user |
| `CONFLICT: ...` | 409 | concurrent poll or webhook delivery |
| `INTERNAL_SERVER_ERROR` | 500 / 503 | store or transport failure |

A TL error that the gateway does not know is folded into `BAD_REQUEST`; the
list above is the whitelist that survives.

## Method summary

The gateway registers **186 HTTP method names**: **184 implemented official
Bot API 10.3 methods**, the `dropPendingUpdates` extension, and the recognized
but blocked `answerShippingQuery` (HTTP 501). File download is a separate
route, not another official method. The table below covers the existing core;
the [additional method tables](#additional-bot-api-103-methods) enumerate all
106 newly implemented methods and form the rest of this summary.

| Group | Methods |
|---|---|
| Bot & commands | `getMe`, `setMyCommands`, `deleteMyCommands`, `getMyCommands` |
| Bot profile | `setMyName`, `getMyName`, `setMyDescription`, `getMyDescription`, `setMyShortDescription`, `getMyShortDescription` |
| Updates | `getUpdates`, `dropPendingUpdates` (telesrv extension) |
| Text | `sendMessage`, `sendRichMessage` |
| Media | `sendPhoto`, `sendAnimation`, `sendAudio`, `sendDocument`, `sendLivePhoto`, `sendSticker`, `sendVideo`, `sendVideoNote`, `sendVoice` |
| Albums, polls, dice, actions | `sendMediaGroup`, `sendPoll`, `sendDice`, `sendChatAction` |
| Location | `sendContact`, `sendLocation`, `sendVenue` |
| Forwarding | `forwardMessage`, `forwardMessages`, `copyMessage`, `copyMessages` |
| Edit/delete | `editMessageText`, `editMessageCaption`, `editMessageMedia`, `editMessageReplyMarkup`, `stopPoll`, `deleteMessage` |
| Ephemeral edit/delete | `editEphemeralMessageText`, `editEphemeralMessageMedia`, `editEphemeralMessageCaption`, `editEphemeralMessageReplyMarkup`, `deleteEphemeralMessage` |
| Callback | `answerCallbackQuery` |
| Chat queries | `getChat`, `getChatMember`, `getChatAdministrators`, `getChatMemberCount` |
| Chat management | `banChatMember`, `unbanChatMember`, `restrictChatMember`, `promoteChatMember`, `setChatAdministratorCustomTitle`, `setChatPermissions`, `pinChatMessage`, `unpinChatMessage`, `unpinAllChatMessages`, `setChatTitle`, `setChatDescription`, `setChatPhoto`, `deleteChatPhoto`, `leaveChat` |
| Files | `getFile`, `GET /file/bot<TOKEN>/<file_path>` |
| Webhooks | `setWebhook`, `deleteWebhook`, `getWebhookInfo` |
| Menus & status | `setChatMenuButton`, `getChatMenuButton`, `setUserEmojiStatus` |
| Web apps & inline | `answerWebAppQuery`, `savePreparedInlineMessage` |
| Payments & gifts | `giftPremiumSubscription`, `getAvailableGifts`, `sendGift`, `sendInvoice`, `answerPreCheckoutQuery`, `refundStarPayment` |
| Blocked by design | `answerShippingQuery` (HTTP 501) |

The method groups added on top of the core switch (albums/polls/dice/actions,
the extra edit methods, forwarding, chat queries, chat management, bot profile)
each depend on an optional gateway interface; a deployment that does not wire
one answers that group with `501 METHOD_NOT_FOUND`.

## Updates

Updates include the following core kinds:

| `allowed_updates` value | Update key | Delivered by default |
|---|---|---|
| `message` | `message` | yes |
| `edited_message` | `edited_message` | yes |
| `callback_query` | `callback_query` | yes |
| `pre_checkout_query` | `pre_checkout_query` | yes |
| `my_chat_member` | `my_chat_member` | yes |
| `chat_member` | `chat_member` | **no** — only when listed explicitly |
| `message_reaction` | `message_reaction` | **no** — only when listed explicitly |
| `message_reaction_count` | `message_reaction_count` | **no** — only when listed explicitly |
| `chat_join_request` | `chat_join_request` | yes |
| `channel_post`, `edited_channel_post` | same as kind | yes |
| `poll`, `poll_answer` | same as kind | yes |
| `inline_query`, `chosen_inline_result`, `guest_message` | same as kind | yes |
| `business_connection`, `business_message`, `edited_business_message`, `deleted_business_messages` | same as kind | yes |
| `managed_bot`, `purchased_paid_media` | same as kind | yes |
| `chat_boost`, `removed_chat_boost` | recognized filter names; no new producer | not a delivery guarantee |

Extended object updates retain durable event-time JSON snapshots rather than
reconstructing old content from the latest message. Subscription/payment,
suggested-post and draft lifecycle events use their corresponding message or
object projections. Recognition of an update name is not proof that this
server produces it; in particular boost producers are not implemented.

`allowed_updates` is a JSON array of up to 100 strings, accepted both by
`getUpdates` and by `setWebhook`, and it persists per bot. The filter is
applied when an update is queued: with an explicit list, kinds that are not
listed are never queued (changing the list does not touch what is already in
the queue). An empty array `[]` resets the bot to the default set, which is
every kind above except `chat_member`, `message_reaction` and
`message_reaction_count`. Unknown names parse successfully but never match anything.

Ephemeral (Bot API 10.2) messages arrive as normal `message` updates, but
their `message_id` belongs to the ephemeral namespace rather than the chat's
message numbering, so ordinary `reply_to_message_id` will not address them.
Replies go back through `reply_parameters.ephemeral_message_id`; see
[Methods: ephemeral messages](#methods-ephemeral-messages).

`Message` projection notes:

- `chat` of a broadcast channel is `type: "channel"` and carries `title` and
  `username` when the update has the channel at hand; supergroups are
  `type: "supergroup"`;
- album items carry `media_group_id` (a string);
- `sendDice` results carry `dice` (`emoji`, `value`);
- forwarded messages carry `forward_origin` of type `user`, `chat`,
  `channel` (with `message_id`) or `hidden_user`. `sender_user`,
  `sender_chat` and `chat` inside the origin are built from the id only.
- private live-location edits and checklist toggles, appends and replacements
  produce `edited_message`; a user's permitted edit of a bot-authored checklist
  is also delivered to that bot.

### Reactions and join requests

- `message_reaction` carries the human actor's `old_reaction` and
  `new_reaction`. The counterpart bot receives private-chat changes;
  supergroup/channel bots must be administrators. Reactions set by bots do
  not generate identified reaction updates.
- Anonymous channel changes use `message_reaction_count` with the committed
  aggregate counts instead of exposing the actor. Administrative reaction
  removal follows the same delivery rules.
- `chat_join_request` goes only to administrator bots with `InviteUsers`.
  Approval links include `invite_link`; direct requests to a public group
  omit it. The request's bio and invite are captured when queued, so
  processing or deleting the pending request does not erase the queued event.
- These payload updates share the same durable `update_id` cursor and
  confirmation path in `getUpdates` and Webhook delivery.


### `my_chat_member` and `chat_member`

Both carry an official `ChatMemberUpdated`: `chat`, `from` (who made the
change), `date`, `old_chat_member`, `new_chat_member`, and optionally
`invite_link`, `via_join_request` / `via_chat_folder_invite_link`.

- `my_chat_member` — the bot's own status changed in a supergroup or channel
  (added, promoted, restricted, banned, left). Delivered by default.
- `chat_member` — another member's status changed. Delivered only when
  `allowed_updates` lists `chat_member` explicitly **and** the bot is an
  administrator (or the creator) of that chat at the time of the change.

Known differences from the official gateway:

- member updates exist only for supergroups and channels; there is no
  `my_chat_member` for a user blocking or unblocking the bot in a private chat;
- approval through an invite link returns both `invite_link` and
  `via_join_request: true`, as required by
  [the local compatibility specification](https://github.com/Telegram-Server/TgSever/issues/47).
  This differs from the [official field description](https://core.telegram.org/bots/api/types/chatmemberupdated.md),
  which reserves `via_join_request` for requests without an invite link.

The old member state preserves the actual previous restriction/ban state.
`can_be_edited` is computed from the recipient bot's membership snapshot;
legacy queued rows without that snapshot fall back to `false`.

Queue retention is controlled by `TELESRV_BOT_API_UPDATE_RETENTION`
(default `24h`); acknowledged updates are dropped earlier.

## Methods: bot identity and commands

### `getMe`

No parameters. Returns the bot's own `User` object.

### `setMyCommands`

| Parameter | Type | Notes |
|---|---|---|
| `commands` | JSON array | up to 100 entries of `{command, description, is_ephemeral}` |
| `scope` | object | only `{"type":"default"}` is accepted |
| `language_code` | string | must be empty |

`is_ephemeral=true` marks the command as an ephemeral one: invoking it in a
group produces a message only the caller can see, and the bot must answer
inside the ephemeral action window. Returns `true`.

### `deleteMyCommands`

Same scope rules as `setMyCommands`; clears the list. Returns `true`.

### `getMyCommands`

Same scope rules. Returns the stored array; entries include
`is_ephemeral: true` only when the flag is set.

## Methods: bot profile

### `setMyName`, `setMyDescription`, `setMyShortDescription`

| Method | Parameter | Limit | Too long |
|---|---|---|---|
| `setMyName` | `name` | 64 characters | `NAME_TOO_LONG` |
| `setMyDescription` | `description` | 512 characters | `DESCRIPTION_TOO_LONG` |
| `setMyShortDescription` | `short_description` | 120 characters | `ABOUT_TOO_LONG` |

They go through MTProto `bots.setBotInfo` (the short description is the
MTProto `about`), so other clients see the change. Returns `true`.

Differences from the official gateway:

- `language_code` is not supported: only the default-language profile is
  stored, and a non-empty `language_code` is `LANG_CODE_NOT_SUPPORTED`
  instead of being stored per language;
- `setMyName` cannot clear the name: an empty `name` is rejected (`BOT_INVALID`).
  An empty description or short description clears it, as officially.

### `getMyName`, `getMyDescription`, `getMyShortDescription`

Return `{"name": ...}`, `{"description": ...}` and
`{"short_description": ...}`. `language_code` is accepted but ignored: the
default-language value is always returned.

## Methods: sending

### `sendMessage`

| Parameter | Type | Notes |
|---|---|---|
| `chat_id` | integer | required, non-zero; negative for groups/channels |
| `text` | string | required, up to 4096 characters after formatting |
| `parse_mode` | string | `HTML`, `Markdown`, `MarkdownV2`, or omitted for plain text |
| `entities` | JSON array | used when `parse_mode` is absent; ignored otherwise |
| `reply_markup` | object | any of the four constructors, see [Markup](#markup-and-entities) |
| `disable_web_page_preview` | boolean | |
| `disable_notification` | boolean | |
| `reply_to_message_id` | integer | |
| `ephemeral_message_parameters` | object | official nested receiver/callback addressing; see [Ephemeral messages](#methods-ephemeral-messages) |
| `reply_parameters` | object | ordinary message reply, or ephemeral reply with nested ephemeral addressing |
| `message_thread_id` | integer | forum/private topic, or ephemeral discussion top message |

The shared send-options parser also handles `business_connection_id`,
`direct_messages_topic_id`, `suggested_post_parameters`, `allow_paid_broadcast`,
`protect_content` and `message_effect_id` on the handlers that use it. These
are validated and authorized, not silently downgraded. A topic must belong to
the addressed chat; private topics remain scoped to the bot/user conversation.
Business sends require a live connection and the relevant recipient/right.
Ordinary `reply_parameters` supports message, cross-chat and quote metadata,
`allow_sending_without_reply`, and checklist/poll selectors where applicable.
Conflicting legacy and nested reply selectors are rejected.

### `sendPhoto`, `sendAnimation`, `sendAudio`, `sendDocument`, `sendSticker`, `sendVideo`, `sendVideoNote`, `sendVoice`

All share one handler.

| Parameter | Type | Notes |
|---|---|---|
| `chat_id` | integer | required |
| `<kind>` | string / file | the method's own field: `photo`, `animation`, `audio`, `document`, `sticker`, `video`, `video_note`, `voice` |
| `caption` | string | up to 1024 characters |
| `parse_mode`, `caption_entities` | | as in `sendMessage` |
| `reply_markup` | object | all four constructors |
| `disable_notification`, `reply_to_message_id` | | |
| `width`, `height`, `duration`, `title`, `performer` | integer / string | accepted and forwarded as metadata hints |
| `emoji` | string | sticker only |
| `ephemeral_message_parameters`, `reply_parameters`, `message_thread_id` | | nested ephemeral addressing or ordinary topic/reply addressing |

The file field accepts, in this order of precedence:

1. `attach://<field>` referencing a multipart file part;
2. an uploaded multipart part with the same field name;
3. an `http://` or `https://` URL (the server fetches it);
4. a `file_id` previously returned by `getFile`.

Anything else is `FILE_ID_INVALID`.

### `sendLivePhoto`

Two media fields: `photo` (still frame) and `live_photo` (the video part).
Both accept upload, `attach://`, or `file_id`. The video part **cannot** be an
HTTP URL — that is `FILE_ID_INVALID`, matching the official restriction.
Captions, `parse_mode`, and nested ephemeral addressing work as for the other
media methods.

### `sendContact`, `sendLocation`, `sendVenue`

These three support ordinary sends as well as explicitly addressed ephemeral
sends. Omitting ephemeral parameters never makes a private-only send public.

`sendContact` takes `chat_id`, `phone_number`, `first_name`, optional
`last_name`, and `vcard` (up to 2048 bytes), plus an optional inline
`reply_markup`.

`sendLocation` takes `chat_id`, `latitude` (`-90..90`), `longitude`
(`-180..180`), `horizontal_accuracy` (`0..1500`, default `0`), and live-location
options `live_period`, `heading`, `proximity_alert_radius`. Live locations can
be edited or stopped using the methods listed below; stopped/expired locations
cannot be revived.

`sendVenue` adds required `title` and `address`, and accepts either
`foursquare_id` + `foursquare_type` or `google_place_id` +
`google_place_type`.

## Methods: media groups, polls, dice, chat actions

`sendMediaGroup`, `sendPoll` and `sendDice` share these parameters:

| Parameter | Type | Notes |
|---|---|---|
| `chat_id` | integer / `@username` | required |
| `disable_notification`, `protect_content` | boolean | |
| `reply_parameters` | object | `{"message_id": N}`; the legacy `reply_to_message_id` is also accepted, but not both |
| `reply_markup` | object | `sendPoll` and `sendDice` only |

These handlers use the shared validated message options (including topic,
business, effects and paid broadcast options) rather than silently ignoring
them. Not every method supports every combination: incompatible ephemeral,
business or topic addressing is rejected instead of changing visibility.

### `sendMediaGroup`

`media` is a JSON array of 2–10 `InputMedia` objects (`MEDIA_GROUP_INVALID`
otherwise). Each item takes `type`, `media`, `caption` (up to 1024
characters), `parse_mode`, `caption_entities`, `has_spoiler`, and the
`width` / `height` / `duration` / `title` / `performer` hints. `media`
accepts `attach://<field>`, an `http(s)` URL, or a `file_id`.

Album rules: `photo` and `video` may be mixed; `audio` and `document` can only
form albums of their own type. Any other `type` (including `animation`) is
`MEDIA_GROUP_INVALID`. Returns the array of sent `Message` objects, all sharing
one `media_group_id`.

### `sendPoll`

| Parameter | Type | Notes |
|---|---|---|
| `question` | string | required, up to 255 characters; `question_parse_mode`, `question_entities` |
| `options` | JSON array | strings or `InputPollOption` objects (`text`, `text_parse_mode`, `text_entities`); each up to 100 characters |
| `type` | string | `regular` (default) or `quiz` |
| `is_anonymous` | boolean | default `true` |
| `allows_multiple_answers` | boolean | |
| `correct_option_id` | integer | required for `quiz`, rejected otherwise |
| `explanation` | string | up to 200 characters; `explanation_parse_mode`, `explanation_entities` |
| `open_period` / `close_date` | integer | mutually exclusive (`POLL_CLOSE_INVALID`) |
| `is_closed` | boolean | send an already closed poll |

The number of options is checked by the MTProto handler
(`POLL_ANSWERS_INVALID`, `OPTIONS_TOO_MUCH`). Returns the sent `Message`.

### `sendDice`

`emoji` defaults to `🎲`; the accepted set is `🎲`, `🎯`, `🏀`, `⚽`, `🎳`,
`🎰` (`EMOTICON_INVALID` otherwise). Returns the `Message` with `dice`.

### `sendChatAction`

`chat_id`, `action` — one of `typing`, `upload_photo`, `record_video`,
`upload_video`, `record_voice`, `upload_voice`, `upload_document`,
`choose_sticker`, `find_location`, `record_video_note`,
`upload_video_note` (`ACTION_INVALID` otherwise). An optional
`message_thread_id` is passed through as the top message id. Returns `true`.

## Methods: rich messages

### `sendRichMessage`

Official Bot API 10.3 rich text, backed by Layer 228. Sends `InputRichMessage`
instead of a plain formatted string, so tables, headings, dividers, details
blocks and footers reach the client as real rich blocks.

| Parameter | Type | Notes |
|---|---|---|
| `chat_id` | integer | required |
| `rich_message` | object | `{"html": "..."}` **or** `{"markdown": "..."}`, plus optional `is_rtl` and `skip_entity_detection` |
| `reply_markup` | object | inline keyboards only |
| `reply_parameters` | object | `{"message_id": N}`; the legacy `reply_to_message_id` is also accepted, but not both |
| `disable_notification`, `protect_content` | boolean | |
| `message_effect_id` | integer | message effect id |

Rules and rejections:

- exactly one of `html` / `markdown` must be non-empty; zero or both is
  `RICH_MESSAGE_INVALID`;
- `blocks` is parsed but rejected — `RICH_MESSAGE_BLOCKS_UNSUPPORTED`;
- `media` is parsed but rejected — `RICH_MESSAGE_MEDIA_UNSUPPORTED`;
- the source is capped at 256 KiB;
- shared message options are validated and use their normal authorization;
  specifying a connection or topic does not bypass membership or recipient
  checks;
- rich HTML that references remote media (`img`, `video`, `audio`, `tg-map`,
  `tg-collage`, `tg-slideshow`) fails with `WEBPAGE_MEDIA_EMPTY`, because the
  local blob backend cannot materialize an arbitrary URL atomically. The
  documented workaround (see `cmd/bots/bedolagaformat`) is to retry once with
  the logo removed rather than degrade to a classic menu.

Supported HTML elements: `b`/`strong`, `i`/`em`, `u`/`ins`,
`s`/`strike`/`del`, `tg-spoiler`, `span` (only as
`<span class="tg-spoiler">`), `a`, `code`, `pre` (with `class="language-…"`),
`blockquote`, `tg-emoji`, `tg-time`, `footer`, and `table` (with the
`bordered` and `striped` attributes, per-cell alignment).
`tg-time` requires `unix` in `1..2^31-1` and a `format` built from `t`, `T`,
`d`, `D`, `w`/`W`, or exactly `r`/`R` for a relative timestamp.

Markdown input is parsed by the same rich builder used on the MTProto side,
so `#`-style headings, `---` dividers, pipe tables, `>` quotes, fenced code
and footer blocks all work.

`editMessageText` accepts the same `rich_message` object, and there is no
separate `editMessageRichMessage` method.

## Methods: ephemeral messages

Ephemeral messages (Bot API 10.2) are visible only to their receiver, who
sees a "only visible to you" notice. telesrv supports the whole lifecycle.

Sending uses the official nested object, for example:

```json
{"chat_id":-100123,"text":"Only for you","ephemeral_message_parameters":{"receiver_user_id":456}}
```

Text, media, contact, location and venue handlers support this addressing.
The old top-level sending fields `receiver_user_id`, `callback_query_id` and
`replace_callback_query_message` are rejected with
`EPHEMERAL_MESSAGE_PARAMETERS_INVALID`, not interpreted as ordinary sends.

Addressing rules:

- `ephemeral_message_parameters.receiver_user_id` is a required positive id;
- its optional `callback_query_id` is a string; the callback must authorize
  that receiver, and `replace_callback_query_message` requires a callback;
- `reply_parameters.ephemeral_message_id` requires the nested object and
  cannot be combined with a callback or ordinary reply selectors;
- business, direct-message-topic and suggested-post addressing cannot be
  combined with ephemeral sending;
- `message_thread_id` is the top message of the ephemeral discussion group;
- the action window is 15 seconds — after it the server answers
  `EPHEMERAL_ACTION_EXPIRED`.

Editing and deleting:

| Method | Parameters |
|---|---|
| `editEphemeralMessageText` | `chat_id`, `receiver_user_id`, `ephemeral_message_id`, `text`, `parse_mode`, `entities`, `reply_markup` |
| `editEphemeralMessageCaption` | same, with `caption` + `caption_entities` |
| `editEphemeralMessageMedia` | same, plus `media` (see below) |
| `editEphemeralMessageReplyMarkup` | same, plus `reply_markup` only |
| `deleteEphemeralMessage` | `chat_id`, `receiver_user_id`, `ephemeral_message_id` |

`media` is an `InputMedia`-like object: `type` (one of `photo`, `animation`,
`audio`, `document`, `video`, `live_photo`), the file field, and optional
`caption`, `parse_mode`, `caption_entities`, `width`, `height`, `duration`,
`title`, `performer`. `live_photo` uses `photo` for the still and `media` for
the video. Raw uploads are not allowed here — only `file_id`, `attach://`-free
file references, or HTTPS URLs (`FILE_ID_INVALID` otherwise).

`reply_markup` is inline-only, and its *presence* matters: sending
`reply_markup` with an empty value clears the keyboard.

## Methods: forwarding and copying

All four methods act as the bot: the bot must be able to see the source
messages in `from_chat_id` (a chat it is a member of, or its own private chat
with a user) and to write to `chat_id`. Both accept a numeric id or
`@username`.

| Method | Parameters | Result |
|---|---|---|
| `forwardMessage` | `chat_id`, `from_chat_id`, `message_id`, `disable_notification`, `protect_content` | `Message` |
| `forwardMessages` | `chat_id`, `from_chat_id`, `message_ids`, `disable_notification`, `protect_content` | array of `MessageId` |
| `copyMessage` | `chat_id`, `from_chat_id`, `message_id`, `caption`, `parse_mode`, `caption_entities`, `reply_markup`, `reply_parameters` / `reply_to_message_id`, `disable_notification`, `protect_content` | `MessageId` |
| `copyMessages` | `chat_id`, `from_chat_id`, `message_ids`, `remove_caption`, `disable_notification`, `protect_content` | array of `MessageId` |

- `message_ids` is a JSON array of 1–100 positive ids; it is de-duplicated and
  processed in ascending order. An empty array is `MESSAGE_IDS_EMPTY`.
- As officially, ids that cannot be found or cannot be forwarded/copied are
  skipped; only when nothing is left is the call `MESSAGE_ID_INVALID`.
  Service messages are never forwarded; service messages, invoices and
  giveaways are never copied.
- `copyMessage`: a `caption` key that is present replaces the original caption
  (an empty string removes it); a missing key keeps it. The caption is checked
  against the 4096-character text limit, not the 1024 caption limit.

Differences from the official gateway:

- forwarding/copying use validated message options where supported; topic and
  paid-broadcast options are not a permission or content-protection bypass;
- a plain copy is a forward without the author header and keeps albums
  intact; a copy that changes the caption, uses `remove_caption`, or adds
  `reply_markup` is a **re-send** of the source content, so an album copied
  that way arrives as separate messages without a shared `media_group_id`;
- protected content (a chat or message with forwarding restricted, or a
  private chat whose owner disabled forwarding) fails the whole call with
  `CHAT_FORWARDS_RESTRICTED`.

## Methods: editing and deleting

### `editMessageText`

Covers both ordinary and rich text edits, and both chat messages and inline
messages.

| Parameter | Type | Notes |
|---|---|---|
| `chat_id` + `message_id` | | one addressing mode |
| `inline_message_id` | string | the other mode; must not be mixed with the first |
| `text` | string | one content form |
| `rich_message` | object | the other content form; must not be mixed with `text` |
| `parse_mode`, `entities` | | as in `sendMessage` |
| `reply_markup` | object | inline only; presence clears when empty |
| `disable_web_page_preview` | boolean | |

For an `inline_message_id` the result is a boolean; for a chat message it is
the updated `Message`. An edit that changes nothing returns
`MESSAGE_NOT_MODIFIED`.

### `editMessageCaption`, `editMessageReplyMarkup`, `editMessageMedia`

All three accept `chat_id` (integer or `@username`) + `message_id`, or
`inline_message_id` (mixing them is `MESSAGE_IDENTIFIER_INVALID`). The result
is the edited `Message` for a chat message and `true` for an inline message.
`reply_markup` is inline-only and its presence matters: present but empty
clears the keyboard, absent keeps it.

- `editMessageCaption` — `caption` (up to 1024 characters), `parse_mode`,
  `caption_entities`, `reply_markup`.
- `editMessageReplyMarkup` — `reply_markup` only; omitting it removes the
  keyboard.
- `editMessageMedia` — `media` is one `InputMedia` (`animation`, `audio`,
  `document`, `photo`, `video`; `MEDIA_INVALID` otherwise) with its own
  `caption` / `parse_mode` / `caption_entities` / `has_spoiler`, plus
  `reply_markup`. The file may be an upload via `attach://`, a URL, or a
  `file_id`. For a chat message only messages that already carry a photo or a
  document (which includes video, audio and animation files) can have their
  media replaced; text, poll, dice and other messages are `MEDIA_PREV_INVALID`.

Live-location editing and stopping are implemented; see the additional
message methods below.

### `stopPoll`

`chat_id` + `message_id` (there is no inline form), optional `reply_markup`.
Closes a poll the bot sent and returns the final `Poll`. A poll that is
already closed is `MESSAGE_POLL_CLOSED`; a message without a poll is
`MESSAGE_ID_INVALID`.

### `deleteMessage`

`chat_id` + `message_id`. Returns `true` only after deletion. Private message
IDs are checked against the specified conversation before revoke deletion.
Supergroups/channels use the normal MTProto deletion permissions: a bot can
delete its own messages without administrator rights, and needs
`DeleteMessages` to delete someone else's. Missing or wrong-chat messages
return HTTP 400 with `Bad Request: message to delete not found`; permission
denials use `Bad Request: message can't be deleted`. Bot deletions are audited.

### `answerCallbackQuery`

| Parameter | Type | Notes |
|---|---|---|
| `callback_query_id` | string | required |
| `text` | string | notification text |
| `url` | string | |
| `show_alert` | boolean | modal instead of toast |
| `cache_time` | integer | seconds |

Returns `true`.

## Methods: chat queries

All four read as the bot, with the bot as the viewer. A chat the bot cannot
see (never joined, kicked, deleted) is `CHAT_ID_INVALID`.

### `getChat`

`chat_id` — a positive user id or a `-100…` supergroup/channel id (any other
negative id is `CHAT_ID_INVALID`). Returns a `ChatFullInfo` with this subset of
fields:

- private chats: `id`, `type: "private"`, `first_name`, `last_name`,
  `username`, `bio`, `birthdate`, `accent_color_id`,
  `background_custom_emoji_id`, `profile_accent_color_id`,
  `profile_background_custom_emoji_id`, `emoji_status_custom_emoji_id`,
  `emoji_status_expiration_date`, `max_reaction_count`,
  `accepted_gift_types`. `bio` and `birthdate` honour the user's privacy
  settings towards the bot;
- supergroups and channels (`type` is `channel` for broadcast channels,
  `supergroup` otherwise): `id`, `type`, `title`, `username`, `description`,
  `is_forum`, `has_protected_content`, `has_hidden_members`, `linked_chat_id`,
  `message_auto_delete_time`, `paid_message_star_count`,
  `available_reactions`, the colour and emoji status fields, `max_reaction_count`,
  `accepted_gift_types`;
- supergroups only: `permissions`, `slow_mode_delay`, `unrestrict_boost_count`,
  `join_to_send_messages`, `join_by_request`, and — only when the bot is an
  administrator — `has_visible_history` and `has_aggressive_anti_spam_enabled`.

Differences from the official gateway:

- not returned: `photo`, `active_usernames`, `invite_link`, `pinned_message`,
  `sticker_set_name`, `can_set_sticker_set`, `custom_emoji_sticker_set_name`,
  `location`, `personal_chat`, the `business_*` fields,
  `has_private_forwards`, `has_restricted_voice_and_video_messages`,
  `can_send_paid_media` and the other fields not listed above;
- `accepted_gift_types` is a fixed rule, not the user's real settings: users
  accept every gift type including Premium, channels accept every gift type
  except Premium, supergroups accept none.

### `getChatMember`

`chat_id` (supergroup or channel), `user_id`. Returns a `ChatMember` in one of
the six official shapes (`creator`, `administrator`, `member`, `restricted`,
`left`, `kicked`). A user who is not in the chat is `status: "left"`, not an
error.

### `getChatAdministrators`

`chat_id` (supergroup or channel). Returns the creator and every
administrator. Other bots are omitted unless `return_bots=true`; the calling
bot itself is always included.

In both methods `can_be_edited` of an `administrator` is `true` only when the
calling bot is the chat's **creator**: the server does not record who promoted
an admin, so an admin bot with `can_promote_members` conservatively gets
`false`.

### `getChatMemberCount`

`chat_id` (supergroup or channel). Returns an integer.

## Methods: chat management

Every method acts as the bot through the corresponding MTProto handler, so
rights checks, service messages and update fan-out are the same as for a
human admin. Missing rights are `CHAT_ADMIN_REQUIRED`. Every method returns
`true`.

telesrv has no legacy basic groups: everything except pinning works only on
supergroups and channels. A private-chat `chat_id` passed to a group-only
method is `CHAT_ID_INVALID`. Pinning methods also work in private chats.

| Method | Parameters | Notes |
|---|---|---|
| `banChatMember` | `chat_id`, `user_id`, `until_date`, `revoke_messages` | `until_date` under 30 seconds or over 366 days from now means forever |
| `unbanChatMember` | `chat_id`, `user_id`, `only_if_banned` | as officially, without `only_if_banned` an active member is removed (and free to rejoin) |
| `restrictChatMember` | `chat_id`, `user_id`, `permissions`, `use_independent_chat_permissions`, `until_date` | `permissions` is a `ChatPermissions` object; missing fields are `false`; unparsable is `BAD_REQUEST` |
| `promoteChatMember` | `chat_id`, `user_id`, every `can_*` right and `is_anonymous` | all rights `false` demotes |
| `setChatAdministratorCustomTitle` | `chat_id`, `user_id`, `custom_title` | 0–16 characters (`ADMIN_RANK_INVALID`); the target must already be an admin (`USER_NOT_ADMIN`) |
| `setChatPermissions` | `chat_id`, `permissions`, `use_independent_chat_permissions` | supergroups |
| `pinChatMessage` | `chat_id`, `message_id`, `disable_notification` | also private chats |
| `unpinChatMessage` | `chat_id`, `message_id` (optional) | without `message_id` unpins the most recent pin |
| `unpinAllChatMessages` | `chat_id` | also private chats |
| `setChatTitle` | `chat_id`, `title` | 1–128 characters (`CHAT_TITLE_EMPTY` / `TITLE_INVALID`) |
| `setChatDescription` | `chat_id`, `description` | 0–255 characters (`CHAT_ABOUT_TOO_LONG`) |
| `setChatPhoto` | `chat_id`, `photo` | multipart upload only |
| `deleteChatPhoto` | `chat_id` | |
| `leaveChat` | `chat_id` | |

With `use_independent_chat_permissions=false` (the default), any media, poll,
"other messages" or web-preview permission also allows plain text messages, as
officially.

Differences from the official gateway:

- `banChatMember` deletes the user's message history only when
  `revoke_messages` is explicitly `true`; omitted or `false` keeps it.
  Officially the history is always revoked in supergroups and channels;
- if a ban with `revoke_messages=true` is made by a bot that has the
  ban right but not the delete right, the ban takes effect but the call
  returns `CHAT_ADMIN_REQUIRED` (the history is not deleted);
- `setChatPhoto` accepts only a file uploaded with this request (`photo` as a
  multipart file, or `attach://<field>`); a `file_id` or URL is
  `PHOTO_INVALID`;
- there are no basic groups, so methods and errors specific to them do not
  exist.

## Methods: files

### `getFile`

`file_id` must be a telesrv-issued id. The result always has
`file_unique_id == file_id` and `file_path == file_id`, so the download URL is
`/file/bot<TOKEN>/<file_id>`. Foreign or unknown ids are `FILE_ID_INVALID`.

### `GET /file/bot<TOKEN>/<file_path>`

Streams the file, chunk by chunk, with the stored MIME type and
`Content-Length`. The path is the `file_id`; a path with extra segments is
`404 FILE_NOT_FOUND`.

## Methods: webhooks

### `setWebhook`

| Parameter | Type | Notes |
|---|---|---|
| `url` | string | empty value deletes the webhook instead |
| `secret_token` | string | `[A-Za-z0-9_-]`, up to 256 characters |
| `max_connections` | integer | `1..100`, default `40` |
| `allowed_updates` | JSON array | see [Updates](#updates) |
| `drop_pending_updates` | boolean | |

`certificate` (and any uploaded certificate part) is rejected with
`CERTIFICATE_PINNING_UNSUPPORTED`; use a publicly trusted certificate.
`ip_address` is rejected with `IP_ADDRESS_UNSUPPORTED` — the gateway uses the
configured address list.

Setting a webhook while `getUpdates` is long-polling, or while a delivery is
in flight, returns `409 CONFLICT`.

Delivery behaviour:

- one HTTP `POST` per update, with a **single `Update` object** as the body
  (not an array like `getUpdates` returns);
- header `X-Telegram-Bot-Api-Secret-Token` carries the secret token;
- `Content-Type: application/json`; any `2xx` counts as delivered;
- redirects are never followed, so the secret cannot leak to another host;
- up to `max_connections` updates are delivered in parallel, 16 bots and 64
  concurrent HTTP requests overall;
- failures retry with exponential backoff from 1s up to 5 minutes, plus a
  small per-bot jitter;
- delivery is confirmed up to the first failed update, so nothing is skipped.

### `getWebhookInfo`

Returns `url`, `has_custom_certificate` (always `false` here),
`pending_update_count`, and — when a webhook is configured — `max_connections`,
`allowed_updates`, `last_error_date`, `last_error_message`.

### `deleteWebhook`

`drop_pending_updates` is optional. Returns `409` while a delivery is active.

## Methods: menus, emoji status, web apps

### `setChatMenuButton` / `getChatMenuButton`

`menu_button` is `{"type": "default"}`, `{"type": "commands"}`, or
`{"type": "web_app", "text": "...", "web_app": {"url": "..."}}`. A flat body
that carries `type` at the top level is also accepted, which keeps older
library serializers working. Setting returns `true`; getting returns the
current button (never `null` — an unset button reads as `{"type":
"default"}`).

### `setUserEmojiStatus`

`user_id`, `emoji_status_custom_emoji_id`, `emoji_status_expiration_date`.
Requires the user to have granted the bot permission to set their status
(otherwise `403 USER_PERMISSION_DENIED`) and to be a Premium user
(`PREMIUM_ACCOUNT_REQUIRED`). Returns `true`.

### `answerWebAppQuery`

`web_app_query_id` plus `result`. Only `InlineQueryResult` of type `article`
is supported: `id` (up to 64 characters), `title`, `description`, `url` (HTTPS
only), `input_message_content` (or the `message_text` shorthand),
`reply_markup` (inline only). Other result types are
`RESULT_TYPE_INVALID`. The response contains `inline_message_id` when the
answer produced one.

### `savePreparedInlineMessage`

`user_id`, `result` (same `article` restriction), and the peer filters
`allow_user_chats`, `allow_bot_chats`, `allow_group_chats`,
`allow_channel_chats`. Returns `{id, expiration_date}`.

## Methods: payments

### `giftPremiumSubscription`

Pays for a Premium gift out of the bot's Stars balance, through the same
durable Stars pipeline as MTProto `payments.sendStarsForm`.

| Parameter | Type | Notes |
|---|---|---|
| `user_id` | integer | recipient; self-gifts are rejected |
| `month_count` | integer | `1..120` |
| `star_count` | integer | positive, at most 10^15 |
| `text` | string | up to 128 characters |
| `text_parse_mode`, `text_entities` | | restricted entity set |
| `request_id` | string | telesrv extension for idempotency |

Send an `Idempotency-Key` HTTP header (or the local `request_id` field) — the
allowed charset is `[A-Za-z0-9]` plus `-`, `_`, `.`, `:` up to 128
characters. Replaying the same key with the same request returns the stored
result; replaying it with a different recipient, plan, price, or text is
`IDEMPOTENCY_KEY_INVALID`. Requests without any key still work, but each
retry charges again, so always send a key.

### `getAvailableGifts`

No parameters. Returns `{"gifts": [...]}` — the server's enabled Star Gift
catalog, the same list MTProto `payments.getStarGifts` serves. Each gift has
`id`, `star_count`, `sticker`, `upgrade_star_count` (only while an upgrade can
actually be bought) and, for limited gifts, `total_count` and
`remaining_count`. Sold-out or not-yet-released gifts still appear; read
`remaining_count` before buying.

### `sendGift`

`gift_id` (required), exactly one of `user_id` / `chat_id` (neither or both is
`CHAT_ID_INVALID`), `pay_for_upgrade`, `text` (up to 128 characters),
`text_parse_mode`, `text_entities`. Paid from the bot's Stars balance. The
same `Idempotency-Key` / `request_id` extension as `giftPremiumSubscription`
applies. Returns `true`. `STAR_GIFT_UNAVAILABLE` (HTTP 500) means the gift
subsystem itself is down, not that the request was wrong.

### `sendInvoice`

Stars invoices only: `currency` must be `XTR` (or omitted;
`CURRENCY_INVALID` otherwise). Takes `chat_id` (numeric), `title`,
`description`, `payload`, and the price as either a scalar `price` or a
single-entry `prices` array (`INVOICE_AMOUNT_INVALID` otherwise). Returns the
sent `Message`. Shipping, tips, photos and provider tokens are not modelled.

### `refundStarPayment`

`user_id`, `telegram_payment_charge_id` (from `successful_payment`). Refunds
exactly the Stars that the charge credited; `user_id` must match the payer
recorded on the invoice. Returns `true`.

### `dropPendingUpdates`

Implemented: discards the bot's unconfirmed queued updates and answers `true`.
A bot that has fallen behind needs it, otherwise it spends forever working through
a backlog while `pre_checkout_query`, which lives 10 seconds, quietly expires.

### `answerPreCheckoutQuery`

Implemented. Before any Stars move, the payer waits for the bot's answer for **at
most 10 seconds** (Telegram's requirement). The bot receives a `pre_checkout_query`
update and answers with `pre_checkout_query_id`, `ok` and, when `ok=false`, an
`error_message`.

- `ok=true` — the Stars are charged and the bot receives `successful_payment`;
- `ok=false` — the payment is cancelled and `error_message` is shown to the payer;
- no answer within 10 seconds — the payment does not go through (`PAYMENT_FAILED`).

The timeout never degrades into charging anyway: a silent bot means a refusal,
otherwise pre-checkout would verify nothing. Only the bot that owns the order may
answer; a stale `pre_checkout_query_id` gets `QUERY_ID_INVALID`. The currency and
`invoice_payload` in the answer are not compared against what the bot recorded at
`sendInvoice`, so that check stays on the bot side, same as in Telegram.

### `answerShippingQuery`

Recognized, but answers HTTP `501 METHOD_NOT_FOUND`: a Stars payment has no
shipping step, so there is nothing to answer.

## Markup and entities

`reply_markup` accepts exactly one constructor:

| Constructor | Notes |
|---|---|
| `inline_keyboard` | rows of buttons |
| `keyboard` | reply keyboard; `resize_keyboard`, `one_time_keyboard`, `is_persistent`, `input_field_placeholder`, `selective` |
| `remove_keyboard` | `remove_keyboard: true`, `selective` |
| `force_reply` | `force_reply: true`, `input_field_placeholder`, `selective` |

Inline keyboard buttons (exactly one action per button, otherwise
`BUTTON_INVALID`): `url`, `callback_data` (up to 64 bytes),
`web_app`, `login_url` (`url`, `bot_username`, `forward_text`,
`request_write_access`), `switch_inline_query`,
`switch_inline_query_current_chat`, `switch_inline_query_chosen_chat`,
`copy_text`. Every button also accepts `text` (required),
`style` (`primary` / `danger` / `success` / omitted), and
`icon_custom_emoji_id`. Unknown fields are rejected rather than ignored.

Reply keyboard buttons: a plain string, or an object with `text` plus one of
`request_contact`, `request_location`, `request_poll` (`type`),
`request_users` (`request_id`, `user_is_bot`, `user_is_premium`,
`max_quantity`, `request_name`, `request_username`, `request_photo`),
`request_chat` (`request_id`, `chat_is_channel`, `chat_is_forum`,
`chat_has_username`, `chat_is_created`, `user_administrator_rights`,
`bot_administrator_rights`, `bot_is_member`, `request_title`,
`request_username`, `request_photo`), or `web_app`.

Message entities (for `entities` / `caption_entities`, and for the
`date_time` entity, which takes `unix_time` + `date_time_format`):

`bold`, `italic`, `underline`, `strikethrough`, `code`, `pre` (`language`),
`text_link` (`url`), `text_mention` (`user.id`), `spoiler`, `blockquote`,
`expandable_blockquote`, `custom_emoji` (`custom_emoji_id`), `mention`,
`hashtag`, `cashtag`, `bot_command`, `url`, `email`, `phone_number`,
`bank_card_number`, `date_time`.

Limits: 256 entities per message (`ENTITIES_TOO_LONG`), offsets in UTF-16 code
units, unknown types are `ENTITY_TYPE_UNSUPPORTED`.

## Limits

| Limit | Value |
|---|---|
| Text length | 4096 characters after parsing |
| Caption length | 1024 characters (`copyMessage` caption: 4096) |
| Entities per message | 256 |
| Inline result id | 64 bytes |
| Callback data | 64 bytes |
| Commands per `setMyCommands` | 100 |
| Upload size | 25 MiB per file |
| `allowed_updates` entries | 100 |
| `getUpdates` `limit` | default and maximum 100 |
| `getUpdates` `timeout` | clamped to 0..50 seconds |
| `max_connections` | 1..100, default 40 |
| Rich message source | 256 KiB |
| `getFile` offset/limit | offset ignored; size reported in full |
| `sendMediaGroup` items | 2..10 |
| `forwardMessages` / `copyMessages` ids | 1..100 |
| Poll question / option / explanation | 255 / 100 / 200 characters |
| Bot name / description / short description | 64 / 512 / 120 characters |
| Chat title / description | 1..128 / 0..255 characters |
| Admin custom title | 0..16 characters |

## MTProto prerequisites for Bot API adapters

[Issue #37](https://github.com/Telegram-Server/TgSever/issues/37) tracks
the shared MTProto/app/store foundations used by the HTTP adapters documented
here. Photo/boost privacy and the new adapters are included in this upgrade.
- Main invites have explicit identity; ordinary creation never promotes a
  link to main. Non-owner administrators manage only their own links, and
  management/pending-request visibility require `InviteUsers`. MTProto member
  updates include a private invite only for authorized viewers or that link's
  creator; ordinary members and direct joiners do not receive its secret.
  Bot API events expose another administrator's link with its secret replaced
  by `...`, while preserving non-secret metadata. Batch approvals are atomic;
  direct invites and link deletion clear stale pending requests and counts.
- Participant queries preserve restrictions, bans and member tags. Tags use
  at most 16 Unicode characters and reject emoji. Message ranks are loaded
  once per author batch/event, not per sender or fanout recipient, and are not
  retained in a long-lived cache. Channel-identity bans cover text, media,
  albums, forwards and linked-discussion copies; nonzero temporary expiry is
  rejected. Unbanning an already-unbanned channel identity creates no audit
  entry, PTS change or membership broadcast.
- Supergroup sticker sets require `ChangeInfo` and 100 members when setting
  a non-empty set. Authorized administrators may still remove an existing
  set below that threshold; `can_set_stickers` includes this removal capability.
- Live locations reject revival after expiry/stop, including inline edits.
  Checklist replacement is author-only and merges against current locked
  completion state: surviving item IDs retain their completer/date, removed
  IDs lose completion, and new IDs start incomplete. Allowed collaboration
  continues to produce edit updates.
- Text/rich streaming drafts are bot-only, validate IDs/text/entities,
  respect blocking, and emit server-side rich constructors. Paid media uses
  persisted purchase/ownership state; paid reactions remain independent.
- `messages.getPersonalChannelHistory` lets bots read a valid public personal
  broadcast channel only while its associated user is the active creator.
  Transferring ownership revokes the old association even if that user remains
  an administrator; ordinary bot history privacy is unchanged. Default bot
  administrator rights persist separately, and avatar byte import uses avatar
  sizing/512 KiB video parts and notifies the bot owner.
- Batch deletion uses an explicit authorized skip mode. Normal MTProto
  deletion remains atomic and does not silently skip unauthorized messages.
- Reaction delivery snapshots the committed old/new selections and counts
  before admission to the bounded FIFO worker. Bot discovery, current role
  checks and update insertion run there rather than on the reaction RPC's
  synchronous path. Queue pressure applies FIFO backpressure; cancellation
  before admission can reject delivery. Admitted work drains on shutdown with
  a bounded per-job deadline. This is not a durable outbox or an exactly-once
  delivery guarantee.

New migrations sort after the published `20261027010000` head:
`20261028010000` (profile-photo offers), `20261028010100` through
`20261028010600` (invite identity, sender bans, sticker sets, default admin
rights, reactions and join requests), then `20261029010000`,
`20261029020000`, `20261029030000`, `20261029040000`, `20261029050000`,
`20261029060000`, `20261029070000`, `20261029080000`, `20261029110000`,
`20261029120000`, `20261029130000`, `20261029130001`, `20261029140000`,
`20261029150000` and `20261029170000`. The sequence is intentionally not
continuous. Apply the embedded migrations in order rather than inserting
older versions into an already-upgraded database.

Invite repair enforces unique active main links; that data correction is not
undone on rollback. Removing reaction/join-request/object payload schemas can
discard queued events. Financial rollback fails closed if any Stars balance
or transaction has a fractional `nanos` component, or a broadcast reservation
is still reserved: it never truncates money to whole Stars. Back up before
upgrading or rolling back.

## Additional Bot API 10.3 methods

These tables complete the method summary with all 106 additions. Parameters
listed are the principal inputs actually decoded by the local handlers, not a
claim that every optional field in the upstream specification is supported.
Use the [official Bot API reference](https://core.telegram.org/bots/api) for
object definitions; local restrictions below take precedence. Mutations return
`true` unless a different result is stated.

### Messages, reactions, live locations, checklists and drafts

| Methods | Inputs and result / boundary |
|---|---|
| `deleteMessages` | `chat_id`, `message_ids` (1–100 positive ids); authorized batch deletion, not unrestricted cross-chat deletion |
| `setMessageReaction` | `chat_id`, `message_id`, `reaction` (emoji/custom-emoji objects), `is_big`; normal reaction eligibility |
| `deleteMessageReaction`, `deleteAllMessageReactions` | `chat_id`, `user_id` or `actor_chat_id` (exactly one); single removal also takes `message_id`; moderation rights checked |
| `editMessageLiveLocation`, `stopMessageLiveLocation` | chat/message pair or `inline_message_id`, optional inline `reply_markup`; edit takes `latitude`, `longitude`, optional `horizontal_accuracy`, `live_period`, `heading`, `proximity_alert_radius`; returns `Message` or inline `true` |
| `sendChecklist`, `editMessageChecklist` | `business_connection_id`, `chat_id`, `checklist`, optional inline `reply_markup`; edit adds `message_id`; returns `Message` |
| `sendMessageDraft`, `sendRichMessageDraft` | positive private `chat_id`, nonzero `draft_id`, optional `message_thread_id`, `can_stop`, `keep_on_stop`; respectively `text`/formatting or `rich_message`; transient streaming state, not a committed chat message |
| `sendPaidMedia` | `chat_id`, `star_count` (1–25000), `media` photo/video array, optional caption/formatting, payload and send options; returns `Message` with paid-media projection |

Checklists require 1–30 tasks with unique positive `id`, title up to 255
characters and task text up to 100. `title_entities` / `text_entities` or
their `parse_mode` forms are validated. Collaboration flags are
`others_can_add_tasks` and `others_can_mark_tasks_as_done`. Editing requires
the author/delegated business authority and preserves completion state for
surviving task IDs. Draft stop behavior respects `keep_on_stop`; private
topic ownership and user blocking are enforced. Paid-media previews do not
grant access to purchased content.

### Chat administration, invites and join requests

| Methods | Inputs and result / boundary |
|---|---|
| `banChatSenderChat`, `unbanChatSenderChat` | `chat_id`, `sender_chat_id`; channel-identity moderation through the normal ban permission |
| `setChatStickerSet`, `deleteChatStickerSet` | `chat_id`, plus `sticker_set_name` for setting; ChangeInfo and supergroup size rules apply |
| `setChatMemberTag` | `chat_id`, `user_id`, `tag`; ManageRanks permission, 16 Unicode characters maximum, no emoji |
| `exportChatInviteLink` | `chat_id`; rotates the bot's main link and returns its string |
| `createChatInviteLink`, `editChatInviteLink` | `chat_id`, optional `name`, `expire_date`, `member_limit`, `creates_join_request`; edit requires `invite_link`; returns `ChatInviteLink` |
| `revokeChatInviteLink` | `chat_id`, `invite_link`; returns revoked `ChatInviteLink` |
| `approveChatJoinRequest`, `declineChatJoinRequest` | `chat_id`, `user_id`; requires a pending request and InviteUsers authority |
| `createChatSubscriptionInviteLink` | `chat_id`, `name`, `subscription_period` (2592000), `subscription_price` (1–10000); returns `ChatInviteLink` |
| `editChatSubscriptionInviteLink` | `chat_id`, `invite_link`, `name`; changes the name, not the subscription price |
| `answerChatJoinRequestQuery` | `chat_join_request_query_id`, `result`; query ownership and lifetime checked |
| `sendChatJoinRequestWebApp` | `chat_join_request_query_id`, `web_app_url`; authorized query-scoped Web App delivery |

Non-owner admins manage only their own invites. Main links have explicit
identity; creating an ordinary link does not promote it to main. Join-request
links cannot also specify a member limit. Invite secrets are redacted in
events for viewers without authority.

### Forum and private-chat topics

| Methods | Inputs and result / boundary |
|---|---|
| `getForumTopicIconStickers` | no inputs; array of `Sticker` |
| `createForumTopic` | `chat_id`, `name`, optional `icon_color`, `icon_custom_emoji_id`; returns `ForumTopic` |
| `editForumTopic` | `chat_id`, `message_thread_id`, optional `name`, `icon_custom_emoji_id` |
| `closeForumTopic`, `reopenForumTopic`, `deleteForumTopic`, `unpinAllForumTopicMessages` | `chat_id`, `message_thread_id`; topic management/deletion/pinning rights enforced |
| `editGeneralForumTopic` | `chat_id`, `name` |
| `closeGeneralForumTopic`, `reopenGeneralForumTopic`, `hideGeneralForumTopic`, `unhideGeneralForumTopic`, `unpinAllGeneralForumTopicMessages` | `chat_id`; general-topic operations are for forum supergroups |

Private topic IDs belong to one bot/user conversation and cannot address
another user's topic. Supergroup topics retain their normal membership and
administrator requirements; topic support does not create a membership bypass.

### User profiles and bot configuration

| Methods | Inputs and result / boundary |
|---|---|
| `getUserProfilePhotos` | `user_id`, optional `offset`, `limit`; `UserProfilePhotos`, filtered by viewer privacy |
| `getUserProfileAudios` | `user_id`, optional `offset`, `limit`; saved profile audio page |
| `getUserPersonalChatMessages` | `user_id`, optional `limit`; messages from the user's valid public personal channel, not arbitrary private history |
| `getUserChatBoosts` | `chat_id`, `user_id`; `UserChatBoosts`, subject to chat visibility/admin privacy |
| `setMyDefaultAdministratorRights`, `getMyDefaultAdministratorRights` | optional `for_channels`; setter takes `rights`; getter returns `ChatAdministratorRights`; suggestions do not grant actual rights |
| `setMyProfilePhoto`, `removeMyProfilePhoto` | setter takes `photo` (`type: static` + `photo`, or `type: animated` + `animation`, optional `main_frame_timestamp`); imports actual bytes and notifies owner |

### Stickers and custom emoji

| Methods | Inputs and result / boundary |
|---|---|
| `getStickerSet` | `name`; `StickerSet` |
| `getCustomEmojiStickers` | `custom_emoji_ids` (up to 200); array of `Sticker` |
| `uploadStickerFile` | `user_id`, uploaded `sticker`, `sticker_format`; `File` |
| `createNewStickerSet` | `user_id`, `name`, `title`, `stickers` (1–50 InputSticker), optional `sticker_type`, `needs_repainting` |
| `addStickerToSet`, `replaceStickerInSet` | `user_id`, `name`, `sticker`; replacement also requires `old_sticker` |
| `setStickerPositionInSet` | `sticker`, nonnegative `position` |
| `deleteStickerFromSet` | `sticker` |
| `setStickerEmojiList` | `sticker`, `emoji_list` |
| `setStickerKeywords` | `sticker`, `keywords` |
| `setStickerMaskPosition` | `sticker`, optional `mask_position` |
| `setStickerSetTitle` | `name`, `title` |
| `setStickerSetThumbnail` | `name`, `user_id`, `format`, optional `thumbnail` |
| `setCustomEmojiStickerSetThumbnail` | `name`, optional `custom_emoji_id` |
| `deleteStickerSet` | `name` |

InputSticker decodes `sticker`, `format`, `emoji_list`, `keywords` and
`mask_position`. Mutations check the owning bot/user relationship and set
ownership; knowing a public set name or a file id is not write authority.
Sticker references in mutation methods are local document-backed `file_id`s.

### Inline, guest and Mini App

| Methods | Inputs and result / boundary |
|---|---|
| `answerInlineQuery` | `inline_query_id`, `results`, optional `cache_time`, `is_personal`, `next_offset`, `button`; `true` |
| `answerGuestQuery` | `guest_query_id`, `result`; `{"inline_message_id":"..."}` |
| `savePreparedKeyboardButton` | `user_id`, `button`; `{"id":"..."}` |

Inline results are parsed into supported local constructors, not arbitrary
unvalidated JSON. Query owner, expiry and result identifiers are checked.
Guest replies require a one-use grant tied to the actual query/chat; they do
not impersonate a caller or allow an uninvited bot to send anywhere.
Prepared buttons preserve their user scope and validated action.

### Stars payments and subscriptions

| Methods | Inputs and result / boundary |
|---|---|
| `createInvoiceLink` | `title`, `description`, `payload`, `currency: XTR`, `prices`, optional `business_connection_id`, `subscription_period: 2592000`; returns URL |
| `getStarTransactions` | optional nonnegative `offset`, `limit` (1–100); `StarTransactions` |
| `getMyStarBalance` | no inputs; `StarAmount`, including fractional `nanostar_amount` |
| `editUserStarSubscription` | `user_id`, `telegram_payment_charge_id`, `is_canceled`; toggles renewal of the bot's subscription |

Provider tokens/non-XTR invoice links are rejected. The ledger is the same
wallet used for gifts, invoices and broadcasts, not a separate synthetic
balance. Paid broadcast requires the bot's paid-broadcast setting and at least
10000 Stars. The ordinary allowance is 30 messages/second; opt-in capacity is
up to 1000/second, charging 0.1 Star (100000000 nanostars) per successful
over-limit message. Reservations, success commits and failure releases are
atomic; failed sends are not charged.

### Business connections and delegated operations

All methods below require `business_connection_id`. Connections are opaque,
bot-owned, enabled grants, not numeric user IDs.

| Methods | Other inputs and result / boundary |
|---|---|
| `getBusinessConnection` | `BusinessConnection` with user and granted rights |
| `readBusinessMessage` | positive private `chat_id`, `message_id`; read right and eligible recipient required |
| `deleteBusinessMessages` | `message_ids` (1–100); sent/all-delete rights, chat isolation and age rules apply |
| `setBusinessAccountName` | `first_name`, optional `last_name`; name-edit right |
| `setBusinessAccountUsername` | `username`; username-edit right |
| `setBusinessAccountBio` | `bio`; bio-edit right |
| `setBusinessAccountProfilePhoto`, `removeBusinessAccountProfilePhoto` | optional `is_public`; setter takes static/animated `photo` object; profile-photo right |
| `setBusinessAccountGiftSettings` | `show_gift_button`, `accepted_gift_types`; change-gift-settings right |
| `getBusinessAccountStarBalance` | `StarAmount`; view-gifts-and-Stars right |
| `transferBusinessAccountStars` | `star_count`; transfer-Stars right, actual debit/credit |
| `getBusinessAccountGifts` | pagination/filter options; `OwnedGifts`; view-gifts-and-Stars right |

Delegation checks the connection's bot, enabled state, specific right,
recipient inclusion/exclusion, pause state and applicable activity window.
It never substitutes the owner as an unrestricted caller. Revoking or pausing
a connection stops subsequent delegated operations and rechecks queued
business delivery.

### Owned gifts

| Methods | Inputs and result / boundary |
|---|---|
| `getUserGifts` | `user_id`, gift filters, `offset`, `limit` (1–100); `OwnedGifts` with privacy filtering before pagination |
| `getChatGifts` | `chat_id`, same pagination/filters; `OwnedGifts` subject to chat authority |
| `convertGiftToStars` | `business_connection_id`, `owned_gift_id`; conversion permission and gift eligibility |
| `upgradeGift` | same identifiers, optional `keep_original_details`, `star_count`; upgrade permission, eligibility and real Stars debit |
| `transferGift` | same identifiers, `new_owner_chat_id`, optional `star_count`; transfer permission and gift ownership |

Gift identifiers do not convey ownership. Conversion, upgrade and transfer
operate on persisted inventory, enforce availability/cost and use the shared
Stars ledger.

### Stories

| Methods | Inputs and result / boundary |
|---|---|
| `postStory` | `business_connection_id`, `content`, `active_period`, optional caption/formatting, `areas`, `post_to_chat_page`, `protect_content`; `Story` |
| `repostStory` | `business_connection_id`, positive `from_chat_id`, `from_story_id`, `active_period`, posting/protection flags; `Story` |
| `editStory` | `business_connection_id`, `story_id`, `content`, optional caption/formatting and `areas`; `Story` |
| `deleteStory` | `business_connection_id`, `story_id`; `true` |

Content is a static-photo or video upload object; video accepts `duration`,
`cover_frame_timestamp` and `is_animation`. Captions are limited to 2048
characters. Story ownership and `can_manage_stories` delegation are enforced;
reposting does not bypass source visibility or protection.

### Verification, managed bots and suggested posts

| Methods | Inputs and result / boundary |
|---|---|
| `verifyUser`, `removeUserVerification` | `user_id`; verification also accepts `custom_description`; requires verifier authority |
| `verifyChat`, `removeChatVerification` | `chat_id`; verification also accepts `custom_description`; cannot remove another verifier's mark |
| `getManagedBotToken`, `replaceManagedBotToken` | `user_id` (the managed bot); token string; management relationship required, replacement invalidates old token |
| `getManagedBotAccessSettings` | `user_id` (the managed bot); managed access settings |
| `setManagedBotAccessSettings` | `user_id` (the managed bot), `is_access_restricted`, optional `added_user_ids` (up to 10); management authority required |
| `approveSuggestedPost` | `chat_id`, `message_id`, optional `send_date`; approval authority and pending-state transition |
| `declineSuggestedPost` | `chat_id`, `message_id`, optional `comment`; authorized rejection |

Managed-bot access controls do not turn the manager into the managed bot for
unrelated APIs. Tokens are credentials and are not included in public updates.

### Games, Passport and gateway lifecycle

| Methods | Inputs and result / boundary |
|---|---|
| `sendGame` | `chat_id`, `game_short_name`, optional markup/reply/send options; `Message`; game must belong to the bot |
| `setGameScore` | `user_id`, `score`, chat/message pair or `inline_message_id`, optional `force`, `disable_edit_message`; `Message` or inline `true` |
| `getGameHighScores` | `user_id`, chat/message pair or `inline_message_id`; array of `GameHighScore` |
| `setPassportDataErrors` | `user_id`, nonempty `errors`; validates secure-data error constructors and bot authority |
| `logOut`, `close` | no inputs; stops the local bot API instance according to its lifecycle/lease rules; cooldown errors include retry information |

## Compatibility boundaries

- `answerShippingQuery` remains recognized but always returns HTTP
  `501 METHOD_NOT_FOUND`: this server implements XTR payments, not physical
  shipping or external payment providers.
- `dropPendingUpdates` is the sole non-official method in the registered
  count (individual parameters such as idempotency keys can still be local
  extensions). Rich messages and ephemeral methods are official Bot API 10.3
  methods, not local extensions.
- Unknown method names return HTTP 404. Optional gateway services omitted by
  a custom embedding can return 501; production wiring includes the new
  adapters, but registration alone does not prove a business operation works.
- No legacy basic groups, localized bot-profile storage or non-default
  command scopes. Existing upload, formatting and webhook certificate/IP
  restrictions still apply.
- Rich source `blocks` and embedded `media` remain explicitly unsupported;
  remote rich-HTML media cannot be materialized atomically.
- Boost update names are recognized, but no new `chat_boost` or
  `removed_chat_boost` producer is claimed. A successful empty ledger/profile
  query is not proof of a funded payment or every update transition.

MTProto and HTTP share authorization, persistence and the Stars/Premium
pipelines; see [premium-stars.md](./premium-stars.md). This is an implemented
method inventory, not a claim of complete upstream optional-field parity.

## Demos and self-tests

| Path | Purpose |
|---|---|
| `cmd/bots/ptbecho` | `python-telegram-bot` echo against the gateway, including an ephemeral command demo |
| `cmd/bots/aiogramecho` | aiogram 3 echo, ephemeral replies, reply and inline keyboards |
| `cmd/bots/bedolagaformat` | HTML / Markdown / MarkdownV2, rich menus via `sendRichMessage`, Telegram Login |
| `cmd/bots/botcheck` | Go probe: imports the bot token over MTProto and checks the bot loop |
| `cmd/bots/botdemo` | Go example: a bot sending to a channel or user on a timer |
| `cmd/bots/grammystore` | Production grammY bot on the HTTP gateway (Postgres, own deployment) |
| `scripts/botapi_echo_once.py` | Single-shot send/receive self-test |
| `scripts/botapi_receive_selftest.py` | Update-queue and webhook self-test |
