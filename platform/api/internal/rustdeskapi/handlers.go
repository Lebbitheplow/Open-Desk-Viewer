package rustdeskapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/deviceauth"
	"github.com/OpenDeskViewer/platform/api/internal/devicepw"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/OpenDeskViewer/platform/api/internal/telemetry"
	"github.com/rs/zerolog/log"
)

// Handlers handles RustDesk Pro API endpoints
type Handlers struct {
	authService      *identity.AuthService
	telemetryService *telemetry.Service
	auditRecorder    audit.Recorder
	deviceAuth       *deviceauth.Service
	devicePasswords  *devicepw.Service
}

// NewHandlers creates new handlers
func NewHandlers(auth *identity.AuthService, telem *telemetry.Service, auditRecorder audit.Recorder, deviceAuth *deviceauth.Service, devicePasswords *devicepw.Service) *Handlers {
	return &Handlers{
		authService:      auth,
		telemetryService: telem,
		auditRecorder:    auditRecorder,
		deviceAuth:       deviceAuth,
		devicePasswords:  devicePasswords,
	}
}

// ssoLoginOption is what the RustDesk client is told it may sign in with.
//
// The shape is the client's, not ours: UserModel.queryOidcLoginOptions
// (flutter/lib/models/user_model.dart) keeps only entries prefixed
// "common-oidc/" -- whose remainder is a JSON array of providers -- or "oidc/".
// Anything else is dropped.
//
// This used to be the single string "common", which matches neither prefix, so
// every option was discarded and the sign-in dialog rendered no provider button
// at all. Together with password sign-in refusing everybody for want of a way to
// set a password, that left the client with no working way in whatsoever, which
// is the whole of R7's client half. The name is what the button says
// ("Continue with SSO"); the flow itself ignores it, because this deployment has
// exactly one provider.
var ssoLoginOption = `common-oidc/[{"name":"SSO"}]`

// HandleLoginOptions returns available login options
func (h *Handlers) HandleLoginOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode([]string{ssoLoginOption})
}

// HandleCurrentUser returns current user info
func (h *Handlers) HandleCurrentUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user, ok := httpx.GetUserFromContext(ctx)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	isAdmin := false
	for _, role := range user.Roles {
		if role.Name == "Administrator" || role.Name == "Support Manager" {
			isAdmin = true
			break
		}
	}

	status := 1
	if !user.Active {
		status = 0
	}

	resp := map[string]interface{}{
		"name":         user.Email,
		"display_name": user.DisplayName,
		"avatar":       "",
		"email":        user.Email,
		"note":         "",
		"verifier":     "",
		"status":       status,
		"is_admin":     isAdmin,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// heartbeatRequest represents the JSON body of a heartbeat.
//
// Token is the fork's addition: the device's enrollment secret. The stock
// client sends no credential at all, which is precisely the problem, and
// src/hbbs_http/sync.rs carries the matching change. Accepting it in the body
// as well as in a header means provisioning can be exercised with curl and does
// not depend on which of the two the client build sends.
// PasswordVersion is the version of the platform-managed connection password
// the device has applied, and is the fork's second addition. It works the same
// way ModifiedAt does for the strategy: the device echoes what it has, and the
// server sends a new password only when the two differ. The echo is also the
// acknowledgement, which is what lets the portal say whether a rotation has
// actually reached the machine.
type heartbeatRequest struct {
	ID   string `json:"id"`
	UUID string `json:"uuid"`
	// Ver is the client's numeric version: hbb_common::get_version_number
	// returns an i64, so 1.4.4 arrives as 1004004. It is recorded nowhere and
	// is kept only so the shape of the request is visible here.
	//
	// It must not be typed as a string. encoding/json fails the whole decode on
	// the mismatch, so every heartbeat in the fleet was answered with 400
	// "invalid JSON body" and every device stayed enrolled but never live: no
	// liveness, no connectivity, and no managed password, because that is
	// issued on the first heartbeat that succeeds.
	Ver             int64 `json:"ver"`
	Conns           []int  `json:"conns"`
	ModifiedAt      int64  `json:"modified_at"`
	Token           string `json:"device_token"`
	PasswordVersion int64  `json:"password_version"`
}

// deviceSecret pulls the device credential from wherever the client put it. The
// header is preferred: it keeps the secret out of a body that might be logged
// by something downstream.
func deviceSecret(r *http.Request, bodyToken string) string {
	if v := strings.TrimSpace(r.Header.Get("X-Device-Token")); v != "" {
		return v
	}
	if v := r.Header.Get("Authorization"); strings.HasPrefix(v, "Device ") {
		return strings.TrimSpace(strings.TrimPrefix(v, "Device "))
	}
	return strings.TrimSpace(bodyToken)
}

// HandleHeartbeat records liveness and answers with the control channel.
//
// The response is the only way the platform can reach a device between
// connections: "disconnect" carries connection ids the client terminates, and
// "strategy" carries configuration it applies. Both were hardcoded empty, so
// the platform could describe a revocation in its own database and do nothing
// about it on the machine.
func (h *Handlers) HandleHeartbeat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req heartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.ID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing device id")
		return
	}

	device, err := h.authenticateDevice(ctx, r, req.ID, req.UUID, deviceSecret(r, req.Token))
	if err != nil {
		writeDeviceUnauthorized(w, err)
		return
	}

	if err := h.telemetryService.ProcessHeartbeat(ctx, device.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to process heartbeat")
		return
	}

	disconnect, err := h.telemetryService.TakePendingDisconnects(ctx, device.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to read pending disconnects")
		return
	}

	resp := map[string]any{
		"disconnect": disconnect,
	}

	// The client echoes the modified_at it last applied. Sending the strategy
	// only when that differs is what keeps a 15-second heartbeat from
	// rewriting the client's configuration every 15 seconds.
	strategy, err := h.telemetryService.GetStrategy(ctx, device.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to read strategy")
		return
	}
	if strategy != nil {
		resp["modified_at"] = strategy.ModifiedAt
		if strategy.ModifiedAt != req.ModifiedAt {
			resp["strategy"] = map[string]any{"config_options": strategy.ConfigOptions}
		}
	}

	// The connection password. Sent only while the device's applied version
	// disagrees with ours, so a 15-second heartbeat does not rewrite the
	// device's password every 15 seconds, and a rotation keeps being offered
	// until the device confirms it rather than being delivered once into a
	// dropped connection.
	if h.devicePasswords != nil {
		password, err := h.devicePasswords.Pending(ctx, device.ID, req.PasswordVersion)
		if err != nil {
			// Deliberately not fatal to the heartbeat. Liveness and pending
			// disconnects are the parts an operator depends on during an
			// incident, and losing them because the password store is unhappy
			// would turn a degraded feature into a blind fleet.
			log.Error().Err(err).
				Str("device_id", device.ID.String()).
				Msg("failed to resolve the device connection password")
		} else if password != nil {
			resp["device_password"] = map[string]any{
				"value":   password.Value,
				"version": password.Version,
			}
		}
	}

	httpx.WriteJSON(w, http.StatusOK, resp)
}

// sysinfoRequest represents the JSON body of a sysinfo post
type sysinfoRequest struct {
	ID       string `json:"id"`
	UUID     string `json:"uuid"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
	OS       string `json:"os"`
	// openapi.yaml has documented this field since the initial spec and nothing
	// read it. It is how a device enrolled before serials existed acquires one.
	Serial string `json:"serial"`
	Token  string `json:"device_token"`
}

// HandleSysinfo handles device sysinfo
func (h *Handlers) HandleSysinfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req sysinfoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.ID == "" || req.UUID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing required fields")
		return
	}

	device, err := h.authenticateDevice(ctx, r, req.ID, req.UUID, deviceSecret(r, req.Token))
	if err != nil {
		writeDeviceUnauthorized(w, err)
		return
	}

	if err := h.telemetryService.ProcessSysinfo(ctx, device.ID, req.Hostname, req.Version, req.OS, req.Serial); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to process sysinfo")
		return
	}

	// The client treats this exact string as "stored", and anything else as a
	// reason to upload again on the next heartbeat (sync.rs:216-224).
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("SYSINFO_UPDATED"))
}

// authenticateDevice verifies the device secret and checks that the id the
// device claims is the one the credential belongs to.
//
// An unauthenticated or mismatched caller is recorded as an observation, then
// refused. Recording first is deliberate: the sighting is the only trace an
// operator gets of a device that was shipped but never enrolled, and it is what
// 1.5 asked for in place of registering the id.
func (h *Handlers) authenticateDevice(ctx context.Context, r *http.Request, claimedID, deviceUUID, secret string) (*deviceauth.Device, error) {
	device, err := h.deviceAuth.Authenticate(ctx, secret)
	if err != nil {
		if errors.Is(err, deviceauth.ErrUnauthorized) {
			h.observe(ctx, r, claimedID, deviceUUID)
			return nil, deviceauth.ErrUnauthorized
		}
		return nil, err
	}

	// A valid secret presented with somebody else's id. The credential decides
	// who this is, so the request is refused rather than silently attributed to
	// the credential's device.
	if device.RustdeskID != claimedID {
		log.Warn().
			Str("claimed_id", claimedID).
			Str("credential_id", device.RustdeskID).
			Msg("device presented a credential belonging to another id")
		h.observe(ctx, r, claimedID, deviceUUID)
		return nil, deviceauth.ErrUnauthorized
	}

	return device, nil
}

func (h *Handlers) observe(ctx context.Context, r *http.Request, rustdeskID, deviceUUID string) {
	if err := h.telemetryService.RecordObservation(ctx, rustdeskID, deviceUUID, httpx.ClientIP(r)); err != nil {
		log.Error().Err(err).Str("rustdesk_id", rustdeskID).Msg("failed to record device observation")
	}
}

func writeDeviceUnauthorized(w http.ResponseWriter, err error) {
	if errors.Is(err, deviceauth.ErrUnauthorized) {
		httpx.WriteError(w, http.StatusUnauthorized, "device is not enrolled")
		return
	}
	httpx.WriteError(w, http.StatusInternalServerError, "failed to verify device")
}
