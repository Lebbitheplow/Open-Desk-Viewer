package addressbook

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Peer is one address book entry, shaped like the client's Peer model in
// flutter/lib/models/peer_model.dart.
type Peer struct {
	ID               string
	Username         string
	Hostname         string
	Platform         string
	Alias            string
	Note             string
	Hash             string
	ForceAlwaysRelay bool
	RdpPort          string
	RdpUsername      string
	Tags             []string
	LoginName        string
	DeviceGroupName  string
}

// PeerUpdate carries a partial update. The client sends only the fields it is
// changing, so a nil pointer means "leave alone" and is not the same as empty.
type PeerUpdate struct {
	ID               string
	Alias            *string
	Note             *string
	Hash             *string
	Tags             *[]string
	ForceAlwaysRelay *bool
	RdpPort          *string
	RdpUsername      *string
}

// devicePeerColumns projects a managed device into the client's peer shape.
// Alias carries the managed device name, which is the label technicians expect
// to see, and login name carries the customer.
const devicePeerColumns = `
	SELECT d.rustdesk_id, COALESCE(d.hostname, ''), COALESCE(d.os, ''), d.name,
	       COALESCE(dg.name, ''), COALESCE(c.name, '')
	FROM devices d
	LEFT JOIN LATERAL (
	    SELECT g.name
	    FROM device_group_members m
	    JOIN device_groups g ON m.device_group_id = g.id
	    WHERE m.device_id = d.id
	    ORDER BY g.name
	    LIMIT 1
	) dg ON true
	LEFT JOIN customers c ON d.customer_id = c.id
`

// inSupportGroup restricts devices to those reachable from the support group in
// $1. EXISTS rather than a join, so it cannot multiply rows.
const inSupportGroup = `
	EXISTS (
	    SELECT 1
	    FROM device_group_members m
	    JOIN support_group_device_groups sgdg ON m.device_group_id = sgdg.device_group_id
	    WHERE m.device_id = d.id AND sgdg.support_group_id = $1
	)
`

// ListPeers returns one page of a book's peers, with the total across all pages.
func (s *Service) ListPeers(ctx context.Context, userID int64, guid uuid.UUID, offset, limit int64) ([]Peer, int64, error) {
	scope, err := s.ResolveScope(ctx, userID, guid)
	if err != nil {
		return nil, 0, err
	}

	if scope.Personal {
		return s.listStoredPeers(ctx, scope.GUID, offset, limit)
	}
	return s.listDevicePeers(ctx, scope, offset, limit)
}

// listStoredPeers reads the personal book.
func (s *Service) listStoredPeers(ctx context.Context, profileID uuid.UUID, offset, limit int64) ([]Peer, int64, error) {
	var total int64
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM ab_peers WHERE profile_id = $1`, profileID).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count address book peers: %w", err)
	}

	rows, err := s.db.Query(ctx, `
		SELECT rustdesk_id, username, hostname, platform, alias, note, hash,
		       force_always_relay, rdp_port, rdp_username, tags
		FROM ab_peers
		WHERE profile_id = $1
		ORDER BY rustdesk_id
		LIMIT $2 OFFSET $3
	`, profileID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch address book peers: %w", err)
	}
	defer rows.Close()

	var peers []Peer
	for rows.Next() {
		var p Peer
		if err := rows.Scan(
			&p.ID, &p.Username, &p.Hostname, &p.Platform, &p.Alias, &p.Note,
			&p.Hash, &p.ForceAlwaysRelay, &p.RdpPort, &p.RdpUsername, &p.Tags,
		); err != nil {
			return nil, 0, fmt.Errorf("failed to scan address book peer: %w", err)
		}
		peers = append(peers, p)
	}

	return peers, total, rows.Err()
}

// listDevicePeers projects the fleet, or one support group's slice of it.
func (s *Service) listDevicePeers(ctx context.Context, scope Scope, offset, limit int64) ([]Peer, int64, error) {
	var (
		total int64
		rows  pgx.Rows
		err   error
	)

	if scope.Fleet {
		err = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM devices d WHERE d.state = 'ACTIVE'`).Scan(&total)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to count devices: %w", err)
		}
		rows, err = s.db.Query(ctx, devicePeerColumns+`
			WHERE d.state = 'ACTIVE'
			ORDER BY d.name
			LIMIT $1 OFFSET $2
		`, limit, offset)
	} else {
		err = s.db.QueryRow(ctx, `
			SELECT COUNT(*) FROM devices d
			WHERE d.state = 'ACTIVE' AND `+inSupportGroup, scope.GroupID).Scan(&total)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to count devices: %w", err)
		}
		rows, err = s.db.Query(ctx, devicePeerColumns+`
			WHERE d.state = 'ACTIVE' AND `+inSupportGroup+`
			ORDER BY d.name
			LIMIT $2 OFFSET $3
		`, scope.GroupID, limit, offset)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch devices: %w", err)
	}
	defer rows.Close()

	var peers []Peer
	for rows.Next() {
		var p Peer
		var os string
		if err := rows.Scan(&p.ID, &p.Hostname, &os, &p.Alias, &p.DeviceGroupName, &p.LoginName); err != nil {
			return nil, 0, fmt.Errorf("failed to scan device: %w", err)
		}
		p.Platform = normalisePlatform(os)
		p.Tags = []string{}
		peers = append(peers, p)
	}

	return peers, total, rows.Err()
}

// normalisePlatform maps a reported OS string onto the platform names the
// client's Peer model expects, so it picks the right icon.
func normalisePlatform(os string) string {
	switch lower := strings.ToLower(os); {
	case strings.Contains(lower, "windows"):
		return "Windows"
	case strings.Contains(lower, "android"):
		return "Android"
	case strings.Contains(lower, "mac"), strings.Contains(lower, "darwin"), strings.Contains(lower, "ios"):
		return "Mac OS"
	case strings.Contains(lower, "linux"), strings.Contains(lower, "ubuntu"), strings.Contains(lower, "debian"),
		strings.Contains(lower, "fedora"), strings.Contains(lower, "centos"):
		return "Linux"
	default:
		return ""
	}
}

// AddPeer inserts a peer into a writable book. Re-adding an existing id updates
// it rather than failing, which matches the client re-syncing a known peer.
func (s *Service) AddPeer(ctx context.Context, userID int64, guid uuid.UUID, p Peer) error {
	scope, err := s.requireWritable(ctx, userID, guid)
	if err != nil {
		return err
	}

	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("peer id is required")
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO ab_peers (profile_id, rustdesk_id, username, hostname, platform,
		                      alias, note, hash, force_always_relay, rdp_port,
		                      rdp_username, tags)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (profile_id, rustdesk_id) DO UPDATE
		    SET username = EXCLUDED.username,
		        hostname = EXCLUDED.hostname,
		        platform = EXCLUDED.platform,
		        alias = EXCLUDED.alias,
		        note = EXCLUDED.note,
		        hash = EXCLUDED.hash,
		        force_always_relay = EXCLUDED.force_always_relay,
		        rdp_port = EXCLUDED.rdp_port,
		        rdp_username = EXCLUDED.rdp_username,
		        tags = EXCLUDED.tags,
		        updated_at = now()
	`, scope.GUID, p.ID, p.Username, p.Hostname, p.Platform, p.Alias, p.Note,
		p.Hash, p.ForceAlwaysRelay, p.RdpPort, p.RdpUsername, p.Tags)
	if err != nil {
		return fmt.Errorf("failed to add address book peer: %w", err)
	}

	return nil
}

// UpdatePeer applies a partial update. COALESCE leaves any field the client
// omitted untouched.
func (s *Service) UpdatePeer(ctx context.Context, userID int64, guid uuid.UUID, u PeerUpdate) error {
	scope, err := s.requireWritable(ctx, userID, guid)
	if err != nil {
		return err
	}

	if strings.TrimSpace(u.ID) == "" {
		return fmt.Errorf("peer id is required")
	}

	tag, err := s.db.Exec(ctx, `
		UPDATE ab_peers SET
		    alias = COALESCE($3, alias),
		    note = COALESCE($4, note),
		    hash = COALESCE($5, hash),
		    tags = COALESCE($6, tags),
		    force_always_relay = COALESCE($7, force_always_relay),
		    rdp_port = COALESCE($8, rdp_port),
		    rdp_username = COALESCE($9, rdp_username),
		    updated_at = now()
		WHERE profile_id = $1 AND rustdesk_id = $2
	`, scope.GUID, u.ID, u.Alias, u.Note, u.Hash, u.Tags, u.ForceAlwaysRelay, u.RdpPort, u.RdpUsername)
	if err != nil {
		return fmt.Errorf("failed to update address book peer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

// DeletePeers removes peers by RustDesk id.
func (s *Service) DeletePeers(ctx context.Context, userID int64, guid uuid.UUID, ids []string) error {
	scope, err := s.requireWritable(ctx, userID, guid)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}

	_, err = s.db.Exec(ctx, `
		DELETE FROM ab_peers WHERE profile_id = $1 AND rustdesk_id = ANY($2)
	`, scope.GUID, ids)
	if err != nil {
		return fmt.Errorf("failed to delete address book peers: %w", err)
	}

	return nil
}
