# Phone number + password login (client integration)

`telesrv` can let a user sign up and sign in with a phone number and a
**login password** instead of a login code. This is a telesrv protocol
extension, not an official Telegram feature: five `telesrv.*` methods that only
`telesrv` and the project's own clients (Telegram Desktop, Android, iOS)
understand. This document is the contract the three clients implement. The
server side lives in `internal/rpc/telesrv_login_password*.go` and
`internal/app/loginpassword`; the end-to-end tests that pin the call orders
below are `internal/mtprotoedge/login_password*_e2e_test.go`.

The operator switch, rate limits and risks are in
[configuration.en.md](configuration.en.md) section 5.1
([中文](configuration.zh-CN.md)).

## Schema

The methods come from `_schema/telesrv.tl` in the td fork
(`github.com/Telegram-Server/td`, which `go.mod` substitutes for
`github.com/iamxvbaba/td`). They reuse official types only, so a client needs
five request serializers and no new deserializers:

| Method | Wire ID | Returns | Needs authorization |
|---|---|---|---|
| `telesrv.getLoginPassword flags:# phone_number:string` | `#e35bc783` | `account.Password` | no |
| `telesrv.signInWithPassword flags:# phone_number:string password:InputCheckPasswordSRP` | `#b57ee87d` | `auth.Authorization` | no |
| `telesrv.signUpWithPassword flags:# phone_number:string first_name:string last_name:string new_settings:account.PasswordInputSettings` | `#e57cf5b6` | `auth.Authorization` | no |
| `telesrv.getMyLoginPassword flags:#` | `#70c0eadb` | `account.Password` | yes |
| `telesrv.updateLoginPassword flags:# current:InputCheckPasswordSRP new_settings:account.PasswordInputSettings` | `#917c32ca` | `Bool` | yes |

`flags` is always `0` today. Wrap the calls in `invokeWithLayer` /
`initConnection` like any other request.

### Keeping clients in sync with `telesrv.tl`

The rules are in td's `_schema/TELESRV_EXTENSIONS.md`; in short:

- `_schema/telesrv.tl` is the single source of truth. Clients copy or mirror
  it and never become the source; wire IDs never change once released (a
  semantic change is a new method).
- **Telegram Desktop:** copy `telesrv.tl` byte for byte into
  `Telegram/SourceFiles/mtproto/scheme/` and add it to `scheme_files` in
  `Telegram/cmake/td_scheme.cmake`. Never append it to `api.tl`, which every
  upstream Layer update replaces.
- **iOS:** hand-write the five serializers in a separate file (for example
  `submodules/TelegramApi/Sources/ApiTelesrv.swift`) via
  `extension Api.functions { public enum telesrv {} }`. Do not edit the
  generated `Api*.swift` files.
- **Android:** hand-write the five request classes apart from the upstream
  `TLRPC.java`.
- Every hand-written serializer names, in a comment, the `telesrv.tl` line and
  wire ID it implements. Re-check the table above against `telesrv.tl` whenever
  the td dependency moves.

## Capability discovery

Show the password entry points only when the server advertises them. Call
`help.getAppConfig` (hash `0` the first time) and read the `JSONObject` in
`help.appConfig.config`: key `telesrv_features` is a `JSONArray` of
`JSONString`. Password login is available when it contains `"password_login"`.

- Switch off: the key is absent (`TestLoginPasswordDisabledEndToEnd`), and any
  of the five methods returns `403 PASSWORD_LOGIN_DISABLED`.
- Official servers and older `telesrv` builds never send the key; the client
  must fall back to code login only.
- Turning the switch on or off changes the app-config hash, so the client's
  cached hash gets a full config back, not `help.appConfigNotModified`.

Test: `TestLoginPasswordEndToEnd/getAppConfig advertises password_login`.

## Call sequences

`srp` below is the client's existing 2FA SRP code (see [SRP](#srp-parameters)).
Each step is one RPC on the same connection (the same auth key) unless noted.

### Choosing sign-in or sign-up

`telesrv.getLoginPassword(phone_number)` has four outcomes:

| Result | Meaning | Next step |
|---|---|---|
| `account.password` with `has_password` false | No account has this number | Sign-up |
| `400 SIGN_UP_DISABLED` | No account has this number and the operator has closed sign-up ([sign-up-switch.md](sign-up-switch.md)) | Show "Sign-up is closed on this server, please contact the administrator" and return to the phone-number screen; do not open the sign-up page |
| `400 PASSWORD_LOGIN_NOT_SET` | The account exists but has no login password | Offer code login |
| `account.password` with `has_password` true, `current_algo`, `srp_B`, `srp_id` | The account has a login password | Sign-in |

Tests: `getLoginPassword on an unregistered phone returns the registration algo`,
`getLoginPassword on a registered phone without a login password returns PASSWORD_LOGIN_NOT_SET`,
`getLoginPassword on a phone with a login password returns a complete SRP challenge`
(all in `TestLoginPasswordEndToEnd`).

### Sign-up

1. `telesrv.getLoginPassword(phone)` → `has_password` false; keep `new_algo`.
2. `(hash, salt1') = srp.NewHash(password, new_algo)`; `salt1'` is
   `new_algo.salt1` with 32 random bytes appended, exactly as when setting a
   2FA password.
3. `telesrv.signUpWithPassword(phone, first_name, last_name, new_settings)`
   with `new_settings = account.passwordInputSettings{new_algo: {salt1', salt2, g, p}, new_password_hash: hash}`.
   Leave `email` unset; `hint` is accepted and ignored.
4. → `auth.authorization`. The session is signed in at once; the account gets the
   normal 777000 welcome message (and, if the operator grants it, Premium),
   exactly like a code sign-up. No "new login" notice is sent (there are no
   other sessions).

Tests: `signUpWithPassword authorizes at once and sends the welcome message`,
`signUpWithPassword on an occupied phone returns PHONE_NUMBER_OCCUPIED`,
`signUpWithPassword rejects invalid input without creating an account`.

### Sign-in

1. `telesrv.getLoginPassword(phone)` → `has_password` true, `current_algo`,
   `srp_B`, `srp_id`.
2. `(A, M1) = srp.Check(password, srp_B, random a, current_algo)`.
3. `telesrv.signInWithPassword(phone, inputCheckPasswordSRP{srp_id, A, M1})`
   with the **same phone string** and on the **same connection** as step 1.
4. → `auth.authorization`. The tail is the same as `auth.signIn`: the session
   is bound, the account's other online sessions get the "New login" service
   notification, and the welcome message is sent.

A challenge is single-use. Any answer to `signInWithPassword` (success or
error) burns it, so **every retry starts again at step 1**, and each
`getLoginPassword` spends the login-code quota (see FLOOD_WAIT below).

Tests: `signInWithPassword with the right password signs in and notifies other sessions`,
`signInWithPassword with a wrong password fails and burns the challenge`,
`signInWithPassword rejects a challenge issued to another auth key`,
`signInWithPassword rejects a challenge submitted for a different phone`,
`repeated getLoginPassword from others does not evict a pending challenge`.

### Sign-in with 2FA

The login password does not replace the 2FA cloud password; an account with
both needs both.

1. `telesrv.getLoginPassword(phone)`.
2. `telesrv.signInWithPassword(...)` → `401 SESSION_PASSWORD_NEEDED`. The
   session is now *password pending*: business calls return
   `401 AUTH_KEY_UNREGISTERED`.
3. `account.getPassword` → the 2FA parameters (a different salt and challenge).
4. `auth.checkPassword(srp.Check(cloud password, ...))` → `auth.authorization`.
   From here this is the ordinary 2FA screen, including its own recovery
   options. A proof computed with the login password against these parameters
   returns `PASSWORD_HASH_INVALID`.

Test: `signInWithPassword on an account with 2FA requires auth.checkPassword`;
while pending, `getMyLoginPassword`/`updateLoginPassword` return
`AUTH_KEY_UNREGISTERED` (`unauthorized and password_pending sessions cannot manage the login password`).

### Managing the login password (signed in)

Always start with `telesrv.getMyLoginPassword()`. It returns `has_password` and
`new_algo`, plus `current_algo`, `srp_B`, `srp_id` when a password is set.

- **Set** (no password yet): `getMyLoginPassword` → `has_password` false →
  `srp.NewHash(new, new_algo)` →
  `telesrv.updateLoginPassword(current: inputCheckPasswordEmpty, new_settings)` → `boolTrue`.
- **Change**: `getMyLoginPassword` → proof of the old password from
  `current_algo`/`srp_B`/`srp_id`, and `new_settings` from the same response's
  `new_algo` → `telesrv.updateLoginPassword(current: proof, new_settings)` → `boolTrue`.
- **Remove**: `getMyLoginPassword` → proof of the current password →
  `telesrv.updateLoginPassword(current: proof, new_settings{new_algo: passwordKdfAlgoUnknown, new_password_hash: empty})`
  → `boolTrue`. Afterwards `getLoginPassword(phone)` returns
  `PASSWORD_LOGIN_NOT_SET`. Removing when there is no password is a no-op that
  returns `boolTrue`.

Every successful set, change or removal makes 777000 send the account a message
(bold title "Login password set." / "Login password changed." /
"Login password removed.") that reaches all its sessions as `updateNewMessage`.
A reset without the old password (see "Forgotten password") sends
"Login password reset." instead.
Challenges issued before a change answer `SRP_PASSWORD_CHANGED` afterwards.
Deleting the account deletes its login password.

Tests (`runLoginPasswordManageSubtests`, run from `TestLoginPasswordEndToEnd`):
`getMyLoginPassword without a login password lets a code account set one and sign in with it`,
`updateLoginPassword with a login password set requires the current one`,
`updateLoginPassword with an empty new hash removes the login password`,
`updateLoginPassword rejects an email and a malformed new salt`,
`every login password change notifies the account's other sessions from 777000`,
`challenges issued before a password change return SRP_PASSWORD_CHANGED`,
`deleting the account deletes its login password`.

### Forgotten password

There is no recovery email. A user who forgot the login password:

1. Signs in with a login code: `auth.sendCode` → `auth.signIn` (plus
   `auth.checkPassword` if 2FA is on).
2. `telesrv.getMyLoginPassword()` → `has_password` true; keep `new_algo`.
3. `telesrv.updateLoginPassword(current: inputCheckPasswordEmpty, new_settings from new_algo)`
   → `boolTrue`, **only if the operator enabled the reset window and the
   session's authorization is younger than it**
   (`TELESRV_PASSWORD_LOGIN_RESET_WINDOW`, **off by default**).

The window counts from when the current authorization was created, and **any**
new authorization starts it, not only a code sign-in: completing 2FA, QR login
(`auth.acceptLoginToken`), passkey login and password sign-in or sign-up do too.
That is why it is off by default: with it on, anyone holding one existing session
of the account can approve a new device by QR and, from that device, replace the
login password without knowing it. Operators enable it only if they accept that.

**The reset may not be available.** When the window is off (the default), has
passed, or the session's authorization cannot be read, step 3 returns
`400 PASSWORD_HASH_INVALID`, exactly like a wrong password. The client cannot
query the window. Handle it like this:

- Offer "Forgot password?" right after a code sign-in, never as the only way
  out. On `PASSWORD_HASH_INVALID` for an empty `current`, say the password
  cannot be reset on this server without the old one (or that the user must
  sign in with a code again and retry at once), and keep the account usable:
  sign-in with a login code still works without the login password.
- A user who cannot reset it can still remove it only with the old password;
  without it they stay on code sign-in until an operator helps.

A successful reset changes the password like any other change (earlier
challenges answer `SRP_PASSWORD_CHANGED`), but 777000 sends a distinct notice to
every session of the account: bold title "Login password reset.", saying the
login password was reset without the old password from a session that signed in
recently, and advising to check active sessions if it was not the user. Removing
the password with an empty `current` inside the window sends the same title.

Test: `TestLoginPasswordResetEndToEnd` (`within the reset window an empty current resets a forgotten login password`,
`with the reset window set to 0 an empty current is rejected`).

## Errors

The codes below are what the server code returns (not a wish list), with the
checks in the order the server runs them. The first table applies to all five
methods.

### Handling common to every method

| Error | Meaning | Client handling |
|---|---|---|
| `403 PASSWORD_LOGIN_DISABLED` | The operator switched the feature off (checked before the arguments are parsed) | Hide the password entry points, fall back to code login, refresh `help.getAppConfig` |
| `420 FLOOD_WAIT_X` | A rate limit; `X` is the wait in seconds | Show the wait; do not retry before `X` seconds |
| `500 INTERNAL_SERVER_ERROR` | Server or storage failure | Generic error, retry later |

### `telesrv.getLoginPassword`

Checks: switch → login-code quota shared with `auth.sendCode` (per auth key,
then per phone) → phone → account → sign-up switch (only when the number has
no account).

| Error | When | Client handling |
|---|---|---|
| `406 PHONE_NUMBER_INVALID` | Malformed number, or a reserved system-account number | Ask for a valid number |
| `420 FLOOD_WAIT_X` | The shared login-code quota is used up (operator defaults: 20 per auth key and 5 per phone per 10 minutes); every call counts, including the ones you make to retry a sign-in | Show the wait |
| `400 PASSWORD_LOGIN_NOT_SET` | The account exists but has no login password | Offer code login |
| `400 SIGN_UP_DISABLED` | The number has no account and sign-up is closed. Existing accounts never get it | "Sign-up is closed on this server, please contact the administrator"; back to the phone-number screen |

### `telesrv.signUpWithPassword`

Checks: switch → shared login-code quota (it counts even when the request is
rejected later) → `new_settings` algorithm → sign-up switch → phone → name →
`new_settings` → create the account. Only the algorithm *type* is checked
before the sign-up switch, so a client that sends a well-formed request gets
`SIGN_UP_DISABLED` whenever sign-up is closed.

| Error | When | Client handling |
|---|---|---|
| `406 PHONE_NUMBER_INVALID` | Malformed or reserved number | Ask for a valid number |
| `420 FLOOD_WAIT_X` | Shared login-code quota | Show the wait |
| `400 SIGN_UP_DISABLED` | The operator closed sign-up (possibly after `getLoginPassword` answered `has_password` false). No account is created | "Sign-up is closed on this server, please contact the administrator"; back to the phone-number screen |
| `400 FIRSTNAME_INVALID` | First name empty after trimming, or first/last name longer than 64 characters | Ask for a name |
| `400 PASSWORD_HASH_INVALID` | `new_algo` is not the SHA256-SHA256-PBKDF2-ModPow algorithm, or `new_password_hash` is out of range | Client bug: recompute from `new_algo` |
| `400 NEW_SETTINGS_INVALID` | `email` is set, or `new_algo`/`new_password_hash` is missing | Client bug: send only `new_algo` and the hash |
| `400 NEW_SALT_INVALID` | `g`, `p` or `salt2` differ from the server's, or `salt1` is not the server prefix plus exactly 32 bytes | Client bug: start again from a fresh `getLoginPassword` |
| `400 PHONE_NUMBER_OCCUPIED` | The number was registered meanwhile | Go back to `getLoginPassword` (now sign-in or code login) |
| `401 AUTH_KEY_PERM_EMPTY` | The request came over a temporary key not yet bound to a permanent one | Bind the temp key (`auth.bindTempAuthKey`) and retry |

### `telesrv.signInWithPassword`

Checks: SRP rate group (before everything else, so it can answer even when the
switch is off) → switch → challenge → per-account limit → password still
current → proof → account state → 2FA.

| Error | When | Client handling |
|---|---|---|
| `420 FLOOD_WAIT_X` | SRP group (default 10 per minute per auth key before sign-in), or the per-account limit (default 10 per hour across all auth keys, right or wrong) | Show the wait; suggest code login, which the per-account limit does not affect |
| `400 PASSWORD_HASH_INVALID` | Wrong password, or `password` is `inputCheckPasswordEmpty` | "Wrong password"; the challenge is burned, restart at `getLoginPassword` |
| `400 SRP_ID_INVALID` | `srp_id` is 0, unknown, already used, expired (5 minutes), evicted (more than 5 pending on this auth key), issued to another connection, or for a different phone. A **malformed phone also lands here**, not on `PHONE_NUMBER_INVALID` | Restart at `getLoginPassword` |
| `400 SRP_PASSWORD_CHANGED` | The login password changed or was removed after the challenge was issued | Restart at `getLoginPassword` (it may now say `PASSWORD_LOGIN_NOT_SET`) |
| `406 PHONE_NUMBER_INVALID` | The account was deleted, is a bot or a system account. Only returned **after a valid proof** | Treat as "no such account"; restart at `getLoginPassword` |
| `401 SESSION_PASSWORD_NEEDED` | Proof accepted, but the account has 2FA | Go to the 2FA screen (`account.getPassword` → `auth.checkPassword`) |
| `401 AUTH_KEY_PERM_EMPTY` | Temp key not bound to a permanent key | Bind and retry from `getLoginPassword` |

### `telesrv.getMyLoginPassword`

| Error | When | Client handling |
|---|---|---|
| `401 AUTH_KEY_UNREGISTERED` | Not signed in, or 2FA still pending | Finish signing in first |
| `400 BOT_METHOD_INVALID` | Called by a bot (or a deleted/system account) | Do not offer the setting to bots |

No rate limit applies.

### `telesrv.updateLoginPassword`

Checks: authorization → account freeze → switch → `new_settings` (an invalid
request does not use up the challenge) → `current` → write → 777000 notice.

| Error | When | Client handling |
|---|---|---|
| `401 AUTH_KEY_UNREGISTERED` | Not signed in, or 2FA still pending | Finish signing in first |
| `420 FROZEN_METHOD_INVALID` | The account is frozen | Show the frozen-account notice |
| `400 BOT_METHOD_INVALID` | Bot, deleted or system account | Do not offer the setting |
| `400 NEW_SETTINGS_INVALID` | `email` is set (also when removing), or a hash without `new_algo` | Client bug |
| `400 NEW_SALT_INVALID` | `new_algo` salts/group differ from `getMyLoginPassword`'s | Client bug: recompute from a fresh `getMyLoginPassword` |
| `400 PASSWORD_HASH_INVALID` | Wrong current password; an unsupported `new_algo` type with a non-empty hash; or `current` empty while a password is set and the reset window is off (the default), has passed, or the authorization cannot be read | Wrong password: re-ask. Empty current rejected: the reset is unavailable; ask for the old password, or explain that code sign-in still works (see "Forgotten password") |
| `400 SRP_ID_INVALID` | `srp_id` 0, unknown, used, expired, from another connection or another account | Restart at `getMyLoginPassword` |
| `400 SRP_PASSWORD_CHANGED` | The password changed after the challenge was issued | Restart at `getMyLoginPassword` |

No FLOOD_WAIT of its own: it is not in the SRP group and not on the login-code
quota.

## SRP parameters

The math is the same as 2FA (`account.getPassword` / `auth.checkPassword` /
`account.updatePasswordSettings`): `passwordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow`
with the same 2048-bit `p`, `g = 3`, and the same `salt1`/`salt2` rules, all
from the server's `internal/srp` package that 2FA also uses. Clients reuse their
2FA code unchanged (Telegram Desktop `Core::ComputeCloudPasswordHash` /
`Core::ComputeCloudPasswordCheck`, Android `SRPHelper`, iOS TelegramCore's
2FA SRP helpers).

Where each field comes from:

- `new_algo` (every response): the server defaults. `salt1` is an 8-byte base;
  the client appends 32 random bytes when it hashes a new password, and the
  server requires exactly that shape. `new_secure_algo` and `secure_random` are
  filled in like `account.getPassword` and are unused here.
- `current_algo` (only with a password): the `salt1`/`salt2` the client sent
  when it set the password (so `salt1` includes its 32 random bytes), with the
  default `g`/`p`.
- `srp_B`: `k·v + g^b mod p` for a fresh secret `b`, 256 bytes.
- `srp_id`: a random non-zero 64-bit id naming the challenge.

Differences from 2FA:

- **The challenge belongs to the connection.** It is bound to the raw auth key
  that called `getLoginPassword`/`getMyLoginPassword`; answering on another
  connection gives `SRP_ID_INVALID`. With PFS, a temp key rotated between the
  two calls is another raw key too: get a fresh challenge after the rotation
  instead of answering the old one.
- **Single use.** The first answer burns it, whatever the outcome; a second
  answer to the same `srp_id` gives `SRP_ID_INVALID`. 2FA's challenge stays on
  the account until the password changes.
- **Independent challenges.** Each call issues a new challenge and does not
  replace anyone else's (only the caps evict: 5 pending per auth key, 10000
  in total, oldest first). It expires after 5 minutes.
- **A separate secret.** The login password and the 2FA cloud password are
  stored apart and never stand in for each other.
- **`new_settings` is narrower.** `email` is rejected (`NEW_SETTINGS_INVALID`;
  there is no recovery email), `hint` is ignored, and an empty
  `new_password_hash` means remove. Forgotten password is the code sign-in path
  above, not `auth.requestPasswordRecovery`.
- A challenge from `getLoginPassword` on the same connection may also be used as
  `current` for `updateLoginPassword`, as long as it is for the signed-in account.
