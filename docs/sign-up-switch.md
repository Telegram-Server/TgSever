# Closed sign-up (client integration)

An operator can close sign-up on a `telesrv` server: numbers that already have
an account keep signing in exactly as before, but no new account can be created
from a client. This document is what the project's own clients (Telegram
Desktop, Android, iOS) implement. No TL schema changes are involved — the state
travels in `help.getAppConfig` and the refusal is an ordinary `rpc_error`.

The server side is one check, `auth.Service.CheckSignUpAllowed`
(`internal/app/auth/service.go`); the end-to-end wiring test is
`internal/mtprotoedge/sign_up_switch_e2e_test.go`. The operator setting is
`TELESRV_SIGN_UP_ENABLED` in [configuration.en.md](configuration.en.md) section 5
([中文](configuration.zh-CN.md)).

## Server state: `telesrv_sign_up_disabled`

`help.getAppConfig` (callable before login) carries

```json
"telesrv_sign_up_disabled": true
```

while sign-up is closed. While sign-up is open the key is **absent** — treat a
missing key as "open", which is also the correct reading for official Telegram
servers and older `telesrv` builds. The key is separate from `telesrv_features`:
that one lists capabilities, this one is current server state.

The key is computed per request and is part of the config hash, so toggling the
switch changes the hash and a client holding the old hash receives the new
config instead of `help.appConfigNotModified`.

## Error: `400 SIGN_UP_DISABLED`

Returned only for a phone number that has **no** account:

| Method | When |
|---|---|
| `auth.sendCode` | The number has no account. No code is issued and no SMS or email is sent. The existing login-code rate limits still run first. |
| `auth.resendCode` | The code was issued while sign-up was open and sign-up has been closed since. |
| `auth.signIn` | Where the server would have answered `auth.authorizationSignUpRequired` (sign-up was closed after the code was sent). |
| `auth.signUp` | Last-resort check right before the account is created. |
| `telesrv.getLoginPassword` | Instead of the sign-up answer (`has_password` false), when password login is enabled. |
| `telesrv.signUpWithPassword` | Before the account is created, when password login is enabled. |

Existing accounts are never affected: login code, 2FA, QR login, passkey login
and password login all behave as before, and bots are not affected either.

## Recommended client behaviour

- Read `telesrv_sign_up_disabled` from the app config on the login screen. When
  it is `true`, hide every sign-up / "create account" entry and show:
  *"Sign-up is closed on this server. Please contact the administrator to get
  an account."*
- On `SIGN_UP_DISABLED` from any method above — the cached config may be stale —
  show the same localized message and return to the phone-number screen.
- Official Telegram clients do not know the key; they show the raw
  `SIGN_UP_DISABLED` error string. That is expected.

New members on a closed server get an account created by the operator in the
admin panel, then sign in with a login code like any existing user.
