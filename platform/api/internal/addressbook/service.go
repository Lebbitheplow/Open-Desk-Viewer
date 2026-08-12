// Package addressbook implements the address books the RustDesk client reads
// and writes over /api/ab/*.
//
// There are two kinds, and they are stored very differently:
//
//   - The personal book is real storage in ab_profiles/ab_peers/ab_tags. It is
//     the user's own scratch space and is fully writable.
//   - Shared books are projections of the fleet, not rows: one per support group
//     the user belongs to, plus a fleet-wide book for administrators. Their guid
//     is the support group's own id. Membership therefore follows device
//     assignment automatically and there is nothing to keep in sync, at the cost
//     of being read-only to the client.
package addressbook

import (
	"context"
	"errors"
	"fmt"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Share rules, matching ShareRule in flutter/lib/common/hbbs/hbbs.dart.
const (
	RuleRead        = 1
	RuleReadWrite   = 2
	RuleFullControl = 3
)

// FleetProfileGUID is the fixed guid of the administrators' fleet-wide book. It
// is a sentinel rather than a stored row, so it must not collide with a real
// support group id.
var FleetProfileGUID = uuid.MustParse("00000000-0000-0000-0000-0000000000fe")

// PersonalProfileName is what the client shows for the personal book.
const PersonalProfileName = "My Address Book"

var (
	// ErrForbidden means the caller may not see this book at all.
	ErrForbidden = errors.New("address book not accessible")
	// ErrReadOnly means the book exists and is visible, but is a projection of
	// the fleet and cannot be edited from the client.
	ErrReadOnly = errors.New("address book is read-only")
	// ErrNotFound means no such peer or tag in this book.
	ErrNotFound = errors.New("not found")
)

// Profile is one address book as the client lists it.
type Profile struct {
	GUID  uuid.UUID
	Name  string
	Owner string
	Note  *string
	Rule  int
}

// Scope is the resolved meaning of a guid for one caller: which book it is and
// what they may do with it.
type Scope struct {
	GUID     uuid.UUID
	Personal bool
	Fleet    bool
	// GroupID is set when the guid names a support group's shared book.
	GroupID *uuid.UUID
	Rule    int
}

// Writable reports whether the client may modify this book.
func (s Scope) Writable() bool {
	return s.Personal
}

// Service handles address book operations
type Service struct {
	db     *postgres.Pool
	access access.Resolver
}

// NewService creates a new address book service
func NewService(db *postgres.Pool, access access.Resolver) *Service {
	return &Service{db: db, access: access}
}

// ResolvePersonalProfile returns the caller's personal book, creating it on
// first use. The partial unique index on (owner_user_id) WHERE personal makes
// this a single atomic upsert, so two concurrent first calls cannot both create.
func (s *Service) ResolvePersonalProfile(ctx context.Context, userID int64) (*Profile, error) {
	var p Profile
	err := s.db.QueryRow(ctx, `
		INSERT INTO ab_profiles (owner_user_id, name, personal)
		VALUES ($1, $2, true)
		ON CONFLICT (owner_user_id) WHERE personal DO UPDATE
		    SET updated_at = now()
		RETURNING id, name, note
	`, userID, PersonalProfileName).Scan(&p.GUID, &p.Name, &p.Note)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve personal address book: %w", err)
	}

	p.Rule = RuleFullControl
	return &p, nil
}

// ListSharedProfiles returns the shared books visible to the caller: one per
// support group they belong to, plus the fleet-wide book for administrators.
func (s *Service) ListSharedProfiles(ctx context.Context, userID int64, offset, limit int64) ([]Profile, int64, error) {
	isAdmin, err := s.access.IsAdminOrManager(ctx, userID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to check admin status: %w", err)
	}

	var profiles []Profile
	if isAdmin {
		profiles = append(profiles, Profile{
			GUID:  FleetProfileGUID,
			Name:  "All Devices",
			Owner: "",
			Rule:  RuleRead,
		})
	}

	// Administrators see every support group; everyone else sees their own.
	var rows pgx.Rows
	if isAdmin {
		rows, err = s.db.Query(ctx, `
			SELECT sg.id, sg.name FROM support_groups sg ORDER BY sg.name
		`)
	} else {
		rows, err = s.db.Query(ctx, `
			SELECT sg.id, sg.name
			FROM support_groups sg
			JOIN user_support_groups usg ON sg.id = usg.support_group_id
			WHERE usg.user_id = $1
			ORDER BY sg.name
		`, userID)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list shared address books: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var p Profile
		if err := rows.Scan(&p.GUID, &p.Name); err != nil {
			return nil, 0, fmt.Errorf("failed to scan shared address book: %w", err)
		}
		p.Rule = RuleRead
		profiles = append(profiles, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to read shared address books: %w", err)
	}

	// The set is small and assembled from two sources, so it is paged in memory
	// rather than in SQL.
	total := int64(len(profiles))
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}

	return profiles[offset:end], total, nil
}

// ResolveScope decides what a guid means for this caller, and refuses guids
// they may not see. Every request that names a book goes through here first.
func (s *Service) ResolveScope(ctx context.Context, userID int64, guid uuid.UUID) (Scope, error) {
	var personal bool
	err := s.db.QueryRow(ctx, `
		SELECT personal FROM ab_profiles WHERE id = $1 AND owner_user_id = $2
	`, guid, userID).Scan(&personal)
	if err == nil {
		rule := RuleFullControl
		if !personal {
			rule = RuleRead
		}
		return Scope{GUID: guid, Personal: personal, Rule: rule}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Scope{}, fmt.Errorf("failed to look up address book: %w", err)
	}

	isAdmin, err := s.access.IsAdminOrManager(ctx, userID)
	if err != nil {
		return Scope{}, fmt.Errorf("failed to check admin status: %w", err)
	}

	if guid == FleetProfileGUID {
		if !isAdmin {
			return Scope{}, ErrForbidden
		}
		return Scope{GUID: guid, Fleet: true, Rule: RuleRead}, nil
	}

	// A support group's shared book: visible to its members, and to admins.
	var visible bool
	err = s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM support_groups sg
			WHERE sg.id = $1
			  AND ($3 OR EXISTS (
			      SELECT 1 FROM user_support_groups usg
			      WHERE usg.support_group_id = sg.id AND usg.user_id = $2
			  ))
		)
	`, guid, userID, isAdmin).Scan(&visible)
	if err != nil {
		return Scope{}, fmt.Errorf("failed to check support group access: %w", err)
	}
	if !visible {
		return Scope{}, ErrForbidden
	}

	group := guid
	return Scope{GUID: guid, GroupID: &group, Rule: RuleRead}, nil
}

// requireWritable resolves a guid and rejects the projections.
func (s *Service) requireWritable(ctx context.Context, userID int64, guid uuid.UUID) (Scope, error) {
	scope, err := s.ResolveScope(ctx, userID, guid)
	if err != nil {
		return Scope{}, err
	}
	if !scope.Writable() {
		return Scope{}, ErrReadOnly
	}
	return scope, nil
}
