use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
    time::Duration,
};

#[cfg(not(any(target_os = "ios")))]
use crate::{ui_interface::get_builtin_option, Connection};
use hbb_common::{
    config::{self, keys, Config, LocalConfig},
    log,
    tokio::{self, sync::broadcast, time::Instant},
};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

const TIME_HEARTBEAT: Duration = Duration::from_secs(15);
const UPLOAD_SYSINFO_TIMEOUT: Duration = Duration::from_secs(120);
const TIME_CONN: Duration = Duration::from_secs(3);

#[cfg(not(any(target_os = "ios")))]
lazy_static::lazy_static! {
    static ref SENDER : Mutex<broadcast::Sender<Vec<i32>>> = Mutex::new(start_hbbs_sync());
    static ref PRO: Arc<Mutex<bool>> = Default::default();
}

#[cfg(not(any(target_os = "ios")))]
pub fn start() {
    let _sender = SENDER.lock().unwrap();
}

#[cfg(not(target_os = "ios"))]
pub fn signal_receiver() -> broadcast::Receiver<Vec<i32>> {
    SENDER.lock().unwrap().subscribe()
}

#[cfg(not(any(target_os = "ios")))]
fn start_hbbs_sync() -> broadcast::Sender<Vec<i32>> {
    let (tx, _rx) = broadcast::channel::<Vec<i32>>(16);
    std::thread::spawn(move || start_hbbs_sync_async());
    return tx;
}

#[derive(Debug, Serialize, Deserialize)]
pub struct StrategyOptions {
    #[serde(default, skip_serializing_if = "HashMap::is_empty")]
    pub config_options: HashMap<String, String>,
    #[serde(default, skip_serializing_if = "HashMap::is_empty")]
    pub extra: HashMap<String, String>,
}

struct InfoUploaded {
    uploaded: bool,
    url: String,
    last_uploaded: Option<Instant>,
    id: String,
    username: Option<String>,
}

impl Default for InfoUploaded {
    fn default() -> Self {
        Self {
            uploaded: false,
            url: "".to_owned(),
            last_uploaded: None,
            id: "".to_owned(),
            username: None,
        }
    }
}

impl InfoUploaded {
    fn uploaded(url: String, id: String, username: String) -> Self {
        Self {
            uploaded: true,
            url,
            last_uploaded: None,
            id,
            username: Some(username),
        }
    }
}

#[cfg(not(any(target_os = "ios")))]
#[tokio::main(flavor = "current_thread")]
async fn start_hbbs_sync_async() {
    let mut interval = crate::rustdesk_interval(tokio::time::interval_at(
        Instant::now() + TIME_CONN,
        TIME_CONN,
    ));
    let mut last_sent: Option<Instant> = None;
    let mut info_uploaded = InfoUploaded::default();
    let mut sysinfo_ver = "".to_owned();
    loop {
        tokio::select! {
            _ = interval.tick() => {
                let url = heartbeat_url();
                let id = Config::get_id();
                if url.is_empty() {
                    *PRO.lock().unwrap() = false;
                    continue;
                }
                if config::option2bool("stop-service", &Config::get_option("stop-service")) {
                    continue;
                }
                // OpenDeskViewer: without a device credential every request
                // below is refused, so redeem the enrollment token first. This
                // is a no-op once enrolled, and once the enrollment token has
                // been spent or was never provisioned.
                if Config::get_option(DEVICE_TOKEN_OPTION).is_empty() {
                    enroll_device(&url).await;
                }
                let conns = Connection::alive_conns();
                if info_uploaded.uploaded && (url != info_uploaded.url || id != info_uploaded.id) {
                    info_uploaded.uploaded = false;
                    *PRO.lock().unwrap() = false;
                }
                // For Windows:
                // We can't skip uploading sysinfo when the username is empty, because the username may
                // always be empty before login. We also need to upload the other sysinfo info.
                //
                // https://github.com/rustdesk/rustdesk/discussions/8031
                // We still need to check the username after uploading sysinfo, because
                // 1. The username may be empty when logining in, and it can be fetched after a while.
                //    In this case, we need to upload sysinfo again.
                // 2. The username may be changed after uploading sysinfo, and we need to upload sysinfo again.
                //
                // The Windows session will switch to the last user session before the restart,
                // so it may be able to get the username before login.
                // But strangely, sometimes we can get the username before login,
                // we may not be able to get the username before login after the next restart.
                let mut v = crate::get_sysinfo();
                let sys_username = v["username"].as_str().unwrap_or_default().to_string();
                // Though the username comparison is only necessary on Windows,
                // we still keep the comparison on other platforms for consistency.
                let need_upload = (!info_uploaded.uploaded || info_uploaded.username.as_ref() != Some(&sys_username)) &&
                    info_uploaded.last_uploaded.map(|x| x.elapsed() >= UPLOAD_SYSINFO_TIMEOUT).unwrap_or(true);
                if need_upload {
                    v["version"] = json!(crate::VERSION);
                    v["id"] = json!(id);
                    v["uuid"] = json!(crate::encode64(hbb_common::get_uuid()));
                    // Also sent at enrollment. Repeated here so a device that
                    // enrolled before it had a serial acquires one without
                    // having to re-enroll, which it never does on its own.
                    let serial = Config::get_option(SERIAL_OPTION);
                    if !serial.is_empty() {
                        v["serial"] = json!(serial);
                    }
                    let ab_name = Config::get_option(keys::OPTION_PRESET_ADDRESS_BOOK_NAME);
                    if !ab_name.is_empty() {
                        v[keys::OPTION_PRESET_ADDRESS_BOOK_NAME] = json!(ab_name);
                    }
                    let ab_tag = Config::get_option(keys::OPTION_PRESET_ADDRESS_BOOK_TAG);
                    if !ab_tag.is_empty() {
                        v[keys::OPTION_PRESET_ADDRESS_BOOK_TAG] = json!(ab_tag);
                    }
                    let ab_alias = Config::get_option(keys::OPTION_PRESET_ADDRESS_BOOK_ALIAS);
                    if !ab_alias.is_empty() {
                        v[keys::OPTION_PRESET_ADDRESS_BOOK_ALIAS] = json!(ab_alias);
                    }
                    let ab_password = Config::get_option(keys::OPTION_PRESET_ADDRESS_BOOK_PASSWORD);
                    if !ab_password.is_empty() {
                        v[keys::OPTION_PRESET_ADDRESS_BOOK_PASSWORD] = json!(ab_password);
                    }
                    let ab_note = Config::get_option(keys::OPTION_PRESET_ADDRESS_BOOK_NOTE);
                    if !ab_note.is_empty() {
                        v[keys::OPTION_PRESET_ADDRESS_BOOK_NOTE] = json!(ab_note);
                    }
                    let username = get_builtin_option(keys::OPTION_PRESET_USERNAME);
                    if !username.is_empty() {
                        v[keys::OPTION_PRESET_USERNAME] = json!(username);
                    }
                    let strategy_name = get_builtin_option(keys::OPTION_PRESET_STRATEGY_NAME);
                    if !strategy_name.is_empty() {
                        v[keys::OPTION_PRESET_STRATEGY_NAME] = json!(strategy_name);
                    }
                    let device_group_name = get_builtin_option(keys::OPTION_PRESET_DEVICE_GROUP_NAME);
                    if !device_group_name.is_empty() {
                        v[keys::OPTION_PRESET_DEVICE_GROUP_NAME] = json!(device_group_name);
                    }
                    let device_username = Config::get_option(keys::OPTION_PRESET_DEVICE_USERNAME);
                    if !device_username.is_empty() {
                        v["username"] = json!(device_username);
                    }
                    let device_name = Config::get_option(keys::OPTION_PRESET_DEVICE_NAME);
                    if !device_name.is_empty() {
                        v["hostname"] = json!(device_name);
                    }
                    let note = Config::get_option(keys::OPTION_PRESET_NOTE);
                    if !note.is_empty() {
                        v[keys::OPTION_PRESET_NOTE] = json!(note);
                    }
                    let v = v.to_string();
                    let mut hash = "".to_owned();
                    if crate::is_public(&url) {
                        use sha2::{Digest, Sha256};
                        let mut hasher = Sha256::new();
                        hasher.update(url.as_bytes());
                        hasher.update(&v.as_bytes());
                        let res = hasher.finalize();
                        hash = hbb_common::base64::encode(&res[..]);
                        let old_hash = config::Status::get("sysinfo_hash");
                        let ver = config::Status::get("sysinfo_ver"); // sysinfo_ver is the version of sysinfo on server's side
                        if hash == old_hash {
                            // When the api doesn't exist, Ok("") will be returned in test.
                            let samever = match crate::post_request(url.replace("heartbeat", "sysinfo_ver"), "".to_owned(), &device_auth_header()).await {
                                Ok(x)  => {
                                    sysinfo_ver = x.clone();
                                    *PRO.lock().unwrap() = true;
                                    x == ver
                                }
                                _ => {
                                    false // to make sure Pro can be assigned in below post for old
                                            // hbbs pro not supporting sysinfo_ver, use false for ensuring
                                }
                            };
                            if samever {
                                info_uploaded = InfoUploaded::uploaded(url.clone(), id.clone(), sys_username);
                                log::info!("sysinfo not changed, skip upload");
                                continue;
                            }
                        }
                    }
                    match crate::post_request(url.replace("heartbeat", "sysinfo"), v, &device_auth_header()).await {
                        Ok(x)  => {
                            if x == "SYSINFO_UPDATED" {
                                info_uploaded = InfoUploaded::uploaded(url.clone(), id.clone(), sys_username);
                                log::info!("sysinfo updated");
                                if !hash.is_empty() {
                                    config::Status::set("sysinfo_hash", hash);
                                    config::Status::set("sysinfo_ver", sysinfo_ver.clone());
                                }
                                *PRO.lock().unwrap() = true;
                            } else if x == "ID_NOT_FOUND" {
                                info_uploaded.last_uploaded = None; // next heartbeat will upload sysinfo again
                            } else {
                                info_uploaded.last_uploaded = Some(Instant::now());
                            }
                        }
                        _ => {
                            info_uploaded.last_uploaded = Some(Instant::now());
                        }
                    }
                }
                if conns.is_empty() && last_sent.map(|x| x.elapsed() < TIME_HEARTBEAT).unwrap_or(false) {
                    continue;
                }
                last_sent = Some(Instant::now());
                let mut v = Value::default();
                v["id"] = json!(id);
                v["uuid"] = json!(crate::encode64(hbb_common::get_uuid()));
                v["ver"] = json!(hbb_common::get_version_number(crate::VERSION));
                if !conns.is_empty() {
                    v["conns"] = json!(conns);
                }
                let modified_at = LocalConfig::get_option("strategy_timestamp").parse::<i64>().unwrap_or(0);
                v["modified_at"] = json!(modified_at);
                // OpenDeskViewer: the version of the platform-managed connection
                // password this device has applied. The server sends a password
                // only while this disagrees with its own, so echoing it is both
                // the request for one and the acknowledgement of the last.
                let password_version = applied_password_version();
                v["password_version"] = json!(password_version);
                if let Ok(s) = crate::post_request(url.clone(), v.to_string(), &device_auth_header()).await {
                    if let Ok(mut rsp) = serde_json::from_str::<HashMap::<&str, Value>>(&s) {
                        if rsp.remove("sysinfo").is_some() {
                            info_uploaded.uploaded = false;
                            config::Status::set("sysinfo_hash", "".to_owned());
                            log::info!("sysinfo required to forcely update");
                        }
                        if let Some(conns)  = rsp.remove("disconnect") {
                                if let Ok(conns) = serde_json::from_value::<Vec<i32>>(conns) {
                                    SENDER.lock().unwrap().send(conns).ok();
                                }
                        }
                        if let Some(rsp_modified_at) = rsp.remove("modified_at") {
                            if let Ok(rsp_modified_at) = serde_json::from_value::<i64>(rsp_modified_at) {
                                if rsp_modified_at != modified_at {
                                    LocalConfig::set_option("strategy_timestamp".to_string(), rsp_modified_at.to_string());
                                }
                            }
                        }
                        if let Some(strategy) = rsp.remove("strategy") {
                            if let Ok(strategy) = serde_json::from_value::<StrategyOptions>(strategy) {
                                log::info!("strategy updated");
                                handle_config_options(strategy.config_options);
                            }
                        }
                        if let Some(password) = rsp.remove("device_password") {
                            apply_device_password(password);
                        }
                    }
                }
            }
        }
    }
}

// OpenDeskViewer: device identity.
//
// Stock RustDesk reports to the API with no credential at all: the heartbeat
// carries a rustdesk id, and the server is expected to believe it. That is why
// this fork's server used to register any id that reported in, and why anyone
// could forge liveness or squat an id before the real device arrived.
//
// A device now redeems an enrollment token once, at first contact, and keeps
// the secret it gets back. Every later heartbeat and sysinfo carries that
// secret in a header. The enrollment token is provisioned at build time or by
// the installer, alongside the server address and key.
const DEVICE_TOKEN_OPTION: &str = "device-token";
const ENROLLMENT_TOKEN_OPTION: &str = "enrollment-token";

// The identifier a technician searches on. Written during provisioning: on
// Android from managed configuration, the hardware serial or ANDROID_ID, in
// that order of preference (flutter/lib/main.dart); elsewhere it is whatever
// the installer set, and empty is allowed.
//
// The client only reports it. What the platform does with it, including naming
// the device after it, is the server's decision.
const SERIAL_OPTION: &str = "odv-serial";

// The header form post_request expects is "Name: Value" (src/common.rs:1475).
// An empty string means no header, which is what an unenrolled device sends and
// what the server answers 401 to.
fn device_auth_header() -> String {
    let token = Config::get_option(DEVICE_TOKEN_OPTION);
    if token.is_empty() {
        return "".to_owned();
    }
    format!("X-Device-Token: {}", token)
}

// Redeem the enrollment token, once, and keep the secret.
//
// Deliberately quiet about failure: a device that cannot enroll keeps
// heartbeating and keeps being refused, which is visible on the server as an
// observation. Retrying every heartbeat is correct, because the reason for
// failure is usually that the server is not reachable yet.
async fn enroll_device(heartbeat_url: &str) {
    let enrollment_token = Config::get_option(ENROLLMENT_TOKEN_OPTION);
    if enrollment_token.is_empty() {
        return;
    }

    // hostname, os and serial are what the platform names the device from. Sent
    // at enrollment rather than left to the first sysinfo upload, because the
    // name is decided when the row is created and a device that arrives as its
    // own nine-digit id is one a technician cannot find.
    let sysinfo = crate::get_sysinfo();
    let body = serde_json::json!({
        "token": enrollment_token,
        "id": Config::get_id(),
        "uuid": crate::encode64(hbb_common::get_uuid()),
        "version": crate::VERSION,
        "hostname": sysinfo["hostname"].as_str().unwrap_or_default(),
        "os": sysinfo["os"].as_str().unwrap_or_default(),
        "serial": Config::get_option(SERIAL_OPTION),
    });

    let url = heartbeat_url.replace("heartbeat", "enroll");
    match crate::post_request(url, body.to_string(), "").await {
        Ok(response) => {
            let token = serde_json::from_str::<Value>(&response)
                .ok()
                .and_then(|v| v["device_token"].as_str().map(|s| s.to_owned()))
                .unwrap_or_default();
            if token.is_empty() {
                log::error!("enrollment response carried no device token");
                return;
            }
            Config::set_option(DEVICE_TOKEN_OPTION.to_owned(), token);
            // The enrollment token is spent. Clearing it means a stolen device
            // image cannot be used to enroll a second machine, and that the
            // token is not sitting in the config of every deployed device.
            Config::set_option(ENROLLMENT_TOKEN_OPTION.to_owned(), "".to_owned());
            log::info!("device enrolled");
        }
        Err(err) => {
            log::error!("device enrollment failed: {}", err);
        }
    }
}

// OpenDeskViewer: the platform-managed connection password.
//
// Stock RustDesk generates its own permanent password and keeps it. That is
// right for a personal installation and wrong for a managed fleet: nobody
// central knows it, so nobody central can take it away, and "this technician no
// longer has access" is a statement about a web page rather than about the
// machine.
//
// The password now comes from the platform over the heartbeat. The device
// records which version it has applied and echoes it on every heartbeat; the
// server sends a password only while the two disagree, so this costs one field
// per poll and writes to permanent password storage only when something has
// actually changed.
//
// Note that this cannot go through the strategy channel, which writes the
// options map. The permanent password is separate storage with its own hashing
// and encryption (hbb_common's Config::set_permanent_password), and putting a
// password into the options map would store something the device never checks.
const PASSWORD_VERSION_OPTION: &str = "odv-password-version";

#[cfg(not(any(target_os = "ios")))]
fn applied_password_version() -> i64 {
    LocalConfig::get_option(PASSWORD_VERSION_OPTION)
        .parse::<i64>()
        .unwrap_or(0)
}

#[cfg(not(any(target_os = "ios")))]
fn apply_device_password(password: Value) {
    let value = password["value"].as_str().unwrap_or_default();
    let Some(version) = password["version"].as_i64() else {
        log::error!("device password arrived without a version");
        return;
    };
    if value.is_empty() {
        log::error!("device password arrived empty; keeping the current one");
        return;
    }

    // The version is recorded only after the write succeeds. set_permanent_password
    // returns false when the deployment has disabled password changes or when
    // the value cannot be prepared for storage, and recording the version anyway
    // would tell the server the rotation had landed when the machine is still
    // accepting the old password.
    if !Config::set_permanent_password(value) {
        log::error!("failed to apply the device password sent by the platform");
        return;
    }

    LocalConfig::set_option(PASSWORD_VERSION_OPTION.to_string(), version.to_string());
    log::info!("device password updated to version {}", version);
}

fn heartbeat_url() -> String {
    let url = crate::common::get_api_server(
        Config::get_option("api-server"),
        Config::get_option("custom-rendezvous-server"),
    );
    if url.is_empty() || crate::is_public(&url) {
        return "".to_owned();
    }
    format!("{}/api/heartbeat", url)
}

fn handle_config_options(config_options: HashMap<String, String>) {
    let mut options = Config::get_options();
    let default_settings = config::DEFAULT_SETTINGS.read().unwrap().clone();
    config_options
        .iter()
        .map(|(k, v)| {
            // Priority: user config > default advanced options.
            // Only when default advanced options are also empty, remove user option (fallback to built-in default);
            // otherwise insert an empty value so user config remains present.
            if v.is_empty() && default_settings.get(k).map_or("", |v| v).is_empty() {
                options.remove(k);
            } else {
                options.insert(k.to_string(), v.to_string());
            }
        })
        .count();
    Config::set_options(options);
}

#[allow(unused)]
#[cfg(not(any(target_os = "ios")))]
pub fn is_pro() -> bool {
    PRO.lock().unwrap().clone()
}

// Fire-and-forget by design: the switch flow must not block on this POST.
// If the device clock is outside the server's accepted window, the server
// returns its current Unix time and this task re-signs and retries once.
#[cfg(feature = "flutter")]
#[cfg(not(any(target_os = "android", target_os = "ios")))]
pub fn register_switch_grant(switch_uuid: String) {
    tokio::spawn(async move {
        let api_server = crate::ui_interface::get_api_server();
        if api_server.is_empty() || crate::is_public(&api_server) {
            return;
        }
        use hbb_common::sodiumoxide::crypto::{hash::sha256, sign};
        let switch_code = crate::encode64(sha256::hash(switch_uuid.as_bytes()).0);
        let switch_code_verifier = switch_code_verifier(&switch_code);
        let timestamp = (hbb_common::get_time() / 1000).to_string();
        let id = Config::get_id();
        let kp = Config::get_key_pair();
        let Some(sk) = sign::SecretKey::from_slice(&kp.0) else {
            log::error!("Failed to register switch grant: no device key");
            return;
        };
        let url = format!("{}/api/switch-grant", api_server);
        let mut timestamp = timestamp;
        for attempt in 0..2 {
            let signature = sign::sign_detached(
                &switch_grant_signed_msg(&id, &switch_code_verifier, &timestamp),
                &sk,
            );
            let body = json!({
                "id": &id,
                "switch_code_verifier": &switch_code_verifier,
                "timestamp": &timestamp,
                "signature": crate::encode64(signature.to_bytes()),
            })
            .to_string();
            let response = match crate::post_request(url.clone(), body, "").await {
                Ok(response) => response,
                Err(e) => {
                    log::error!("Failed to register switch grant: {}", e);
                    return;
                }
            };
            let response = match serde_json::from_str::<Value>(&response) {
                Ok(response) => response,
                Err(e) => {
                    log::error!("Failed to register switch grant: invalid response: {}", e);
                    return;
                }
            };
            match response.get("accepted").and_then(Value::as_bool) {
                Some(true) => return,
                Some(false) => {}
                None => {
                    log::error!("Failed to register switch grant: missing accepted response");
                    return;
                }
            }
            let Some(server_time) = response["server_time"].as_i64() else {
                log::error!("Failed to register switch grant: rejected by server");
                return;
            };
            if attempt == 0 {
                log::warn!("Switch grant timestamp rejected, retrying with server time");
                timestamp = server_time.to_string();
            } else {
                log::error!("Failed to register switch grant after retrying with server time");
            }
        }
    });
}

#[cfg(feature = "flutter")]
#[cfg(not(any(target_os = "android", target_os = "ios")))]
fn switch_code_verifier(switch_code: &str) -> String {
    use hbb_common::sodiumoxide::crypto::hash::sha256;

    let prefix = b"switch-grant-verifier\0";
    let mut msg = Vec::with_capacity(prefix.len() + switch_code.len());
    msg.extend_from_slice(prefix);
    msg.extend_from_slice(switch_code.as_bytes());
    crate::encode64(sha256::hash(&msg).0)
}

#[cfg(feature = "flutter")]
#[cfg(not(any(target_os = "android", target_os = "ios")))]
fn switch_grant_signed_msg(id: &str, switch_code_verifier: &str, timestamp: &str) -> Vec<u8> {
    let mut msg =
        Vec::with_capacity(13 + id.len() + 1 + switch_code_verifier.len() + 1 + timestamp.len());
    msg.extend_from_slice(b"switch-grant\0");
    msg.extend_from_slice(id.as_bytes());
    msg.push(0);
    msg.extend_from_slice(switch_code_verifier.as_bytes());
    msg.push(0);
    msg.extend_from_slice(timestamp.as_bytes());
    msg
}

#[cfg(all(
    test,
    feature = "flutter",
    not(any(target_os = "android", target_os = "ios"))
))]
mod tests {
    use super::{switch_code_verifier, switch_grant_signed_msg};

    #[test]
    fn test_switch_code_verifier_is_not_raw_switch_code() {
        let switch_code = "code-abc";
        let verifier = switch_code_verifier(switch_code);
        assert_ne!(verifier, switch_code);
        assert_eq!(verifier, switch_code_verifier(switch_code));
        assert_eq!(
            verifier,
            "dMIn3uiPe77XodFB5IKi7PrKJ7l7+zVquNn0ObSaHQc="
        );
    }

    #[test]
    fn test_switch_grant_signed_msg_layout() {
        let expected: Vec<u8> = [
            &b"switch-grant\0"[..],
            b"id1",
            b"\0",
            b"c1",
            b"\0",
            b"1700000000",
        ]
        .concat();
        assert_eq!(switch_grant_signed_msg("id1", "c1", "1700000000"), expected);
    }
}
