package rustdeskapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AuditHandler handles audit endpoints, and the deployment-script endpoints
// that share its dependencies.
type AuditHandler struct {
	db            *postgres.Pool
	access        access.Resolver
	auditRecorder audit.Recorder

	// The deployment's own coordinates, baked into the scripts /api/devices/deploy
	// and /api/devices/cli hand out.
	publicHost string
	publicKey  string
	apiServer  string
}

// NewAuditHandler creates a new audit handler.
func NewAuditHandler(db *postgres.Pool, accessResolver access.Resolver, auditRecorder audit.Recorder, cfg *config.Config) *AuditHandler {
	h := &AuditHandler{db: db, access: accessResolver, auditRecorder: auditRecorder}
	if cfg != nil {
		h.publicHost = cfg.PublicHost
		h.publicKey = cfg.RustdeskPublicKey
		// Set api-server explicitly. It takes precedence over the client's
		// derived http://<host>:21114 fallback, which is plain HTTP.
		h.apiServer = fmt.Sprintf("https://%s", cfg.PublicHost)
	}
	return h
}

// connectionStatuses is the connection_status enum, which Postgres will not
// coerce from an arbitrary string. An unrecognised value is a client error, not
// a 500 from a failed cast.
var connectionStatuses = map[string]bool{
	"REQUESTED": true,
	"STARTED":   true,
	"ENDED":     true,
	"FAILED":    true,
}

// auditConnRequest is the union of the two shapes that reach /api/audit/conn.
//
// The stock client sends id, uuid, conn_id, session_id, nonce and action
// (src/server/connection.rs:1507-1519); the portal and the deployment tooling
// send the explicit session fields. Nothing here is trusted for authorisation:
// the actor comes from the token and the device is looked up, not asserted.
type auditConnRequest struct {
	// The device. The client names it by its RustDesk id; the portal has our
	// uuid. Either is accepted, and both are resolved through the devices table.
	ID       string `json:"id"`
	DeviceID string `json:"device_id"`

	// Stock-client fields.
	Action    string `json:"action"`
	ConnID    int64  `json:"conn_id"`
	SessionID uint64 `json:"session_id"`
	IP        string `json:"ip"`

	// Portal fields.
	SupportGroup  string `json:"support_group_id"`
	ClientID      string `json:"client_id"`
	Protocol      string `json:"protocol"`
	StartTime     string `json:"start_time"`
	EndTime       string `json:"end_time"`
	Duration      int    `json:"duration_seconds"`
	ConnectedFrom string `json:"connected_from"`
	Status        string `json:"status"`
}

// HandleAuditConn handles POST /api/audit/conn.
//
// It used to insert device_id, user_id and support_group_id straight from the
// body, so any authenticated user could write a connection record naming any
// other user on any device. The actor is now the authenticated caller and the
// device has to be one the caller may reach, which makes the connection log
// evidence rather than a suggestion box.
func (h *AuditHandler) HandleAuditConn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req auditConnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	user, ok := httpx.GetUserFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	target := req.DeviceID
	if target == "" {
		target = req.ID
	}
	if target == "" {
		httpx.WriteError(w, http.StatusBadRequest, "device_id is required")
		return
	}

	deviceID, found, err := h.resolveDevice(r.Context(), target)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to resolve device")
		return
	}
	// An unknown device is a 403 rather than a 404: answering "no such device"
	// turns this endpoint into an oracle for which ids exist.
	if !found {
		httpx.WriteError(w, http.StatusForbidden, "not authorized for this device")
		return
	}

	canAccess, err := h.access.CanAccessDevice(r.Context(), user.ID, deviceID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to check device access")
		return
	}
	if !canAccess {
		httpx.WriteError(w, http.StatusForbidden, "not authorized for this device")
		return
	}

	status := "REQUESTED"
	switch {
	case req.Status != "":
		status = strings.ToUpper(req.Status)
	case strings.EqualFold(req.Action, "close"):
		status = "ENDED"
	case strings.EqualFold(req.Action, "new"):
		status = "REQUESTED"
	}
	if !connectionStatuses[status] {
		httpx.WriteError(w, http.StatusBadRequest, "invalid status")
		return
	}

	startTime, err := parseAuditTime(req.StartTime)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid start_time")
		return
	}
	endTime, err := parseAuditTime(req.EndTime)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid end_time")
		return
	}
	if endTime == nil && status == "ENDED" {
		now := time.Now().UTC()
		endTime = &now
	}

	supportGroupID, err := h.resolveSupportGroup(r.Context(), user.ID, req.SupportGroup)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid support_group_id")
		return
	}

	clientID := req.ClientID
	if clientID == "" && req.SessionID != 0 {
		// The client identifies a connection by session_id and conn_id, not by
		// anything we issue. Keeping the pair is what lets a later record be
		// tied back to this one.
		clientID = fmt.Sprintf("%d/%d", req.SessionID, req.ConnID)
	}

	connectedFrom := req.ConnectedFrom
	if connectedFrom == "" {
		connectedFrom = req.IP
	}

	var sessionID uuid.UUID
	err = h.db.QueryRow(r.Context(), `
		INSERT INTO connection_sessions (device_id, user_id, support_group_id, client_id,
		                                 protocol, start_time, end_time, duration_seconds,
		                                 connected_from, status)
		VALUES ($1, $2, $3, $4, $5, COALESCE($6, now()), $7, $8, $9, $10)
		RETURNING id
	`, deviceID, user.ID, supportGroupID, nullIfEmpty(clientID),
		nullIfEmpty(req.Protocol), startTime, endTime, nullIfZero(req.Duration),
		nullIfEmpty(connectedFrom), status).Scan(&sessionID)

	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to record audit event")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"guid": sessionID.String()})
}

// resolveDevice maps whatever identifier the caller used onto our device id.
// The RustDesk client knows a device by its rustdesk_id; the portal has the
// uuid. Accepting both keeps one code path, and neither is trusted past this
// lookup: the caller still has to pass CanAccessDevice.
func (h *AuditHandler) resolveDevice(ctx context.Context, ident string) (uuid.UUID, bool, error) {
	var deviceID uuid.UUID
	err := h.db.QueryRow(ctx, `SELECT id FROM devices WHERE rustdesk_id = $1`, ident).Scan(&deviceID)
	if err == nil {
		return deviceID, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, err
	}

	parsed, parseErr := uuid.Parse(ident)
	if parseErr != nil {
		return uuid.Nil, false, nil
	}
	err = h.db.QueryRow(ctx, `SELECT id FROM devices WHERE id = $1`, parsed).Scan(&deviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return deviceID, true, nil
}

// resolveSupportGroup validates a caller-supplied support group. A technician
// may only file a session against a group they belong to; anyone else's group
// id is a forgery attempt, not a hint. An empty value records no group rather
// than guessing one.
func (h *AuditHandler) resolveSupportGroup(ctx context.Context, userID int64, raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	groupID, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}

	isAdmin, err := h.access.IsAdminOrManager(ctx, userID)
	if err != nil {
		return nil, err
	}
	if isAdmin {
		return &groupID, nil
	}

	groups, err := h.access.GetUserSupportGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g == groupID {
			return &groupID, nil
		}
	}
	return nil, nil
}

// parseAuditTime accepts RFC 3339, which is what every caller in this
// repository emits. An empty value is absent, not an error; anything else that
// fails to parse is a bad request rather than a silent zero timestamp.
func parseAuditTime(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullIfZero(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// HandleAuditNote handles PUT /api/audit (note attached to a session)
func (h *AuditHandler) HandleAuditNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		GUID string `json:"guid"`
		Note string `json:"note"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Get user from context
	user, ok := httpx.GetUserFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusForbidden, "not authorized")
		return
	}

	// Parse GUID
	sessionID, err := uuid.Parse(req.GUID)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid session GUID")
		return
	}

	// Check if user can access this session (owner or admin/manager)
	canAccess, err := h.access.CanAccessSession(r.Context(), user.ID, sessionID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to check access")
		return
	}

	if !canAccess {
		httpx.WriteError(w, http.StatusForbidden, "not authorized to update this session")
		return
	}

	// Update the note
	result, err := h.db.Exec(r.Context(), `
		UPDATE connection_sessions
		SET note = COALESCE(NULLIF($1, ''), note)
		WHERE id = $2
	`, req.Note, sessionID)

	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to update audit note")
		return
	}

	affected := result.RowsAffected()
	if affected == 0 {
		httpx.WriteError(w, http.StatusNotFound, "session not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

// HandleDevicesDeploy handles POST /api/devices/deploy
func (h *AuditHandler) HandleDevicesDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Server identity alone does not let a client join the fleet: without an
	// enrollment token it heartbeats, is refused, and is only ever recorded as
	// an observation. The token is optional here because an operator may be
	// reconfiguring an already-enrolled machine.
	enrollmentToken, ok := deployEnrollmentToken(w, r)
	if !ok {
		return
	}

	// The placeholders below used to be Go template syntax in a plain string
	// literal, so the script was served with a literal {{.RendezvousServer}} in
	// it and could not work. Substitute the deployment's real values.
	script := fmt.Sprintf(`#!/bin/sh
# OpenDeskViewer deployment script.
#
# Configures a client to reach this deployment. Run as root on the target
# machine. Re-running is safe: --config overwrites the previous values.
set -eu

RENDEZVOUS_SERVER=%q
RS_PUB_KEY=%q
API_SERVER=%q
ENROLLMENT_TOKEN=%q

if [ -z "$RS_PUB_KEY" ]; then
    echo "This deployment has not published its public key." >&2
    echo "Set RUSTDESK_PUBLIC_KEY in the server's .env and try again." >&2
    exit 1
fi

if ! command -v rustdesk >/dev/null 2>&1; then
    echo "rustdesk is not installed on this machine." >&2
    echo "Install the client first, then re-run this script." >&2
    exit 1
fi

rustdesk --config "host=${RENDEZVOUS_SERVER},key=${RS_PUB_KEY},api=${API_SERVER}"

# The token is spent at first contact and cleared by the client, so this is
# safe to leave in a provisioning run. Without it the client never enrols and
# never appears in the portal.
if [ -n "$ENROLLMENT_TOKEN" ]; then
    rustdesk --option enrollment-token "${ENROLLMENT_TOKEN}"
    echo "Enrollment token provisioned."
else
    echo "No enrollment token supplied: this client will not join the fleet." >&2
fi

echo "Configured against ${RENDEZVOUS_SERVER}."
`, h.publicHost, h.publicKey, h.apiServer, enrollmentToken)

	w.Header().Set("Content-Type", "application/x-shellscript")
	w.Header().Set("Content-Disposition", `attachment; filename="opendeskviewer-deploy.sh"`)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(script))
}

// deployEnrollmentToken reads the optional enrollment token from the request.
//
// It is rejected rather than sanitised when it carries whitespace or control
// characters: the value is interpolated into a shell script, and a token that
// needs quoting is a token that was not issued by this server.
func deployEnrollmentToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		EnrollmentToken string `json:"enrollment_token"`
	}
	// An empty body is the reconfigure-only case and stays valid.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return "", false
	}
	token := strings.TrimSpace(req.EnrollmentToken)
	if token == "" {
		return "", true
	}
	if strings.ContainsFunc(token, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '"' || r == '\\'
	}) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid enrollment token")
		return "", false
	}
	return token, true
}

// HandleDevicesCLI handles POST /api/devices/cli
func (h *AuditHandler) HandleDevicesCLI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Previously this printed "Add installation commands here" and claimed
	// success. It now emits the one command that actually configures a client,
	// so an operator can paste it rather than run a script.
	// The filename route carries host and key but cannot carry api-server: a
	// Windows filename cannot contain the "://" of a URL. A client provisioned
	// that way falls back to deriving http://<host>:21114, which is why the
	// api-server line below is not optional and why :21114 is a redirect rather
	// than a service. See the Caddyfile.
	script := fmt.Sprintf(`#!/bin/sh
# OpenDeskViewer client configuration, as a single command.
#
# Requires an installed client and root.
#
# The Windows filename route (renaming the installer to
# rustdesk-host=%s,key=%s.exe) needs no rebuild and no elevation, but a
# filename cannot hold a URL, so it cannot set api-server. A client provisioned
# that way derives http://<host>:21114 and sends its first request in the
# clear. Follow it with the command below, which sets api-server explicitly.
set -eu

rustdesk --config "host=%s,key=%s,api=%s"
`, h.publicHost, h.publicKey, h.publicHost, h.publicKey, h.apiServer)

	w.Header().Set("Content-Type", "application/x-shellscript")
	w.Header().Set("Content-Disposition", `attachment; filename="opendeskviewer-cli.sh"`)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(script))
}

// HandleRecord serves POST /api/record: server-side session recording storage.
//
// **Deliberately 501, and item 6.8 asked for the feasibility question to be
// settled before anything was promised. It is settled, and the answer is not
// the one the plan expected.**
//
// The plan's concern was that the relay is unmodified upstream and never sees
// decrypted media, so recording would have to happen at an endpoint. That is
// correct, and it is already how RustDesk does it: `src/hbbs_http/record_upload.rs`
// records locally through `scrap::record` and streams the file here with
// `?type=new|append|finish|remove&file=<name>`, the body being raw bytes. So the
// client half exists, upstream, today. Recording is feasible.
//
// What is missing is not code but a place to put the files, and that is a
// deployment decision rather than a default somebody should pick on an
// operator's behalf:
//
//   - a recorded session is video, so a busy fleet is gigabytes a day. It does
//     not belong in Postgres, and this deployment has no object store;
//   - it needs a retention period, a per-deployment quota and a disk-full
//     behaviour, because the failure mode of getting those wrong is an API that
//     stops serving because a volume filled up;
//   - the recordings are the most sensitive thing this platform would ever hold.
//     Playback needs its own authorisation and its own audit trail, and both are
//     new surfaces rather than reuses of existing ones.
//
// Answering 501 is therefore the honest state: the endpoint is reachable, the
// client understands the error, and nothing pretends to be storing a recording
// it is discarding. Accepting uploads into a directory nobody had sized would be
// the worse failure, because an operator would believe sessions were recorded.
//
// **What does work today, and is the answer for most deployments:**
// `enable-record-session` is in apiv1's pushableOptions, so an administrator can
// turn on recording fleet-wide through PUT /api/v1/devices/{id}/strategy. The
// client then records locally on the machine being controlled. That gives the
// recording without the platform taking custody of the media.
func (h *AuditHandler) HandleRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Named so an operator reading a client log knows this is a configuration
	// state rather than a fault, and knows what to do instead.
	httpx.WriteError(w, http.StatusNotImplemented,
		"server-side session recording storage is not enabled on this deployment; "+
			"push enable-record-session through the device strategy to record on the device itself")
}

// HandleSwitchGrant handles POST /api/switch-grant
// It answers the policy question the client asks before swapping which side
// controls the session: may this user drive that peer? The answer is
// CanAccessDevice on the target, the same rule the address book and /api/peers
// use, so there is no second authorisation path to keep in step.
//
// This used to return {"accepted": true} unconditionally with a comment saying
// to validate it in production. That is worse than not implementing the
// endpoint at all: an unanswered request leaves the client deciding locally,
// whereas an unconditional yes is the server actively approving every switch,
// including ones onto devices the requester cannot otherwise see.
func (h *AuditHandler) HandleSwitchGrant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		ID     string `json:"id"`
		PeerID string `json:"peer_id"`
		UUID   string `json:"uuid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	user, ok := httpx.GetUserFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// The client names the peer by its RustDesk id, not our uuid.
	target := req.PeerID
	if target == "" {
		target = req.ID
	}
	if target == "" {
		httpx.WriteError(w, http.StatusBadRequest, "peer_id is required")
		return
	}

	var deviceID uuid.UUID
	err := h.db.QueryRow(r.Context(),
		`SELECT id FROM devices WHERE rustdesk_id = $1`, target).Scan(&deviceID)
	if err != nil {
		// An unknown peer is a denial, not an error: the client only needs to
		// know whether it may proceed.
		writeSwitchGrant(w, false)
		return
	}

	canAccess, err := h.access.CanAccessDevice(r.Context(), user.ID, deviceID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to check device access")
		return
	}

	h.auditRecorder.Record(r.Context(), audit.Event{
		Type:        "device.switch_grant",
		ActorID:     user.ID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		DeviceID:    &deviceID,
		Description: fmt.Sprintf("Control switch request %s", grantOutcome(canAccess)),
		Metadata:    map[string]any{"peer_id": target, "accepted": canAccess},
	})

	writeSwitchGrant(w, canAccess)
}

func grantOutcome(accepted bool) string {
	if accepted {
		return "approved"
	}
	return "denied"
}

func writeSwitchGrant(w http.ResponseWriter, accepted bool) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"accepted": accepted})
}

// HandlePluginSign handles POST /lic/web/api/plugin-sign
func (h *AuditHandler) HandlePluginSign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	httpx.WriteError(w, http.StatusNotImplemented, "plugin signing verification is not enabled")
}

// HandleAuditFile handles POST /api/audit/file.
//
// Same treatment as HandleAuditConn: the actor is the token's, the device is
// resolved and authorised, and the row goes through the audit recorder rather
// than a hand-written INSERT that pushed strings into a bigint and a uuid.
func (h *AuditHandler) HandleAuditFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		ID        string `json:"id"`
		DeviceID  string `json:"device_id"`
		FileName  string `json:"file_name"`
		Direction string `json:"direction"`
		FilePath  string `json:"file_path"`
		Size      int64  `json:"size"`
		Timestamp string `json:"timestamp"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	user, ok := httpx.GetUserFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	target := req.DeviceID
	if target == "" {
		target = req.ID
	}
	if target == "" {
		httpx.WriteError(w, http.StatusBadRequest, "device_id is required")
		return
	}

	deviceID, found, err := h.resolveDevice(r.Context(), target)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to resolve device")
		return
	}
	if !found {
		httpx.WriteError(w, http.StatusForbidden, "not authorized for this device")
		return
	}

	canAccess, err := h.access.CanAccessDevice(r.Context(), user.ID, deviceID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to check device access")
		return
	}
	if !canAccess {
		httpx.WriteError(w, http.StatusForbidden, "not authorized for this device")
		return
	}

	h.auditRecorder.Record(r.Context(), audit.Event{
		Type:        "file_transfer",
		ActorID:     user.ID,
		Resource:    "device",
		ResourceID:  deviceID.String(),
		DeviceID:    &deviceID,
		Description: fmt.Sprintf("File %s: %s", req.Direction, req.FileName),
		Metadata: map[string]any{
			"file_name": req.FileName,
			"direction": req.Direction,
			"file_path": req.FilePath,
			"size":      req.Size,
			"timestamp": req.Timestamp,
		},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
