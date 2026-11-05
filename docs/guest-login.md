# Guest login (client integration)

An operator can let clients register on a `telesrv` server without a phone
number. The client calls one `telesrv.*` extension method; the server picks a
number, creates the account and signs the current authorization in. This
document is what the project's own clients (Telegram Desktop, Android, iOS)
implement.

The method comes from the `telesrv` TL overlay in our `td` fork
(`v1.7.0-telesrv.2` and later):

```tl
telesrv.signUpAsGuest#c3c587ce flags:# first_name:flags.0?string last_name:flags.1?string = auth.Authorization;
```

It is callable before login, like `auth.signIn`. The server side is
`internal/app/guestsignup`; the end-to-end wiring test is
`internal/mtprotoedge/guest_login_e2e_test.go`. The operator settings are
`TELESRV_GUEST_LOGIN_ENABLED` and `TELESRV_GUEST_IP_BIND_DAYS` in
[configuration.en.md](configuration.en.md) section 5
([中文](configuration.zh-CN.md)); both can be overridden at runtime from
Admin → Server Settings → **Guest login** without a restart.

## Server state: `telesrv_guest_login_enabled`

`help.getAppConfig` (callable before login) carries

```json
"telesrv_guest_login_enabled": true
```

while guest login is on. While it is off the key is **absent**. The direction
is the opposite of `telesrv_sign_up_disabled` and
`telesrv_code_login_disabled`, because guest login is off by default: a missing
key — also what official Telegram servers and older `telesrv` builds send —
means "no guest option". The key is computed per request and is part of the
config hash, so toggling the switch makes a client holding the old hash
receive the new config.

## What the server does

1. Guest login off: `400 GUEST_LOGIN_DISABLED`, nothing is created.
2. The server takes the connection's client IP (behind a trusted reverse
   proxy, the forwarded one). It is reduced to a key: an IPv4 address as is, an
   IPv6 address by its `/64` prefix. No usable IP: `400 GUEST_IP_UNAVAILABLE`.
3. **Same IP, same account.** If this IP key got a guest account within the
   last `TELESRV_GUEST_IP_BIND_DAYS` days (default 7, counted from when the
   account was created), and that account is still a plain guest account, the
   request signs in to it instead of creating a new one. `first_name` /
   `last_name` are ignored in that case.
4. Otherwise a new account is created:
   - The number is random in the fictional North American range
     `+1 NXX 555-01XX` (reserved for fiction, never assigned to real
     subscribers), using only area codes the numbering library accepts, so it
     is valid under the strict number-format check too. A number already taken
     is skipped. If no free number is found after several tries the answer is
     `500 GUEST_NUMBER_POOL_EXHAUSTED`.
   - Empty `first_name`: a random name of the form "adjective的noun" (500 × 500
     built-in words, e.g. 快乐的熊猫). `last_name` is kept as sent.
   - The account gets the same premium grant, self-sign-up permission defaults
     and welcome message as any self sign-up, and is marked as a guest account
     (visible to the operator).
   - The IP key now points at the new account.

Guest sign-up **ignores the sign-up switch**: it works while
`telesrv_sign_up_disabled` is set. It does not depend on the code login switch
either. Concurrent guest sign-ups from one IP create one account; the others
sign in to it.

### When an account stops being handed out

An account behind a shared IP (office, school, carrier NAT) must not be handed
to the next person once its owner has secured it. The server re-checks the
bound account on every guest sign-up and creates a new one instead when the
account has:

- set a login password (`telesrv.updateLoginPassword`, see
  [password-login.md](password-login.md)) or a two-step verification password;
- changed its phone number;
- been deleted.

Until then anyone behind the same IP key within the window lands in the same
account — that is the point of the binding, and the reason the client should
push the user to set a password (below).

## Response

On success the answer is `auth.authorization` with the user, exactly as for
`auth.signUp`. The user's `phone` is the assigned number. Errors besides the
ones above are the usual sign-in ones (`AUTH_KEY_PERM_EMPTY`, flood waits, …).

| Error | Meaning |
|---|---|
| `400 GUEST_LOGIN_DISABLED` | Guest login is off (the cached config may be stale). |
| `400 GUEST_IP_UNAVAILABLE` | The server could not determine the client IP. |
| `500 GUEST_NUMBER_POOL_EXHAUSTED` | No free guest number; tell the user to try later. |

## Recommended client behaviour

- Show a "Continue as guest" entry on the login screen only while
  `telesrv_guest_login_enabled` is `true`. Offer an optional name field; leave
  it empty to get a generated name.
- On `GUEST_LOGIN_DISABLED`, hide the entry and show *"Guest login is turned off
  on this server."*
- After a guest sign-up, tell the user the account can only be recovered from
  the same network for a limited time, and offer to set a login password right
  away (`telesrv.updateLoginPassword`). Logging out of a guest account without a
  password or another signed-in device means losing it after the binding
  window.
- Official Telegram clients do not know the method or the key; nothing changes
  for them.
