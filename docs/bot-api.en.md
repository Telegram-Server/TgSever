# Bot API

telesrv no longer ships an HTTP Bot API. The Bot API is served by an external
fork of the official [telegram-bot-api](https://github.com/tdlib/telegram-bot-api)
(TDLib), which logs in as each bot over MTProto (port `2398`), exactly as it
does against Telegram. telesrv maintains one bot surface only: MTProto.

Russian version of this document: [bot-api.ru.md](./bot-api.ru.md).

## What this means for bot developers

- Method coverage and behaviour are those of the official implementation,
  limited by what telesrv supports over MTProto (see [Known gaps](#known-gaps)).
  grammY, aiogram, python-telegram-bot and other libraries connect unchanged.
- Point the library's Bot API base URL at telegram-bot-api, not at telesrv.
  telegram-bot-api listens on `8081` by default.
- The former gateway's local extensions are gone: `giftPremiumSubscription`,
  `dropPendingUpdates`, the `Idempotency-Key` header and `request_id`.
- Gateway state was not migrated. Bots that used webhooks must call
  `setWebhook` again against telegram-bot-api.

## Deployment

The fork itself (DC address, RSA public key, no fallback to external config)
and how it is deployed are outside this repository. Things telesrv guarantees
to it:

- `auth.importBotAuthorization` accepts any `api_id` / `api_hash`; there is no
  application registration.
- `help.getConfig` reports `this_dc` as the main DC and `tmp_sessions <= 1`,
  so TDLib does not enable PFS.
- A bot never has to refresh a `file_reference`: `upload.getFile` accepts an
  empty or stale one from a bot session.

These are pinned by the contract tests in
`internal/mtprotoedge/tdlib_bot_contract_e2e_test.go`, each assertion annotated
with the TDLib source location it mirrors.

## Update delivery

| Kind | Delivery | Offline catch-up |
|---|---|---|
| New and edited messages in private chats, groups and channels | pts sequences | `updates.getDifference` / `updates.getChannelDifference` |
| Membership changes and other bot qts updates | per-bot qts sequence (`bot_updates`) | `updates.getDifference`, within the retention window |
| Callback, inline and pre-checkout queries, incoming ephemeral messages and their callbacks | online push only | none (the sender times out, as on Telegram) |

- A bot account's qts comes only from its own sequence; ordinary users keep the
  per-device secret-chat qts. The source event and its qts row are written in
  one transaction and pushed after commit.
- Retention is `TELESRV_BOT_UPDATE_RETENTION` (default `24h`, see
  [configuration](./configuration.en.md)). A bot asking for a qts older than the
  oldest retained row gets the retained rows from the oldest one on; the expired
  part is dropped. qts never produces `updates.differenceTooLong`, because TDLib
  would retry the same stale qts forever.
- `allowed_updates` filtering is done by telegram-bot-api, not by telesrv.
- Privacy-mode bots receive neither new messages nor edits they are not allowed
  to see.

## Known gaps

- `payments.*` and `stickers.*` are not implemented for bots, so `sendInvoice`,
  `createInvoiceLink`, `refundStarPayment` and `editUserStarSubscription` do not
  work yet.
- Valid empty results, no functionality: `bots.setJoinChatResults`,
  `messages.setBotGuestChatResult`, `messages.getPersonalChannelHistory`,
  `users.setSecureValueErrors`.
- `help.setBotUpdatesStatus` returns `true` and stores nothing;
  `help.getCdnConfig` returns an empty `cdnConfig`.

Built-in bots (BotFather, `@verifybot`, `@premiumbot` and others in
`internal/app/bots`) run in-process and do not go through the Bot API.

## Callers and smoke tools

These speak the standard Bot API; configure their base URL to point at
telegram-bot-api:

| Path | Setting |
|---|---|
| `cmd/walletminiapp` | `-bot-api` / `TELESRV_BOT_API_URL` |
| `cmd/bots/ptbecho` | `--base-url` / `--base-file-url` (`TELESRV_BOT_API_BASE_URL`, `TELESRV_BOT_API_BASE_FILE_URL`) |
| `cmd/bots/aiogramecho`, `cmd/bots/bedolagaformat` | `TELESRV_BOT_API_SERVER` |
| `scripts/botapi_*.py` | `--base-url` / `--base-file-url` |

They are manual smoke tools; no test in this repository runs them or
telegram-bot-api. `cmd/bots/botcheck` and `cmd/bots/botdemo` use MTProto
directly and need no Bot API server.
