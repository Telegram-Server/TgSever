# iOS offline push (APNs)

When an iOS user is not looking at the app, `telesrv` sends Apple Push
Notification service (APNs) pushes so the phone still shows new messages and
rings for calls. The iOS client registers its device token with
`account.registerDevice`; the server keeps the registration whether or not push
is configured, so turning push on later works for devices that are already
registered.

Push is **off by default** (`TELESRV_APNS_ENABLED=false`). This guide covers:

- [Choosing an authentication method](#choosing-an-authentication-method)
- [Certificate setup](#certificate-setup)
- [`.p8` token setup](#p8-token-setup)
- [Docker](#docker)
- [What startup checks](#what-startup-checks)
- [Tuning](#tuning)
- [Metrics](#metrics)
- [Renewing the certificate](#renewing-the-certificate)
- [Sharing credentials with official Telegram](#sharing-credentials-with-official-telegram)
- [Troubleshooting](#troubleshooting)

## Choosing an authentication method

APNs accepts two kinds of provider credentials. Configure **exactly one**:

| | Certificate (`.pem`) | Token (`.p8`) |
|---|---|---|
| Variables | `TELESRV_APNS_CERT_FILE` | `TELESRV_APNS_KEY_FILE`, `TELESRV_APNS_KEY_ID`, `TELESRV_APNS_TEAM_ID` |
| Expires | Yes, after one year | No (revoked manually) |
| Topics | Those listed in the certificate | Every app of the team (or those the key is restricted to) |
| Reuse an existing official-Telegram certificate | Yes, no conversion beyond PEM export | Only if you also have the `.p8` |

Both methods pick the APNs sandbox or production gateway per device from the
`app_sandbox` flag the client sent at registration. `TELESRV_APNS_TOPIC` is the
iOS bundle id (for example `co.kaka.tgpro`); VoIP pushes go to
`<topic>.voip`, so a certificate must list both topics if calls should ring.
If it does not, startup logs the warning `apns certificate does not cover the
voip topic; voip push disabled` and only VoIP pushes are turned off; message
pushes keep working.

## Certificate setup

Use an **Apple Push Notification service SSL (Sandbox & Production)**
certificate for the bundle id. The server needs the certificate and its
**unencrypted** private key in one PEM file. From a `.p12` exported from
Keychain Access:

```bash
openssl pkcs12 -in apns.p12 -out apns.pem -nodes -legacy   # -legacy for OpenSSL 3 and old .p12 files
chmod 0400 apns.pem
```

Then:

```env
TELESRV_APNS_ENABLED=true
TELESRV_APNS_TOPIC=co.kaka.tgpro
TELESRV_APNS_CERT_FILE=/etc/telesrv-apns/apns.pem
```

Check the certificate's topics and expiry before deploying:

```bash
openssl x509 -in apns.pem -noout -subject -enddate
openssl x509 -in apns.pem -noout -text | grep -A3 1.2.840.113635.100.6.3.6
```

## `.p8` token setup

In the Apple Developer portal, create a key with the **Apple Push
Notifications service (APNs)** capability and download `AuthKey_<KEY_ID>.p8`
(it can be downloaded once). The Key ID is shown next to the key; the Team ID is
in the account's membership details. Both are 10 characters.

```env
TELESRV_APNS_ENABLED=true
TELESRV_APNS_TOPIC=co.kaka.tgpro
TELESRV_APNS_KEY_FILE=/etc/telesrv-apns/AuthKey_ABC123DEFG.p8
TELESRV_APNS_KEY_ID=ABC123DEFG
TELESRV_APNS_TEAM_ID=TEAM123456
```

The server signs a short-lived JWT with the key and re-signs it before Apple's
one-hour limit; there is nothing to renew.

## Docker

Credentials are never baked into the image. `deploy/docker/compose.yaml`
mounts `deploy/docker/apns-keys` **read-only** at `/etc/telesrv-apns`, in line
with the hardened container (read-only root filesystem, `cap_drop: [ALL]`,
user `10001:10001`):

```bash
cp apns.pem deploy/docker/apns-keys/
sudo chown 10001:10001 deploy/docker/apns-keys/apns.pem
sudo chmod 0400 deploy/docker/apns-keys/apns.pem
```

Set the variables in `deploy/docker/.env` (paths under `/etc/telesrv-apns/`)
and recreate the server container: `docker compose up -d server`. The
directory content other than `.gitkeep` is git-ignored; never commit
credentials.

## What startup checks

With `TELESRV_APNS_ENABLED=true`, startup **fails** with a message naming the
variable when:

- `TELESRV_APNS_TOPIC` is empty;
- both or neither of `TELESRV_APNS_CERT_FILE` and `TELESRV_APNS_KEY_FILE` are set;
- `TELESRV_APNS_KEY_ID`/`TELESRV_APNS_TEAM_ID` are set together with a certificate;
- the `.p8` method misses `TELESRV_APNS_KEY_ID` or `TELESRV_APNS_TEAM_ID`, or
  either is not 10 upper-case letters/digits;
- the file cannot be read or parsed (certificate + key PEM, or PKCS#8 EC key);
- the certificate does not list `TELESRV_APNS_TOPIC`.

Certificate validity does **not** stop the server:

- fewer than 30 days left: warning log `apns certificate expires soon` with
  `not_after` and `days_left`;
- already expired: error log `apns certificate expired; push disabled`; the
  server starts normally, keeps storing registrations, sends no pushes and
  reports `telesrv_push_enabled 0`.

The certificate is read once at startup. After replacing it, restart the server.
A certificate that expires while the server runs is rejected by Apple; the
`telesrv_push_apns_certificate_days_left` gauge goes negative and the restart
turns push off cleanly.

When push is on, the log shows `apns push enabled` with the topic.

## Tuning

Push is best effort and never slows message delivery: jobs go into a bounded
in-memory queue (not persisted) served by a fixed worker pool.

| Variable | Default | Range | Meaning |
|---|---|---|---|
| `TELESRV_PUSH_QUEUE_SIZE` | `4096` | 1–1000000 | Queue capacity; a full queue drops the job and counts it |
| `TELESRV_PUSH_WORKERS` | `4` | 1–256 | Worker count |
| `TELESRV_PUSH_SEND_TIMEOUT` | `10s` | 1s–2m | Timeout of one send to one device |
| `TELESRV_PUSH_MAX_RETRIES` | `3` | 0–10 | Retries after the first attempt for 429, 5xx and timeouts |

Raise workers before the queue: the queue only absorbs bursts, workers set the
sustained rate. A large channel post fans out to many devices.

## Metrics

Exposed on the debug listener (`TELESRV_DEBUG_ADDR`, default
`127.0.0.1:6060`) at `/metrics`:

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `telesrv_push_sent_total` | counter | `type`, `result` | Send attempts. `type` is the push kind (`message`, …); `result` is `sent`, `invalid_token` (410, `BadDeviceToken`, `Unregistered`, `DeviceTokenNotForTopic`), `retryable` (429, 5xx, timeout or network error) or `permanent` (anything else: certificate, topic, payload) |
| `telesrv_push_devices_removed_total` | counter | `reason` | Registrations removed automatically (`invalid_token`, `user_mismatch`) |
| `telesrv_push_queue_dropped_total` | counter | `type` | Jobs dropped because the queue was full |
| `telesrv_push_queue_length` | gauge | | Jobs waiting now (only while push runs) |
| `telesrv_push_queue_capacity` | gauge | | `TELESRV_PUSH_QUEUE_SIZE` |
| `telesrv_push_enabled` | gauge | | `1` when pushes are being sent |
| `telesrv_push_apns_certificate_days_left` | gauge | | Whole days until the certificate expires, negative after expiry; absent with `.p8` |

Suggested alerts: `telesrv_push_apns_certificate_days_left < 30`;
`telesrv_push_enabled == 0` where push is expected;
`rate(telesrv_push_queue_dropped_total[5m]) > 0`; a rising share of
`result="permanent"` or `result="retryable"`. A burst of `permanent` with every
device usually means the credentials were revoked or the topic is wrong
(`InvalidProviderToken`, `BadCertificate`, `TopicDisallowed` in the warning
logs).

Logs carry only the first characters of a device token and never a payload or
the device's encryption secret.

## Renewing the certificate

An APNs certificate is valid for one year. Renew it **before** it expires:

1. In the Apple Developer portal, create a new **Apple Push Notification service
   SSL (Sandbox & Production)** certificate for the same bundle id from a new
   CSR. Do not revoke the old one yet: both stay valid side by side until the
   old one expires.
2. Export the new certificate with its private key (`.p12`) and convert it to
   PEM as in [Certificate setup](#certificate-setup).
3. Replace the certificate **in both places that use it**:
   - official Telegram: upload the new certificate wherever the app's push
     credentials were registered with Telegram, as was done for the current
     one;
   - this server: replace the file in `deploy/docker/apns-keys` (or at
     `TELESRV_APNS_CERT_FILE`) and restart the server.

   Doing only one side leaves the other pushing with the old certificate,
   which stops working the day it expires.
4. Check the startup log (no expiry warning, `apns push enabled`) and that
   `telesrv_push_apns_certificate_days_left` shows the new date.
5. Only after both sides run on the new certificate, revoke the old one if you
   want to; revoking it earlier breaks whichever side still uses it.

`.p8` keys do not expire. To rotate one, create a new key, switch the server to
it (`TELESRV_APNS_KEY_FILE` and `TELESRV_APNS_KEY_ID`), restart, then revoke
the old key.

## Sharing credentials with official Telegram

The same iOS app (bundle `co.kaka.tgpro`) connects to official Telegram and to
this server, and its push certificate is already registered with official
Telegram. Apple allows several providers to use the same credentials at the
same time, so this server can push independently with the same certificate:

- **Pushes do not interfere.** Each server only pushes to the device tokens its
  own accounts registered, and the iOS notification extension decrypts each
  push with the account's own key (matched by the key id in the payload), so a
  push is shown for the right account.
- **Revocation is shared.** Revoking the certificate (for example after a leak)
  breaks push for official Telegram too. Store the PEM like a password: only
  on the server, owned by uid 10001, mode 0400, never in the image or the
  repository.
- **Renewal is shared.** Both sides must switch to the renewed certificate (see
  above); keep the expiry alert on.
- **Rate limits are per device token and topic.** Apple throttles excessive
  pushes per device regardless of the sender, so a misbehaving server can
  delay the other's pushes; the retry cap (`TELESRV_PUSH_MAX_RETRIES`) keeps
  this server from adding to it.
- **Prefer a dedicated key later.** To decouple from official Telegram, create a
  separate `.p8` key for this server, restricted to `co.kaka.tgpro` and
  `co.kaka.tgpro.voip` if the portal offers topic scoping, and switch to
  [token auth](#p8-token-setup). Revoking it then affects this server only.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Startup fails with `TELESRV_APNS_TOPIC ... is not covered by the certificate` | Wrong bundle id, or a certificate for another app |
| Startup fails with `TELESRV_APNS_CERT_FILE: parse certificate and private key` | The PEM lacks the private key or it is encrypted; re-export with `-nodes` |
| Startup fails with `TELESRV_APNS_KEY_FILE: load .p8 key` | Not the downloaded `AuthKey_*.p8` file, or the file was altered |
| Startup fails with `permission denied` | Container user 10001 cannot read the file; fix owner and mode |
| `apns certificate expired; push disabled` | Renew the certificate and restart |
| `result="invalid_token"` growing | Users reinstalled the app or logged out; the registrations are removed automatically |
| No pushes, `telesrv_push_sent_total` not growing | The device is in the foreground, the chat is muted, or the device never registered |
