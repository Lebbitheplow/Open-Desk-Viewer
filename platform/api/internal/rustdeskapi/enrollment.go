package rustdeskapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/enrollment"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
)

// EnrollmentHandler handles enrollment token endpoints
type EnrollmentHandler struct {
	db         *postgres.Pool
	access     access.Resolver
	enrollment *enrollment.Service
	fleet      *fleet.Service
}

// NewEnrollmentHandler creates a new enrollment handler
func NewEnrollmentHandler(db *postgres.Pool, accessResolver access.Resolver, enrollmentService *enrollment.Service, fleetService *fleet.Service) *EnrollmentHandler {
	return &EnrollmentHandler{
		db:         db,
		access:     accessResolver,
		enrollment: enrollmentService,
		fleet:      fleetService,
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
	tokens, err := h.enrollment.ListTokens(r.Context(), uuid.Nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to list tokens")
		return
	}

	var response []map[string]interface{}
	for _, t := range tokens {
		response = append(response, map[string]interface{}{
			"id":           t.ID.String(),
			"customer_id":  t.CustomerID.String(),
			"location_id":  func() *string { if t.LocationID != nil { s := t.LocationID.String(); return &s }; return nil }(),
			"device_group": func() *string { if t.DeviceGroupID != nil { s := t.DeviceGroupID.String(); return &s }; return nil }(),
			"device_group_name": t.DeviceGroupName,
			"address_book_name": t.AddressBookName,
			"max_uses":      t.MaxUses,
			"uses":          t.Uses,
			"expires_at":    t.ExpiresAt,
			"created_by":    t.CreatedBy,
			"created_at":    t.CreatedAt,
			"revoked_at":    t.RevokedAt,
		})
	}

	current, pageSize, err := httpx.ParsePagination(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}

	httpx.WritePaginatedResponsePage(w, current, pageSize, int64(len(response)), response)
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

	var maxUses int
	if req.MaxUses != nil {
		maxUses = *req.MaxUses
	} else {
		maxUses = 0 // unlimited
	}

	var expiresAt time.Time
	if req.ExpiresAt != "" {
		expiresAt, err = time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid expires_at format")
			return
		}
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":           token.ID.String(),
		"token":        token.Token,
		"customer_id":  token.CustomerID.String(),
		"max_uses":     token.MaxUses,
		"expires_at":   token.ExpiresAt,
	})
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

	w.WriteHeader(http.StatusNoContent)
}
