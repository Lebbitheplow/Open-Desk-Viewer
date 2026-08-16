# Alpha Scope and Workflow Audit

Audits the tree against the seven stated alpha requirements and lists the work needed to close
the gaps. Companion to `plans/audit-remediation.md`, which covered security and correctness and
is complete. This one covers a different question: **does the intended workflow actually run
end to end.**

Status key: `TODO` / `WIP` / `DONE` / `BLOCKED` / `DECIDE`.

Scope discipline: nothing here adds a feature the requirements do not ask for. Every item either
wires up something already half-built, fixes a contract that does not match its consumer, or
writes down a decision the deployment cannot proceed without.

---

## The requirements, and where each one stands

Verdicts are as of the audit. The Status column tracks them as phases land.

| # | Requirement | Verdict at audit | Now |
|---|---|---|---|
| R1 | Admins create, manage and remove manager accounts | **Partial.** Roles and grant/revoke work. Create and remove do not exist | **fixed** (C.1 to C.3) |
| R2 | Client binaries preconfigured with the server address | **Partial.** The mechanism is real and locked; the Android build that ships it does not work | **fixed** (A.2) |
| R3 | Preinstalled in firmware or MDM, no end-user setup | **Broken.** No enrollment token can reach a device, so no device can join the fleet | **fixed** (A.1); what the image must provide is now written down (D.4) |
| R4 | Serial number in the device name or a searchable field | **Not implemented.** Column, template and search helper exist; nothing is wired and the client sends no serial | **fixed** (B.1 to B.4) |
| R5 | ODV API renames devices from another system | **Partial.** Rename works for a portal admin; an external system can neither authenticate nor address a device | **fixed** (B.5, B.6) |
| R6 | Client auto-starts on boot, stays available, no customer interaction | **Partial.** The receiver exists, is off by default, and prompts the customer on every boot | **fixed** (A.4, D.1, D.5); capture without a user is an image property, specified in D.4 |
| R7 | Technician finds a device by serial or name and connects unattended | **Partial.** The portal path works. The client path returns an id that cannot be connected to | **fixed** (A.3, B, and C.4, which found the client could not sign in at all) |

The pattern is consistent with the last plan's closing note: everything verified by a Go test or
a running stack is in good shape, and everything in the three unverified bands (no CI run, no
browser sign-in, no real client) is where the gaps are.

---

## Phase A: the workflow does not currently run end to end

Each of these alone stops the primary workflow. Nothing below them is worth doing first.

| # | Status | Item |
|---|---|---|
| A.1 | DONE | An enrollment token cannot reach a device by any path |
| A.2 | DONE | The Android CI build does not produce a working client |
| A.3 | DONE | `/api/peers` returns an id the client cannot connect to |
| A.4 | DONE | Start on boot is off by default and blocked by ungranted permissions |

### What landed in Phase A

**A.1, two provisioning channels, because Android and desktop genuinely differ.**

- **Android: managed configuration.** `res/xml/app_restrictions.xml` declares the keys an MDM
  console offers, `AndroidManifest.xml` points `android.content.APP_RESTRICTIONS` at it,
  `common.kt:managedConfig` reads them through `RestrictionsManager`, and `MainActivity` exposes
  them on the existing `mChannel` as `get_managed_config`. The keys are the client's own config
  names, so what an administrator types is what the client stores.
- **Desktop: `--enrollment-token=`**, plus the same value in the generated deploy script, which
  now emits `rustdesk --option enrollment-token <token>`. `--option` is upstream's own mechanism,
  so this needed no client change at all.
- **The token deliberately bypasses the already-configured guard** (`_applyEnrollmentToken`).
  That guard exists to stop a client being re-pointed at another deployment, and a token names no
  server. More to the point, a build with its deployment baked in is "configured" by definition,
  so leaving the token inside the guard would have meant **no locked build could ever enrol** —
  the exact failure this item exists to fix, reintroduced one layer down.
- **It is applied only while `device-token` is empty.** A spent token rewritten on every launch
  would leave a live credential in the config of every deployed device for no gain.

**A.2, the Android workflow rebuilt against upstream's own Android job.** Four defects, and the
first two meant the artifact had no Rust in it at all:

- `cargo ndk … -p hbb_common` built an rlib, not the `librustdesk` cdylib the APK loads. Now
  `flutter/ndk_arm64.sh` and siblings, with the `.so` and `libc++_shared.so` copied into
  `jniLibs/<abi>/` as `flutter/build_android.sh` expects.
- There was no vcpkg step, so `build.rs:install_android_deps` had nothing to link against. Now
  `flutter/build_android_deps.sh <abi>` with the pinned `VCPKG_COMMIT_ID`.
- `dart run build_runner build` is not this project's generator. Now
  `flutter_rust_bridge_codegen 1.80.1`, matching the `=1.80` pin in `Cargo.toml`.
- **Every `|| true` is gone**, and signing uses a real keystore from `ANDROID_SIGNING_KEY` with an
  explicit warning annotation when it is absent. The old `--ks /dev/null … || true` produced an
  artifact an MDM cannot update and firmware cannot trust.

The AAB build was dropped: an app bundle is for the Play Store, and this fleet is preinstalled or
MDM-pushed. Expect the first real run to fail at `--verify` if anything above is still wrong,
which is that guard working rather than a new defect.

**A.3, `/api/peers` now answers what the client connects to.** `peers.go` returns
`d.rustdesk_id` as `id`, adds `info.device_name` from `devices.name` (the field
`PeerPayload.toPeer` actually displays), and maps `status` from `devices.connectivity` through
`peerStatus` instead of the hardcoded `0` that reported the whole fleet offline.

`internal/integration/peers_test.go` is the check that would have caught it, and the assertion is
a **comparison rather than a value**: it enrols a device, asks `/api/v1/devices/{id}/connect` what
the portal connects to, and requires the peer list to offer that same id. Each surface passed its
own test while disagreeing about what an id is, so only comparing them finds it.

**Proved load-bearing by negative control.** Reverting `id` to `p.ID.String()` and `status` to `0`
fails both tests with the predicted messages (`the portal connects to "760000042" and the peer
list offers [a973f4a1-…]`), and restoring passes. `TestPeerStatusFollowsConnectivity` is
discriminating for the same reason: the same query must return 1, 0 and -1 for three connectivity
values, which no constant can do.

**A.4, start on boot.** `_enableStartOnBoot` turns it on **once**, for a client that knows its
deployment, recording the fact in `odv-start-on-boot-provisioned` so an operator who turns it back
off keeps that decision. This is provisioning, not policy. The `BootReceiver` toast reading
"RustDesk is Open" is gone (visible to the customer, wrong brand), and the permission refusal now
logs at warn naming the two grants and where a managed device gets them, because a silent debug
line is how a fleet stays offline after a power cut without anyone knowing why.

### Phase A verification, actually run

| Check | Result |
|---|---|
| `go build`, `go vet`, `gofmt -l` | clean |
| Full Go suite with `ODV_TEST_DB=1` against real PostgreSQL | pass; integration 28.9 s, migrations 5.4 s, so neither silently skipped |
| `TestDeployScriptCarriesTheEnrollmentToken` | 3 subtests pass, including a token carrying quotes or whitespace being refused rather than escaped |
| `TestPeersAnswerWhatTheClientConnectsTo`, `TestPeerStatusFollowsConnectivity` | pass, and both fail as predicted under negative control |
| `flutter analyze` on Flutter 3.24.0 | the new code contributes **zero** issues. The one error in `main.dart` is `setResizable(!bind.isIncomingOnly())` at line 394, upstream code, the same one recorded in the previous plan |
| `odv-android.yml` | parses as YAML; 21 steps, 3 matrix entries, no step swallows errors |

**Not verified, and it is the same band as before:** no `odv-*` workflow has run, so the rebuilt
Android job is correct by construction and by comparison with upstream's, not by observation. No
real device has read a managed configuration.

### A.1 An enrollment token cannot reach a device by any path

**This is the item that most breaks the stated workflow.** `enrollment-token` is read at
`src/hbbs_http/sync.rs:324` and written **nowhere in the tree**. Verified by grepping every
`.rs`, `.dart`, `.go`, `.kt`, `.sh` and `.yml` outside `plans/`: the only other mention is
`src/common.rs:2163`, a comment saying it is deliberately not locked.

Every route is closed:

- **Not baked at build time.** `ODV_LOCKED_SETTINGS` carries four keys and this is not one of
  them, correctly, because `sync.rs` has to clear it after redemption.
- **Not a boot argument.** `_kPreConfigOptions` (`flutter/lib/main.dart:125-130`) accepts
  `--id-server=`, `--api-server=`, `--relay-server=` and `--key=` only.
- **Not in the deploy script.** `HandleDevicesDeploy` (`internal/rustdeskapi/audit.go:375-401`)
  emits `rustdesk --config "host=…,key=…,api=…"`. No token.
- **Not pushable.** `strategy.config_options` arrives on the heartbeat, and the heartbeat
  requires the device credential that redeeming the token is supposed to produce. Chicken and egg.

Consequence, and it is total: a deployed client heartbeats, gets 401 forever, and lands in
`device_observations` rather than `devices`. It never appears in the portal, never receives a
password, and is never connectable. The Phase 3 verification in the last plan drove enrollment
with curl, which is why this was not visible: the server half is correct and tested, and the
device half has no way to start it.

**Fix, and there are two halves because Android and desktop differ.**

1. **Android, the primary platform: managed configuration.** Add
   `android:restrictionsMetadata` and `res/xml/app_restrictions.xml` declaring
   `enrollment_token` (and, for a firmware image that cannot use MDM, the four server-identity
   keys as an optional fallback). Read it in `MainActivity`/`MainApplication` through
   `RestrictionsManager` on first run and hand it to the Rust side as the `enrollment-token`
   option. This is the standard channel every Android MDM already speaks, which is precisely
   what the requirement assumes exists.
2. **Desktop and lab: one more boot argument.** Add `--enrollment-token=` to
   `_kPreConfigOptions` and the matching key to `HandleDevicesDeploy`'s `--config` line. It goes
   through the same first-run-only guard as the other four, and it must be excluded from the
   deployment lock so `sync.rs` can still clear it.

**Design points to hold to.** The token is single-use-ish by policy (`max_uses`), so a firmware
image carrying one token is a deliberate choice about how many devices may enrol on it; say so
where the token is issued. Clearing it after redemption (already implemented) is what stops a
cloned image enrolling twice.

**Verification that would actually establish this:** a device with only the managed
configuration set, no hand editing, reaching `ACTIVE` in the portal.

### A.2 The Android CI build does not produce a working client

`.github/workflows/odv-android.yml`. Three independent defects, and the workflow has never run
(item 5.8 of the previous plan), so none has ever surfaced.

- **The wrong crate is built, and failure is swallowed.**
  `cargo ndk -t $abi build --release -p hbb_common 2>&1 || true`. `hbb_common` is an rlib
  dependency and produces no shared object. The APK needs `librustdesk`, the root crate's
  `cdylib` (`Cargo.toml:11-13`). The `|| true` means a total failure is a green step.
- **Nothing puts a `.so` where the APK looks for it.** `flutter/build_android.sh` strips
  `android/app/src/main/jniLibs/arm64-v8a/*`, so that is the expected location.
  `flutter/android/app/src/main/jniLibs` **does not exist in the tree** and no step creates it.
  The correct invocation is the upstream one: `cargo ndk -t <abi> -o
  ../flutter/android/app/src/main/jniLibs build --release --features flutter` from the repo root.
- **The bridge codegen is the wrong tool.** `dart run build_runner build … || true` is not what
  this project uses. `Cargo.toml` pins `flutter_rust_bridge = "=1.80"` and the generator is
  `flutter_rust_bridge_codegen 1.80.1` against `src/flutter_ffi.rs`, as recorded in the previous
  plan's toolchain notes.

Note the interaction with the 6.4 guard: `check-client-config.sh --verify` greps the shared
object inside each APK for the baked-in api-server. With no `.so` present it should fail, which
means the first CI run will most likely fail at that step. That is the guard doing its job and
is the expected first symptom, not a new defect.

**Fix:** replace the two Rust steps with the upstream build path, drop every `|| true`, and pin
the codegen version. Also reconcile `platform/android/README.md`, which documents Flutter 3.22.3
/ Rust 1.75 / NDK r28c against a workflow pinning 3.24.0 / 1.82.0 / NDK 26.1.10909125.

### A.3 `/api/peers` returns an id the client cannot connect to

`internal/rustdeskapi/peers.go:64` sends `"id": p.ID.String()`, which is `devices.id`, our
internal UUID (`internal/peers/service.go:19`, `SELECT d.id`). The RustDesk client reads that
field as the peer's connect target: `flutter/lib/models/group_model.dart:262` builds a
`PeerPayload`, `flutter/lib/common/hbbs/hbbs.dart:87` takes `id = json['id']`, and
`PeerPayload.toPeer` passes it straight into `Peer.fromJson` as the id used to connect.

So a technician signed in to the client sees a Devices list of UUIDs and cannot connect to any
of them. `HandleDeviceConnect` (`internal/apiv1/devices.go:638`) returns
`rustdesk://connect/<rustdesk_id>` and is correct, which is direct evidence the two disagree.

Two smaller mismatches in the same response, both worth fixing in the same change because they
are what makes a device findable in the client:

- **The platform's device name never reaches the client.** `PeerPayload.toPeer`
  (`hbbs.dart:95-105`) builds the display from `info['username']`, `info['device_name']` and
  `user_name`. The server sends `info: {hostname, os}` and a top-level `name` the client ignores.
  So the name that R4 and R5 exist to control is portal-only. Send `info.device_name` (and
  `alias` if the client honours it on this path) so the assigned name is what the technician sees.
- **`"status": 0` is hardcoded** (`peers.go:67`), so no group peer ever shows as online. The
  data is available: `d.connectivity` is maintained by the worker and `Peer.LastSeenAt` is
  already selected and scanned.

**Fix:** return `rustdesk_id` as `id`, populate `info.device_name` from `devices.name`, and map
`status` from `connectivity`. Add an integration test asserting the peers id matches what
`/api/v1/devices/{id}/connect` returns for the same device, which is the assertion that would
have caught this.

### A.4 Start on boot is off by default and blocked by ungranted permissions

`flutter/android/app/src/main/kotlin/com/carriez/flutter_hbb/BootReceiver.kt:25-33`. The receiver
returns early unless **all three** hold:

1. `KEY_START_ON_BOOT_OPT` is true in SharedPreferences. It defaults to false and nothing in the
   fork sets it.
2. `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS` is granted. A special-access screen, not a dialog.
3. `SYSTEM_ALERT_WINDOW` is granted. Likewise.

A preconfigured client that does not start on boot is not preconfigured. All three need to be
part of provisioning: (1) as a default the fork sets when the deployment lock is present, (2) and
(3) as things the MDM policy or the firmware image grants, which belongs in the deployment spec
(D.4).

Also here, and separable:

- **`Toast.makeText(context, "RustDesk is Open", …)` on every boot** (`BootReceiver.kt:39`). It
  is visible to the customer and names the wrong product. Remove it or make it silent.
- **`START_NOT_STICKY`** (`MainService.kt:359`) is upstream's deliberate choice and means a
  service killed by the OS does not return until the next reboot. For an unattended fleet that
  deserves a decision rather than an inheritance. Flag as D.5, not a blocker.

---

## Phase B: device identification, the part R4 and R5 rest on

The schema anticipated this and nothing was connected to it.

| # | Status | Item |
|---|---|---|
| B.1 | DONE | The client sends no serial number, and no hostname either |
| B.2 | DONE | `serial_number` is never written, and the naming template is dead code |
| B.3 | DONE | Portal search does not cover the serial |
| B.4 | DECIDED | What counts as the serial on Android |
| B.5 | DONE | An external system has no way to authenticate |
| B.6 | DONE | An external system has no way to address a device |

### What landed in Phase B

**B.1, the client now reports what the platform names it from.** `enroll_device`
(`src/hbbs_http/sync.rs`) sends `hostname`, `os` and `serial` alongside the token, taking the
first two from `crate::get_sysinfo()`. `EnrollRequest` gained `Serial` and the handler passes it
through. The serial itself comes from a new `odv-serial` config option, written once during
provisioning.

**The serial also rides on sysinfo, and that turned out to matter more than expected.**
`openapi.yaml` has documented a `serial` field on `/api/sysinfo` since the initial spec and
**nothing ever read it** — the same documented-but-not-delivered shape the previous plan kept
finding. Wiring it is what gives an **already-installed** fleet serials: a device enrolled before
this change never re-enrols on its own, so enrollment alone would have left every existing device
permanently unsearchable. `ProcessSysinfo` fills the column only when it is null:
`serial_number = COALESCE(serial_number, NULLIF($5, ''))`. A serial that changes is hardware being
swapped, and silently replacing the identifier a technician searches by is worse than leaving a
human to correct it.

**B.2, the naming template is connected.** `enrollment.deviceName` now calls
`fleet.GenerateDeviceName` when a serial is present, resolving the customer and location names
from the token's ids, and falls back to hostname then RustDesk id when it is not. Three functions
and one config value that had **zero callers** — `GenerateDeviceName`, `SearchDevices`,
`UpdateDevice`, `DEVICE_NAME_TEMPLATE` — are now either used or reachable.

Two safeguards worth naming:

- **`name` is deliberately absent from the re-enrollment UPDATE.** A name set through the API is
  the customer's own identifier for the machine, and regenerating it on re-enrolment would
  silently undo the rename that R5 exists to perform. `serial_number` *is* updated there, because
  the serial is a fact about hardware and the name is a decision.
- **An empty template falls back to the serial.** `devices.name` is `NOT NULL`, so a deployment
  that blanked `DEVICE_NAME_TEMPLATE` would otherwise insert an empty name and produce a device
  invisible in a list sorted by name.

**B.3 and B.6, one change serving two callers.** `q` now matches `serial_number` as well as name
and hostname, and a new exact `?serial=` parameter is the route an external system takes: it holds
an identifier and needs a device id before it can PATCH a name. Exact rather than substring
because a serial is known whole, and `serial_number` has no trigram index. `serial_number` moved
from `DeviceDetail` onto `Device`, so the list returns it and a caller can confirm it matched the
right machine; the portal shows it as a column and the search box says so.

**B.4, decided: a preference chain rather than a single source**, because which identifier is
available is a property of the deployment rather than something the app can choose.
`common.kt:deviceSerial` takes, in order: an asset tag from managed configuration (`serial-number`,
now in `app_restrictions.xml`), the hardware serial via `Build.getSerial()` (readable only by a
privileged or device-owner app since Android 10, so this is the firmware-preinstall case), then
`ANDROID_ID`. **Which one a given fleet actually carries has to be recorded in the deployment
spec (D.4), because it changes what a technician types into the search box.**

**B.5, the Keycloak service account, and `api_clients` deleted.** The plan said to pick one of the
two options and delete the other. Picked: a client-credentials token, validated by the same JWT
middleware as every other caller and resolved to an ordinary `users` row. That keeps access
control, the audit actor and the IDOR sweep working on one kind of caller rather than two.

`provisionUser` previously refused any token with no email claim, which is exactly what a
client-credentials token is, so **no machine caller could authenticate at all**. It now recognises
Keycloak's `service-account-` username convention and provisions with a synthetic address in
`.invalid`, a domain no mailbox can occupy, so no invitation can ever be addressed to a machine.

**A service account is granted no role.** A person signing in is a technician somebody hired; a
machine account's reach is a decision, so it starts inert and its first call answers 403 until an
administrator grants what the integration needs. That is the correct failure.

Migration `000011` drops `api_clients` and `api_tokens`. Neither was read by any Go code, ever.
An unused credential table is the exact defect shape the earlier audit kept finding, and a
half-built second authentication path is a worse one to leave lying around than most.

### Phase B verification, actually run

| Check | Result |
|---|---|
| `go build`, `go vet`, `gofmt -l` | clean |
| Full Go suite with `ODV_TEST_DB=1` | pass; integration 29.6 s, migrations 5.6 s |
| Migration round trip, all four tests | pass, including the per-version schema fingerprint that 000011 has to survive in both directions |
| `TestEnrollmentNamesTheDeviceFromItsSerial`, `…WithoutASerialFallsBackToHostname` | pass |
| `TestExternalSystemFindsBySerialAndRenames` | pass: find by serial, rename, findable by both the new name and the serial |
| `TestReenrollmentKeepsAnAPIAssignedName` | pass: name survives, serial updates |
| `TestSysinfoBackfillsAMissingSerialButDoesNotOverwriteOne` | pass, both halves |
| `TestServiceAccountIsProvisionedWithoutAnEmail`, `TestATokenWithNoEmailAndNoServiceAccountIsStillRefused` | pass; the second is what stops the first from being a hole |
| Negative control | reverting `deviceName` to ignore the serial fails with `name = "tablet-7", want it to carry the serial SN-ABC-001`; restoring passes |
| `cargo check --features linux-pkg-config` on Rust 1.82.0 | zero errors |
| `flutter analyze` | new Dart contributes zero issues; total unchanged at 1160, the one `main.dart` error still the upstream `setResizable` |
| Portal `npm run lint`, `build`, `test` | clean, builds, 34 tests pass |
| `client-api-sweep.sh --check` | matches the baseline: no new client-called path, only new fields on existing ones |

### B.1 The client sends no serial number, and no hostname either

`enroll_device` (`src/hbbs_http/sync.rs:329-334`) posts `{token, id, uuid, version}`.
`EnrollRequest` (`internal/enrollment/service.go:245-253`) also declares `Hostname` and `OS`,
and neither is ever populated on the wire.

Consequence: `deviceName(req)` (`service.go:442-447`) falls through to `req.RustdeskID`, so
**every enrolled device is named its nine-digit RustDesk id.** The last plan's Phase 3 table
records "named from the hostname" because the curl payload supplied one; no client does.

**Fix:** add `hostname`, `os` and `serial` to the enrollment payload, and a `SerialNumber` field
to `EnrollRequest` written into `devices.serial_number` in the same insert. Grepping confirmed
there is no occurrence of "serial" anywhere in `src/`, `libs/` or the Kotlin, so this is new
client code, gated per platform.

### B.2 `serial_number` is never written, and the naming template is dead code

Everything needed exists and has no callers. Verified by grepping for call sites:

- `fleet.GenerateDeviceName` (`internal/fleet/fleet.go:184`), which implements
  `{customer}-{location}-{serial}`: **zero callers.**
- `fleet.SearchDevices` (`fleet.go:238`), which already searches
  `name ILIKE … OR hostname ILIKE … OR serial_number ILIKE …`: **zero callers.**
- `fleet.UpdateDevice` (`fleet.go:152`), the only writer of `serial_number`: **zero callers.**
- `DEVICE_NAME_TEMPLATE` (`internal/config/config.go:125`): read into config, used nowhere.

**Fix:** call `GenerateDeviceName` from `enrollment.Enroll` in place of `deviceName`, falling
back to the current behaviour when the serial is absent so an unenrichable device still gets a
name. The customer and location are already on the token, so the template's inputs are all in
scope at that point.

Naming is also the join with R5: an API rename must not be undone by the next enrolment. The
re-enrolment branch (`service.go:331-345`) already leaves `name` alone, which is the right
behaviour and should get a test saying so on purpose.

### B.3 Portal search does not cover the serial

`apiv1.HandleDevices` (`internal/apiv1/devices.go:110-115`) matches `q` against `d.name` and
`d.hostname` only. Even a populated `serial_number` would not be findable, and `SearchDevices`,
which does cover it, is not reachable from HTTP.

**Fix:** add `d.serial_number ILIKE $n` to the same clause. Note the trigram indexes exist on
`name` and `hostname` but not on `serial_number` (`000001_initial_schema.up.sql:153-155`); a
serial is usually searched whole rather than by substring, so decide between an exact-match
branch and adding the index rather than adding an unindexed `ILIKE '%…%'` to the hot path.

### B.4 What counts as the serial on Android

**A decision, not a task.** `Build.getSerial()` requires `READ_PHONE_STATE` and since Android 10
is restricted to device-owner and privileged apps. For a firmware preinstall that is available;
for an MDM-deployed APK on a device the customer owns it generally is not.

Options, in the order they should be preferred:

1. The real serial, when the app is a privileged or device-owner app in the firmware image.
2. An asset tag injected through the same managed configuration as A.1. This is the most likely
   answer for the MDM path and costs nothing extra once A.1 exists.
3. `Settings.Secure.ANDROID_ID` as a stable per-device fallback, clearly labelled as not a
   manufacturer serial.

Whichever is chosen has to be recorded in the deployment spec (D.4), because the technician's
search behaviour depends on which one the fleet actually carries.

### B.5 An external system has no way to authenticate

`api_clients` exists in `000001_initial_schema.up.sql:244` and **no Go code reads it**. Every
`/api/v1` route requires a Keycloak-issued user JWT resolved to a `users` row.

A Keycloak service account is the natural substitute and does not currently work:
`provisionUser` (`internal/identity/provisioning.go:29-32`) refuses a token with no `email`
claim, which is exactly what a client-credentials token has, and a newly provisioned user gets
`RoleTechnician` (`provisioning.go:53`), so `PATCH /api/v1/devices/{id}` would answer 403 anyway.

**Fix, and the cheaper option is the second one.** Either implement `api_clients` as a real
credential (hashed key, scoped to specific routes, audited under its own actor identity), or
document the service-account path and make it work: allow a token carrying a
`client_id`-derived identity to provision without an email, and give the operator a documented
way to grant it the role a rename needs. Pick one and delete the other, because a table nobody
reads is what the last plan kept finding.

Whichever is chosen, the audit event for a rename must name the calling system rather than a
human, since "who renamed this device" is the question the log exists to answer.

### B.6 An external system has no way to address a device

`PATCH /api/v1/devices/{id}` keys on our internal UUID (`pathUUID`). The external system knows a
serial or a customer asset tag and has no way to turn one into the other: there is no
lookup-by-serial route, and `q` does not search serial (B.3).

**Fix:** once B.3 lands, `GET /api/v1/devices?serial=…` is enough, and a caller does one lookup
then one PATCH. That is preferable to a second write route keyed on serial, which would need its
own authorization and its own audit path.

---

## Phase C: manager accounts

| # | Status | Item |
|---|---|---|
| C.1 | DONE | An administrator cannot create an account |
| C.2 | DONE | An administrator cannot remove an account |
| C.3 | DONE | The self-lockout guard checks role names that do not exist |
| C.4 | DECIDED | Client password sign-in has no way to set a password |

### What landed in Phase C

**C.1, option (a) after all: the API creates the Keycloak account.** The plan recommended the
invitation flow, (b), and the realm export settles it the other way:
`"registrationAllowed": false`. An invitation would create a local row for somebody who still had
no way to obtain a Keycloak account, so (b) alone leaves the administrator in the console they
cannot reach — which is the defect, not the fix.

(a) also turns out to be the *smaller* change. Creating the account yields the subject
immediately, so the local row is written complete: no nullable `keycloak_subject`, no claim-on-
first-sign-in path, no migration.

- `identity.AccountProvisioner` is the seam, and `KeycloakAdmin` the implementation. It
  authenticates as the **odv-api service account**, not as a realm administrator: the client
  secret is one the API already holds, and the account needs only `manage-users` and `view-users`.
  The realm export now grants exactly those, through the `service-account-odv-api` user entry.
- **Unconfigured means 503 naming what is missing**, not a startup failure. An API that refused to
  boot because accounts cannot be created would take the fleet down for a portal feature.
- **The temporary password is returned exactly once** and stored nowhere; the account carries
  `UPDATE_PASSWORD`, so it is spent on first sign-in. No invitation email, because there is no
  mail server, and an invitation that silently goes nowhere is worse than a password handed over.
- **A failed local insert takes the Keycloak account back down with it.** The account is created
  first because the subject is Keycloak's to issue, so without the compensation the retry the
  administrator is about to make answers 409 from a conflict they cannot see.

**C.2, removal removes both halves and refuses when it cannot.** A local-only delete is not a
removal: the person signs in again, the middleware provisions a fresh row, and `provisionUser`
grants the default Technician role. So "removed" would have meant "demoted until they next sign
in". If Keycloak refuses, the request answers 502 and the row stays — the failure that leaves
somebody with too little access is better than the one that leaves them with some.

- **Deactivation and removal both rotate device passwords**, over `devicesReachableByUser`. This
  is the hole C.2 named: an ex-manager who wrote a device password down still held a working
  credential, because the device goes on accepting it. `rotateForAccessChange` was generalised
  from a support-group-shaped helper to name its resource, so all three withdrawals report through
  one path.
- **A service account's Keycloak side is left alone.** It belongs to the client it hangs off;
  deleting it would break the integration's client rather than withdraw its access.
- **The audit trail outlives the account** — `audit_events` lost its foreign keys in 000006 —
  and a test asserts the `user.created` and `user.deleted` rows survive the user they name.

**C.3, and the guard was worse than dead.** The literals `"admin"` and `"manager"` matched no
seeded role, so an administrator could revoke their own `Administrator`. Now compared against
`identity.RoleAdministrator` and `RoleSupportManager`. The self guard is not the invariant that
matters, though: `admin()` also admits a Support Manager, so **the last active administrator
cannot be demoted, deactivated or removed by anybody**, on all three routes.

**C.4, decided: drop password sign-in, and make the option that works visible.** Reading the
client's own parser turned up a bigger fault than the item recorded. `queryOidcLoginOptions`
(`user_model.dart:255-263`) keeps only entries prefixed `common-oidc/` or `oidc/`; the server sent
`["common"]`, which matches neither. **So the client rendered no provider button at all**, and the
password fields beside it could authenticate nobody. The client sign-in path was closed both ways,
which is the whole of R7's client half.

- `HandleLoginOptions` now returns `common-oidc/[{"name":"SSO"}]`, and the test transcribes the
  client's parser rather than asserting our own idea of the format — the old response was valid
  JSON, accepted without error, and discarded.
- `/api/login` refuses a username and password with a message naming SSO, instead of the "invalid
  credentials" it returned to everybody.
- Migration `000012` drops `user_credentials` and `login_attempts`, and `Authenticate`,
  `SetPassword`, the Argon2id code, the lockout and the per-IP throttle go with them. Correct code,
  and unreachable: identity lives in Keycloak, which owns password policy, lockout and
  disablement, and a second credential store here would have been governed by none of them and
  would have kept working after an account was disabled. Same call B.5 made about `api_clients`.

### Phase C verification, actually run

| Check | Result |
|---|---|
| `go build`, `go vet`, `gofmt -l` | clean |
| Full Go suite with `ODV_TEST_DB=1` | pass; integration 27.9 s, migrations 4.3 s |
| Migration round trip including 000012 | pass, both directions, per-version fingerprints intact |
| `TestAdministratorCreatesAndRemovesAManagerAccount` | pass: account in both places, role granted, both removed, audit rows survive |
| `TestRemovingAUserRotatesThePasswordsTheyCouldRead`, `TestDeactivationRotates…` | pass, and neither rotates a device the user never reached |
| `TestTheLastAdministratorCannotBeRemovedByAnybody` | pass on all three routes, and the same call succeeds once a second administrator exists |
| `TestAFailedLocalInsertRemovesTheAccountAgain`, `TestRemovalIsRefusedWhenTheIdentityProviderFails` | pass |
| `TestLoginOptionsOfferAProviderTheClientCanRender` | pass, with `["common"]` kept as a negative control that the transcribed parser discards |
| Negative control on C.3 | reverting the guard to `"admin"`/`"manager"` fails with `revoking your own Administrator role got 200, want 400: {"success":true}`; restoring passes |
| IDOR sweep and Read Only sweep | both extended to the two new routes and passing |
| Portal `npm run lint`, `build`, `test` | clean, builds, 34 tests pass |
| `client-api-sweep.sh --check` | matches the baseline |

**Not verified:** no request has been made against a real Keycloak admin API. `KeycloakAdmin` is
correct by construction and against the admin REST contract; the integration tests drive a fake.
The realm's `manage-users` grant has not been imported into a running Keycloak either. That is the
same unverified band as before, and the first real `POST /api/v1/users` is where it gets tested.

The parts that do work: the four seeded roles include `Support Manager`
(`000001_initial_schema.up.sql:36-40`), which is the "manager" of the requirement; grant and
revoke are administrator-gated and audited (`internal/apiv1/users.go`); deactivation takes
effect immediately because the JWT middleware rejects a disabled user; and since item 6.9 the
portal's role list comes from the API rather than a hardcoded array, so all four roles are
grantable.

### C.1 An administrator cannot create an account

There is no `POST /api/v1/users`. `Routes()` (`internal/apiv1/apiv1.go:137-141`) has list,
detail, PATCH, grant role and revoke role. A `users` row appears only as a side effect of
someone signing in through Keycloak (`ResolveUser` → `provisionUser`).

So the real flow today is: create the person in the Keycloak admin console, which is
**deliberately not routed through Caddy** (`platform/Caddyfile`, item 0.5), so the administrator
needs container-level access to reach it; have them sign in once; then find the row and grant
`Support Manager`. That is not "admins can create manager accounts".

**Fix, and the choice is a real one.** Either (a) `POST /api/v1/users` creates the Keycloak user
through the admin API and the local row in one step, which needs a Keycloak admin credential in
the API's configuration and is the only option that makes the portal self-sufficient; or (b) keep
Keycloak as the sole source of accounts and add an invitation flow: the administrator creates a
pending local row with a role pre-assigned, and the first sign-in matching that email claims it.
(b) is smaller, adds no new privileged credential, and keeps identity in one place. Recommend (b)
unless the operator wants the portal to be the only console.

**Decided: (a).** The realm sets `registrationAllowed: false`, so an invited person still has no
way to obtain a Keycloak account and the administrator is back in the console they cannot reach.
The credential is not a new privileged one either: it is the odv-api service account the API
already authenticates as, granted `manage-users`. See "What landed in Phase C" above.

### C.2 An administrator cannot remove an account

There is no `DELETE /api/v1/users/{id}`. `PATCH {active: false}` is the closest thing and leaves
the Keycloak account intact.

Two consequences worth being explicit about:

- **Deactivation does not rotate device passwords.** The automatic rotation from item 3.3 hangs
  off support-group membership changes, not off deactivation. A deactivated manager who wrote
  down device passwords still holds working credentials for every device they could reach. This
  is a real hole in "remove a manager account", and the fix is small: deactivation should trigger
  the same `RotateMany` over the devices that user could reach, using the existing helper.
- **Removal should not delete the audit trail.** `audit_events` deliberately outlives the rows it
  references (item 3.5), so a real delete is safe there. `user_roles` and `user_support_groups`
  cascade. Deletion is therefore implementable; decide with C.1 whether it also removes the
  Keycloak account or only the local one, and make the portal say which.

### C.3 The self-lockout guard checks role names that do not exist

`HandleRevokeRole` (`internal/apiv1/users.go`) refuses self-revocation when
`role == "admin" || role == "manager"`. The seeded names are `Administrator` and
`Support Manager` (`000001_initial_schema.up.sql:37-38`). **The guard never fires.** An
administrator can revoke their own `Administrator` role, and there is no way back through the
portal.

This is the same defect shape as the one item 6.9 found in the portal's hardcoded
`['Administrator', 'Manager', 'Technician']`: a role name written by hand that does not match
the database. Fix by comparing against the same constants `apiv1` already defines
(`RoleAdministrator`, `RoleReadOnly` and siblings) rather than string literals, and add a test
that drives the real role names.

Worth checking in the same pass whether a deployment can end up with zero administrators by any
other route (deactivating the last one is already blocked for self, not for others).

### C.4 Client password sign-in has no way to set a password

`identity.SetPassword` (`internal/identity/service.go:349`) has **no HTTP caller**. So
`/api/login`, the RustDesk client's password sign-in, cannot authenticate anyone: there is no way
to create a credential except writing to `user_credentials` by hand.

The OIDC path (`/api/oidc/auth`, implemented in 6.3) does work, and is the better path anyway.
**Decide and write it down:** either drop password sign-in from the client's login options
(`HandleLoginOptions`) so it does not offer a door that cannot open, or wire `SetPassword` to a
self-service change-password route. Leaving it as-is is the one option that should be ruled out,
because it looks supported.

**Decided: dropped, and the OIDC path was not reachable either.** This item assumed the client
offered a working sign-in beside the broken one. It did not: `HandleLoginOptions` returned
`["common"]`, which the client's parser discards, so no provider button was rendered at all. Both
halves are fixed above, and migration 000012 takes the credential store with it.

---

## Phase D: unattended operation and the deployment contract

| # | Status | Item |
|---|---|---|
| D.1 | DONE | MediaProjection consent on every boot |
| D.2 | DONE | The APK ships as RustDesk, and rebranding never runs |
| D.3 | DONE | CI signs with a placeholder keystore |
| D.4 | DONE | There is no firmware/MDM deployment spec |
| D.5 | DECIDED | `START_NOT_STICKY` for an unattended fleet |

### What landed in Phase D

**D.1, the client stops asking a question nobody is there to answer.** A boot start carries no
projection token, so `onStartCommand` fell through to `requestMediaProjection()` and put a system
consent dialog in front of whoever was holding the device, on every reboot. On an unattended fleet
nobody answers it and it sits there. The boot path now logs at warn instead, naming what the image
has to provide, and the interactive path is unchanged — a person who starts the app still gets the
dialog, because there a dialog is the correct thing.

**This does not make capture work; nothing in this repository can.** Screen capture without a user
is a property of the image: a privileged/system app or a device-owner deployment. What changed is
that the failure is now legible and silent rather than illegible and in the customer's face, and
§2 of the deployment spec says which of the two the operator has to choose.

**D.2, the brand split in two, which is what the old script got wrong.**

- **Customer-visible: OpenDeskViewer.** `app_name` in `strings.xml` (referenced by
  `android:label`, so the launcher, the app info screen and the accessibility list all follow),
  and `applicationId "com.opendeskviewer.client"`. The manifest carried a literal
  `android:label="RustDesk"` while `strings.xml` already held an unused `app_name` — the resource
  existed and nothing pointed at it.
- **Protocol-level: still RustDesk, deliberately.** `config::APP_NAME` is only ever written by
  upstream's signed custom-client config path, which this deployment does not use, and it derives
  `rustdesk://` — the scheme `POST /api/v1/devices/{id}/connect` returns and the manifest
  registers. Changing one end without the other breaks click-to-connect, and changing both buys
  nothing any requirement asks for. `is_custom_client()` staying false is correct for the same
  reason: it gates the feature we do not use.
- **`platform/android/rebrand.sh` deleted, not fixed.** It was called by no workflow, seded an
  `android:label="flutter_hbb"` the manifest has never had, and moved the Kotlin package directory
  in place — which breaks every JNI lookup from the Rust side, in a build that still compiles.
  `applicationId` plus a string resource does the whole job declaratively and leaves the tree
  clean, which is what the item recommended.

**D.3 was already closed by A.2** and is recorded here rather than redone: the
`--ks /dev/null … || true` placeholder is gone, CI signs from `ANDROID_SIGNING_KEY` and friends,
and an absent key raises a warning annotation on an artifact that is explicitly not deployable.
`platform/android/README.md` now points local release builds at `key.properties.example`, which
existed and was documented nowhere that a build would reach.

**D.4, the deployment spec, in `platform/README.md` under "Deploying the client".** Six sections,
one per thing the earlier phases ended up requiring of the image: the managed-configuration keys
with `enrollment-token` named, the four grants and what each one costs when it is missing,
start-on-boot, which of the three sources carries the serial, the signing identity and update
channel, and what a deployed device may do to the fleet. Every claim in it was checked against the
code rather than written from the plan: `odv-start-on-boot-provisioned`, `deviceSerial`'s
preference order, `GET /api/v1/devices?serial=`, `DEVICE_NAME_TEMPLATE`.

Two things it states that are decisions rather than descriptions, and that a deployment cannot
proceed without: **which of privileged-preinstall or device-owner the fleet uses**, and **which of
the three serial sources it carries**. Both are left as blanks the operator fills in, because
neither is ours to choose — but the spec says plainly that leaving them unanswered means a fleet
that cannot be captured, or one a technician cannot search.

**D.5, decided: keep `START_NOT_STICKY`.** A restarted service is handed no projection token, so a
sticky restart produces a service that looks alive in the notification tray and cannot capture —
worse for an unattended fleet than being plainly gone, because the platform already notices a
device that stops heartbeating (`device_connectivity_events`, and the notification targets that
watch them) and the boot receiver brings it back at the next restart. A `WorkManager` keepalive
was considered and rejected for the same reason: it could restart the process, not the consent it
lost. The reasoning is now a comment at the `return`, where the next person to wonder will be.

### Phase D verification, actually run

| Check | Result |
|---|---|
| `AndroidManifest.xml`, `strings.xml`, `app_restrictions.xml` | all parse; no `RustDesk` literal left in the manifest |
| Grep for `applicationId` dependants | none: no `authorities`, no `BuildConfig.APPLICATION_ID`, and `check-client-config.sh` makes no package assumption, so the id change carries nothing with it |
| Grep for `rebrand.sh` references | none outside the file itself, which is why deleting it changes no behaviour |
| Go suite, portal build | unchanged and passing; Phase D touched no Go and no Dart |

**Not verified, and it is the same band as A.2:** no Kotlin was compiled and no APK was built —
there is no Android SDK on this machine, and no `odv-*` workflow has ever run. The manifest and
gradle changes are correct by inspection. The first CI run is where `applicationId`, the label
resource and the Kotlin edit get tested, and D.1's log line is only observable on a real device
that reboots.

### D.1 MediaProjection consent on every boot

`MainService.onStartCommand` (`MainService.kt:339-357`): a boot start carries no
`EXT_MEDIA_PROJECTION_RES_INTENT`, so it falls to `requestMediaProjection()`, which launches
`PermissionRequestTransparentActivity` and puts a system consent dialog in front of whoever is
holding the device. Input control additionally needs the `InputService` accessibility service
enabled, which is a manual toggle in Settings.

On stock Android there is no way around either without a privileged app or a device-owner
policy. **This is the requirement's "without requiring interaction from the customer" meeting
the platform's screen-capture consent model, and it is a deployment decision, not a bug.**

The two workable answers, both of which the requirement already anticipates:

1. **Firmware preinstall as a privileged/system app**, where MediaProjection can be pre-granted
   and the accessibility service enabled in the system image.
2. **Device-owner provisioning through the MDM**, which can grant the accessibility service and
   the special-access permissions, and on recent Android can suppress the projection dialog for a
   device-owner app.

Whichever is chosen has to be stated in D.4 and the client should not fight it: if the grant is
pre-provisioned, `requestMediaProjection` should not be reached at all, and the fallback should
log rather than raise a dialog on a machine with no user.

### D.2 The APK ships as RustDesk, and rebranding never runs

`platform/android/rebrand.sh` is **called by no workflow**, and it is broken on its own terms:
it seds `android:label="flutter_hbb"` while the manifest says
`android:label="RustDesk"` (`AndroidManifest.xml:31`). Today's artifact would carry the RustDesk
label, the `com.carriez.flutter_hbb` package and the `rustdesk://` deep-link scheme.

There is a second-order effect: `common.rs:is_custom_client()` returns
`get_app_name() != "RustDesk"`, so a fork that has not rebranded is treated as a stock client by
every upstream branch that consults it.

Note the script is destructive and in-place (it moves the Kotlin package directory), so calling
it from CI needs care. A gradle-level `applicationId` and `resValue` override, or a product
flavour, is the less brittle shape and leaves the tree clean.

### D.3 CI signs with a placeholder keystore

`odv-android.yml`'s signing step uses `--ks /dev/null --ks-pass pass:changeit` under
`|| true`. `platform/android/key.properties.example` exists and CI does not use it.

Both target deployments need a real, stable signing identity: an MDM cannot push an update that
does not match the installed signature, and a firmware preinstall needs a key decided before the
image is cut. Wire the release keystore from repository secrets and drop the `|| true`.

### D.4 There is no firmware/MDM deployment spec

Several items above end in "and the firmware or MDM has to do this". Nothing in the repository
tells whoever builds that image what is required. It should be one document, and it is a
deliverable of the alpha rather than a nicety, because the alpha cannot be deployed without it:

- the managed-configuration keys the app reads (A.1), with the enrollment token named
- which permissions must be pre-granted, and which grant mechanism (D.1, A.4)
- what the app expects to be set for start-on-boot
- which value carries the serial (B.4)
- the signing identity and update channel (D.3)

`platform/README.md` is the right home. It already carries the authorization-limits section that
the root README links, and this is the same kind of statement.

### D.5 `START_NOT_STICKY` for an unattended fleet

Upstream's comment says a restarted service loses control, which is a good reason for a
user-attended client and a poor one for a machine nobody is standing next to. Decide whether to
keep it, and if kept, whether a periodic `WorkManager` check that the service is alive is worth
the complexity. Not a blocker; record the decision either way.

**Decided: kept.** The thing a restarted service loses is the MediaProjection token, which no
keepalive can restore, so both alternatives produce a service that looks alive and cannot capture.
The recovery path is the boot receiver, and the *detection* path already exists in the platform:
`device_connectivity_events` and the notification targets that watch them. Recorded at the
`return` in `MainService.kt`.

---

## Suggested order

A.1 and A.2 first and together: neither is testable without the other, and until both land no
real device has ever run any of this. Then A.3 and A.4, which are small and make the technician
path work. Then Phase B in order, since B.1 and B.2 are one change on two sides of the wire and
everything else in B depends on them. Phase C is independent and can run in parallel with B.
Phase D closes with the spec, which should be written last because it is the record of the
decisions the earlier phases make.

## Verification this plan would accept

Not a test suite passing. The three things the previous plan listed as never verified are
exactly the three things every gap here lives in, so the exit criteria are behavioural:

1. An Android client, built by CI, installed with only a managed configuration, reaches `ACTIVE`
   in the portal with its serial in its name and no human touching the device.
2. That device is found in the portal by typing its serial, and found in a technician's client
   Devices list by the same name.
3. The technician connects to it unattended, with the customer not present, using the password
   the portal issued.
4. An administrator creates a manager account, that person signs in, is granted a support group,
   sees exactly that group's devices, and on removal both loses access and has the device
   passwords they could reach rotated.
5. An external system authenticates, looks a device up by serial, renames it, and the new name is
   what both the portal and the technician's client show.

## Where this leaves the alpha

Every item in A, B, C and D has landed, and **none of the five criteria above has been executed**,
because each needs something this repository cannot supply: a CI run, a real device, a real
Keycloak, a browser. That is not a gap in the work; it is the same three unverified bands the
previous plan closed on, and they are now the only thing between here and a deployable alpha.

What each criterion needs first, in the order they unblock each other:

1. **Run `odv-android.yml`.** Nothing about the Android client has ever been compiled. Expect the
   first run to fail — the `--verify` guard exists precisely to catch a build that lost its baked-in
   configuration, and a first-run failure there is the guard working. Criteria 1 to 3 all sit
   behind it.
2. **Import the realm and grant `manage-users`.** Criterion 4's first step answers 503 until the
   odv-api service account can reach the Keycloak admin API, and a realm created before this phase
   needs the grant applying by hand.
3. **Decide the two blanks in the deployment spec** — privileged-preinstall or device-owner, and
   which value carries the serial. Criterion 1 cannot be attempted without the first; criterion 2
   is about the second.

The failure modes to expect on a first real run are the ones the code now names rather than
hides: a device that enrols but cannot be captured (§2 of the spec), a device named from its
hostname because no serial reached it (§4), and a 503 from account creation (§Accounts). Each of
those is a sentence in a log or a response that says what to change, which is the difference
between this and the state the audit started in.
