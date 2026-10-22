# HTTP Bot API: поддерживаемые методы

Документ перечисляет все методы Bot API, которые HTTP-шлюз telesrv
(`internal/botapi`, порт по умолчанию `8081`) действительно реализует, вместе
с принимаемыми параметрами, добавленными расширениями и официальными методами,
которые сознательно не поддержаны.

Английская версия этого документа: [bot-api.en.md](./bot-api.en.md).

## Как пользоваться этим документом

- [Транспорт](#транспорт)
- [Аутентификация](#аутентификация)
- [Формат запросов и ответов](#формат-запросов-и-ответов)
- [Ошибки](#ошибки)
- [Сводка методов](#сводка-методов)
- [Обновления](#обновления)
- [Методы: бот и команды](#методы-бот-и-команды)
- [Методы: профиль бота](#методы-профиль-бота)
- [Методы: отправка](#методы-отправка)
- [Методы: медиагруппы, опросы, dice, chat actions](#методы-медиагруппы-опросы-dice-chat-actions)
- [Методы: rich-сообщения](#методы-rich-сообщения)
- [Методы: ephemeral-сообщения](#методы-ephemeral-сообщения)
- [Методы: пересылка и копирование](#методы-пересылка-и-копирование)
- [Методы: редактирование и удаление](#методы-редактирование-и-удаление)
- [Методы: запросы о чатах](#методы-запросы-о-чатах)
- [Методы: управление чатами](#методы-управление-чатами)
- [Методы: файлы](#методы-файлы)
- [Методы: webhooks](#методы-webhooks)
- [Методы: меню, emoji status и web apps](#методы-меню-emoji-status-и-web-apps)
- [Методы: платежи](#методы-платежи)
- [Разметка и entities](#разметка-и-entities)
- [Лимиты](#лимиты)
- [Не реализовано](#не-реализовано)
- [Демо и self-тесты](#демо-и-self-тесты)

## Транспорт

| Параметр | Значение |
|---|---|
| Метод | `http(s)://<host>/bot<TOKEN>/<METHOD>` |
| Скачивание файлов | `http(s)://<host>/file/bot<TOKEN>/<file_path>` |
| Настройка слушателя | `TELESRV_BOT_API_ADDR`; пустое значение отключает шлюз |
| Лимит тела запроса | 25 MiB файла + 1 MiB служебных данных (`FILE_TOO_BIG`) |
| Content-Type | `application/json`, `multipart/form-data`, `application/x-www-form-urlencoded` |

`<METHOD>` сопоставляется без учёта регистра. Диспетчер доступен и по `GET`, и
по `POST`, поэтому `urlencoded`-запросы методом GET работают.

Настройка клиентов:

- `python-telegram-bot`: `--base-url http://host:8081/bot` и
  `--base-file-url http://host:8081/file/bot`;
- `aiogram`: `TelegramAPIServer.from_base("http://host:8081")`;
- grammY: `new Api("host:8081")` либо
  `api.config.use((ctx) => ({ apiRoot: "http://host:8081/bot" }))`.

## Аутентификация

Токен в пути должен быть парой `<bot_id>:<secret>` бота, а секрет — совпадать
со значением, сохранённым для этого бота. Всё остальное даёт HTTP `401` с
`ACCESS_TOKEN_INVALID`. Отдельного auth-заголовка нет: токен в URL и есть
credential, ровно как на официальном шлюзе.

Скачивание файлов (`/file/bot...`) аутентифицируется так же.

## Формат запросов и ответов

Успех:

```json
{"ok": true, "result": {}}
```

Ошибка:

```json
{"ok": false, "error_code": 400, "description": "CHAT_ID_INVALID"}
```

`error_code` — это HTTP-статус. `description` — маркер ошибки в верхнем регистре
(`SCREAMING_SNAKE`), не обязательно в официальных формулировках Telegram —
сопоставляйте по маркеру, а не по всей строке.

Разбор полей универсальный: JSON-тело «расплющивается» в строки, нестроковые
значения перемаршируются в свой JSON, а в `multipart` берётся первое значение
каждого поля. Поэтому часть методов принимает и сокращение `message_text`, и
полный объект `InputTextMessageContent`, а `setChatMenuButton` — и вложенный
объект `menu_button`, и «плоское» тело с `type` на верхнем уровне.

`chat_id` в виде `@username`: методы медиагрупп / опросов / dice / chat
action, методы правки `editMessageCaption`, `editMessageMedia`,
`editMessageReplyMarkup` и `stopPoll`, методы пересылки (и `chat_id`, и
`from_chat_id`), запросы о чатах и методы управления чатами принимают либо
числовой id, либо `@username`. Username разрешается только в **публичную
супергруппу или канал** — пользователей адресуют числовым id, как и на
официальном шлюзе. Неизвестный username — `USERNAME_NOT_OCCUPIED`,
некорректный — `CHAT_ID_INVALID` или `USERNAME_INVALID`. Более старые методы
(`sendMessage`, семейство `send<Media>`, `sendRichMessage`, `editMessageText`,
`deleteMessage`, ephemeral-методы, `sendInvoice`) по-прежнему принимают только
числовой `chat_id`.

Id чатов следуют официальной нумерации: положительные для пользователей,
`-100<channel_id>` для супергрупп и каналов. В telesrv **нет старых обычных
групп**, любая группа — супергруппа.

## Ошибки

Маркеры, которые шлюз может вернуть напрямую:

| Маркер | HTTP | Комментарий |
|---|---:|---|
| `ACCESS_TOKEN_INVALID` | 401 | неверный или неизвестный токен |
| `METHOD_NOT_FOUND` | 404 / 501 | неизвестный метод либо фича не собрана в этой сборке |
| `BAD_REQUEST` | 400 | неопознанная ошибка или неразбираемый объектный параметр (например, `permissions`) |
| `BOT_INVALID` | 400 | вызов недопустим для этого бота (в том числе `setMyName` с пустым именем) |
| `CHAT_ID_INVALID` | 400 | `chat_id` отсутствует, равен нулю, не разбирается или не того вида (например, личный чат в методе только для групп) |
| `USERNAME_NOT_OCCUPIED`, `USERNAME_INVALID` | 400 | `@username` в `chat_id` не разрешается |
| `PEER_ID_INVALID` | 400 | бот не может обратиться к целевому чату |
| `MESSAGE_ID_INVALID` | 400 | `message_id` отсутствует или `<= 0` либо по этому id нет подходящего сообщения |
| `MESSAGE_IDS_EMPTY` | 400 | `message_ids` отсутствует или пустой массив |
| `MESSAGE_IDENTIFIER_INVALID` | 400 | `inline_message_id` смешан с `chat_id`/`message_id` |
| `MESSAGE_EMPTY` | 400 | не передан `text` |
| `MESSAGE_TOO_LONG` | 400 | превышен лимит текста |
| `MESSAGE_NOT_MODIFIED` | 400 | правка ничего не меняет |
| `MESSAGE_AUTHOR_REQUIRED`, `EDIT_MESSAGES_FORBIDDEN` | 400 | бот не может править это сообщение |
| `MESSAGE_POLL_CLOSED` | 400 | `stopPoll` для уже закрытого опроса |
| `MEDIA_PREV_INVALID` | 400 | `editMessageMedia` для сообщения, которое не является фото/документом |
| `CHAT_FORWARDS_RESTRICTED` | 400 | пересылка или копирование защищённого контента |
| `REPLY_PARAMETERS_INVALID` | 400 | конфликтующие селекторы ответа |
| `REPLY_MESSAGE_ID_INVALID` | 400 | отрицательный или некорректный адресат ответа |
| `MESSAGE_THREAD_ID_INVALID` | 400 | вне диапазона id верхнего сообщения ephemeral-треда |
| `ENTITY_INVALID`, `ENTITY_BOUNDS_INVALID`, `ENTITY_TYPE_UNSUPPORTED`, `ENTITIES_TOO_LONG` | 400 | проблемы со списком entity |
| `FILE_ID_INVALID`, `FILE_TOO_BIG`, `MEDIA_INVALID`, `MEDIA_EMPTY`, `PHOTO_INVALID`, `MEDIA_CAPTION_TOO_LONG` | 400 | проблемы с файлом/медиа |
| `MEDIA_GROUP_INVALID` | 400 | в `media` не 2–10 элементов или смешаны типы, которые не могут быть в одном альбоме |
| `POLL_QUESTION_INVALID`, `POLL_ANSWERS_INVALID`, `POLL_ANSWER_INVALID`, `POLL_OPTION_INVALID`, `OPTIONS_TOO_MUCH`, `OPTION_INVALID`, `QUIZ_CORRECT_ANSWERS_INVALID`, `POLL_TYPE_INVALID`, `POLL_CLOSE_INVALID` | 400 | проблемы `sendPoll` |
| `EMOTICON_INVALID` | 400 | `sendDice` с неподдерживаемым emoji |
| `ACTION_INVALID` | 400 | `sendChatAction` с неизвестным action |
| `BUTTON_INVALID`, `BUTTON_TYPE_INVALID`, `BUTTON_DATA_INVALID`, `BUTTON_URL_INVALID` | 400 | проблемы с разметкой |
| `RICH_MESSAGE_INVALID`, `RICH_MESSAGE_TOO_LONG`, `RICH_MESSAGE_DATE_INVALID`, `RICH_MESSAGE_BLOCKS_UNSUPPORTED`, `RICH_MESSAGE_MEDIA_UNSUPPORTED`, `RICH_MESSAGE_OPTION_UNSUPPORTED` | 400 | проблемы rich-сообщения |
| `WEBPAGE_MEDIA_EMPTY` | 400 | rich HTML сослался на удалённое медиа, которое локальный blob-backend не может материализовать |
| `EFFECT_ID_INVALID`, `VALUE_INVALID`, `LIMIT_INVALID` | 400 | некорректный числовой параметр |
| `BOT_COMMAND_INVALID` | 400 | некорректный или слишком большой `commands` |
| `BOT_COMMAND_SCOPE_UNSUPPORTED` | 400 | не-default scope или задан `language_code` |
| `LANG_CODE_NOT_SUPPORTED` | 400 | `setMyName` / `setMyDescription` / `setMyShortDescription` с непустым `language_code` |
| `NAME_TOO_LONG`, `DESCRIPTION_TOO_LONG`, `ABOUT_TOO_LONG` | 400 | текст профиля бота длиннее лимита |
| `EPHEMERAL_MESSAGE_ID_INVALID`, `EPHEMERAL_TARGET_REQUIRED`, `EPHEMERAL_ACTION_EXPIRED` | 400 | проблемы адресации ephemeral-сообщений |
| `QUERY_ID_INVALID`, `USER_ID_INVALID`, `RESULT_ID_INVALID`, `RESULT_ID_EMPTY`, `RESULT_TYPE_INVALID` | 400 | проблемы query/user/result |
| `USER_NOT_PARTICIPANT`, `PARTICIPANT_ID_INVALID` | 400 | пользователь не является (или не может быть) участником |
| `USER_ADMIN_INVALID`, `USER_NOT_ADMIN`, `RIGHT_FORBIDDEN`, `USER_CREATOR`, `ADMINS_TOO_MUCH`, `ADMIN_RANK_INVALID` | 400 | проблемы назначения админа / custom title |
| `CHAT_NOT_MODIFIED`, `CHAT_TITLE_EMPTY`, `TITLE_INVALID`, `CHAT_ABOUT_TOO_LONG`, `CHAT_ABOUT_NOT_MODIFIED` | 400 | проблемы названия / описания / прав чата |
| `CHANNEL_INVALID`, `CHANNEL_PRIVATE`, `USER_BANNED_IN_CHANNEL`, `MEGAGROUP_ID_INVALID`, `FROZEN_METHOD_INVALID` | 400 | этот бот не может управлять чатом |
| `OFFSET_INVALID` | 400 | offset в `getUpdates` меньше `-10000` |
| `ALLOWED_UPDATES_INVALID` | 400 | не JSON-массив из не более 100 строк |
| `ALLOWED_UPDATES_UNSUPPORTED` | 501 | шлюз без сервиса управления обновлениями |
| `MAX_CONNECTIONS_INVALID` | 400 | вне `1..100` |
| `SECRET_TOKEN_INVALID` | 400 | символы вне `[A-Za-z0-9_-]` или длиннее 256 |
| `WEBHOOK_URL_INVALID` | 400 | не абсолютный `http(s)`-URL, длиннее 2048, с userinfo/fragment или с портом вне `1..65535` |
| `CERTIFICATE_PINNING_UNSUPPORTED` | 400 | передан `certificate` или загруженный сертификат |
| `IP_ADDRESS_UNSUPPORTED` | 400 | передан `ip_address` |
| `WEBHOOK_UNSUPPORTED` | 501 | шлюз без сервиса webhooks |
| `METHOD_NOT_FOUND` | 501 | `answerShippingQuery` |
| `BLOCKED_USER_EMOJI_STATUS_SERVICE_MISSING`, `BLOCKED_WEBAPP_QUERY_SERVICE_MISSING`, `BLOCKED_PREPARED_INLINE_SERVICE_MISSING` | 501 | опциональный сервис не собран |
| `USER_PERMISSION_DENIED` | 403 | emoji status без нужного разрешения пользователя |
| `PREMIUM_ACCOUNT_REQUIRED` | 400 | emoji status без Premium |
| `PREMIUM_GIFT_SELF_INVALID`, `PREMIUM_GIFT_CODE_INVALID`, `BALANCE_TOO_LOW`, `STAR_COUNT_INVALID`, `MONTH_COUNT_INVALID` | 400 | проблемы подарка Premium |
| `GIFT_ID_INVALID`, `GIFT_NOT_AVAILABLE`, `GIFT_UPGRADE_UNAVAILABLE` | 400 | проблемы `sendGift` |
| `STAR_GIFT_UNAVAILABLE` | 500 | недоступна сама подсистема подарков |
| `CURRENCY_INVALID`, `INVOICE_AMOUNT_INVALID`, `INVOICE_INVALID`, `INVOICE_ALREADY_PAID`, `INVOICE_REFUNDED` | 400 | проблемы `sendInvoice` / `refundStarPayment` |
| `IDEMPOTENCY_KEY_INVALID` | 400 | конфликт `request_id`/`Idempotency-Key` либо плохой charset/длина |
| `PAYMENT_FORM_INVALID` | 400 | платёжная форма отклонена |
| `CHAT_WRITE_FORBIDDEN`, `CHAT_ADMIN_REQUIRED` | 400 | не хватает прав |
| `USER_BOT_REQUIRED` | 400 | адресат не является ботом |
| `CONFLICT: ...` | 409 | параллельный опрос или активная доставка webhook |
| `INTERNAL_SERVER_ERROR` | 500 / 503 | сбой хранилища или транспорта |

TL-ошибка, которой шлюз не знает, сворачивается в `BAD_REQUEST`; список выше —
это белый список маркеров, которые доходят до бота как есть.

## Сводка методов

Реализовано (90 методов + 1 файловый маршрут):

| Группа | Методы |
|---|---|
| Бот и команды | `getMe`, `setMyCommands`, `deleteMyCommands`, `getMyCommands` |
| Миграция | `logOut`, `close` (заглушки: всегда `True`, состояние не меняется, 10-минутного ограничения `close` нет) |
| Профиль бота | `setMyName`, `getMyName`, `setMyDescription`, `getMyDescription`, `setMyShortDescription`, `getMyShortDescription`, `setMyProfilePhoto`, `removeMyProfilePhoto` |
| Обновления | `getUpdates`, `dropPendingUpdates` (расширение telesrv) |
| Текст | `sendMessage`, `sendRichMessage` |
| Медиа | `sendPhoto`, `sendAnimation`, `sendAudio`, `sendDocument`, `sendLivePhoto`, `sendSticker`, `sendVideo`, `sendVideoNote`, `sendVoice` |
| Альбомы, опросы, dice, actions | `sendMediaGroup`, `sendPoll`, `sendDice`, `sendChatAction` |
| Гео | `sendContact`, `sendLocation`, `sendVenue` |
| Пересылка | `forwardMessage`, `forwardMessages`, `copyMessage`, `copyMessages` |
| Правка/удаление | `editMessageText`, `editMessageCaption`, `editMessageMedia`, `editMessageReplyMarkup`, `stopPoll`, `deleteMessage` |
| Правка/удаление ephemeral | `editEphemeralMessageText`, `editEphemeralMessageMedia`, `editEphemeralMessageCaption`, `editEphemeralMessageReplyMarkup`, `deleteEphemeralMessage` |
| Callback | `answerCallbackQuery` |
| Запросы о чатах | `getChat`, `getChatMember`, `getChatAdministrators`, `getChatMemberCount`, `getUserProfilePhotos` |
| Управление чатами | `banChatMember`, `unbanChatMember`, `restrictChatMember`, `promoteChatMember`, `setChatAdministratorCustomTitle`, `setChatPermissions`, `pinChatMessage`, `unpinChatMessage`, `unpinAllChatMessages`, `setChatTitle`, `setChatDescription`, `setChatPhoto`, `deleteChatPhoto`, `leaveChat` |
| Ссылки-приглашения | `createChatInviteLink`, `editChatInviteLink`, `revokeChatInviteLink`, `exportChatInviteLink` |
| Заявки на вступление | `approveChatJoinRequest`, `declineChatJoinRequest` |
| Файлы | `getFile`, `GET /file/bot<TOKEN>/<file_path>` |
| Webhooks | `setWebhook`, `deleteWebhook`, `getWebhookInfo` |
| Меню и статус | `setChatMenuButton`, `getChatMenuButton`, `setUserEmojiStatus` |
| Web apps и inline | `answerWebAppQuery`, `savePreparedInlineMessage` |
| Платежи и подарки | `giftPremiumSubscription`, `getAvailableGifts`, `sendGift`, `sendInvoice`, `answerPreCheckoutQuery`, `refundStarPayment` |
| Заблокировано по design | `answerShippingQuery` (HTTP 501) |

Группы методов, добавленные поверх основного switch (альбомы/опросы/dice/
actions, дополнительные методы правки, пересылка, запросы о чатах, управление
чатами, профиль бота), каждая зависит от опционального интерфейса шлюза;
сборка, где он не подключён, отвечает на всю группу `501 METHOD_NOT_FOUND`.

## Обновления

Шлюз порождает шесть видов обновлений:

| Значение `allowed_updates` | Ключ обновления | Доставляется по умолчанию |
|---|---|---|
| `message` | `message` | да |
| `edited_message` | `edited_message` | да |
| `callback_query` | `callback_query` | да |
| `pre_checkout_query` | `pre_checkout_query` | да |
| `my_chat_member` | `my_chat_member` | да |
| `chat_member` | `chat_member` | **нет** — только при явном указании |

`allowed_updates` — JSON-массив из не более 100 строк; он принимается и
`getUpdates`, и `setWebhook` и хранится отдельно для каждого бота. Фильтр
применяется в момент постановки обновления в очередь: при явном списке
неуказанные виды в очередь не попадают вовсе (смена списка не затрагивает то,
что уже лежит в очереди). Пустой массив `[]` возвращает бота к набору по
умолчанию — все виды выше, кроме `chat_member`. Неизвестные имена разбираются
без ошибки, но ни с чем не совпадают.

Ephemeral-сообщения (Bot API 10.2) приходят как обычные `message`-обновления,
но их `message_id` принадлежит ephemeral-пространству, а не нумерации
сообщений чата, поэтому обычный `reply_to_message_id` их не адресует. Ответ
идёт через `reply_parameters.ephemeral_message_id`; см.
[Методы: ephemeral-сообщения](#методы-ephemeral-сообщения).

Особенности проекции `Message`:

- `chat` широковещательного канала имеет `type: "channel"` и несёт `title` и
  `username`, если канал есть в обновлении; супергруппы — `type: "supergroup"`;
- элементы альбома несут `media_group_id` (строку);
- результаты `sendDice` несут `dice` (`emoji`, `value`);
- пересланные сообщения несут `forward_origin` типа `user`, `chat`,
  `channel` (с `message_id`) или `hidden_user`. `sender_user`,
  `sender_chat` и `chat` внутри origin строятся только по id.

### `my_chat_member` и `chat_member`

Оба несут официальный `ChatMemberUpdated`: `chat`, `from` (кто произвёл
изменение), `date`, `old_chat_member`, `new_chat_member` и, при наличии,
`via_join_request` / `via_chat_folder_invite_link`.

- `my_chat_member` — изменился статус самого бота в супергруппе или канале
  (добавлен, назначен админом, ограничен, забанен, вышел). Доставляется по
  умолчанию.
- `chat_member` — изменился статус другого участника. Доставляется только если
  `allowed_updates` явно содержит `chat_member` **и** бот в момент изменения —
  администратор (или владелец) этого чата.

Известные отличия от официального шлюза:

- обновления участников есть только для супергрупп и каналов; `my_chat_member`
  о блокировке/разблокировке бота пользователем в личном чате нет;
- у событий вступления (добавлен кем-то, вступил сам, по пригласительной
  ссылке, одобрена заявка) `old_chat_member` всегда `left`, даже если
  пользователь до этого был забанен или ограничен;
- `can_be_edited` у участника со статусом `administrator` всегда `false` —
  сервер не хранит, кто назначил админа;
- поля `invite_link` нет; `via_join_request` и `via_chat_folder_invite_link`
  заполняются.

Время жизни очереди задаёт `TELESRV_BOT_API_UPDATE_RETENTION` (по умолчанию
`24h`); подтверждённые обновления удаляются раньше.

## Методы: бот и команды

### `getMe`

Без параметров. Возвращает собственный объект `User` бота.

### `setMyCommands`

| Параметр | Тип | Комментарий |
|---|---|---|
| `commands` | JSON-массив | до 100 записей вида `{command, description, is_ephemeral}` |
| `scope` | объект | принимается только `{"type":"default"}` |
| `language_code` | строка | должно быть пустым |

`is_ephemeral=true` помечает команду как ephemeral: её вызов в группе создаёт
сообщение, видимое только вызвавшему, и бот должен ответить в пределах
ephemeral-окна. Возвращает `true`.

### `deleteMyCommands`

Те же правила по `scope`, что и у `setMyCommands`; очищает список. Возвращает
`true`.

### `getMyCommands`

Те же правила по `scope`. Возвращает сохранённый массив; поле
`is_ephemeral: true` присутствует только у помеченных команд.

## Методы: профиль бота

### `setMyName`, `setMyDescription`, `setMyShortDescription`

| Метод | Параметр | Лимит | Слишком длинно |
|---|---|---|---|
| `setMyName` | `name` | 64 символа | `NAME_TOO_LONG` |
| `setMyDescription` | `description` | 512 символов | `DESCRIPTION_TOO_LONG` |
| `setMyShortDescription` | `short_description` | 120 символов | `ABOUT_TOO_LONG` |

Вызовы идут через MTProto `bots.setBotInfo` (short description — это `about` в
MTProto), поэтому другие клиенты видят изменение. Возвращает `true`.

Отличия от официального шлюза:

- `language_code` не поддерживается: хранится только профиль на языке по
  умолчанию, и непустой `language_code` даёт `LANG_CODE_NOT_SUPPORTED`, а не
  сохраняется для отдельного языка;
- `setMyName` не может стереть имя: пустой `name` отклоняется (`BOT_INVALID`).
  Пустые description и short description очищают поле, как и официально.

### `getMyName`, `getMyDescription`, `getMyShortDescription`

Возвращают `{"name": ...}`, `{"description": ...}` и
`{"short_description": ...}`. `language_code` принимается, но игнорируется:
всегда возвращается значение для языка по умолчанию.

### `removeMyProfilePhoto`

Без параметров. Удаляет текущее фото профиля бота и возвращает `True`. Если
фото нет, тоже возвращается `True`, как на официальном сервере. Владелец бота
получает `updateUser` для бота, и его клиенты сразу показывают изменение.

### `setMyProfilePhoto`

| Параметр | Тип | Комментарий |
|---|---|---|
| `photo` | InputProfilePhoto | обязателен; JSON-объект, запрос должен быть `multipart/form-data` |

Поддерживается только `InputProfilePhotoStatic`:
`{"type":"static","photo":"attach://<поле>"}`, где `<поле>` — файловая часть
того же запроса. Как и официально, фото профиля нельзя задать через `file_id`
или URL. Изображение сохраняется по правилам размеров фото профиля (`s`, `a`,
`c`), становится текущим фото бота, возвращается `True`. Как и при
`removeMyProfilePhoto`, владелец получает `updateUser` для бота.

Ошибки: отсутствующий или некорректный `photo` даёт `Bad Request: parameter
"photo" is required`, `Bad Request: can't parse InputProfilePhoto JSON object`
или `Bad Request: invalid InputProfilePhoto type specified`; `photo`, не
указывающий на загруженный файл, — `Bad Request: profile photo must be uploaded
as a new file using attach://`. Файл, который не декодируется как изображение,
даёт `Bad Request: IMAGE_PROCESS_FAILED`; файл больше 10 МБ —
`Bad Request: file is too big`.

## Методы: отправка

### `sendMessage`

| Параметр | Тип | Комментарий |
|---|---|---|
| `chat_id` | целое | обязателен, не ноль; отрицательный для групп/каналов |
| `text` | строка | обязателен, до 4096 символов после разбора разметки |
| `parse_mode` | строка | `HTML`, `Markdown`, `MarkdownV2` или пусто для обычного текста |
| `entities` | JSON-массив | используется при отсутствии `parse_mode`, иначе игнорируется |
| `reply_markup` | объект | любой из четырёх конструкторов, см. [Разметка](#разметка-и-entities) |
| `disable_web_page_preview` | булево | |
| `disable_notification` | булево | |
| `reply_to_message_id` | целое | |
| `receiver_user_id` | целое | **расширение telesrv**: переключает вызов в ephemeral-отправку |
| `callback_query_id` | целое | ephemeral-отправка, привязанная к callback query |
| `reply_parameters` | объект | `{ephemeral_message_id}` для ephemeral-ответа, `{message_id}` для обычного |
| `message_thread_id` | целое | ephemeral: id верхнего сообщения в треде |

Обычная и ephemeral-адресация взаимоисключающи: если задан
`receiver_user_id`, поле `reply_to_message_id` должно остаться пустым, а
`reply_parameters` может нести только `ephemeral_message_id`. Для
ephemeral-отправки допустимы лишь inline-клавиатуры (иначе
`BUTTON_TYPE_INVALID`). Результат — та же структура `Message`, где
`message_id` содержит ephemeral-id.

### `sendPhoto`, `sendAnimation`, `sendAudio`, `sendDocument`, `sendSticker`, `sendVideo`, `sendVideoNote`, `sendVoice`

У них один общий обработчик.

| Параметр | Тип | Комментарий |
|---|---|---|
| `chat_id` | целое | обязателен |
| `<kind>` | строка / файл | одноимённое поле метода: `photo`, `animation`, `audio`, `document`, `sticker`, `video`, `video_note`, `voice` |
| `caption` | строка | до 1024 символов |
| `parse_mode`, `caption_entities` | | как в `sendMessage` |
| `reply_markup` | объект | все четыре конструктора |
| `disable_notification`, `reply_to_message_id` | | |
| `width`, `height`, `duration`, `title`, `performer` | целое / строка | принимаются и передаются как подсказки метаданных |
| `emoji` | строка | только для стикеров |
| `receiver_user_id`, `callback_query_id`, `reply_parameters`, `message_thread_id` | | то же ephemeral-расширение, что и в `sendMessage` |

Файловое поле принимает, по убыванию приоритета:

1. `attach://<field>`, ссылающееся на multipart-часть;
2. загруженную multipart-часть с тем же именем поля;
3. URL `http://` или `https://` (сервер забирает его сам);
4. `file_id`, ранее выданный `getFile`.

Всё остальное — `FILE_ID_INVALID`.

### `sendLivePhoto`

Два медиа-поля: `photo` (кадр) и `live_photo` (видеочасть). Оба принимают
загрузку, `attach://` или `file_id`. Видеочасть **не может** быть HTTP-URL —
это `FILE_ID_INVALID`, как и в официальном ограничении. Подписи, `parse_mode` и
ephemeral-расширение работают так же, как в остальных медиа-методах.

### `sendContact`, `sendLocation`, `sendVenue`

Эти три метода **только ephemeral**: они требуют `receiver_user_id` (или
`callback_query_id`), иначе `EPHEMERAL_TARGET_REQUIRED`.

`sendContact` принимает `chat_id`, `phone_number`, `first_name`, необязательные
`last_name` и `vcard` (до 2048 байт), плюс необязательную inline-разметку.

`sendLocation` принимает `chat_id`, `latitude` (`-90..90`), `longitude`
(`-180..180`), `horizontal_accuracy` (`0..1500`, по умолчанию `0`). Ненулевой
`live_period` отклоняется с `MEDIA_INVALID` — live-локация не реализована.

`sendVenue` добавляет обязательные `title` и `address` и принимает либо
`foursquare_id` + `foursquare_type`, либо `google_place_id` +
`google_place_type`.

## Методы: медиагруппы, опросы, dice, chat actions

`sendMediaGroup`, `sendPoll` и `sendDice` имеют общие параметры:

| Параметр | Тип | Комментарий |
|---|---|---|
| `chat_id` | целое / `@username` | обязателен |
| `disable_notification`, `protect_content` | булево | |
| `reply_parameters` | объект | `{"message_id": N}`; устаревшее `reply_to_message_id` тоже принимается, но не оба сразу |
| `reply_markup` | объект | только `sendPoll` и `sendDice` |

`message_thread_id`, `business_connection_id`, `message_effect_id`,
`allow_paid_broadcast` и прочие новые опции отправки не поддерживаются и
игнорируются. Это только обычные отправки — ephemeral-формы у них нет.

### `sendMediaGroup`

`media` — JSON-массив из 2–10 объектов `InputMedia` (иначе
`MEDIA_GROUP_INVALID`). Каждый элемент принимает `type`, `media`, `caption`
(до 1024 символов), `parse_mode`, `caption_entities`, `has_spoiler` и
подсказки `width` / `height` / `duration` / `title` / `performer`. `media`
принимает `attach://<field>`, `http(s)`-URL или `file_id`.

Правила альбома: `photo` и `video` можно смешивать; `audio` и `document`
образуют альбом только из элементов своего типа. Любой другой `type` (включая
`animation`) — `MEDIA_GROUP_INVALID`. Возвращает массив отправленных `Message`
с общим `media_group_id`.

### `sendPoll`

| Параметр | Тип | Комментарий |
|---|---|---|
| `question` | строка | обязателен, до 255 символов; `question_parse_mode`, `question_entities` |
| `options` | JSON-массив | строки или объекты `InputPollOption` (`text`, `text_parse_mode`, `text_entities`); каждый до 100 символов |
| `type` | строка | `regular` (по умолчанию) или `quiz` |
| `is_anonymous` | булево | по умолчанию `true` |
| `allows_multiple_answers` | булево | |
| `correct_option_id` | целое | обязателен для `quiz`, для остальных отклоняется |
| `explanation` | строка | до 200 символов; `explanation_parse_mode`, `explanation_entities` |
| `open_period` / `close_date` | целое | взаимоисключающи (`POLL_CLOSE_INVALID`) |
| `is_closed` | булево | отправить уже закрытый опрос |

Число вариантов проверяет MTProto-обработчик (`POLL_ANSWERS_INVALID`,
`OPTIONS_TOO_MUCH`). Возвращает отправленный `Message`.

### `sendDice`

`emoji` по умолчанию `🎲`; допустимы `🎲`, `🎯`, `🏀`, `⚽`, `🎳`, `🎰`
(иначе `EMOTICON_INVALID`). Возвращает `Message` с полем `dice`.

### `sendChatAction`

`chat_id`, `action` — одно из `typing`, `upload_photo`, `record_video`,
`upload_video`, `record_voice`, `upload_voice`, `upload_document`,
`choose_sticker`, `find_location`, `record_video_note`,
`upload_video_note` (иначе `ACTION_INVALID`). Необязательный
`message_thread_id` передаётся как id верхнего сообщения. Возвращает `true`.

## Методы: rich-сообщения

### `sendRichMessage`

Расширение telesrv, rich-текст Layer 228. Отправляет `InputRichMessage` вместо
обычной отформатированной строки, поэтому таблицы, заголовки, разделители,
блоки `details` и `footer` доходят до клиента настоящими rich-блоками.

| Параметр | Тип | Комментарий |
|---|---|---|
| `chat_id` | целое | обязателен |
| `rich_message` | объект | `{"html": "..."}` **или** `{"markdown": "..."}`, плюс необязательные `is_rtl` и `skip_entity_detection` |
| `reply_markup` | объект | только inline-клавиатуры |
| `reply_parameters` | объект | `{"message_id": N}`; устаревшее `reply_to_message_id` тоже принимается, но не оба сразу |
| `disable_notification`, `protect_content` | булево | |
| `message_effect_id` | целое | id эффекта сообщения |

Правила и отказы:

- ровно одно из `html` / `markdown` должно быть непустым; ноль или оба —
  `RICH_MESSAGE_INVALID`;
- `blocks` разбирается, но отклоняется — `RICH_MESSAGE_BLOCKS_UNSUPPORTED`;
- `media` разбирается, но отклоняется — `RICH_MESSAGE_MEDIA_UNSUPPORTED`;
- исходник ограничен 256 KiB;
- `business_connection_id`, `message_thread_id`,
  `direct_messages_topic_id`, `allow_paid_broadcast` и
  `suggested_post_parameters` отклоняются явной ошибкой, а не игнорируются
  молча;
- rich HTML со ссылкой на удалённое медиа (`img`, `video`, `audio`, `tg-map`,
  `tg-collage`, `tg-slideshow`) падает с `WEBPAGE_MEDIA_EMPTY`, потому что
  локальный blob-backend не может атомарно материализовать произвольный URL.
  Описанный в `cmd/bots/bedolagaformat` обходной путь — повторить запрос один
  раз без логотипа, не деградируя до classic-меню.

Поддерживаемые HTML-элементы: `b`/`strong`, `i`/`em`, `u`/`ins`,
`s`/`strike`/`del`, `tg-spoiler`, `span` (только как
`<span class="tg-spoiler">`), `a`, `code`, `pre` (с `class="language-…"`),
`blockquote`, `tg-emoji`, `tg-time`, `footer` и `table` (с атрибутами
`bordered` и `striped`, выравниванием по ячейкам). Для `tg-time` обязателен
`unix` в диапазоне `1..2^31-1` и `format`, собранный из `t`, `T`, `d`, `D`,
`w`/`W` либо ровно `r`/`R` для относительной отметки времени.

Markdown разбирает тот же rich-builder, что и на стороне MTProto, поэтому
работают заголовки `#`, разделители `---`, pipe-таблицы, цитаты `>`, огороженный
код и footer-блоки.

`editMessageText` принимает тот же объект `rich_message`; отдельного метода
`editMessageRichMessage` нет.

## Методы: ephemeral-сообщения

Ephemeral-сообщения (Bot API 10.2) видны только получателю, который видит
пометку «видно только вам». telesrv поддерживает весь жизненный цикл.

Отправка идёт через обычные методы с `receiver_user_id`:

- `sendMessage` — текст;
- медиа-методы — медиа плюс подпись;
- `sendContact`, `sendLocation`, `sendVenue` — только ephemeral.

Правила адресации:

- `receiver_user_id` обязателен и должен быть положительным целым;
- вместо него якорем может быть `callback_query_id` (получатель берётся из
  query);
- `reply_parameters.ephemeral_message_id` отвечает на существующее
  ephemeral-сообщение и не комбинируется с `callback_query_id`;
- `reply_parameters.message_id` — обычная форма ответа, с получателем не
  комбинируется;
- `message_thread_id` — верхнее сообщение ephemeral-треда;
- окно действия — 15 секунд, после него сервер отвечает
  `EPHEMERAL_ACTION_EXPIRED`.

Правка и удаление:

| Метод | Параметры |
|---|---|
| `editEphemeralMessageText` | `chat_id`, `receiver_user_id`, `ephemeral_message_id`, `text`, `parse_mode`, `entities`, `reply_markup` |
| `editEphemeralMessageCaption` | то же, с `caption` + `caption_entities` |
| `editEphemeralMessageMedia` | то же, плюс `media` (см. ниже) |
| `editEphemeralMessageReplyMarkup` | то же, только `reply_markup` |
| `deleteEphemeralMessage` | `chat_id`, `receiver_user_id`, `ephemeral_message_id` |

`media` — объект в духе `InputMedia`: `type` (одно из `photo`, `animation`,
`audio`, `document`, `video`, `live_photo`), файловое поле и необязательные
`caption`, `parse_mode`, `caption_entities`, `width`, `height`, `duration`,
`title`, `performer`. Для `live_photo` кадр берётся из `photo`, видео — из
`media`. Сырая загрузка здесь не допускается — только `file_id`, ссылка на файл
без `attach://` или HTTPS-URL (иначе `FILE_ID_INVALID`).

`reply_markup` принимается только inline, и важно его *наличие*: переданный
пустой `reply_markup` снимает клавиатуру.

## Методы: пересылка и копирование

Все четыре метода действуют от имени бота: бот должен видеть исходные
сообщения в `from_chat_id` (чат, где он участник, или свой личный чат с
пользователем) и иметь право писать в `chat_id`. Оба поля принимают числовой
id или `@username`.

| Метод | Параметры | Результат |
|---|---|---|
| `forwardMessage` | `chat_id`, `from_chat_id`, `message_id`, `disable_notification`, `protect_content` | `Message` |
| `forwardMessages` | `chat_id`, `from_chat_id`, `message_ids`, `disable_notification`, `protect_content` | массив `MessageId` |
| `copyMessage` | `chat_id`, `from_chat_id`, `message_id`, `caption`, `parse_mode`, `caption_entities`, `reply_markup`, `reply_parameters` / `reply_to_message_id`, `disable_notification`, `protect_content` | `MessageId` |
| `copyMessages` | `chat_id`, `from_chat_id`, `message_ids`, `remove_caption`, `disable_notification`, `protect_content` | массив `MessageId` |

- `message_ids` — JSON-массив из 1–100 положительных id; дубликаты
  убираются, обработка идёт по возрастанию. Пустой массив — `MESSAGE_IDS_EMPTY`.
- Как и официально, id, которые не найдены или не могут быть
  пересланы/скопированы, пропускаются; только если не осталось ничего, вызов
  даёт `MESSAGE_ID_INVALID`. Служебные сообщения никогда не пересылаются;
  служебные сообщения, счета и розыгрыши никогда не копируются.
- `copyMessage`: присутствующий ключ `caption` заменяет исходную подпись
  (пустая строка её убирает); отсутствующий ключ её сохраняет. Подпись
  проверяется по лимиту текста в 4096 символов, а не по лимиту подписи 1024.

Отличия от официального шлюза:

- `message_thread_id`, `direct_messages_topic_id`, `video_start_timestamp`,
  `show_caption_above_media`, `allow_paid_broadcast` и
  `suggested_post_parameters` не поддерживаются (игнорируются);
- простое копирование — это пересылка без заголовка автора, альбомы при этом
  сохраняются; копирование, которое меняет подпись, использует
  `remove_caption` или добавляет `reply_markup`, — это **повторная отправка**
  исходного содержимого, поэтому альбом, скопированный так, приходит
  отдельными сообщениями без общего `media_group_id`;
- защищённый контент (чат или сообщение с запретом пересылки либо личный чат,
  владелец которого запретил пересылку) отклоняет весь вызов с
  `CHAT_FORWARDS_RESTRICTED`.

## Методы: редактирование и удаление

### `editMessageText`

Покрывает и обычный текст, и rich-текст, и сообщения чата, и
inline-сообщения.

| Параметр | Тип | Комментарий |
|---|---|---|
| `chat_id` + `message_id` | | один способ адресации |
| `inline_message_id` | строка | другой способ; не смешивать с первым |
| `text` | строка | одна форма содержимого |
| `rich_message` | объект | другая форма содержимого; не смешивать с `text` |
| `parse_mode`, `entities` | | как в `sendMessage` |
| `reply_markup` | объект | только inline; наличие с пустым значением снимает |
| `disable_web_page_preview` | булево | |

Для `inline_message_id` результат — булево значение, для сообщения чата —
обновлённый `Message`. Правка, ничего не меняющая, даёт
`MESSAGE_NOT_MODIFIED`.

### `editMessageCaption`, `editMessageReplyMarkup`, `editMessageMedia`

Все три принимают `chat_id` (целое или `@username`) + `message_id` либо
`inline_message_id` (смешивание — `MESSAGE_IDENTIFIER_INVALID`). Результат —
изменённый `Message` для сообщения чата и `true` для inline-сообщения.
`reply_markup` только inline, и важно его наличие: переданный пустой снимает
клавиатуру, отсутствующий — сохраняет.

- `editMessageCaption` — `caption` (до 1024 символов), `parse_mode`,
  `caption_entities`, `reply_markup`.
- `editMessageReplyMarkup` — только `reply_markup`; без него клавиатура
  снимается.
- `editMessageMedia` — `media` — один `InputMedia` (`animation`, `audio`,
  `document`, `photo`, `video`; иначе `MEDIA_INVALID`) со своими
  `caption` / `parse_mode` / `caption_entities` / `has_spoiler`, плюс
  `reply_markup`. Файл может быть загрузкой через `attach://`, URL или
  `file_id`. Для сообщения чата заменить медиа можно только у сообщений, где
  уже есть фото или документ (сюда входят файлы видео, аудио и анимации);
  текст, опрос, dice и прочие сообщения дают `MEDIA_PREV_INVALID`.

Методов `editMessageLiveLocation` и `stopMessageLiveLocation` нет (live-локация
не реализована).

### `stopPoll`

`chat_id` + `message_id` (inline-формы нет), необязательный `reply_markup`.
Закрывает опрос, отправленный ботом, и возвращает итоговый `Poll`. Уже
закрытый опрос — `MESSAGE_POLL_CLOSED`; сообщение без опроса —
`MESSAGE_ID_INVALID`.

### `deleteMessage`

`chat_id` + `message_id`. Возвращает `true`. Удаление уже удалённого или
чужого сообщения возвращает `false` либо ошибку, как и на официальном шлюзе.

### `answerCallbackQuery`

| Параметр | Тип | Комментарий |
|---|---|---|
| `callback_query_id` | строка | обязателен |
| `text` | строка | текст уведомления |
| `url` | строка | |
| `show_alert` | булево | модальное окно вместо тоста |
| `cache_time` | целое | секунды |

Возвращает `true`.

## Методы: запросы о чатах

Все четыре метода читают данные с ботом в роли смотрящего. Чат, который бот
не видит (не вступал, кикнут, удалён), — `CHAT_ID_INVALID`.

### `getChat`

`chat_id` — положительный id пользователя или id супергруппы/канала вида
`-100…` (любой другой отрицательный id — `CHAT_ID_INVALID`). Возвращает
`ChatFullInfo` со следующим подмножеством полей:

- личные чаты: `id`, `type: "private"`, `first_name`, `last_name`,
  `username`, `bio`, `birthdate`, `accent_color_id`,
  `background_custom_emoji_id`, `profile_accent_color_id`,
  `profile_background_custom_emoji_id`, `emoji_status_custom_emoji_id`,
  `emoji_status_expiration_date`, `max_reaction_count`,
  `accepted_gift_types`. `bio` и `birthdate` учитывают настройки
  приватности пользователя по отношению к боту;
- супергруппы и каналы (`type` — `channel` для широковещательных каналов,
  иначе `supergroup`): `id`, `type`, `title`, `username`, `description`,
  `is_forum`, `has_protected_content`, `has_hidden_members`, `linked_chat_id`,
  `message_auto_delete_time`, `paid_message_star_count`,
  `available_reactions`, поля цвета и emoji-статуса, `max_reaction_count`,
  `accepted_gift_types`;
- только супергруппы: `permissions`, `slow_mode_delay`,
  `unrestrict_boost_count`, `join_to_send_messages`, `join_by_request` и —
  только если бот администратор — `has_visible_history` и
  `has_aggressive_anti_spam_enabled`.

Отличия от официального шлюза:

- не возвращаются: `photo`, `active_usernames`, `invite_link`,
  `pinned_message`, `sticker_set_name`, `can_set_sticker_set`,
  `custom_emoji_sticker_set_name`, `location`, `personal_chat`, поля
  `business_*`, `has_private_forwards`,
  `has_restricted_voice_and_video_messages`, `can_send_paid_media` и прочие
  поля, не перечисленные выше;
- `accepted_gift_types` — фиксированное правило, а не реальные настройки
  пользователя: пользователи принимают все виды подарков, включая Premium,
  каналы — все, кроме Premium, супергруппы — никакие.

### `getChatMember`

`chat_id` (супергруппа или канал), `user_id`. Возвращает `ChatMember` в одной
из шести официальных форм (`creator`, `administrator`, `member`,
`restricted`, `left`, `kicked`). Пользователь, которого нет в чате, — это
`status: "left"`, а не ошибка.

### `getChatAdministrators`

`chat_id` (супергруппа или канал). Возвращает владельца и всех
администраторов. Другие боты опускаются, если не передан `return_bots=true`;
сам вызывающий бот включается всегда.

В обоих методах `can_be_edited` у `administrator` равно `true` только когда
вызывающий бот — **владелец** чата: сервер не хранит, кто назначил админа,
поэтому бот-админ с `can_promote_members` консервативно получает `false`.

### `getChatMemberCount`

`chat_id` (супергруппа или канал). Возвращает целое число.

### `getUserProfilePhotos`

`user_id`, `offset` (по умолчанию 0), `limit` (1–100, по умолчанию 100;
значения вне диапазона приводятся к границам, как и официально). Возвращает
`UserProfilePhotos` `{total_count, photos}`, новые первыми; каждая фотография —
массив `PhotoSize` от меньшего к большему. Видеоразмер анимированной аватарки
не включается. `file_id` формируется так же, как у фото в сообщениях, поэтому
работает с `getFile` и может быть переотправлен через `sendPhoto`.

Альбом тот же, что MTProto-клиент видит через `photos.getUserPhotos`: если
настройка приватности фото профиля не разрешает боту, бот видит только
публичное (fallback) фото пользователя или пустой альбом, и `total_count`
считает именно это представление, а не настоящий альбом. У удалённого
аккаунта альбом пуст.

Ошибки: отсутствующий или нечисловой `user_id` —
`Bad Request: invalid user_id specified`; пользователь, которого бот не
видит, — `Bad Request: user not found`.

## Методы: управление чатами

Каждый метод действует от имени бота через соответствующий MTProto-обработчик,
поэтому проверка прав, служебные сообщения и рассылка обновлений те же, что и
для админа-человека. Нехватка прав — `CHAT_ADMIN_REQUIRED`. Каждый метод
возвращает `true`.

В telesrv нет старых обычных групп: всё, кроме закрепления, работает только в
супергруппах и каналах. `chat_id` личного чата в методе только для групп —
`CHAT_ID_INVALID`. Методы закрепления работают и в личных чатах.

| Метод | Параметры | Комментарий |
|---|---|---|
| `banChatMember` | `chat_id`, `user_id`, `until_date`, `revoke_messages` | `until_date` меньше 30 секунд или больше 366 дней от текущего момента — навсегда |
| `unbanChatMember` | `chat_id`, `user_id`, `only_if_banned` | как и официально, без `only_if_banned` активный участник удаляется (и может вернуться) |
| `restrictChatMember` | `chat_id`, `user_id`, `permissions`, `use_independent_chat_permissions`, `until_date` | `permissions` — объект `ChatPermissions`; отсутствующие поля — `false`; неразбираемый — `BAD_REQUEST` |
| `promoteChatMember` | `chat_id`, `user_id`, все права `can_*` и `is_anonymous` | все права `false` — снятие админа |
| `setChatAdministratorCustomTitle` | `chat_id`, `user_id`, `custom_title` | 0–16 символов (`ADMIN_RANK_INVALID`); цель уже должна быть админом (`USER_NOT_ADMIN`) |
| `setChatPermissions` | `chat_id`, `permissions`, `use_independent_chat_permissions` | супергруппы |
| `pinChatMessage` | `chat_id`, `message_id`, `disable_notification` | и личные чаты |
| `unpinChatMessage` | `chat_id`, `message_id` (необязателен) | без `message_id` открепляет последнее закреплённое |
| `unpinAllChatMessages` | `chat_id` | и личные чаты |
| `setChatTitle` | `chat_id`, `title` | 1–128 символов (`CHAT_TITLE_EMPTY` / `TITLE_INVALID`) |
| `setChatDescription` | `chat_id`, `description` | 0–255 символов (`CHAT_ABOUT_TOO_LONG`) |
| `setChatPhoto` | `chat_id`, `photo` | только multipart-загрузка |
| `deleteChatPhoto` | `chat_id` | |
| `leaveChat` | `chat_id` | |
| `approveChatJoinRequest` | `chat_id`, `user_id` | нужно право приглашать пользователей (или менять информацию); заявитель вступает, участники и администраторы получают те же обновления, что и при действии администратора-человека |
| `declineChatJoinRequest` | `chat_id`, `user_id` | нужно право приглашать пользователей (или менять информацию); заявка удаляется |

Несуществующая или уже обработанная заявка —
`Bad Request: HIDE_REQUESTER_MISSING`; для этих двух методов
`CHAT_ADMIN_REQUIRED` и `USER_ALREADY_PARTICIPANT` тоже идут с
официальным префиксом `Bad Request: `.

При `use_independent_chat_permissions=false` (по умолчанию) любое право на
медиа, опросы, «другие сообщения» или превью ссылок также разрешает обычные
текстовые сообщения, как и официально.

Отличия от официального шлюза:

- `banChatMember` удаляет историю сообщений пользователя, только если
  `revoke_messages` явно равен `true`; отсутствие или `false` её сохраняет.
  Официально в супергруппах и каналах история удаляется всегда;
- если бан с `revoke_messages=true` делает бот, у которого есть право банить,
  но нет права удалять сообщения, бан применяется, но вызов возвращает
  `CHAT_ADMIN_REQUIRED` (история не удаляется);
- `setChatPhoto` принимает только файл, загруженный в этом же запросе (`photo`
  как multipart-файл или `attach://<field>`); `file_id` или URL —
  `PHOTO_INVALID`;
- обычных групп нет, поэтому специфичных для них методов и ошибок тоже нет.

## Методы: ссылки-приглашения

Бот должен быть создателем или админом с правом приглашать пользователей или
менять информацию; иначе каждый метод возвращает
`Bad Request: not enough rights to manage chat invite links`. Создание и
экспорт идут через `messages.exportChatInvite`, изменение и отзыв — через
`messages.editExportedChatInvite` от имени бота, поэтому владелец видит эти
ссылки как созданные ботом. Только супергруппы и каналы.

| Метод | Параметры | Результат |
|---|---|---|
| `createChatInviteLink` | `chat_id`, `name`, `expire_date`, `member_limit`, `creates_join_request` | `ChatInviteLink` |
| `editChatInviteLink` | `chat_id`, `invite_link`, `name`, `expire_date`, `member_limit`, `creates_join_request` | `ChatInviteLink` |
| `revokeChatInviteLink` | `chat_id`, `invite_link` | `ChatInviteLink` с `is_revoked=true`; при отзыве основной ссылки создаётся новая |
| `exportChatInviteLink` | `chat_id` | новая основная ссылка бота строкой; прежняя основная ссылка перестаёт работать |

Проверка параметров (`Bad Request: ...`, до каких-либо изменений): `name` — не
длиннее 32 символов, `member_limit` — 1–99999, `member_limit` нельзя сочетать с
`creates_join_request=true`; `invite_link` обязателен для изменения и отзыва.

`ChatInviteLink` содержит `invite_link`, `creator`, `creates_join_request`,
`is_primary`, `is_revoked` и, если заданы, `name`, `expire_date`,
`member_limit` и `pending_join_request_count`.

Отличия от официального шлюза:

- `editChatInviteLink` меняет только переданные параметры; официально
  непереданный параметр сбрасывается;
- ссылка, созданная без параметров, считается основной ссылкой бота
  (`is_primary=true`), и `exportChatInviteLink` отзывает и её.

## Методы: файлы

### `getFile`

`file_id` должен быть выдан telesrv. В результате всегда
`file_unique_id == file_id` и `file_path == file_id`, поэтому URL скачивания —
`/file/bot<TOKEN>/<file_id>`. Чужие или неизвестные id — `FILE_ID_INVALID`.

### `GET /file/bot<TOKEN>/<file_path>`

Отдаёт файл потоком, чанк за чанком, с сохранённым MIME-типом и
`Content-Length`. Путь — это `file_id`; путь с лишними сегментами даёт
`404 FILE_NOT_FOUND`.

## Методы: webhooks

### `setWebhook`

| Параметр | Тип | Комментарий |
|---|---|---|
| `url` | строка | пустое значение, наоборот, удаляет webhook |
| `secret_token` | строка | `[A-Za-z0-9_-]`, до 256 символов |
| `max_connections` | целое | `1..100`, по умолчанию `40` |
| `allowed_updates` | JSON-массив | см. [Обновления](#обновления) |
| `drop_pending_updates` | булево | |

`certificate` (и любая загруженная часть сертификата) отклоняется с
`CERTIFICATE_PINNING_UNSUPPORTED` — нужен публично доверенный сертификат.
`ip_address` отклоняется с `IP_ADDRESS_UNSUPPORTED`: шлюз использует
сконфигурированный список адресов.

Установка webhook во время long-polling `getUpdates` или во время доставки
возвращает `409 CONFLICT`.

Поведение доставки:

- один HTTP `POST` на обновление, тело — **объект `Update`**, а не массив, как
  у `getUpdates`;
- заголовок `X-Telegram-Bot-Api-Secret-Token` несёт секретный токен;
- `Content-Type: application/json`; успехом считается любой `2xx`;
- редиректы не следуются, поэтому секрет не утечёт на другой хост;
- параллельно доставляется до `max_connections` обновлений, суммарно 16 ботов
  и 64 одновременных HTTP-запроса;
- при сбое повтор экспоненциально от 1s до 5 минут плюс небольшой
  бот-зависимый джиттер;
- доставка подтверждается до первого неуспешного обновления, так что ничего
  не пропускается.

### `getWebhookInfo`

Возвращает `url`, `has_custom_certificate` (здесь всегда `false`),
`pending_update_count` и — если webhook настроен — `max_connections`,
`allowed_updates`, `last_error_date`, `last_error_message`.

### `deleteWebhook`

`drop_pending_updates` необязателен. Во время активной доставки возвращает
`409`.

## Методы: меню, emoji status и web apps

### `setChatMenuButton` / `getChatMenuButton`

`menu_button` — это `{"type": "default"}`, `{"type": "commands"}` или
`{"type": "web_app", "text": "...", "web_app": {"url": "..."}}`. Также
принимается «плоское» тело с `type` на верхнем уровне — это держит
работоспособность старых сериализаторов библиотек. Установка возвращает `true`,
чтение — текущую кнопку (никогда не `null`: незаданная кнопка читается как
`{"type": "default"}`).

### `setUserEmojiStatus`

`user_id`, `emoji_status_custom_emoji_id`, `emoji_status_expiration_date`.
Требуется, чтобы пользователь разрешил боту менять свой статус (иначе
`403 USER_PERMISSION_DENIED`) и чтобы он был Premium
(`PREMIUM_ACCOUNT_REQUIRED`). Возвращает `true`.

### `answerWebAppQuery`

`web_app_query_id` плюс `result`. Поддерживается только `InlineQueryResult` типа
`article`: `id` (до 64 символов), `title`, `description`, `url` (только HTTPS),
`input_message_content` (или сокращение `message_text`), `reply_markup` (только
inline). Другие типы результатов — `RESULT_TYPE_INVALID`. В ответе есть
`inline_message_id`, если ответ его породил.

### `savePreparedInlineMessage`

`user_id`, `result` (то же ограничение на `article`) и фильтры пиров
`allow_user_chats`, `allow_bot_chats`, `allow_group_chats`,
`allow_channel_chats`. Возвращает `{id, expiration_date}`.

## Методы: платежи

### `giftPremiumSubscription`

Оплачивает подарок Premium из Stars-баланса бота через тот же надёжный
Stars-пайплайн, что и MTProto `payments.sendStarsForm`.

| Параметр | Тип | Комментарий |
|---|---|---|
| `user_id` | целое | получатель; подарок себе отклоняется |
| `month_count` | целое | `1..120` |
| `star_count` | целое | положительное, не более 10^15 |
| `text` | строка | до 128 символов |
| `text_parse_mode`, `text_entities` | | ограниченный набор entity |
| `request_id` | строка | расширение telesrv для идемпотентности |

Передавайте HTTP-заголовок `Idempotency-Key` (или локальное поле `request_id`) —
допустимый charset `[A-Za-z0-9]` плюс `-`, `_`, `.`, `:`, до 128 символов.
Повтор того же ключа с тем же запросом возвращает сохранённый результат; повтор
с другим получателем, тарифом, ценой или текстом — `IDEMPOTENCY_KEY_INVALID`.
Запросы без ключа тоже работают, но каждый ретрай списывает средства снова,
поэтому ключ всегда стоит слать.

### `getAvailableGifts`

Без параметров. Возвращает `{"gifts": [...]}` — включённый каталог Star Gift
сервера, тот же список, что отдаёт MTProto `payments.getStarGifts`. У каждого
подарка есть `id`, `star_count`, `sticker`, `upgrade_star_count` (только пока
апгрейд действительно можно купить) и, для лимитированных подарков,
`total_count` и `remaining_count`. Распроданные и ещё не вышедшие подарки тоже
присутствуют; перед покупкой смотрите `remaining_count`.

### `sendGift`

`gift_id` (обязателен), ровно одно из `user_id` / `chat_id` (ни одного или оба
— `CHAT_ID_INVALID`), `pay_for_upgrade`, `text` (до 128 символов),
`text_parse_mode`, `text_entities`. Оплачивается из Stars-баланса бота. Работает
то же расширение `Idempotency-Key` / `request_id`, что и у
`giftPremiumSubscription`. Возвращает `true`. `STAR_GIFT_UNAVAILABLE`
(HTTP 500) означает, что недоступна сама подсистема подарков, а не что запрос
неверен.

### `sendInvoice`

Только счета в Stars: `currency` должен быть `XTR` (или отсутствовать; иначе
`CURRENCY_INVALID`). Принимает `chat_id` (числовой), `title`, `description`,
`payload` и цену либо скалярным `price`, либо массивом `prices` из одного
элемента (иначе `INVOICE_AMOUNT_INVALID`). Возвращает отправленный `Message`.
Доставка, чаевые, фото и provider-токены не моделируются.

### `refundStarPayment`

`user_id`, `telegram_payment_charge_id` (из `successful_payment`). Возвращает
ровно те Stars, что были зачислены по платежу; `user_id` должен совпадать с
плательщиком, записанным в счёте. Возвращает `true`.

### `dropPendingUpdates`

Работает: отбрасывает неподтверждённые апдейты в очереди бота и отвечает `true`.
Нужен боту, который отстал, — иначе он годами продирается через бэклог, а
`pre_checkout_query` живёт всего 10 секунд и протухает незамеченным.

### `answerPreCheckoutQuery`

Работает. Перед списанием Stars плательщик ждёт ответа бота **не более 10 секунд**
(требование Telegram). Бот получает апдейт `pre_checkout_query` и отвечает
`pre_checkout_query_id` плюс `ok` и, при `ok=false`, `error_message`.

- `ok=true` — Stars списываются, боту приходит `successful_payment`;
- `ok=false` — оплата отменяется, `error_message` показывается плательщику;
- нет ответа за 10 секунд — оплата считается несостоявшейся (`PAYMENT_FAILED`).

Таймаут не переходит в «списать напрямую»: не ответивший бот означает отказ, иначе
pre-checkout ничего бы не проверял. Ответ принимается только от бота, которому
принадлежит заказ; на устаревший `pre_checkout_query_id` бот получает
`QUERY_ID_INVALID`. Сумма и `invoice_payload` в запросе не сверяются с тем, что бот
записывал при `sendInvoice`, — проверка остаётся на стороне бота, как и в Telegram.

### `answerShippingQuery`

Метод распознаётся, но отвечает HTTP `501 METHOD_NOT_FOUND`: оплата Stars не имеет
шага доставки, отвечать не на что.

## Разметка и entities

`reply_markup` принимает ровно один конструктор:

| Конструктор | Комментарий |
|---|---|
| `inline_keyboard` | ряды кнопок |
| `keyboard` | reply-клавиатура; `resize_keyboard`, `one_time_keyboard`, `is_persistent`, `input_field_placeholder`, `selective` |
| `remove_keyboard` | `remove_keyboard: true`, `selective` |
| `force_reply` | `force_reply: true`, `input_field_placeholder`, `selective` |

Кнопки inline-клавиатуры (ровно одно действие на кнопку, иначе
`BUTTON_INVALID`): `url`, `callback_data` (до 64 байт), `web_app`, `login_url`
(`url`, `bot_username`, `forward_text`, `request_write_access`),
`switch_inline_query`, `switch_inline_query_current_chat`,
`switch_inline_query_chosen_chat`, `copy_text`. У каждой кнопки также есть
`text` (обязателен), `style` (`primary` / `danger` / `success` / пусто) и
`icon_custom_emoji_id`. Неизвестные поля отклоняются, а не игнорируются.

Кнопки reply-клавиатуры: обычная строка либо объект с `text` и одним из
`request_contact`, `request_location`, `request_poll` (`type`), `request_users`
(`request_id`, `user_is_bot`, `user_is_premium`, `max_quantity`,
`request_name`, `request_username`, `request_photo`), `request_chat`
(`request_id`, `chat_is_channel`, `chat_is_forum`, `chat_has_username`,
`chat_is_created`, `user_administrator_rights`, `bot_administrator_rights`,
`bot_is_member`, `request_title`, `request_username`, `request_photo`) или
`web_app`.

Message entities (для `entities` / `caption_entities`, а также entity
`date_time`, которая принимает `unix_time` + `date_time_format`):

`bold`, `italic`, `underline`, `strikethrough`, `code`, `pre` (`language`),
`text_link` (`url`), `text_mention` (`user.id`), `spoiler`, `blockquote`,
`expandable_blockquote`, `custom_emoji` (`custom_emoji_id`), `mention`,
`hashtag`, `cashtag`, `bot_command`, `url`, `email`, `phone_number`,
`bank_card_number`, `date_time`.

Лимиты: 256 entity на сообщение (`ENTITIES_TOO_LONG`), смещения в кодовых
единицах UTF-16, неизвестные типы — `ENTITY_TYPE_UNSUPPORTED`.

## Лимиты

| Лимит | Значение |
|---|---|
| Длина текста | 4096 символов после разбора |
| Длина подписи | 1024 символа (подпись `copyMessage`: 4096) |
| Entity на сообщение | 256 |
| Id inline-результата | 64 байта |
| Callback data | 64 байта |
| Команд в `setMyCommands` | 100 |
| Размер загрузки | 25 MiB на файл |
| Записей в `allowed_updates` | 100 |
| `limit` в `getUpdates` | по умолчанию и максимум 100 |
| `timeout` в `getUpdates` | ограничивается диапазоном 0..50 секунд |
| `max_connections` | 1..100, по умолчанию 40 |
| Исходник rich-сообщения | 256 KiB |
| `offset`/`limit` в `getFile` | `offset` игнорируется, размер отдаётся целиком |
| Элементов в `sendMediaGroup` | 2..10 |
| Id в `forwardMessages` / `copyMessages` | 1..100 |
| Вопрос / вариант / пояснение опроса | 255 / 100 / 200 символов |
| Имя / описание / краткое описание бота | 64 / 512 / 120 символов |
| Название / описание чата | 1..128 / 0..255 символов |
| Custom title админа | 0..16 символов |

## Не реализовано

Все остальные официальные методы возвращают HTTP `404 METHOD_NOT_FOUND`. Чаще
всего до них доходят:

- live-локация: `editMessageLiveLocation`, `stopMessageLiveLocation` (и
  `sendLocation` с `live_period`);
- прочая отправка: `sendGame`, `sendPaidMedia`, `sendChecklist`,
  `editMessageChecklist`;
- inline-режим: `answerInlineQuery` (и обновлений `inline_query` /
  `chosen_inline_result` нет);
- платежи: `createInvoiceLink`, `getStarTransactions`,
  `editUserStarSubscription`, `getMyStarBalance` и методы управления
  подарками помимо `getAvailableGifts` / `sendGift`;
- администрирование чатов: `banChatSenderChat`, `unbanChatSenderChat`,
  `setChatStickerSet`, `deleteChatStickerSet`, пригласительные ссылки
  (`exportChatInviteLink`, `createChatInviteLink`, `editChatInviteLink`,
  `revokeChatInviteLink`, ссылки-подписки), заявки на вступление
  (`approveChatJoinRequest`, `declineChatJoinRequest`);
- темы форума: `createForumTopic`, `editForumTopic`, `closeForumTopic`,
  `getForumTopicIconStickers` и остальное семейство тем;
- запросы: `getUserChatBoosts`;
- администрирование бота: `setMyDefaultAdministratorRights`,
  `getMyDefaultAdministratorRights`;
- реакции (`setMessageReaction`), стикеры и стикерпаки, stories, методы
  business-аккаунтов и Telegram Passport.

MTProto-клиенты это не затрагивает: всё, что реализовано для официальных
клиентов Android/iOS/Desktop, доступно по MTProto, а не по HTTP. Общие
Stars- и Premium-пайплайны описаны в `docs/premium-stars.md`.

## Демо и self-тесты

| Путь | Назначение |
|---|---|
| `cmd/bots/ptbecho` | echo на `python-telegram-bot` против шлюза, включая демо ephemeral-команды |
| `cmd/bots/aiogramecho` | echo на aiogram 3, ephemeral-ответы, reply- и inline-клавиатуры |
| `cmd/bots/bedolagaformat` | HTML / Markdown / MarkdownV2, rich-меню через `sendRichMessage`, Telegram Login |
| `cmd/bots/botcheck` | Go-проба: импортирует токен бота по MTProto и проверяет цикл бота |
| `cmd/bots/botdemo` | Go-пример: бот шлёт в канал или пользователю по таймеру |
| `cmd/bots/grammystore` | Продовый grammY-бот на HTTP-шлюзе (Postgres, собственный деплой) |
| `scripts/botapi_echo_once.py` | Одноразовый self-тест отправки/приёма |
| `scripts/botapi_receive_selftest.py` | Self-тест очереди обновлений и webhook |
