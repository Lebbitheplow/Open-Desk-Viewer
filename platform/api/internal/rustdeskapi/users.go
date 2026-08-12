package rustdeskapi

import (
	"net/http"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
)

// UsersHandler handles /api/users
type UsersHandler struct {
	auth   *identity.AuthService
	access access.Resolver
}

// NewUsersHandler creates a new users handler
func NewUsersHandler(auth *identity.AuthService, accessResolver access.Resolver) *UsersHandler {
	return &UsersHandler{
		auth:   auth,
		access: accessResolver,
	}
}

// HandleUsers handles /api/users
func (h *UsersHandler) HandleUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	user, ok := httpx.GetUserFromContext(ctx)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	isAdmin, err := h.access.IsAdminOrManager(ctx, user.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to check user role")
		return
	}

	var users []identity.User
	if isAdmin {
		users, err = h.auth.ListAllUsers(ctx)
	} else {
		users, err = h.auth.ListUsersSharingSupportGroups(ctx, user.ID)
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to fetch users")
		return
	}

	usersResponse := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		usersResponse = append(usersResponse, map[string]interface{}{
			"id":               u.ID,
			"email":            u.Email,
			"display_name":     u.DisplayName,
			"active":           u.Active,
			"keycloak_subject": u.KeycloakSubject,
		})
	}

	httpx.WritePaginatedResponse(w, int64(len(usersResponse)), usersResponse)
}
