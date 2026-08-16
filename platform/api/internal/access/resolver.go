package access

import (
	"context"

	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Resolver interface for authorization checks
type Resolver interface {
	// Check if user can access device
	CanAccessDevice(ctx context.Context, userID int64, deviceID uuid.UUID) (bool, error)

	// Check if user can access session
	CanAccessSession(ctx context.Context, userID int64, sessionID uuid.UUID) (bool, error)

	// Get all devices user can access
	GetAccessibleDevices(ctx context.Context, userID int64) ([]uuid.UUID, error)

	// Get paginated devices user can access
	GetAccessibleDevicesPaginated(ctx context.Context, userID int64, offset, limit int64) ([]uuid.UUID, int64, error)

	// Get all devices accessible from a device group
	GetDevicesInGroup(ctx context.Context, groupID uuid.UUID) ([]uuid.UUID, error)

	// Check if user has role
	HasRole(ctx context.Context, userID int64, roleName string) (bool, error)

	// Get user's support groups
	GetUserSupportGroups(ctx context.Context, userID int64) ([]uuid.UUID, error)

	// Get device's customer
	GetDeviceCustomer(ctx context.Context, deviceID uuid.UUID) (*uuid.UUID, error)

	// Get user's accessible customers
	GetAccessibleCustomers(ctx context.Context, userID int64) ([]uuid.UUID, error)

	// Get all technicians in a support group
	GetTechniciansInGroup(ctx context.Context, groupID uuid.UUID) ([]int64, error)

	// Check if user is admin/manager
	IsAdminOrManager(ctx context.Context, userID int64) (bool, error)
}

// SQLResolver implements authorization using SQL
type SQLResolver struct {
	db  *postgres.Pool
	cfg *config.Config
}

// NewSQLResolver creates a resolver that answers authorisation questions with
// SQL against the support group and device group tables. The Resolver interface
// stays as the extension point if that ever needs to change.
func NewSQLResolver(db *postgres.Pool, cfg *config.Config) *SQLResolver {
	return &SQLResolver{db: db, cfg: cfg}
}

// CanAccessDevice checks if a user can access a specific device
func (r *SQLResolver) CanAccessDevice(ctx context.Context, userID int64, deviceID uuid.UUID) (bool, error) {
	// Admin/Manager short-circuit: see all active devices
	isAdmin, err := r.IsAdminOrManager(ctx, userID)
	if err != nil {
		return false, err
	}
	if isAdmin {
		var deviceActive bool
		err = r.db.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM devices WHERE id = $1 AND state = 'ACTIVE'
			)
		`, deviceID).Scan(&deviceActive)
		return deviceActive, err
	}

	// Regular user: check via support groups
	var exists bool
	err = r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM devices d
			JOIN device_group_members dgm ON d.id = dgm.device_id
			JOIN support_group_device_groups sgdg ON dgm.device_group_id = sgdg.device_group_id
			JOIN user_support_groups usg ON sgdg.support_group_id = usg.support_group_id
			WHERE usg.user_id = $1 AND d.id = $2
			  AND d.state = 'ACTIVE'
		)
	`, userID, deviceID).Scan(&exists)

	return exists, err
}

// accessiblePredicate restricts a device query to the devices reachable by the
// user in $1, through the device-group / support-group chain. EXISTS rather
// than a join, so it cannot multiply rows.
const accessiblePredicate = `
	EXISTS (
	    SELECT 1
	    FROM device_group_members m
	    JOIN support_group_device_groups sgdg ON m.device_group_id = sgdg.device_group_id
	    JOIN user_support_groups usg ON sgdg.support_group_id = usg.support_group_id
	    WHERE m.device_id = d.id AND usg.user_id = $1
	)
`

// scanDeviceIDs collects a single-column device id result set.
func scanDeviceIDs(rows pgx.Rows) ([]uuid.UUID, error) {
	var devices []uuid.UUID
	for rows.Next() {
		var d uuid.UUID
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

// GetAccessibleDevices returns all devices a user can access
func (r *SQLResolver) GetAccessibleDevices(ctx context.Context, userID int64) ([]uuid.UUID, error) {
	// Admin/Manager short-circuit: all active devices
	isAdmin, err := r.IsAdminOrManager(ctx, userID)
	if err != nil {
		return nil, err
	}

	var rows pgx.Rows
	if isAdmin {
		rows, err = r.db.Query(ctx, `SELECT d.id FROM devices d WHERE d.state = 'ACTIVE'`)
	} else {
		rows, err = r.db.Query(ctx, `
			SELECT d.id FROM devices d
			WHERE d.state = 'ACTIVE' AND `+accessiblePredicate, userID)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanDeviceIDs(rows)
}

// GetDevicesInGroup returns all devices in a device group
func (r *SQLResolver) GetDevicesInGroup(ctx context.Context, groupID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, `
		SELECT device_id
		FROM device_group_members
		WHERE device_group_id = $1
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []uuid.UUID
	for rows.Next() {
		var d uuid.UUID
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}

	return devices, nil
}

// HasRole checks if a user has a specific role
func (r *SQLResolver) HasRole(ctx context.Context, userID int64, roleName string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM user_roles ur
			JOIN roles r ON ur.role_id = r.id
			WHERE ur.user_id = $1 AND r.name = $2
		)
	`, userID, roleName).Scan(&exists)

	return exists, err
}

// GetUserSupportGroups returns support groups for a user
func (r *SQLResolver) GetUserSupportGroups(ctx context.Context, userID int64) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, `
		SELECT support_group_id
		FROM user_support_groups
		WHERE user_id = $1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []uuid.UUID
	for rows.Next() {
		var g uuid.UUID
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}

	return groups, nil
}

// GetDeviceCustomer returns the customer owning a device
func (r *SQLResolver) GetDeviceCustomer(ctx context.Context, deviceID uuid.UUID) (*uuid.UUID, error) {
	var customerID *uuid.UUID
	err := r.db.QueryRow(ctx, `
		SELECT customer_id FROM devices WHERE id = $1
	`, deviceID).Scan(&customerID)

	return customerID, err
}

// GetAccessibleCustomers returns customers accessible by a user
func (r *SQLResolver) GetAccessibleCustomers(ctx context.Context, userID int64) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT c.id
		FROM customers c
		JOIN locations l ON c.id = l.customer_id
		JOIN devices d ON l.id = d.location_id
		JOIN device_group_members dgm ON d.id = dgm.device_id
		JOIN support_group_device_groups sgdg ON dgm.device_group_id = sgdg.device_group_id
		JOIN user_support_groups usg ON sgdg.support_group_id = usg.support_group_id
		WHERE usg.user_id = $1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var customers []uuid.UUID
	for rows.Next() {
		var c uuid.UUID
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		customers = append(customers, c)
	}

	return customers, nil
}

// GetTechniciansInGroup returns all technicians in a support group
func (r *SQLResolver) GetTechniciansInGroup(ctx context.Context, groupID uuid.UUID) ([]int64, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT usg.user_id
		FROM user_support_groups usg
		JOIN user_roles ur ON usg.user_id = ur.user_id
		JOIN roles r ON ur.role_id = r.id
		WHERE usg.support_group_id = $1 AND r.name IN ('Administrator', 'Support Manager', 'Technician')
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var userIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, id)
	}

	return userIDs, nil
}

// IsAdminOrManager checks if a user has Administrator or Support Manager role
func (r *SQLResolver) IsAdminOrManager(ctx context.Context, userID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM user_roles ur
			JOIN roles ro ON ur.role_id = ro.id
			WHERE ur.user_id = $1 AND ro.name IN ('Administrator', 'Support Manager')
		)
	`, userID).Scan(&exists)

	return exists, err
}

// GetAccessibleDevicesPaginated returns paginated accessible devices
func (r *SQLResolver) GetAccessibleDevicesPaginated(ctx context.Context, userID int64, offset, limit int64) ([]uuid.UUID, int64, error) {
	isAdmin, err := r.IsAdminOrManager(ctx, userID)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	var rows pgx.Rows

	if isAdmin {
		err = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM devices WHERE state = 'ACTIVE'`).Scan(&total)
		if err != nil {
			return nil, 0, err
		}
		rows, err = r.db.Query(ctx, `
			SELECT d.id FROM devices d
			WHERE d.state = 'ACTIVE'
			ORDER BY d.id
			LIMIT $1 OFFSET $2
		`, limit, offset)
	} else {
		err = r.db.QueryRow(ctx, `
			SELECT COUNT(*) FROM devices d
			WHERE d.state = 'ACTIVE' AND `+accessiblePredicate, userID).Scan(&total)
		if err != nil {
			return nil, 0, err
		}
		rows, err = r.db.Query(ctx, `
			SELECT d.id FROM devices d
			WHERE d.state = 'ACTIVE' AND `+accessiblePredicate+`
			ORDER BY d.id
			LIMIT $2 OFFSET $3
		`, userID, limit, offset)
	}

	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	devices, err := scanDeviceIDs(rows)
	if err != nil {
		return nil, 0, err
	}

	return devices, total, nil
}

// CanAccessSession checks if a user can access a specific connection session
func (r *SQLResolver) CanAccessSession(ctx context.Context, userID int64, sessionID uuid.UUID) (bool, error) {
	// Check if user is admin/manager
	if isAdmin, err := r.IsAdminOrManager(ctx, userID); err != nil {
		return false, err
	} else if isAdmin {
		return true, nil
	}

	// Check if user owns the session
	var ownerID int64
	err := r.db.QueryRow(ctx, `
		SELECT user_id FROM connection_sessions WHERE id = $1
	`, sessionID).Scan(&ownerID)
	if err == pgx.ErrNoRows {
		return false, nil
	} else if err != nil {
		return false, err
	}

	return ownerID == userID, nil
}
