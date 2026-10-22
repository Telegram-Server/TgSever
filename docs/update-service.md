# Desktop and native client update service

The update service tells our Telegram Desktop build (forked from upstream
tdesktop) that a new version exists and serves the signed package. It speaks the
tdesktop **v2** update contract: `td-update-{os}-{arch}-{base}[-beta]` file
names, `TDUP` packages signed under a key manifest that our own update root key
signs, and the `stable` and `beta` channels. The same service also answers
`help.getAppUpdate` for Android, iOS and other MTProto clients.

There are two ways to run it, and both use the same handler and the same checks:

- **In process** (the normal case): `telesrv` serves the catalog under
  `<TELESRV_PUBLIC_BASE_URL>/_updates` on its public link listener. Set a
  manifest path and the update root public key; nothing else to deploy.
- **Standalone**: `cmd/telesrv-update` runs as its own HTTP process, for example
  as the origin behind a CDN on a separate node.

Updates are **off by default**. With `TELESRV_UPDATE_MANIFEST` and
`TELESRV_UPDATE_PUBLIC_URL` empty, `telesrv` advertises no
`autoupdate_url_prefix` and `help.getAppUpdate` reports no update.

Contents:

- [How the trust chain works](#how-the-trust-chain-works)
- [Keys and the key manifest](#keys-and-the-key-manifest)
- [Building packages with Packer](#building-packages-with-packer)
- [File names](#file-names)
- [Publishing a release](#publishing-a-release)
- [Publishing from the admin panel](#publishing-from-the-admin-panel)
- [In-process deployment](#in-process-deployment)
- [Standalone deployment](#standalone-deployment)
- [Docker](#docker)
- [Upgrading from an earlier configuration](#upgrading-from-an-earlier-configuration)
- [Clients remember the update prefix](#clients-remember-the-update-prefix)
- [HTTP behavior](#http-behavior)
- [Release manifest fields](#release-manifest-fields)
- [Android and iOS](#android-and-ios)

## How the trust chain works

```
update root key (Ed25519, offline)
  └─ signs the key manifest: signing keys (Ed25519 / ES256), channels, revocations
       └─ package signatures by those signing keys, per channel
            └─ TDUP package: channel, target os/arch, version, payload
```

The custom client has our **update root public key** compiled in; it replaces
Telegram's. Every package carries the key manifest and the root's signature over
it, plus one or more signatures by the signing keys that manifest lists. A
client only installs a package whose manifest our root signed and whose
signatures satisfy that manifest's rules for the package's channel.

The server applies the same rules before it publishes or serves anything. When
`telesrv` or `telesrv-update` loads the release manifest, every enabled desktop
entry is fully verified:

- the package parses as a v2 `TDUP` package within the client's size limits,
  with no trailing bytes;
- the key manifest carries a valid Ed25519 signature by the configured root
  public key;
- every signing group of the package's channel has at least one valid signature
  by a key that is neither revoked nor expired;
- the package's target matches the platform key, its channel matches the entry's
  channel, and its version base equals the entry's `build`;
- the file name, size and SHA-256 match the entry.

Any mismatch is a load error naming the entry (`desktop.<platform>.<channel>`)
and the reason. A package signed by Telegram's keys, by a key that is not in our
manifest, or renamed to look like another platform is rejected on the server
instead of being downloaded by every client and discarded.

What the server does **not** check: whether the client has already adopted a
newer key manifest, and whether the package is newer than the build a given
client runs. Both depend on client state and are left to the client.

## Keys and the key manifest

Key generation, storage and rotation tooling lives on the **tdesktop side**. The
tdesktop repository's canary test fixture script shows the whole procedure:
it creates a root key, signing keys and a key manifest, signs the manifest with
the root, and builds signed test packages. Use it as the reference for your own
release tooling. The server only ever needs the root **public** key.

### Update root key

The root key is an Ed25519 key pair. The server reads the public half as a
PEM `PUBLIC KEY` block (PKIX, as written by `openssl pkey -pubout`), for example:

```sh
openssl genpkey -algorithm ed25519 -out update-root.key
openssl pkey -in update-root.key -pubout -out update-root.pub
```

`update-root.pub` is what `TELESRV_UPDATE_ROOT_PUBLIC_KEY` and
`-root-public-key` point to. The same public key must be compiled into the
custom client (a tdesktop-side change).

The public key is not secret, but it is the server's trust anchor: whoever can
replace the file can make the server accept packages signed by their own root.
Keep it in a file and directory the user `telesrv` (and `telesrv-update`) runs
as **cannot write**, for example owned by root with mode `0644` under
`/etc/telesrv-updates/`, and not inside the manifest or files directory, which
the publishing user must be able to write. The Docker layout below mounts it
read-only for this reason.

The root private key signs nothing but key manifests. Keep it **offline**: on a
machine or hardware token that is not a build server, used only when the key
manifest changes. Losing it means you cannot add or rotate signing keys;
leaking it means anyone can sign updates your clients will install. Replacing
the root key requires a new client build that trusts the new root, distributed
out of band.

### Key manifest

The key manifest is JSON. The format the server accepts (identical to the
client's parser):

```json
{
  "format": 1,
  "manifest_version": 2,
  "keys": [
    { "id": "release-2026a", "alg": "Ed25519", "x": "<base64url, 32 bytes>" },
    {
      "id": "hsm-2026",
      "alg": "ES256",
      "crv": "P-256",
      "x": "<base64url, 32 bytes>",
      "y": "<base64url, 32 bytes>",
      "expires": 1830297600
    }
  ],
  "channels": {
    "stable": [["release-2026a"], ["hsm-2026"]],
    "beta": [["release-2026a", "hsm-2026"]]
  },
  "revoked": ["release-2025"]
}
```

- `format` must be `1`; `manifest_version` is an integer from 1 to 2^32-1.
- `keys`: `id` is unique, at most 64 bytes. Ed25519 keys give the raw public key
  as unpadded base64url `x`; ES256 keys give `crv: "P-256"` and the raw `x` and
  `y` coordinates. `expires` is optional Unix seconds; omitted means the key
  never expires. Keys with any other `alg` are ignored.
- `channels` maps a channel name to a list of **signing groups**, each a list of
  key ids. A package is authorized when **every** group has at least one valid
  signature (AND of ORs). In the example a stable package needs signatures from
  both keys; a beta package needs either.
- `revoked` lists key ids that no longer count. It must be present (it may be
  empty).
- Limits: 64 keys, 16 channels, 8 groups per channel, 16 keys per group,
  64 revoked ids, 256 KiB of manifest JSON.

The root signs the exact manifest bytes with Ed25519; the package stores those
bytes and the signature unchanged, so do not reformat the JSON after signing.

### Rotating signing keys

Signing keys are cheap to rotate because the client trusts whatever key manifest
the root signs:

1. Add the new key to `keys` and to the channel groups, increase
   `manifest_version`, and sign the manifest with the root key offline.
2. Build and publish the next release with the new manifest and the new key.
3. Retire the old key by moving its id to `revoked` (or by setting `expires`) in
   a later manifest version.

Two server-side consequences to plan for:

- The server judges each package only by the manifest **it carries**. Revoking a
  key affects packages built with the new manifest; an older package that
  carries an older manifest still verifies on the server. Disable or replace
  such entries in the release manifest yourself.
- `expires` is evaluated every time the release manifest is loaded. Once a
  published, enabled package's only valid signing key expires, the next load
  fails: a running server keeps serving the previous catalog and logs the
  error, and a restarting `telesrv` refuses to start. Publish a build signed by
  a fresh key before a key expires.

## Building packages with Packer

Packages are built with tdesktop's `Packer` target from the custom tdesktop
tree, the same way upstream builds its updates. Our deployment adds the key
material through three Packer options (these are tdesktop-side; check your
checkout's `Packer` usage for the exact argument forms):

- `-keys-loc` points Packer at the root-signed key manifest that it embeds into
  the package.
- `-local-key` gives Packer a local Ed25519 signing key to sign the package with.
- `-local-key-id` is that key's `id` in the key manifest.

Keys that are held as ES256 (P-256) keys, typically in an HSM or a cloud KMS
that will not export them, are applied with tdesktop's signing script instead of
Packer's local key. For reference, the signature input is the package from the
magic through the key manifest bytes followed by the SHA-256 of the payload.
Ed25519 signs that input directly; ES256 signs its SHA-256 and stores the raw
64-byte `r||s`.

Packer writes the channel, the target os/arch and the version into the signed
part of the package. For stable and beta packages the version is
`<AppVersion> << 32` (the low 32 bits, the counter, are 0), and the server uses
the high 32 bits, the **base**, as the entry's `build`. The name of the file
Packer writes does not matter: `telesrv-update publish` derives the canonical
name from the package contents.

Only the `stable` and `beta` channels can be published. `alpha` is a v1 channel
the v2 client does not know, and `canary-public` / `canary-private` packages are
discovered by the client over MTProto channels, which this server does not
provide.

## File names

The client only accepts a downloaded file whose name matches the v2 pattern, so
the server requires it for every enabled desktop entry:

| Platform | `/current4` key | Package target | Stable file | Beta file |
|---|---|---|---|---|
| Windows x86 | `win` | `win-x86` | `td-update-win-x86-<base>` | `td-update-win-x86-<base>-beta` |
| Windows x64 | `win64` | `win-x64` | `td-update-win-x64-<base>` | `td-update-win-x64-<base>-beta` |
| Windows ARM64 | `winarm` | `win-arm` | `td-update-win-arm-<base>` | `td-update-win-arm-<base>-beta` |
| macOS Intel | `mac` | `mac-x64` | `td-update-mac-x64-<base>` | `td-update-mac-x64-<base>-beta` |
| macOS Apple Silicon | `armac` | `mac-arm` | `td-update-mac-arm-<base>` | `td-update-mac-arm-<base>-beta` |
| Linux x64 | `linux` | `linux-x64` | `td-update-linux-x64-<base>` | `td-update-linux-x64-<base>-beta` |

`<base>` is the entry's `build`, written without leading zeros. Targets without
a platform key (Linux ARM, for example) cannot be published. Old v1 names such
as `tx64upd<build>` are rejected.

## Publishing a release

`telesrv-update publish` turns a Packer package into a published release:

```sh
telesrv-update publish [flags] <package>
```

It verifies the package with the root public key, derives platform, channel,
build, size and SHA-256 from its contents, copies it into the files directory
under its canonical name, and replaces the release manifest atomically (a
temporary file in the same directory, then a rename). All other entries,
including mobile `apps` entries, are kept. Before the rename the new manifest is
loaded with exactly the rules the server uses; if anything fails, nothing is
replaced. When the manifest does not exist yet, publish creates it.

Flags (they go **before** the package path):

| Flag | Default | Meaning |
|---|---|---|
| `-manifest` | `TELESRV_UPDATE_MANIFEST`, else `data/updates/manifest.json` | Release manifest to update. |
| `-files` | `TELESRV_UPDATE_FILES_DIR`, else `files` next to the manifest | Package directory. |
| `-root-public-key` | `TELESRV_UPDATE_ROOT_PUBLIC_KEY` | Update root public key PEM. Required. |
| `-dry-run` | off | Verify the package and the resulting manifest; write nothing. |
| `-allow-downgrade` | off | Accept a build that is not newer than the current entry. |

The output names the entry and what happened:

```text
published desktop.win64.stable
  build:   7000007 (package version 7000007.0, created 2026-10-19T08:00:00Z)
  file:    td-update-win-x64-7000007
  size:    123456789
  sha256:  …
  replaces: build 7000006 (td-update-win-x64-7000006)
  manifest: data/updates/manifest.json
```

Semantics:

- **Idempotent.** Publishing the same package again reports
  `already published` and writes nothing. If the entry is already this package
  but the file is missing from the files directory, publish only restores the
  file.
- **Immutable files.** A file with the same name but different content is
  rejected; a published package never changes.
- **No accidental rollback.** A build that is not newer than the current entry
  for the same platform and channel is rejected, including a disabled entry, so
  re-running an old publish cannot re-enable a withdrawn release. To roll back
  to an older build, or to re-enable a disabled entry, pass `-allow-downgrade`.
- **Verify only.** `-dry-run` does every check, including loading the resulting
  manifest, and prints what it would do (`dry run: would publish …`). It writes
  no file and takes no lock, so it fits a CI pre-check.
- **One publish at a time.** A writing publish holds `<manifest>.lock`. If a
  publish is killed, the lock stays behind and the next one fails with
  `another publish is in progress`; delete the lock file by hand once you are
  sure no publish is running.
- **Ownership.** On Unix, the replaced manifest keeps the previous manifest's
  owner when the publisher is allowed to set it, so a publish run as root does
  not lock the service user out.

The running service notices the new manifest by its modification time and size
and switches to it without a restart. To withdraw a release, disable its entry
on the admin panel's Client updates page (see below), or set `"disabled": true`
on it by hand, or publish an older build with `-allow-downgrade`. Validate a hand-edited manifest before putting it in place
with `telesrv-update -check` (see below).

## Publishing from the admin panel

Once the update service runs in process (`TELESRV_UPDATE_MANIFEST` is set), later
releases can be published from the admin panel: **Server → Client updates**. The
panel runs the same publish as `telesrv-update publish`, so the two produce the
same manifest and files and can be mixed. Enabling the service the first time is
still done with the CLI and the environment variables above; with the service
disabled the page shows those steps, and with a separate `telesrv-update`
(standalone mode) the page only shows its status and changes nothing.

**Permissions.** Two rights, granted per operator on the operator accounts
screen:

| Right | Allows |
|---|---|
| `updates.read` | The Client updates tab: service status, update URL, manifest reload errors, every desktop entry (disabled ones included). |
| `updates.publish` | Uploading, preflighting and publishing packages, and disabling and re-enabling entries. It does not include `updates.read`; grant both. |

**Upload, preflight, confirm.** The operator picks a package, writes the reason,
and uploads it once. The upload is streamed (browser → panel → admin API, never
buffered in memory or sent as multipart) into a staging file, and the server then
runs a full dry-run publish on it. The page shows the entry it would write
(platform, channel, build, package time, file, size, SHA-256) and the entry it
would replace, or the reason the package was refused (signature, root key,
platform, canary channel, not newer, same name with other content). Nothing
changes until the operator confirms. The confirm only names the staging id: the
staged file is verified again from scratch (the confirm dialog's dry run, then
the publish itself), nothing from the preflight is trusted. After a publish the
list shows the new entry; the server picks the manifest up without a restart.
Uploading a package that is already published reports `already published` and
offers nothing to confirm.

- **Upload limit:** `MaxDesktopPackageSize`, 268,707,243 bytes (a 256 MiB payload
  plus the largest possible header and signatures). The page checks the file
  against it before sending anything; the server also refuses a larger upload as
  soon as its declared length is seen, or cuts it off one byte past the limit.
- **Staging:** `.staging/` in the manifest's directory, on the same volume as the
  manifest and the files directory. The update HTTP handler never serves it.
  A staged upload expires **30 minutes** after it was uploaded; expired files,
  and the partial files of interrupted uploads, are removed on every upload and
  publish. A staging is also removed when its publish succeeds, when its
  preflight fails, and when the upload repeats or conflicts with an earlier
  command. A failed publish (lock held, rollback not allowed) keeps it, so the
  operator can retry without uploading again until it expires. Two confirms of
  one staging publish it once: the first claims the staged file (it is renamed
  to `<id>.claim` while the publish runs, and renamed back if the publish
  fails), and the other reports that the package is no longer staged.
- **Pending uploads are capped.** At most **4** uploads, finished or still
  arriving, may wait in `.staging/` at once, holding at most four times the
  upload limit (about 1 GiB) together. Past either cap a new upload is refused
  with `UPDATE_STAGING_FULL` before any of it is read; publish a staged package or
  wait for stagings to expire.
- **Why an upload failed.** Besides the package's own refusals
  (`UPDATE_PACKAGE_REJECTED`, `UPDATE_PACKAGE_TOO_LARGE`), a failed upload says
  whose problem it is: `UPDATE_UPLOAD_FAILED` (HTTP 400) when the transfer broke
  off (connection dropped, read timeout; upload again), and
  `UPDATE_STAGING_STORAGE_FAILED` (HTTP 500) when the server could not write the
  staging file (update volume full or not writable; the package is not at
  fault). These are not recorded as commands: no command has begun yet.
- **One publish at a time, shared with the CLI.** A panel publish takes the same
  `<manifest>.lock` as `telesrv-update publish`; while either holds it, the other
  is refused with `another publish is in progress`. The preflight and the dry run
  take no lock.
- **Rollback.** Publishing a build that is not newer than the entry on its
  channel (including re-publishing a disabled build) needs **Allow rollback**,
  the panel's counterpart of `-allow-downgrade`. Whether a confirm is a rollback
  is decided by the preflight, not the checkbox: a rollback confirm is drawn as a
  danger action with a warning, while a newer package uploaded with the box
  ticked is an ordinary publish. A rollback only changes what clients still on an older build receive: clients
  that already upgraded to a newer build do not go back.
- **Reason and audit.** Every preflight and publish needs a reason and is an
  audited admin command (`updates.preflight`, a dry run, and `updates.publish`).
  The Audit log shows the operator, the reason and the outcome: platform,
  channel, build, file, SHA-256, the replaced entry, and whether anything
  changed. Failed attempts are recorded with their error.

Only the server container writes the update volume: the admin container
forwards the upload to the admin API inside `telesrv` and needs neither the
volume nor the root public key.

**Disable and re-enable.** With `updates.publish` the entries table has an
action per row:

- **Disable** stops serving that platform and channel at once: the entry stays
  in the manifest with `"disabled": true`, `/current*` stops listing it and
  `/files/` stops serving its package, which stays on disk. Use it to pull a bad
  build. It is a warn-level confirm (reason, dry run, confirm). Clients that
  already installed the build keep it; disabling does not downgrade anyone. A
  later publish still counts the disabled build, so only a newer build, or a
  rollback, replaces it.
- **Re-enable** serves a disabled entry again. Because the entry may be a build
  that was pulled for a reason, it is a danger-level confirm with a required
  reason and a warning. It skips the "newer than the published build" check
  (re-enabling is the explicit decision that check exists to force), but the
  new manifest is fully validated before it replaces the old one: the package
  must still be in the files directory, match the entry's size and SHA-256, and
  still verify against the root key. Otherwise the re-enable is refused and the
  manifest stays as it was.

Both take the same `<manifest>.lock` as a publish, so they are refused with
`another publish is in progress` while a panel or CLI publish runs (their dry
runs take no lock). They write the manifest the same way, a temporary file next
to it renamed into place after validation, and change no other entry. Disabling
a disabled entry, or re-enabling an enabled one, reports that nothing changed
and writes nothing. Each one is an audited command, `updates.disable` or
`updates.enable`, recording the platform, channel, build, file, SHA-256 and
whether anything changed; failed attempts are recorded with their error.

## In-process deployment

Use this unless you need the update traffic on another host. Configuration:

| Variable | Default | Meaning |
|---|---|---|
| `TELESRV_UPDATE_MANIFEST` | empty (off) | Release manifest path. Non-empty turns the in-process update service on. |
| `TELESRV_UPDATE_FILES_DIR` | `files` next to the manifest | Package directory. |
| `TELESRV_UPDATE_ROOT_PUBLIC_KEY` | empty | Update root public key PEM path. Required once the manifest has an enabled desktop entry. Read once at startup. |
| `TELESRV_UPDATE_PUBLIC_URL` | `<TELESRV_PUBLIC_BASE_URL>/_updates` | Prefix advertised as `help.getConfig.autoupdate_url_prefix`. An explicit value wins, for example a CDN in front of `/_updates`. |

With a manifest configured:

- the public link listener (`TELESRV_PUBLIC_LINK_WEB_ADDR`) serves the update
  handler under `/_updates/` (and under `<path>/_updates/` when
  `TELESRV_PUBLIC_BASE_URL` has a path), so `/_updates/current4` and
  `/_updates/files/<name>` reach it. `/_updates` cannot collide with a public
  `/{username}` page because usernames never start with `_`;
- `help.getAppUpdate` is answered from the same catalog in process;
- `TELESRV_UPDATE_SERVICE_URL` must stay empty, and startup fails if it is set;
- when `TELESRV_UPDATE_PUBLIC_URL` is empty, `TELESRV_PUBLIC_LINK_WEB_ADDR`
  must be set, otherwise startup fails: the derived prefix would point at a
  listener that does not exist;
- `telesrv` refuses to start when the manifest or the root key cannot be loaded.
  After startup a broken manifest replacement is logged and the previous good
  catalog keeps being served.

A reverse proxy in front of the public link listener must forward `/_updates/`
unchanged (`TELESRV_PUBLIC_BASE_URL` has to reach it, as for every other public
link) and must allow large, ranged downloads.

Local example (`.env`):

```dotenv
TELESRV_UPDATE_MANIFEST=data/updates/manifest.json
TELESRV_UPDATE_ROOT_PUBLIC_KEY=/etc/telesrv-updates/update-root.pub
```

Publish the first package before setting `TELESRV_UPDATE_MANIFEST`, so the
manifest exists when `telesrv` starts:

```sh
go run ./cmd/telesrv-update publish \
  -manifest data/updates/manifest.json \
  -root-public-key /etc/telesrv-updates/update-root.pub \
  path/to/package
```

`telesrv` reads `.env`; `telesrv-update` does **not**. It only sees its own
process environment, so locally keep passing `-manifest` and
`-root-public-key` (and `-files` if you set `TELESRV_UPDATE_FILES_DIR`), or
export the `TELESRV_UPDATE_*` variables in the shell you publish from. Without
them it uses its built-in defaults (`data/updates/manifest.json`, no root key),
and `publish` refuses to run without a root key. (Inside the Docker server container the variables are in the process
environment, so there `publish` needs only the package path; see
[Docker](#docker).)

## Standalone deployment

Run `cmd/telesrv-update` as its own process when updates should live on
another node or behind a CDN origin. It performs every v2 check the in-process
service does.

```sh
telesrv-update -check        # validate the catalog and exit
telesrv-update               # serve
```

| Setting | Flag | Default |
|---|---|---|
| `TELESRV_UPDATE_LISTEN` | `-listen` | `127.0.0.1:2402` |
| `TELESRV_UPDATE_MANIFEST` | `-manifest` | `data/updates/manifest.json` |
| `TELESRV_UPDATE_FILES_DIR` | `-files` | `files` next to the manifest |
| `TELESRV_UPDATE_ROOT_PUBLIC_KEY` | `-root-public-key` | empty; required once the manifest has an enabled desktop entry |

Point `telesrv` at it:

```dotenv
TELESRV_UPDATE_PUBLIC_URL=https://updates.example.test
TELESRV_UPDATE_SERVICE_URL=http://127.0.0.1:2402
TELESRV_UPDATE_REQUEST_TIMEOUT=2s
```

`TELESRV_UPDATE_PUBLIC_URL` must be reachable by clients and is advertised as
`autoupdate_url_prefix`. `TELESRV_UPDATE_SERVICE_URL` is the route `telesrv`
uses for `help.getAppUpdate` (`/v1/resolve`); it may be loopback or private and
defaults to the public URL.

Do **not** give `telesrv` a `TELESRV_UPDATE_MANIFEST` in this mode: a manifest
`telesrv` can see switches it to in-process mode (and then
`TELESRV_UPDATE_SERVICE_URL` is rejected). Give the manifest settings to
`telesrv-update` only, through its own environment or its flags. Put an HTTPS
reverse proxy in front without rewriting `/current4` or `/files/*`.

## Docker

The server image contains `/usr/local/bin/telesrv-update`, and the production
compose file (`deploy/docker/compose.yaml`) gives the server two update mounts:

- the `server_updates` volume at `/var/lib/telesrv-updates`, for the manifest
  and the packages. It is the only place the read-only server container can
  keep update files. The volume is owned by the container user (10001) and
  belongs to the server service only.
- `deploy/docker/update-keys/` on the host, bind-mounted **read-only** at
  `/etc/telesrv-updates`, for the update root public key. The key must not live
  on `server_updates`: the server user can write that volume, so a compromised
  server could swap the trust root. The directory ships empty (only
  `.gitkeep`, its other contents are git-ignored), so the mount is harmless
  while updates are off.

Compose passes `TELESRV_UPDATE_MANIFEST`, `TELESRV_UPDATE_FILES_DIR`,
`TELESRV_UPDATE_ROOT_PUBLIC_KEY` and `TELESRV_UPDATE_PUBLIC_URL` from
`deploy/docker/.env`, all empty by default, and always runs the public link
listener, so the derived `<TELESRV_PUBLIC_BASE_URL>/_updates` prefix works once
the manifest is set.

Run the commands below from `deploy/docker` (or add
`--project-directory deploy/docker --env-file deploy/docker/.env -f deploy/docker/compose.yaml`).
`docker compose exec` runs as user 10001 and skips the entrypoint.

First publish, with updates still off and the server running:

```sh
# 1. Put the root public key in the read-only key directory, on the host.
#    It must be readable by uid 10001 and writable only by the host admin.
#    (Use sudo if compose created the directory as root before.)
install -m 0644 path/to/update-root.pub update-keys/update-root.pub

# 2. Create an upload directory on the volume and copy the package in
#    (piping through the container keeps the files owned by 10001).
docker compose exec -T server mkdir -p /var/lib/telesrv-updates/incoming
docker compose exec -T server sh -c 'cat > /var/lib/telesrv-updates/incoming/package' < path/to/package

# 3. Publish. The manifest variable is still empty, so pass the paths.
docker compose exec server telesrv-update publish \
  -manifest /var/lib/telesrv-updates/manifest.json \
  -root-public-key /etc/telesrv-updates/update-root.pub \
  /var/lib/telesrv-updates/incoming/package
```

The bind mount shows files added to `update-keys/` without a restart. Then
enable updates in `deploy/docker/.env` and recreate the server:

```dotenv
TELESRV_UPDATE_MANIFEST=/var/lib/telesrv-updates/manifest.json
TELESRV_UPDATE_ROOT_PUBLIC_KEY=/etc/telesrv-updates/update-root.pub
```

```sh
docker compose up -d server
```

Setting `TELESRV_UPDATE_MANIFEST` before the manifest exists stops the server
from starting. Packages land in `/var/lib/telesrv-updates/files`.

Later releases read the variables from the container environment:

```sh
docker compose exec -T server sh -c 'cat > /var/lib/telesrv-updates/incoming/package' < path/to/package
docker compose exec server telesrv-update publish -dry-run /var/lib/telesrv-updates/incoming/package
docker compose exec server telesrv-update publish /var/lib/telesrv-updates/incoming/package
```

`-dry-run` and `-allow-downgrade` go before the package path. A publish that was
killed leaves `/var/lib/telesrv-updates/manifest.json.lock`; remove it with
`docker compose exec server rm` once no publish is running.

Instead of these commands, later releases can be uploaded from the admin panel
(see [Publishing from the admin panel](#publishing-from-the-admin-panel)). The
admin container needs no update volume for that: the upload is forwarded to the
server container, which stages it in `/var/lib/telesrv-updates/.staging`.

### Backing up the update catalog

`gramsrv-backup` and [backup-restore.md](backup-restore.md) do **not** cover
the update catalog in Docker: a bundle holds the databases, the `data/` tree
and the configuration files, not the `server_updates` volume and not
`deploy/docker/update-keys/`. Back both up yourself (for example
`docker run --rm -v <project>_server_updates:/v:ro -v "$PWD":/b alpine tar -C /v -czf /b/server_updates.tgz .`,
and a copy of `update-root.pub`). Losing the volume loses the published
packages that the manifest and your clients refer to. (In a local deployment
whose manifest sits under `data/`, such as `data/updates/`, the bundle's
`data.tar.zst` includes it; the root key under `/etc` it does not.) The
`.staging/` directory next to the manifest holds only uploads waiting for a
confirm from the admin panel; it need not be backed up.

Restore order matters: put the `server_updates` volume and the root key back
**before** setting `TELESRV_UPDATE_MANIFEST` (or before starting a server whose
restored `.env` already sets it). With the variable set and the manifest
missing, `telesrv` refuses to start.

## Upgrading from an earlier configuration

- **`TELESRV_UPDATE_MANIFEST` now turns `telesrv` into an update server.** Older
  `.env.example` files set `TELESRV_UPDATE_MANIFEST=data/updates/manifest.json`
  (and `TELESRV_UPDATE_FILES_DIR`) for the standalone `telesrv-update` only, and
  `telesrv` ignored them. `telesrv` now reads the variable, and any non-empty
  value enables in-process updates. An upgraded `.env` copied from that example
  therefore fails at startup: the manifest does not exist, or
  `TELESRV_UPDATE_SERVICE_URL` is also set, or the public link listener is off.
  These errors say that a non-empty `TELESRV_UPDATE_MANIFEST` enables in-process
  updates. If this server should not serve updates, clear
  `TELESRV_UPDATE_MANIFEST` (and the other in-process variables) in its `.env`;
  a standalone `telesrv-update` keeps getting them from its own environment.
- **Standalone `telesrv-update` without `-files` / `TELESRV_UPDATE_FILES_DIR`.**
  The default package directory used to be `data/updates/files` whatever the
  manifest path; it is now `files` next to the manifest. With the default
  manifest path nothing changes. A deployment that sets a custom
  `-manifest`/`TELESRV_UPDATE_MANIFEST` but no files directory must either move
  its packages from `data/updates/files` to `<manifest dir>/files`, or set
  `-files`/`TELESRV_UPDATE_FILES_DIR` to the old directory explicitly; otherwise
  the catalog fails to load because the published files are missing.

## Clients remember the update prefix

tdesktop persists the `autoupdate_url_prefix` it receives in `help.getConfig`
(in `tdata/prefix`). Once a client has seen our prefix, it keeps polling that
prefix even after the server stops advertising one, for example after
`TELESRV_UPDATE_MANIFEST` is emptied or the update service is moved.

So treat the prefix as permanent:

- Choose `TELESRV_UPDATE_PUBLIC_URL` (or `TELESRV_PUBLIC_BASE_URL`, from which
  the default is derived) as a long-lived URL before enabling updates.
- To pause updates, keep the service running and disable the desktop entries
  instead of turning the service off; `/current4` then lists nothing.
- When moving to another host or a CDN, keep the old prefix answering (serve the
  same catalog there or redirect) until the old clients have updated.

## HTTP behavior

Paths are relative to the update prefix (`/_updates` in process, the root for
the standalone service).

- `/healthz` reports process liveness.
- `/readyz` reports the catalog: `ready`, or `stale` (still HTTP 200) when the
  latest manifest failed to load and the previous catalog is being served. It
  returns `503` only when no catalog has ever loaded.
- `/current`, `/current1` … `/current10` all return the same desktop map (the
  `/current4` format) with `Cache-Control: no-cache`; each enabled entry has
  `released` (the build) and a `link` relative to the prefix (`/files/<name>`).
  Other numbers (`/current0`, `/current11`, …) are `404`.
- `/files/<name>` serves only packages referenced by the current verified
  catalog, supports GET, HEAD and Range (resumable downloads), and sends an
  immutable cache policy and a SHA-256 ETag. Other files in the directory are
  never exposed. A package changed on disk after loading returns `503`.
- `/v1/resolve` returns a newer application release or `204 No Content`.

## Release manifest fields

The release manifest (`manifest.json`) is what the server serves from. Publish
desktop entries with `telesrv-update publish`; edit `apps` entries by hand.
`deploy/update/manifest.example.json` is a complete, disabled example.

- `schema_version`: `1`.
- `desktop.<platform>.<channel>`: `platform` is a `/current4` key from the
  table above; `channel` is `stable` or `beta` (`alpha` is rejected).
  - `build`: the package's version base, compared with the client's
    `AppVersion`.
  - `version`: optional display string.
  - `file`, `sha256`, `size`: canonical file name and integrity metadata.
  - `disabled`: keep the entry without publishing it; disabled entries are not
    verified.
- `apps.<platform>.<channel>`: mobile releases; channels `stable`, `beta` and
  `alpha`.
  - `id`: stable positive release identifier.
  - `version`: compared with the client's `initConnection.app_version`.
  - `url`, `url_by_source`: installer or store URL, optionally per source.
  - `notes`: localized text keyed by `en`, `ru`, `ru-ru` and similar codes.
  - `can_not_skip`: whether the client may dismiss the update.
  - `disabled`: as above.

Unknown fields are an error.

## Android and iOS

`help.getAppUpdate` passes the client platform, current `app_version`, source,
channel and `lang_code` to the resolver (in process, or `/v1/resolve` of a
standalone service). The resolver selects localized notes, never offers an
equal or older version, and applies `url_by_source` when a matching installer or
store source is configured.

- A standalone Android build may open or install an APK URL, but the APK must be
  signed with the same Android application signing key as the installed build.
- Google Play and other store builds should use the corresponding store URL;
  this mechanism does not bypass store policy.
- iOS may display information returned by `help.getAppUpdate`, but installation
  still happens through App Store, TestFlight or MDM. A normal iOS application
  cannot replace itself from an arbitrary IPA URL.
- Set `can_not_skip` only after confirming that the target release is actually
  available to every affected client.
