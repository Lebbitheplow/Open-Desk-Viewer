package apiv1

import (
	"net/http"
	"strings"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/google/uuid"
)

// Customer is an organisation whose devices this deployment manages.
type Customer struct {
	ID          uuid.UUID `json:"id"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	ExternalID  *string   `json:"external_id,omitempty"`
	Description *string   `json:"description,omitempty"`
	DeviceCount int64     `json:"device_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Location is a site belonging to a customer.
type Location struct {
	ID         uuid.UUID `json:"id"`
	CustomerID uuid.UUID `json:"customer_id"`
	Name       string    `json:"name"`
	ExternalID *string   `json:"external_id,omitempty"`
	Address    *string   `json:"address,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// HandleCustomers serves GET /api/v1/customers.
func (h *Handler) HandleCustomers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}

	p := parsePage(r)

	var total int64
	if err := h.db.QueryRow(r.Context(), `SELECT COUNT(*) FROM customers`).Scan(&total); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count customers")
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT c.id, c.code, c.name, c.external_id, c.description, c.created_at, c.updated_at,
		       COUNT(d.id)
		FROM customers c
		LEFT JOIN devices d ON d.customer_id = c.id
		GROUP BY c.id
		ORDER BY c.name
		LIMIT $1 OFFSET $2`, p.PageSize, p.Offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list customers")
		return
	}
	defer rows.Close()

	customers := make([]Customer, 0)
	for rows.Next() {
		var c Customer
		if err := rows.Scan(&c.ID, &c.Code, &c.Name, &c.ExternalID, &c.Description,
			&c.CreatedAt, &c.UpdatedAt, &c.DeviceCount); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read customer")
			return
		}
		customers = append(customers, c)
	}
	if rows.Err() != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read customers")
		return
	}

	writePage(w, p, total, customers)
}

// HandleCustomerDetail serves GET /api/v1/customers/{id}.
func (h *Handler) HandleCustomerDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}
	customerID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var c Customer
	err := h.db.QueryRow(r.Context(), `
		SELECT c.id, c.code, c.name, c.external_id, c.description, c.created_at, c.updated_at,
		       (SELECT COUNT(*) FROM devices d WHERE d.customer_id = c.id)
		FROM customers c WHERE c.id = $1`, customerID).
		Scan(&c.ID, &c.Code, &c.Name, &c.ExternalID, &c.Description, &c.CreatedAt, &c.UpdatedAt, &c.DeviceCount)
	if err != nil {
		dbError(w, err, "failed to read customer")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, c)
}

// HandleCreateCustomer serves POST /api/v1/customers.
func (h *Handler) HandleCreateCustomer(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}

	var req struct {
		Code        string `json:"code"`
		Name        string `json:"name"`
		ExternalID  string `json:"external_id"`
		Description string `json:"description"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	req.Code = strings.TrimSpace(req.Code)
	req.Name = strings.TrimSpace(req.Name)
	if req.Code == "" || req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "code and name are required")
		return
	}

	var c Customer
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO customers (code, name, external_id, description)
		VALUES ($1, $2, $3, $4)
		RETURNING id, code, name, external_id, description, created_at, updated_at`,
		req.Code, req.Name, nullableString(req.ExternalID), nullableString(req.Description)).
		Scan(&c.ID, &c.Code, &c.Name, &c.ExternalID, &c.Description, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		dbError(w, err, "failed to create customer")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "customer.created",
		ActorID:     user.ID,
		Resource:    "customer",
		ResourceID:  c.ID.String(),
		CustomerID:  &c.ID,
		Description: "Customer created: " + c.Name,
		Metadata:    map[string]any{"code": c.Code, "name": c.Name},
	})

	httpx.WriteJSON(w, http.StatusCreated, c)
}

// HandleUpdateCustomer serves PATCH /api/v1/customers/{id}.
func (h *Handler) HandleUpdateCustomer(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	customerID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req struct {
		Code        *string `json:"code"`
		Name        *string `json:"name"`
		ExternalID  *string `json:"external_id"`
		Description *string `json:"description"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	var c Customer
	err := h.db.QueryRow(r.Context(), `
		UPDATE customers
		SET code        = COALESCE($2, code),
		    name        = COALESCE($3, name),
		    external_id = CASE WHEN $4::boolean THEN $5 ELSE external_id END,
		    description = CASE WHEN $6::boolean THEN $7 ELSE description END,
		    updated_at  = now()
		WHERE id = $1
		RETURNING id, code, name, external_id, description, created_at, updated_at`,
		customerID, req.Code, req.Name,
		req.ExternalID != nil, derefOrNil(req.ExternalID),
		req.Description != nil, derefOrNil(req.Description)).
		Scan(&c.ID, &c.Code, &c.Name, &c.ExternalID, &c.Description, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		dbError(w, err, "failed to update customer")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "customer.updated",
		ActorID:     user.ID,
		Resource:    "customer",
		ResourceID:  c.ID.String(),
		CustomerID:  &c.ID,
		Description: "Customer updated: " + c.Name,
	})

	httpx.WriteJSON(w, http.StatusOK, c)
}

// derefOrNil turns a *string that means "set to this" into the value, and an
// absent one into nil, so a nullable column can be cleared by sending "".
func derefOrNil(s *string) *string {
	if s == nil {
		return nil
	}
	return nullableString(*s)
}

// HandleDeleteCustomer serves DELETE /api/v1/customers/{id}.
//
// Devices reference customers with ON DELETE SET NULL, so deleting a customer
// orphans its devices rather than removing them. That is the right trade: a
// device that still exists on someone's desk should not vanish from the fleet
// because a customer record was tidied up.
func (h *Handler) HandleDeleteCustomer(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	customerID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var name string
	if err := h.db.QueryRow(r.Context(), `SELECT name FROM customers WHERE id = $1`, customerID).Scan(&name); err != nil {
		dbError(w, err, "failed to read customer")
		return
	}

	if _, err := h.db.Exec(r.Context(), `DELETE FROM customers WHERE id = $1`, customerID); err != nil {
		dbError(w, err, "failed to delete customer")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "customer.deleted",
		ActorID:     user.ID,
		Resource:    "customer",
		ResourceID:  customerID.String(),
		Description: "Customer deleted: " + name,
		Metadata:    map[string]any{"name": name},
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleLocations serves GET /api/v1/customers/{id}/locations.
func (h *Handler) HandleLocations(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.viewer(w, r); !ok {
		return
	}
	customerID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	p := parsePage(r)

	var total int64
	if err := h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM locations WHERE customer_id = $1`, customerID).Scan(&total); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to count locations")
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT id, customer_id, name, external_id, address, created_at, updated_at
		FROM locations WHERE customer_id = $1
		ORDER BY name
		LIMIT $2 OFFSET $3`, customerID, p.PageSize, p.Offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list locations")
		return
	}
	defer rows.Close()

	locations := make([]Location, 0)
	for rows.Next() {
		var l Location
		if err := rows.Scan(&l.ID, &l.CustomerID, &l.Name, &l.ExternalID, &l.Address,
			&l.CreatedAt, &l.UpdatedAt); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read location")
			return
		}
		locations = append(locations, l)
	}
	if rows.Err() != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read locations")
		return
	}

	writePage(w, p, total, locations)
}

// HandleCreateLocation serves POST /api/v1/customers/{id}/locations.
func (h *Handler) HandleCreateLocation(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	customerID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req struct {
		Name       string `json:"name"`
		ExternalID string `json:"external_id"`
		Address    string `json:"address"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}

	var l Location
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO locations (customer_id, name, external_id, address)
		VALUES ($1, $2, $3, $4)
		RETURNING id, customer_id, name, external_id, address, created_at, updated_at`,
		customerID, strings.TrimSpace(req.Name), nullableString(req.ExternalID), nullableString(req.Address)).
		Scan(&l.ID, &l.CustomerID, &l.Name, &l.ExternalID, &l.Address, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		dbError(w, err, "failed to create location")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "location.created",
		ActorID:     user.ID,
		Resource:    "location",
		ResourceID:  l.ID.String(),
		CustomerID:  &customerID,
		Description: "Location created: " + l.Name,
	})

	httpx.WriteJSON(w, http.StatusCreated, l)
}

// HandleUpdateLocation serves PATCH /api/v1/customers/{id}/locations/{locationId}.
func (h *Handler) HandleUpdateLocation(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	customerID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	locationID, ok := pathUUID(w, r, "locationId")
	if !ok {
		return
	}

	var req struct {
		Name       *string `json:"name"`
		ExternalID *string `json:"external_id"`
		Address    *string `json:"address"`
	}
	if !decodeBody(w, r, &req) {
		return
	}

	// Matching on customer_id as well as id means a location cannot be edited
	// through the wrong customer's URL.
	var l Location
	err := h.db.QueryRow(r.Context(), `
		UPDATE locations
		SET name        = COALESCE($3, name),
		    external_id = CASE WHEN $4::boolean THEN $5 ELSE external_id END,
		    address     = CASE WHEN $6::boolean THEN $7 ELSE address END,
		    updated_at  = now()
		WHERE id = $1 AND customer_id = $2
		RETURNING id, customer_id, name, external_id, address, created_at, updated_at`,
		locationID, customerID, req.Name,
		req.ExternalID != nil, derefOrNil(req.ExternalID),
		req.Address != nil, derefOrNil(req.Address)).
		Scan(&l.ID, &l.CustomerID, &l.Name, &l.ExternalID, &l.Address, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		dbError(w, err, "failed to update location")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "location.updated",
		ActorID:     user.ID,
		Resource:    "location",
		ResourceID:  l.ID.String(),
		CustomerID:  &customerID,
		Description: "Location updated: " + l.Name,
	})

	httpx.WriteJSON(w, http.StatusOK, l)
}

// HandleDeleteLocation serves DELETE /api/v1/customers/{id}/locations/{locationId}.
func (h *Handler) HandleDeleteLocation(w http.ResponseWriter, r *http.Request) {
	user, ok := h.admin(w, r)
	if !ok {
		return
	}
	customerID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	locationID, ok := pathUUID(w, r, "locationId")
	if !ok {
		return
	}

	tag, err := h.db.Exec(r.Context(),
		`DELETE FROM locations WHERE id = $1 AND customer_id = $2`, locationID, customerID)
	if err != nil {
		dbError(w, err, "failed to delete location")
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSONError(w, http.StatusNotFound, "location not found")
		return
	}

	h.record(r.Context(), audit.Event{
		Type:        "location.deleted",
		ActorID:     user.ID,
		Resource:    "location",
		ResourceID:  locationID.String(),
		CustomerID:  &customerID,
		Description: "Location deleted",
	})

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
