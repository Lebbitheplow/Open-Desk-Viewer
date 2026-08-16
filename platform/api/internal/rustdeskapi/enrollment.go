package rustdeskapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/enrollment"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
)

// EnrollmentHandler handles enrollment token endpoints
type EnrollmentHandler struct {
	db            *postgres.Pool
	access        access.Resolver
	enrollment    *enrollment.Service
	fleet         *fleet.Service
	auditRecorder audit.Recorder
}

// NewEnrollmentHandler creates a new enrollment handler
func NewEnrollmentHandler(db *postgres.Pool, accessResolver access.Resolver, enrollmentService *enrollment.Service, fleetService *fleet.Service, auditRecorder audit.Recorder) *EnrollmentHandler {
	return &EnrollmentHandler{
		db:            db,
		access:        accessResolver,
		enrollment:    enrollmentService,
		fleet:         fleetService,
		auditRecorder: auditRecorder,
	}
}

// HandleEnrollmentTokens handles GET and POST /api/enrollment-tokens
func (h *EnrollmentHandler) HandleEnrollmentTokens(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user, ok := httpx.GetUserFromContext(ctx)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	isAdmin, err := h.access.IsAdminOrManager(ctx, user.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to check admin status")
		return
	}

	// Only admins can manage enrollment tokens
	if !isAdmin {
		httpx.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	if r.Method == http.MethodGet {
		h.listTokens(w, r, user.ID)
		return
	}

	if r.Method == http.MethodPost {
		h.issueToken(w, r)
		return
	}

	httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// HandleEnrollmentToken handles DELETE /api/enrollment-tokens/{id}
func (h *EnrollmentHandler) HandleEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user, ok := httpx.GetUserFromContext(ctx)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	isAdmin, err := h.access.IsAdminOrManager(ctx, user.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to check admin status")
		return
	}

	// Only admins can manage enrollment tokens
	if !isAdmin {
		httpx.WriteError(w, http.StatusForbidden, "admin access required")
		return
	}

	if r.Method == http.MethodDelete {
		h.revokeToken(w, r)
		return
	}

	httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *EnrollmentHandler) listTokens(w http.ResponseWriter, r *http.Request, userID int64) {
	current, pageSize, err := httpx.ParsePagination(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}

	// Optional filter. Absent means every token, which is what the previous
	// uuid.Nil argument was trying to express while actually matching nothing.
	var customerFilter *uuid.UUID
	if raw := r.URL.Query().Get("customer_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid customer_id")
			return
		}
		customerFilter = &parsed
	}

	tokens, total, err := h.enrollment.ListTokens(r.Context(), customerFilter, (current-1)*pageSize, pageSize)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to list tokens")
		return
	}

	response := make([]map[string]interface{}, 0, len(tokens))
	for _, t := range tokens {
		response = append(response, map[string]interface{}{
			"id":          t.ID.String(),
			"prefix":      t.Prefix,
			"customer_id": t.CustomerID.String(),
			"location_id": uuidString(t.LocationID),
			// The portal shows the group a redeeming device joins, so name the
			// field for what it holds rather than for the resource.
			"device_group_id":   uuidString(t.DeviceGroupID),
			"device_group_name": t.DeviceGroupName,
			"address_book_name": t.AddressBookName,
			"max_uses":          t.MaxUses,
			"uses":              t.Uses,
			"expires_at":        t.ExpiresAt,
			"created_by":        t.CreatedBy,
			"created_at":        t.CreatedAt,
			"revoked_at":        t.RevokedAt,
		})
	}

	httpx.WritePaginatedResponsePage(w, current, pageSize, total, response)
}

func uuidString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func (h *EnrollmentHandler) issueToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomerID      string  `json:"customer_id"`
		LocationID      *string `json:"location_id"`
		DeviceGroupID   *string `json:"device_group_id"`
		DeviceGroupName string  `json:"device_group_name"`
		AddressBookName string  `json:"address_book_name"`
		MaxUses         *int    `json:"max_uses"`
		ExpiresAt       string  `json:"expires_at"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	customerUUID, err := uuid.Parse(req.CustomerID)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid customer_id")
		return
	}

	var locationUUID *uuid.UUID
	if req.LocationID != nil {
		uid, err := uuid.Parse(*req.LocationID)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid location_id")
			return
		}
		locationUUID = &uid
	}

	var deviceGroupUUID *uuid.UUID
	if req.DeviceGroupID != nil {
		uid, err := uuid.Parse(*req.DeviceGroupID)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid device_group_id")
			return
		}
		deviceGroupUUID = &uid
	}

	// nil means unlimited, and nil means no expiry. Both used to be flattened
	// into Go's zero value, which the redemption query then read as "no uses
	// left" and "expired in year 1": a token created without these fields could
	// never be redeemed. A caller that wants a bounded token still says so.
	maxUses := req.MaxUses
	if maxUses != nil && *maxUses < 1 {
		httpx.WriteError(w, http.StatusBadRequest, "max_uses must be at least 1, or omitted for unlimited")
		return
	}

	var expiresAt *time.Time
	if req.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid expires_at format")
			return
		}
		if !parsed.After(time.Now()) {
			httpx.WriteError(w, http.StatusBadRequest, "expires_at is in the past")
			return
		}
		expiresAt = &parsed
	}

	userID, ok := httpx.GetUserIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusInternalServerError, "user ID not found in context")
		return
	}

	token, err := h.enrollment.GenerateToken(
		r.Context(),
		customerUUID,
		locationUUID,
		deviceGroupUUID,
		req.DeviceGroupName,
		req.AddressBookName,
		maxUses,
		expiresAt,
		userID,
	)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	// An enrollment token is a credential that can add a device to the fleet,
	// so issuing one is a change worth recording. Neither issuance nor
	// revocation was audited before.
	h.auditRecorder.Record(r.Context(), audit.Event{
		Type:        "enrollment_token.issued",
		ActorID:     userID,
		Resource:    "enrollment_token",
		ResourceID:  token.ID.String(),
		CustomerID:  &customerUUID,
		Description: "Issued an enrollment token",
		Metadata: map[string]any{
			"prefix":     token.Prefix,
			"max_uses":   token.MaxUses,
			"expires_at": token.ExpiresAt,
		},
	})

	httpx.WriteJSON(w, http.StatusCreated, map[string]interface{}{
		"id":          token.ID.String(),
		"token":       token.Token,
		"prefix":      token.Prefix,
		"customer_id": token.CustomerID.String(),
		"max_uses":    token.MaxUses,
		"expires_at":  token.ExpiresAt,
	})
}

// HandleEnroll handles POST /api/enroll, the one endpoint a device calls before
// it has an identity.
//
// It is public because it has to be: the device has nothing to authenticate
// with yet. What it does have is an enrollment token, which is single-purpose,
// use-limited, expiring and revocable, and which an administrator issued
// deliberately. That is the trade, and it is the same one every device
// provisioning system makes.
func (h *EnrollmentHandler) HandleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Token string `json:"token"`
		// The device names itself the same way it does in a heartbeat.
		ID       string `json:"id"`
		UUID     string `json:"uuid"`
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Version  string `json:"version"`
		// The identifier a technician searches by. Optional: a desktop client
		// has no source for one.
		Serial string `json:"serial"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" || req.ID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "token and id are required")
		return
	}

	result, err := h.enrollment.Enroll(r.Context(), enrollment.EnrollRequest{
		Token:      req.Token,
		RustdeskID: req.ID,
		UUID:       req.UUID,
		Hostname:   req.Hostname,
		OS:         req.OS,
		Version:    req.Version,
		Serial:     strings.TrimSpace(req.Serial),
		ClientIP:   httpx.ClientIP(r),
	})
	switch {
	case errors.Is(err, enrollment.ErrInvalidToken):
		// One message for unknown, expired, exhausted and revoked. The caller
		// is unauthenticated, so anything more specific is a hint.
		httpx.WriteError(w, http.StatusUnauthorized, "invalid enrollment token")
		return
	case errors.Is(err, enrollment.ErrEnrollmentConflict):
		httpx.WriteError(w, http.StatusConflict, "this device id belongs to another customer")
		return
	case err != nil:
		log.Error().Err(err).Str("rustdesk_id", req.ID).Msg("enrollment failed")
		httpx.WriteError(w, http.StatusInternalServerError, "enrollment failed")
		return
	}

	// ActorID is zero: no user did this, a device did. audit.Record stores a
	// NULL actor for that case rather than a foreign key to nobody.
	h.auditRecorder.Record(r.Context(), audit.Event{
		Type:        "device.enrolled",
		Resource:    "device",
		ResourceID:  result.DeviceID.String(),
		DeviceID:    &result.DeviceID,
		CustomerID:  &result.CustomerID,
		Description: enrollmentDescription(result.Reenrolled),
		Metadata: map[string]any{
			"rustdesk_id": req.ID,
			"token_id":    result.TokenID.String(),
			"client_ip":   httpx.ClientIP(r),
			"reenrolled":  result.Reenrolled,
		},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"device_id":    result.DeviceID.String(),
		"name":         result.Name,
		"device_token": result.Secret,
	})
}

func enrollmentDescription(reenrolled bool) string {
	if reenrolled {
		return "Device re-enrolled, replacing its previous credential"
	}
	return "Device enrolled"
}

func (h *EnrollmentHandler) revokeToken(w http.ResponseWriter, r *http.Request) {
	tokenIDStr := r.PathValue("id")
	if tokenIDStr == "" {
		httpx.WriteError(w, http.StatusBadRequest, "token id required")
		return
	}

	tokenID, err := uuid.Parse(tokenIDStr)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid token id")
		return
	}

	if err := h.enrollment.RevokeToken(r.Context(), tokenID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to revoke token")
		return
	}

	actorID, _ := httpx.GetUserIDFromContext(r.Context())
	h.auditRecorder.Record(r.Context(), audit.Event{
		Type:        "enrollment_token.revoked",
		ActorID:     actorID,
		Resource:    "enrollment_token",
		ResourceID:  tokenID.String(),
		Description: "Revoked an enrollment token",
	})

	w.WriteHeader(http.StatusNoContent)
}
