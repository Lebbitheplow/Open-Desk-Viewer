package peers

import (
	"context"
	"fmt"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// peerColumns is the projection shared by the admin and technician queries. A
// device can belong to several device groups, so the lateral join picks one
// deterministically and keeps the result at one row per device, which is what
// the COUNT queries assume.
const peerColumns = `
	SELECT d.id, d.name, COALESCE(d.hostname, ''), COALESCE(d.os, ''), d.state,
	       d.last_seen_at, dg.id, dg.name, c.id, c.name
	FROM devices d
	LEFT JOIN LATERAL (
	    SELECT g.id, g.name
	    FROM device_group_members m
	    JOIN device_groups g ON m.device_group_id = g.id
	    WHERE m.device_id = d.id
	    ORDER BY g.name
	    LIMIT 1
	) dg ON true
	LEFT JOIN customers c ON d.customer_id = c.id
`

// accessiblePredicate restricts a device query to the devices reachable by the
// user in $1, through the device-group / support-group chain. Written as EXISTS
// rather than a join so it cannot multiply rows, which keeps the row count and
// the COUNT query in agreement.
const accessiblePredicate = `
	EXISTS (
	    SELECT 1
	    FROM device_group_members m
	    JOIN support_group_device_groups sgdg ON m.device_group_id = sgdg.device_group_id
	    JOIN user_support_groups usg ON sgdg.support_group_id = usg.support_group_id
	    WHERE m.device_id = d.id AND usg.user_id = $1
	)
`

// Peer represents a device
type Peer struct {
	ID            uuid.UUID
	Name          string
	Hostname      string
	OS            string
	State         string
	LastSeenAt    *time.Time
	GroupName     *string
	UserName      *string
	DeviceGroupID *uuid.UUID
	CustomerID    *uuid.UUID
}

// Service handles peers operations
type Service struct {
	db     *postgres.Pool
	access access.Resolver
}

// NewService creates a new peers service
func NewService(db *postgres.Pool, access access.Resolver) *Service {
	return &Service{db: db, access: access}
}

// ListAccessiblePeers retrieves all peers accessible by a user
func (s *Service) ListAccessiblePeers(ctx context.Context, userID int64, offset, limit int64) ([]Peer, int64, error) {
	isAdmin, err := s.access.IsAdminOrManager(ctx, userID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to check admin status: %w", err)
	}

	var rows pgx.Rows
	var total int64

	if isAdmin {
		err = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM devices WHERE state = 'ACTIVE'`).Scan(&total)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to count peers: %w", err)
		}
		rows, err = s.db.Query(ctx, peerColumns+`
			WHERE d.state = 'ACTIVE'
			ORDER BY d.name
			LIMIT $1 OFFSET $2
		`, limit, offset)
	} else {
		err = s.db.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM devices d
			WHERE d.state = 'ACTIVE' AND `+accessiblePredicate, userID).Scan(&total)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to count peers: %w", err)
		}
		rows, err = s.db.Query(ctx, peerColumns+`
			WHERE d.state = 'ACTIVE' AND `+accessiblePredicate+`
			ORDER BY d.name
			LIMIT $2 OFFSET $3
		`, userID, limit, offset)
	}

	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch peers: %w", err)
	}
	defer rows.Close()

	var peers []Peer
	for rows.Next() {
		var peer Peer
		if err := rows.Scan(
			&peer.ID, &peer.Name, &peer.Hostname, &peer.OS, &peer.State,
			&peer.LastSeenAt, &peer.DeviceGroupID, &peer.GroupName,
			&peer.CustomerID, &peer.UserName,
		); err != nil {
			return nil, 0, fmt.Errorf("failed to scan peer: %w", err)
		}
		peers = append(peers, peer)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to read peers: %w", err)
	}

	return peers, total, nil
}

// ListDeviceGroups retrieves all device groups accessible by a user
func (s *Service) ListDeviceGroups(ctx context.Context, userID int64, current, pageSize int64) ([]DeviceGroup, int64, error) {
	offset := (current - 1) * pageSize
	
	var rows pgx.Rows
	var total int64
	
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT dg.id)
		FROM device_groups dg
		JOIN support_group_device_groups sgdg ON dg.id = sgdg.device_group_id
		JOIN user_support_groups usg ON sgdg.support_group_id = usg.support_group_id
		WHERE usg.user_id = $1
	`, userID).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count device groups: %w", err)
	}
	
	rows, err = s.db.Query(ctx, `
		SELECT DISTINCT dg.id, dg.name
		FROM device_groups dg
		JOIN support_group_device_groups sgdg ON dg.id = sgdg.device_group_id
		JOIN user_support_groups usg ON sgdg.support_group_id = usg.support_group_id
		WHERE usg.user_id = $1
		ORDER BY dg.name
		LIMIT $2 OFFSET $3
	`, userID, pageSize, offset)

	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch device groups: %w", err)
	}
	defer rows.Close()

	var groups []DeviceGroup
	for rows.Next() {
		var group DeviceGroup
		var groupID, groupName string
		if err := rows.Scan(&groupID, &groupName); err != nil {
			return nil, 0, fmt.Errorf("failed to scan device group: %w", err)
		}

		id, err := uuid.Parse(groupID)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid group ID: %w", err)
		}

		group.ID = id
		group.Name = groupName
		groups = append(groups, group)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to read device groups: %w", err)
	}

	return groups, total, nil
}

// ListPeerIDsAccessible retrieves a list of peer IDs a user can access
func (s *Service) ListPeerIDsAccessible(ctx context.Context, userID int64) ([]uuid.UUID, error) {
	isAdmin, err := s.access.IsAdminOrManager(ctx, userID)
	if err != nil {
		return nil, err
	}

	var rows pgx.Rows
	if isAdmin {
		rows, err = s.db.Query(ctx, `SELECT d.id FROM devices d WHERE d.state = 'ACTIVE'`)
	} else {
		rows, err = s.db.Query(ctx, `
			SELECT d.id FROM devices d
			WHERE d.state = 'ACTIVE' AND `+accessiblePredicate, userID)
	}
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

	return devices, rows.Err()
}

// HasPeerAccess checks if a user can access a specific peer
func (s *Service) HasPeerAccess(ctx context.Context, userID int64, peerID uuid.UUID) (bool, error) {
	return s.access.CanAccessDevice(ctx, userID, peerID)
}

// DeviceGroup represents a device group
type DeviceGroup struct {
	ID   uuid.UUID
	Name string
}
