# OpenDeskViewer

**A fork of [RustDesk](https://github.com/rustdesk/rustdesk)** that adds a self-hosted management
platform around the unmodified open-source RustDesk server, so an organisation can run a fleet of
preconfigured devices and control who may reach which machine.

It is built for one shape of deployment: devices that arrive at a customer already set up, that
nobody at the customer configures, and that a support team reaches by name or serial number.

---

## What RustDesk gives you, and what it does not

Three programs, all upstream and unmodified in this repository:

| Piece | What it is |
|---|---|
| **The client** (`src/`, `flutter/`) | A Rust core with a Flutter UI, for Windows, macOS, Linux and Android. It is both ends of a session: the machine being controlled and the technician controlling it |
| **`hbbs`** | The ID/rendezvous server. Every client registers its nine-digit ID here, and hbbs introduces two clients so they can attempt a direct connection |
| **`hbbr`** | The relay. When a direct connection cannot be punched through NAT, the session is relayed |

A session goes: the client registers with `hbbs` → a technician asks for an ID → the two are
introduced and try to connect directly → if that fails, `hbbr` relays. Access at the far end is a
**per-device password**, either read aloud by whoever is sitting at the machine or set by hand as a
permanent unattended password.

That is the whole model, and for a managed fleet it leaves out everything that makes it a fleet:

- no inventory — the server knows IDs, not machines, customers or sites
- no accounts, so no notion of which technician may reach which device
- nothing that provisions a device, so somebody has to type a server address into every one
- device passwords that are set by hand and are never rotated or withdrawn
- no record of who connected to what
- no way for another system to look a device up or rename it

Upstream sells those as RustDesk Server Pro. **This fork implements them, self-hosted, against the
open-source server.**

## What this fork adds

Everything below is fork-authored. The management layer lives in
**[`platform/`](platform/)** — a Go API, a React portal, and a Docker Compose stack with Caddy,
Keycloak and PostgreSQL in front of upstream's `hbbs` and `hbbr`.

**Fleet and access.** Customers, locations, device groups and support groups. A technician's reach
is the chain *user → support group → device group → device*, resolved in exactly one place
(`access.Resolver`) that both the portal and the client's address book go through, so the two can
never disagree about who may see what. Roles are Administrator, Support Manager, Technician and a
Read Only auditor with fleet-wide read and no writes, no sessions and no credentials.

**Zero-touch provisioning.** A build carries its deployment identity compiled in — the ID server,
relay, API server and public key — so a client cannot be re-pointed at another deployment by
anyone who answers a heartbeat. Devices join with a single-use-ish **enrollment token**, delivered
by Android managed configuration from an MDM console, or `--enrollment-token=` on desktop. A device
that presents no valid credential is recorded as an *observation* and refused, rather than
registering itself.

**Naming and search.** A device reports its hostname, OS and serial number, and is named from a
template (`{customer}-{location}-{serial}`). A technician finds it by typing the serial in the
portal, and an external system finds it with `GET /api/v1/devices?serial=…` and renames it with one
`PATCH`. A name set through the API survives re-enrolment.

**Managed connection passwords.** The platform issues each device's password, encrypts it at rest
with AES-256, and delivers it on the device's next heartbeat. Reading one is a technician's action
and is audited. **Withdrawing access rotates it**: removing a technician from a support group,
revoking a device group, deactivating a user or removing one all rotate the passwords of every
device that person could reach, so a credential they were shown stops working.

**People, from one identity provider.** Sign-in is Keycloak — the portal in the browser, and the
RustDesk client through its SSO flow. Administrators create and remove accounts from the portal,
which creates and removes the Keycloak account with it: a local-only delete is not a removal,
because the person signs in again and is provisioned afresh. The last administrator cannot be
demoted, deactivated or removed.

**Unattended operation.** The client starts on boot, provisions that setting once, and never puts a
dialog in front of the customer. What the firmware image or MDM has to provide in exchange is
written down in
[Deploying the client](platform/README.md#deploying-the-client).

**Reach and observation.** End a live session from the portal, push client configuration,
watch connectivity history per device or fleet-wide, and post connectivity events to a webhook
(https only, optionally HMAC-signed) with a delivery log that records what was abandoned.

**Audit and reporting.** An append-only audit log — the API's database role may insert and select,
and cannot update, delete or truncate a row — plus session history and three reports as JSON or
CSV: `device-inventory`, `session-history` and `access-review`, the last derived from the same
chain the API enforces.

### What changed in the client

Everything outside `platform/` is upstream and kept as close to upstream as possible so merges stay
cheap. `libs/hbb_common` is untouched, and a CI guard fails the build if it is patched. The
fork-authored client changes are additive:

- **`src/common.rs`, `build.rs`** — the deployment identity, baked in at compile time from four
  `ODV_*` variables and locked against later change
- **`src/hbbs_http/sync.rs`** — enrollment, reporting hostname/OS/serial, and applying the
  platform-managed connection password
- **`flutter/lib/main.dart`** — the first-run provisioning guard on boot arguments, and enabling
  start-on-boot once for a client that knows its deployment
- **`flutter/android/…/common.kt`, `MainActivity.kt`** — reading managed configuration through
  `RestrictionsManager`, and resolving the serial number
- **`flutter/android/…/BootReceiver.kt`, `MainService.kt`** — unattended boot behaviour: no toast,
  no consent dialog in front of a customer, and a warning that names what the image must grant
- **`flutter/android/app/build.gradle`, `strings.xml`, `AndroidManifest.xml`** — the app's own
  identity, `com.opendeskviewer.client`

**Read this before deploying:**
[Where authorization is enforced, and where it is not](platform/README.md#where-authorization-is-enforced-and-where-it-is-not).
It states plainly which guarantees the platform does and does not provide — most importantly
that the relay does not check authorization, that revocation reaches a device at its next
heartbeat rather than immediately, and that the deployment key baked into a client is a network
perimeter rather than a device identity.

---

## Setup

This brings up the server, gets an administrator signed in, and enrols the first device. Budget
about half an hour, most of it waiting for images to pull and a certificate to be issued.

`platform/README.md` is the reference for everything here: configuration, the authorization model,
the API surface and the client deployment spec. This is the path through it.

### Prerequisites

- Docker with Compose v2
- **A real public hostname pointing at the machine**, with ports 80 and 443 reachable. Not
  `localhost` and not an internal IP: Caddy provisions a certificate for it, and Keycloak stamps it
  into the issuer claim of every token, so a name you cannot reach from a browser produces tokens
  the API rejects
- To build clients: a GitHub repository for this fork, or a local Flutter/Rust/NDK toolchain
  (`platform/android/README.md`)

### 1. Clone

```bash
git clone https://github.com/Lebbitheplow/Open-Desk-Viewer.git
cd Open-Desk-Viewer
git submodule update --init --recursive
```

Upstream RustDesk is kept as a second remote, which is what makes merging from it cheap:

```bash
git remote add upstream https://github.com/rustdesk/rustdesk.git
```

### 2. Configure

```bash
cp platform/.env.example platform/.env
```

`platform/.env.example` documents every variable. These are the ones with no usable default:

| Variable | How to set it |
|---|---|
| `PUBLIC_HOST` | Your public hostname. Everything else derives from it |
| `OIDC_ISSUER` | `https://<PUBLIC_HOST>/realms/opendeskviewer`. The API validates the shape at startup and refuses to run if it does not end in `/realms/<realm>` |
| `OIDC_AUTH_URL`, `OIDC_TOKEN_URL`, `OIDC_REDIRECT_URI` | Same host substitution |
| `JWT_SECRET` | 64 characters or more: `openssl rand -base64 48` |
| `DEVICE_PASSWORD_KEY` | `openssl rand -base64 32`. **Back this up.** It decrypts every device password; losing it means re-enrolling the fleet |
| `POSTGRES_PASSWORD`, `POSTGRES_APP_PASSWORD`, `KEYCLOAK_DB_PASSWORD`, `KEYCLOAK_ADMIN_PASSWORD` | Distinct, generated |
| `API_BOOTSTRAP_ADMIN_EMAIL` | The one account that gets Administrator on its first sign-in |
| `CADDY_ACME_EMAIL` | Where Let's Encrypt sends expiry warnings |

`RUSTDESK_PUBLIC_KEY` is filled in at step 4; leave the placeholder for now.

### 3. Start the stack

```bash
cd platform
docker compose up -d
docker compose ps
```

Eight services: `postgres`, `keycloak`, `hbbs`, `hbbr`, `api`, `worker`, `web` and `caddy`. The API
applies its own migrations at startup and refuses to serve traffic against a schema it does not
know. Keycloak imports the realm — clients, roles and the API service account — on first boot only,
and takes a minute or two the first time.

Everything downstream of the API waits on its health check, so `docker compose ps` showing `api`
healthy is the signal that the database and Keycloak both came up. To check that Caddy, its
certificate and the API all agree, ask for the one API route that needs no token:

```bash
docker compose ps
curl -sS https://$PUBLIC_HOST/api/login-options    # ["common-oidc/[{\"name\":\"SSO\"}]"]
docker compose logs -f api
```

### 4. Give the client its server key

`hbbs` generates a keypair on first run. The clients need the public half, and so does the API:

```bash
docker compose exec hbbs cat /data/id_ed25519.pub
```

Put that value in `RUSTDESK_PUBLIC_KEY` in `.env`, then:

```bash
docker compose up -d --force-recreate api
```

Keep it: it is one of the four values compiled into every client build.

### 5. Create the bootstrap administrator

Accounts live in Keycloak, and the realm has self-registration off. **The console is deliberately
not routed through Caddy**, so this one account is created over a loopback-only port. Create
`platform/docker-compose.override.yml`:

```yaml
services:
  keycloak:
    ports:
      - "127.0.0.1:8080:8080"
```

```bash
docker compose up -d keycloak
```

Reach `http://127.0.0.1:8080` (through an SSH tunnel if the host is remote), sign in with
`KEYCLOAK_ADMIN_USER` / `KEYCLOAK_ADMIN_PASSWORD`, switch to the **opendeskviewer** realm, and
create a user whose email is exactly `API_BOOTSTRAP_ADMIN_EMAIL`, with a password set and
email-verified on.

While you are there, confirm the API's service account can manage users, which is what lets the
portal create accounts from now on: **Clients → odv-api → Service account roles**. The realm import
grants `realm-management` `manage-users` and `view-users`; a realm created before that was added
needs them assigned by hand.

Then remove the override file and `docker compose up -d keycloak` again, so the console goes back
off the network.

Sign in to the portal at `https://<PUBLIC_HOST>/`. Every account after this one is created from
**Users → New user**, which creates the Keycloak account too and shows a temporary password once.

### 6. Describe the fleet

In the portal, in this order, because each one is the parent of the next:

1. **Customers** → a customer, with a code
2. Its **Locations** → a site
3. **Device groups** → the group devices will be enrolled into
4. **Support groups** → the team, granted that device group, with technicians added

A technician sees a device only through a support group. Getting this wrong is the usual cause of
"the portal shows the fleet and the technician's client shows nothing".

### 7. Issue an enrollment token

The token is what lets a device join, and it carries where the device lands. In the portal,
**Enrollment tokens** takes a customer, a maximum number of uses and an expiry in days, and shows
the token once.

**The portal's form does not set a location or a device group, and both matter.** The device group
is what a support group is granted, so a device enrolled without one is in the fleet and reachable
by nobody until an administrator adds it to a group; the location is the `{location}` part of the
generated name. To set them, issue the token through the API instead, with an administrator's
access token:

```bash
curl -X POST https://$PUBLIC_HOST/api/v1/enrollment-tokens \
  -H "Authorization: Bearer <access token>" -H 'Content-Type: application/json' \
  -d '{"customer_id":"…","location_id":"…","device_group_id":"…",
       "max_uses":50,"expires_at":"2026-12-31T00:00:00Z"}'
```

`max_uses` is a decision about the image: one token baked into a firmware build is one token every
device off that line presents, and it is a fleet-joining credential until its uses run out. Prefer
per-batch tokens with a real expiry.

### 8. Build the client

Set these as **repository variables** so every build carries the deployment, and run the
**Android Build** workflow:

| Variable | Value |
|---|---|
| `ODV_API_SERVER` | `https://<PUBLIC_HOST>` |
| `ODV_RENDEZVOUS_SERVER` | `<PUBLIC_HOST>` |
| `ODV_RELAY_SERVER` | `<PUBLIC_HOST>` — the relay port is derived, so a bare host is right |
| `ODV_RS_PUB_KEY` | the key from step 4 |

And these as **secrets**, so the artifact is deployable: `ANDROID_SIGNING_KEY` (base64 keystore),
`ANDROID_ALIAS`, `ANDROID_KEY_STORE_PASSWORD`, `ANDROID_KEY_PASSWORD`. Without them the build
still runs and warns; the APK it produces is debug-signed and can be neither pushed as an update
nor trusted by a firmware image. **Decide this key once, before the first image.**

The workflow greps the built APK for the baked-in API server and fails if it is not there, which is
the only check that proves the compiler actually saw the variables.

Building locally instead: `platform/android/README.md`.

### 9. Deploy it

**Android, through an MDM:** push the APK and set the managed configuration key
`enrollment-token`, plus `serial-number` if the fleet's identifier is an asset tag rather than the
hardware serial. The console reads the available keys from the app itself.

**Desktop or a test machine:** install the build and start it once with
`--enrollment-token=<token>`.

Before cutting a firmware image, read
[Deploying the client](platform/README.md#deploying-the-client). It lists the four grants the image
has to provide and what each one costs when it is missing — in particular that **screen capture
without a user present needs the app installed as a privileged or device-owner app**, which is a
property of the image and not something any code here can arrange.

### 10. Check it worked

- The device appears in **Devices** as `ACTIVE`, named from its serial, within one heartbeat
- Searching its serial in the portal finds it
- Its detail page's **Connection password → Show password** returns one, and the read lands in the
  audit log
- A technician in the right support group, signed in to their own client, sees it in their Devices
  list under the same name and can connect to it

If the device never appears, ask what reported in without a credential:

```bash
curl -H "Authorization: Bearer <access token>" \
  https://$PUBLIC_HOST/api/v1/device-observations
```

An ID lands there instead of registering itself when its enrollment token never arrived or was
already spent. That is the failure to expect first.

### Ports

| Port | Service | Who needs it |
|---|---|---|
| 80, 443 | Caddy → portal, API, Keycloak | Technicians' browsers and every client |
| 21116 TCP+UDP | `hbbs` rendezvous | Every client |
| 21115, 21118 | `hbbs` NAT test and websocket | Clients |
| 21117, 21119 | `hbbr` relay | Clients that cannot connect directly |

Keycloak's admin console and PostgreSQL are on the internal network only and are not published.

### When something is wrong

| Symptom | Where to look |
|---|---|
| Device never appears | `GET /api/v1/device-observations`. The token did not reach it, or it is pointed at the wrong API server |
| Sign-in fails with an invalid token | `OIDC_ISSUER` does not match what Keycloak stamps. It must be the **public** realm URL |
| Creating a user answers 503 | The odv-api service account has no `manage-users`, or `KEYCLOAK_CLIENT_SECRET` is unset (step 5) |
| The client's sign-in dialog offers no SSO button | The client is not reaching `/api/login-options`; check the baked-in `api-server` |
| Technician sees no devices | Support group membership, the device group is not granted to their support group (step 6), or the device enrolled on a token that named no device group (step 7) |
| Device is `ACTIVE` but a session shows nothing | Screen capture is not provisioned. `adb logcat` shows the warning naming what the image must grant |

---

*The rest of this file is upstream RustDesk's README, unchanged.*

<p align="center">
  <img src="res/logo-header.svg" alt="RustDesk - Your remote desktop"><br>
  <a href="#raw-steps-to-build">Build</a> •
  <a href="#how-to-build-with-docker">Docker</a> •
  <a href="#file-structure">Structure</a> •
  <a href="#snapshot">Snapshot</a><br>
  [<a href="docs/README-UA.md">Українська</a>] | [<a href="docs/README-CS.md">česky</a>] | [<a href="docs/README-ZH.md">中文</a>] | [<a href="docs/README-HU.md">Magyar</a>] | [<a href="docs/README-ES.md">Español</a>] | [<a href="docs/README-FA.md">فارسی</a>] | [<a href="docs/README-FR.md">Français</a>] | [<a href="docs/README-DE.md">Deutsch</a>] | [<a href="docs/README-PL.md">Polski</a>] | [<a href="docs/README-ID.md">Indonesian</a>] | [<a href="docs/README-FI.md">Suomi</a>] | [<a href="docs/README-ML.md">മലയാളം</a>] | [<a href="docs/README-JP.md">日本語</a>] | [<a href="docs/README-NL.md">Nederlands</a>] | [<a href="docs/README-IT.md">Italiano</a>] | [<a href="docs/README-RU.md">Русский</a>] | [<a href="docs/README-PTBR.md">Português (Brasil)</a>] | [<a href="docs/README-EO.md">Esperanto</a>] | [<a href="docs/README-KR.md">한국어</a>] | [<a href="docs/README-AR.md">العربي</a>] | [<a href="docs/README-VN.md">Tiếng Việt</a>] | [<a href="docs/README-DA.md">Dansk</a>] | [<a href="docs/README-GR.md">Ελληνικά</a>] | [<a href="docs/README-TR.md">Türkçe</a>] | [<a href="docs/README-NO.md">Norsk</a>] | [<a href="docs/README-RO.md">Română</a>]<br>
  <b>We need your help to translate this README, <a href="https://github.com/rustdesk/rustdesk/tree/master/src/lang">RustDesk UI</a> and <a href="https://github.com/rustdesk/doc.rustdesk.com">RustDesk Doc</a> to your native language</b>
</p>

> [!Caution]
> **Misuse Disclaimer:** <br>
> The developers of RustDesk do not condone or support any unethical or illegal use of this software. Misuse, such as unauthorized access, control or invasion of privacy, is strictly against our guidelines. The authors are not responsible for any misuse of the application.


Chat with us: [Discord](https://discord.gg/nDceKgxnkV) | [Twitter](https://twitter.com/rustdesk) | [Reddit](https://www.reddit.com/r/rustdesk) | [YouTube](https://www.youtube.com/@rustdesk)

[![RustDesk Server Pro](https://img.shields.io/badge/RustDesk%20Server%20Pro-Advanced%20Features-blue)](https://rustdesk.com/pricing.html)

Yet another remote desktop solution, written in Rust. Works out of the box with no configuration required. You have full control of your data, with no concerns about security. You can use our rendezvous/relay server, [set up your own](https://rustdesk.com/server), or [write your own rendezvous/relay server](https://github.com/rustdesk/rustdesk-server-demo).

![image](https://user-images.githubusercontent.com/71636191/171661982-430285f0-2e12-4b1d-9957-4a58e375304d.png)

RustDesk welcomes contribution from everyone. See [CONTRIBUTING.md](docs/CONTRIBUTING.md) for help getting started.

[**FAQ**](https://github.com/rustdesk/rustdesk/wiki/FAQ)

[**BINARY DOWNLOAD**](https://github.com/rustdesk/rustdesk/releases)

[**NIGHTLY BUILD**](https://github.com/rustdesk/rustdesk/releases/tag/nightly)

[<img src="https://f-droid.org/badge/get-it-on.png"
    alt="Get it on F-Droid"
    height="80">](https://f-droid.org/en/packages/com.carriez.flutter_hbb)
[<img src="https://flathub.org/api/badge?svg&locale=en"
    alt="Get it on Flathub"
    height="80">](https://flathub.org/apps/com.rustdesk.RustDesk)

## Dependencies

Desktop versions use Flutter or Sciter (deprecated) for GUI. This tutorial is for Sciter only, since it is easier and more friendly to start. Check out our [CI](https://github.com/rustdesk/rustdesk/blob/master/.github/workflows/flutter-build.yml) for building the Flutter version.

Please download Sciter dynamic library yourself.

[Windows](https://raw.githubusercontent.com/c-smile/sciter-sdk/master/bin.win/x64/sciter.dll) |
[Linux](https://raw.githubusercontent.com/c-smile/sciter-sdk/master/bin.lnx/x64/libsciter-gtk.so) |
[macOS](https://raw.githubusercontent.com/c-smile/sciter-sdk/master/bin.osx/libsciter.dylib)

## Raw Steps to build

- Prepare your Rust development env and C++ build env

- Install [vcpkg](https://github.com/microsoft/vcpkg), and set `VCPKG_ROOT` env variable correctly

  - Windows: vcpkg install libvpx:x64-windows-static libyuv:x64-windows-static opus:x64-windows-static aom:x64-windows-static
  - Linux/macOS: vcpkg install libvpx libyuv opus aom

- run `cargo run`

## [Build](https://rustdesk.com/docs/en/dev/build/)

## How to Build on Linux

### Ubuntu 18 (Debian 10)

```sh
sudo apt install -y zip g++ gcc git curl wget nasm yasm libgtk-3-dev clang libxcb-randr0-dev libxdo-dev \
        libxfixes-dev libxcb-shape0-dev libxcb-xfixes0-dev libasound2-dev libpulse-dev cmake make \
        libclang-dev ninja-build libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev libpam0g-dev
```

### openSUSE Tumbleweed

```sh
sudo zypper install gcc-c++ git curl wget nasm yasm gcc gtk3-devel clang libxcb-devel libXfixes-devel cmake alsa-lib-devel gstreamer-devel gstreamer-plugins-base-devel xdotool-devel pam-devel
```

### Fedora 28 (CentOS 8)

```sh
sudo yum -y install gcc-c++ git curl wget nasm yasm gcc gtk3-devel clang libxcb-devel libxdo-devel libXfixes-devel pulseaudio-libs-devel cmake alsa-lib-devel gstreamer1-devel gstreamer1-plugins-base-devel pam-devel
```

### Arch (Manjaro)

```sh
sudo pacman -Syu --needed unzip git cmake gcc curl wget yasm nasm zip make pkg-config clang gtk3 xdotool libxcb libxfixes alsa-lib pipewire
```

### Install vcpkg

```sh
git clone https://github.com/microsoft/vcpkg
cd vcpkg
git checkout 2023.04.15
cd ..
vcpkg/bootstrap-vcpkg.sh
export VCPKG_ROOT=$HOME/vcpkg
vcpkg/vcpkg install libvpx libyuv opus aom
```

### Fix libvpx (For Fedora)

```sh
cd vcpkg/buildtrees/libvpx/src
cd *
./configure
sed -i 's/CFLAGS+=-I/CFLAGS+=-fPIC -I/g' Makefile
sed -i 's/CXXFLAGS+=-I/CXXFLAGS+=-fPIC -I/g' Makefile
make
cp libvpx.a $HOME/vcpkg/installed/x64-linux/lib/
cd
```

### Build

```sh
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
source $HOME/.cargo/env
git clone --recurse-submodules https://github.com/rustdesk/rustdesk
cd rustdesk
mkdir -p target/debug
wget https://raw.githubusercontent.com/c-smile/sciter-sdk/master/bin.lnx/x64/libsciter-gtk.so
mv libsciter-gtk.so target/debug
VCPKG_ROOT=$HOME/vcpkg cargo run
```

## How to build with Docker

Begin by cloning the repository and building the Docker container:

```sh
git clone https://github.com/rustdesk/rustdesk
cd rustdesk
git submodule update --init --recursive
docker build -t "rustdesk-builder" .
```

Then, each time you need to build the application, run the following command:

```sh
docker run --rm -it -v $PWD:/home/user/rustdesk -v rustdesk-git-cache:/home/user/.cargo/git -v rustdesk-registry-cache:/home/user/.cargo/registry -e PUID="$(id -u)" -e PGID="$(id -g)" rustdesk-builder
```

Note that the first build may take longer before dependencies are cached, subsequent builds will be faster. Additionally, if you need to specify different arguments to the build command, you may do so at the end of the command in the `<OPTIONAL-ARGS>` position. For instance, if you wanted to build an optimized release version, you would run the command above followed by `--release`. The resulting executable will be available in the target folder on your system, and can be run with:

```sh
target/debug/rustdesk
```

Or, if you're running a release executable:

```sh
target/release/rustdesk
```

Please ensure that you run these commands from the root of the RustDesk repository, or the application may not find the required resources. Also note that other cargo subcommands such as `install` or `run` are not currently supported via this method as they would install or run the program inside the container instead of the host.

## File Structure

- **[libs/hbb_common](https://github.com/rustdesk/rustdesk/tree/master/libs/hbb_common)**: video codec, config, tcp/udp wrapper, protobuf, fs functions for file transfer, and some other utility functions
- **[libs/scrap](https://github.com/rustdesk/rustdesk/tree/master/libs/scrap)**: screen capture
- **[libs/enigo](https://github.com/rustdesk/rustdesk/tree/master/libs/enigo)**: platform specific keyboard/mouse control
- **[libs/clipboard](https://github.com/rustdesk/rustdesk/tree/master/libs/clipboard)**: file copy and paste implementation for Windows, Linux, macOS.
- **[src/ui](https://github.com/rustdesk/rustdesk/tree/master/src/ui)**: obsolete Sciter UI (deprecated)
- **[src/server](https://github.com/rustdesk/rustdesk/tree/master/src/server)**: audio/clipboard/input/video services, and network connections
- **[src/client.rs](https://github.com/rustdesk/rustdesk/tree/master/src/client.rs)**: start a peer connection
- **[src/rendezvous_mediator.rs](https://github.com/rustdesk/rustdesk/tree/master/src/rendezvous_mediator.rs)**: Communicate with [rustdesk-server](https://github.com/rustdesk/rustdesk-server), wait for remote direct (TCP hole punching) or relayed connection
- **[src/platform](https://github.com/rustdesk/rustdesk/tree/master/src/platform)**: platform specific code
- **[flutter](https://github.com/rustdesk/rustdesk/tree/master/flutter)**: Flutter code for desktop and mobile
- **[flutter/web/js](https://github.com/rustdesk/rustdesk/tree/master/flutter/web/v1/js)**: JavaScript for Flutter web client

## Screenshots

![Connection Manager](https://github.com/rustdesk/rustdesk/assets/28412477/db82d4e7-c4bc-4823-8e6f-6af7eadf7651)

![Connected to a Windows PC](https://github.com/rustdesk/rustdesk/assets/28412477/9baa91e9-3362-4d06-aa1a-7518edcbd7ea)

![File Transfer](https://github.com/rustdesk/rustdesk/assets/28412477/39511ad3-aa9a-4f8c-8947-1cce286a46ad)

![TCP Tunneling](https://github.com/rustdesk/rustdesk/assets/28412477/78e8708f-e87e-4570-8373-1360033ea6c5)

