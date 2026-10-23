# Bot API

telesrv больше не содержит HTTP Bot API. Bot API обслуживает внешний форк
официального [telegram-bot-api](https://github.com/tdlib/telegram-bot-api)
(TDLib): он входит как каждый бот по MTProto (порт `2398`) точно так же, как
в Telegram. telesrv поддерживает только одну поверхность для ботов — MTProto.

Английская версия этого документа: [bot-api.en.md](./bot-api.en.md).

## Что это значит для разработчиков ботов

- Набор методов и их поведение — как в официальной реализации, с ограничениями
  того, что telesrv поддерживает по MTProto (см. [Известные пробелы](#известные-пробелы)).
  grammY, aiogram, python-telegram-bot и другие библиотеки подключаются без
  изменений.
- Базовый URL Bot API в библиотеке должен указывать на telegram-bot-api, а не
  на telesrv. По умолчанию telegram-bot-api слушает порт `8081`.
- Локальные расширения прежнего шлюза удалены: `giftPremiumSubscription`,
  `dropPendingUpdates`, заголовок `Idempotency-Key` и `request_id`.
- Состояние шлюза не переносилось. Боты с webhook должны заново вызвать
  `setWebhook` уже у telegram-bot-api.

## Развёртывание

Сам форк (адрес DC, публичный RSA-ключ, отказ от внешней конфигурации) и
способ его развёртывания находятся вне этого репозитория. Что telesrv
гарантирует ему:

- `auth.importBotAuthorization` принимает любые `api_id` / `api_hash`;
  регистрация приложения не нужна.
- `help.getConfig` сообщает `this_dc` равным основному DC и `tmp_sessions <= 1`,
  поэтому TDLib не включает PFS.
- Боту никогда не нужно обновлять `file_reference`: `upload.getFile` из сессии
  бота принимает пустой или устаревший.

Это закреплено контрактными тестами в
`internal/mtprotoedge/tdlib_bot_contract_e2e_test.go`; у каждой проверки указано
место в исходниках TDLib, которому она соответствует.

## Доставка обновлений

| Вид | Доставка | Догон после офлайна |
|---|---|---|
| Новые и отредактированные сообщения в личных чатах, группах и каналах | последовательности pts | `updates.getDifference` / `updates.getChannelDifference` |
| Изменения участников и другие qts-обновления бота | qts-последовательность бота (`bot_updates`) | `updates.getDifference` в пределах срока хранения |
| Callback, inline и pre-checkout запросы, входящие эфемерные сообщения и их callback | только онлайн | нет (отправитель получает таймаут, как в Telegram) |

- qts аккаунта бота берётся только из его собственной последовательности;
  у обычных пользователей остаётся qts секретных чатов по устройствам. Исходное
  событие и его строка qts пишутся в одной транзакции и отправляются после
  коммита.
- Срок хранения — `TELESRV_BOT_UPDATE_RETENTION` (по умолчанию `24h`, см.
  [конфигурацию](./configuration.en.md)). Если бот запрашивает qts старше самой
  старой сохранённой строки, он получает сохранённые строки начиная с самой
  старой; истёкшая часть отбрасывается. qts никогда не даёт
  `updates.differenceTooLong`, потому что TDLib бесконечно повторял бы тот же
  устаревший qts.
- Фильтрацию `allowed_updates` выполняет telegram-bot-api, а не telesrv.
- Боты в режиме приватности не получают ни новых сообщений, ни правок, которые
  им не положено видеть.

## Известные пробелы

- `payments.*` и `stickers.*` для ботов не реализованы, поэтому `sendInvoice`,
  `createInvoiceLink`, `refundStarPayment` и `editUserStarSubscription` пока не
  работают.
- Корректный пустой результат без функциональности: `bots.setJoinChatResults`,
  `messages.setBotGuestChatResult`, `messages.getPersonalChannelHistory`,
  `users.setSecureValueErrors`.
- `help.setBotUpdatesStatus` возвращает `true` и ничего не сохраняет;
  `help.getCdnConfig` возвращает пустой `cdnConfig`.

Встроенные боты (BotFather, `@verifybot`, `@premiumbot` и другие в
`internal/app/bots`) работают внутри процесса и не используют Bot API.

## Клиенты и инструменты для ручной проверки

Они используют стандартный Bot API; их базовый URL должен указывать на
telegram-bot-api:

| Путь | Настройка |
|---|---|
| `cmd/walletminiapp` | `-bot-api` / `TELESRV_BOT_API_URL` |
| `cmd/bots/ptbecho` | `--base-url` / `--base-file-url` (`TELESRV_BOT_API_BASE_URL`, `TELESRV_BOT_API_BASE_FILE_URL`) |
| `cmd/bots/aiogramecho`, `cmd/bots/bedolagaformat` | `TELESRV_BOT_API_SERVER` |
| `scripts/botapi_*.py` | `--base-url` / `--base-file-url` |

Это инструменты ручной проверки; ни один тест в репозитории не запускает ни их,
ни telegram-bot-api. `cmd/bots/botcheck` и `cmd/bots/botdemo` работают напрямую
по MTProto и не требуют сервера Bot API.
