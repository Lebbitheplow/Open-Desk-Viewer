package fleet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrDeviceNotFound = fmt.Errorf("device not found")

// Device represents a managed device
type Device struct {
	ID            uuid.UUID
	RustdeskID    string
	Name          string
	UUID          string
	CustomerID    *uuid.UUID
	LocationID    *uuid.UUID
	SerialNumber  *string
	State         string
	Connectivity  string
	LastSeenAt    time.Time
	ClientVersion string
	OS            string
	Hostname      string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Customer represents a customer entity
type Customer struct {
	ID          uuid.UUID
	Name        string
	Code        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Location represents a location
type Location struct {
	ID         uuid.UUID
	CustomerID uuid.UUID
	Name       string
	Address    string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DeviceGroup represents a device group
type DeviceGroup struct {
	ID        uuid.UUID
	Name      string
	ParentID  *uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SupportGroup represents a support group
type SupportGroup struct {
	ID          uuid.UUID
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Service handles fleet management operations
type Service struct {
	db  *postgres.Pool
	cfg *config.Config
}

// NewService creates a new fleet service
func NewService(db *postgres.Pool, cfg *config.Config) *Service {
	return &Service{
		db:  db,
		cfg: cfg,
	}
}

// GetDeviceByRustdeskID retrieves a device by its RustDesk ID
// deviceColumns is the select list every Device scan uses.
//
// last_seen_at, client_version, os and hostname are nullable columns read into
// non-pointer Go fields, so a row where any of them is NULL fails to scan. That
// is every device the moment it is registered: RegisterDevice writes none of
// them, so the first heartbeat inserted the row and then errored on reading it
// back, and every heartbeat after that errored the same way. Device telemetry
// could never have worked. COALESCE here rather than pointer fields, because
// the callers all want a value and 'epoch' reads correctly as "never seen" in
// the staleness comparison.
const deviceColumns = `
		d.id, d.rustdesk_id, d.name, d.uuid, d.customer_id, d.location_id,
		d.serial_number, d.state, d.connectivity,
		COALESCE(d.last_seen_at, 'epoch'::timestamptz),
		COALESCE(d.client_version, ''), COALESCE(d.os, ''), COALESCE(d.hostname, ''),
		d.created_at, d.updated_at`

func (s *Service) GetDeviceByRustdeskID(ctx context.Context, rustdeskID string) (*Device, error) {
	row := s.db.QueryRow(ctx, `
		SELECT `+deviceColumns+`
		FROM devices d
		WHERE d.rustdesk_id = $1
	`, rustdeskID)

	var d Device
	err := row.Scan(
		&d.ID, &d.RustdeskID, &d.Name, &d.UUID, &d.CustomerID,
		&d.LocationID, &d.SerialNumber, &d.State, &d.Connectivity,
		&d.LastSeenAt, &d.ClientVersion, &d.OS, &d.Hostname,
		&d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrDeviceNotFound
		}
		return nil, err
	}
	return &d, nil
}

// RegisterDevice creates a new device record
func (s *Service) RegisterDevice(ctx context.Context, device Device) (*Device, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `
		INSERT INTO devices (rustdesk_id, name, uuid, state, created_at)
		VALUES ($1, $2, $3, $4, now())
		RETURNING id
	`, device.RustdeskID, device.Name, device.UUID, device.State).Scan(&id)

	if err != nil {
		return nil, fmt.Errorf("failed to register device: %w", err)
	}

	return &Device{ID: id}, nil
}

// UpdateDevice updates an existing device
func (s *Service) UpdateDevice(ctx context.Context, device Device) error {
	_, err := s.db.Exec(ctx, `
		UPDATE devices SET
			name = $1,
			customer_id = $2,
			location_id = $3,
			serial_number = $4,
			state = $5,
			connectivity = $6,
			last_seen_at = $7,
			client_version = $8,
			os = $9,
			hostname = $10,
			updated_at = now()
		WHERE id = $11
	`, device.Name, device.CustomerID, device.LocationID, device.SerialNumber,
		device.State, device.Connectivity, device.LastSeenAt, device.ClientVersion,
		device.OS, device.Hostname, device.ID)

	return err
}

// AssignDeviceToCustomer assigns a device to a customer and location
func (s *Service) AssignDeviceToCustomer(ctx context.Context, deviceID uuid.UUID, customerID uuid.UUID, locationID *uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE devices 
		SET customer_id = $1, location_id = $2, state = 'ACTIVE', updated_at = now()
		WHERE id = $3
	`, customerID, locationID, deviceID)

	if err != nil {
		return fmt.Errorf("failed to assign device: %w", err)
	}

	return nil
}

// GenerateDeviceName generates a device name based on template
func (s *Service) GenerateDeviceName(customer, location string, serial *string) string {
	template := s.cfg.DeviceNameTemplate

	name := strings.ReplaceAll(template, "{customer}", customer)
	name = strings.ReplaceAll(name, "{location}", location)

	if serial != nil {
		name = strings.ReplaceAll(name, "{serial}", *serial)
	} else {
		name = strings.ReplaceAll(name, "{serial}", "unknown")
	}

	if strings.Contains(name, "{random}") {
		name = strings.ReplaceAll(name, "{random}", generateRandomSuffix(4))
	}

	return name
}

func generateRandomSuffix(length int) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = byte(65 + (i % 26))
	}
	return string(b)
}

// GetDeviceAccessibleByUser returns all devices a user can access
func (s *Service) GetDeviceAccessibleByUser(ctx context.Context, userID int64) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT d.id
		FROM devices d
		JOIN device_group_members dgm ON d.id = dgm.device_id
		JOIN support_group_device_groups sgdg ON dgm.device_group_id = sgdg.device_group_id
		JOIN user_support_groups usg ON sgdg.support_group_id = usg.support_group_id
		WHERE usg.user_id = $1
	`, userID)
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

// SearchDevices searches devices by name, hostname, or serial
func (s *Service) SearchDevices(ctx context.Context, query string, limit int) ([]Device, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+deviceColumns+`
		FROM devices d
		WHERE d.name ILIKE $1 OR d.hostname ILIKE $1 OR d.serial_number ILIKE $1
		ORDER BY d.name
		LIMIT $2
	`, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var d Device
		err := rows.Scan(
			&d.ID, &d.RustdeskID, &d.Name, &d.UUID, &d.CustomerID,
			&d.LocationID, &d.SerialNumber, &d.State, &d.Connectivity,
			&d.LastSeenAt, &d.ClientVersion, &d.OS, &d.Hostname,
			&d.CreatedAt, &d.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}

	return devices, nil
}

// GetCustomers returns all customers
func (s *Service) GetCustomers(ctx context.Context) ([]Customer, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, code, description, created_at, updated_at
		FROM customers
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var customers []Customer
	for rows.Next() {
		var c Customer
		if err := rows.Scan(&c.ID, &c.Name, &c.Code, &c.Description, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		customers = append(customers, c)
	}

	return customers, nil
}

// GetCustomerByCode retrieves a customer by code
func (s *Service) GetCustomerByCode(ctx context.Context, code string) (*Customer, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id, name, code, description, created_at, updated_at
		FROM customers
		WHERE code = $1
	`, code)

	var c Customer
	err := row.Scan(&c.ID, &c.Name, &c.Code, &c.Description, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetLocationsByCustomer returns locations for a customer
func (s *Service) GetLocationsByCustomer(ctx context.Context, customerID uuid.UUID) ([]Location, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, customer_id, name, address, created_at, updated_at
		FROM locations
		WHERE customer_id = $1
		ORDER BY name
	`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var locations []Location
	for rows.Next() {
		var l Location
		if err := rows.Scan(&l.ID, &l.CustomerID, &l.Name, &l.Address, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		locations = append(locations, l)
	}

	return locations, nil
}

// GetDeviceGroups returns all device groups
func (s *Service) GetDeviceGroups(ctx context.Context) ([]DeviceGroup, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, parent_id, created_at, updated_at
		FROM device_groups
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []DeviceGroup
	for rows.Next() {
		var g DeviceGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.ParentID, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}

	return groups, nil
}

// GetSupportGroups returns all support groups
func (s *Service) GetSupportGroups(ctx context.Context) ([]SupportGroup, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, description, created_at, updated_at
		FROM support_groups
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []SupportGroup
	for rows.Next() {
		var g SupportGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}

	return groups, nil
}
