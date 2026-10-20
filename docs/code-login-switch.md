# Code login switch (client integration)

An operator can turn off login codes on a `telesrv` server, so that the only
way in is phone number + password ([password-login.md](password-login.md)), QR
login from an already signed-in device, or a passkey. While code login is off
the server issues, delivers and accepts no login or sign-up code — no SMS, no
webhook, no email, no 777000 message — so knowing a phone number is not enough
to get into an account. This document is what the project's own clients
(Telegram Desktop, Android, iOS) implement. No TL schema changes are involved:
the state travels in `help.getAppConfig` and the refusal is an ordinary
`rpc_error`.

The server side is one check, `auth.Service.CheckCodeLoginAllowed`
(`internal/app/auth/service.go`), which every login-code entry calls; the
end-to-end wiring test is `internal/mtprotoedge/code_login_switch_e2e_test.go`.
The operator setting is `TELESRV_CODE_LOGIN_ENABLED` in
[configuration.en.md](configuration.en.md) section 5
([中文](configuration.zh-CN.md)).

## Server state: `telesrv_code_login_disabled`

`help.getAppConfig` (callable before login) carries

```json
"telesrv_code_login_disabled": true
```

while code login is off. While it is on the key is **absent** — treat a missing
key as "code login available", which is also the correct reading for official
Telegram servers and older `telesrv` builds. Like `telesrv_sign_up_disabled`
([sign-up-switch.md](sign-up-switch.md)) it is current server state, separate
from the `telesrv_features` capability list.

The key is computed per request and is part of the config hash, so toggling the
switch changes the hash and a client holding the old hash receives the new
config instead of `help.appConfigNotModified`.

## Error: `400 CODE_LOGIN_DISABLED`

| Method | When |
|---|---|
| `auth.sendCode` | Always, for registered and unregistered numbers alike. No code is issued and nothing is delivered. The existing login-code rate limits still run first. |
| `auth.resendCode` | A login or sign-up code was issued while code login was on and it has been turned off since. Nothing is re-sent. |
| `auth.signIn` | With a phone code or an `email_verification` code that was issued before code login was turned off. The code is not consumed. |
| `auth.signUp` | Last-resort check before the account is created (the code was verified before code login was turned off). |
| `auth.resetLoginEmail` | The reset would issue a replacement phone code. |
| `account.sendVerifyEmailCode`, `account.verifyEmail` with `emailVerifyPurposeLoginSetup` | Login-email setup is part of the login flow. |

The check comes **before** the sign-up switch: while code login is off these
methods answer `CODE_LOGIN_DISABLED`, never `SIGN_UP_DISABLED`, whether or not
the number has an account.

Not affected: the `telesrv.*` password-login methods, `auth.checkPassword`
(2FA after password login), QR login (`auth.exportLoginToken` /
`auth.acceptLoginToken`), passkey login, bot login (`auth.importBotAuthorization`),
and codes used inside a signed-in session — changing the phone number,
confirming account deletion, the 2FA recovery email, and changing the login
email of a signed-in account (`emailVerifyPurposeLoginChange`).

## Nobody is locked out

The server refuses to start when `TELESRV_CODE_LOGIN_ENABLED=false` while
`TELESRV_PASSWORD_LOGIN_ENABLED` is not `true`. So whenever a client sees
`telesrv_code_login_disabled: true`, the same app config also lists
`password_login` in `telesrv_features`. The server does not check that every
account has a login password: an account without one cannot log in while code
login is off until the operator sets a password for it in the admin panel.

## Recommended client behaviour

- Read `telesrv_code_login_disabled` from the app config on the login screen.
  When it is `true`, hide the "send code" path and go straight to password
  login (`telesrv.getLoginPassword`); keep QR login and passkey available.
- On `CODE_LOGIN_DISABLED` from any method above — the cached config may be
  stale — show *"Login codes are turned off on this server. Sign in with your
  password."* and switch to password login.
- When `telesrv.getLoginPassword` answers `PASSWORD_LOGIN_NOT_SET` while code
  login is off, there is no way in from this device: show *"This account has
  no login password. Please contact the administrator."*
- Official Telegram clients do not know the key; they show the raw
  `CODE_LOGIN_DISABLED` error string on `auth.sendCode`. That is expected.
