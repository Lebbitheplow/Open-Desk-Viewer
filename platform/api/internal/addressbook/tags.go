package addressbook

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Tag is a label in an address book, with the client's ARGB colour value.
type Tag struct {
	Name  string
	Color int64
}

// ListTags returns a book's tags. Shared books are projections of the fleet and
// carry no tags, so they return an empty list rather than an error.
func (s *Service) ListTags(ctx context.Context, userID int64, guid uuid.UUID) ([]Tag, error) {
	scope, err := s.ResolveScope(ctx, userID, guid)
	if err != nil {
		return nil, err
	}
	if !scope.Personal {
		return []Tag{}, nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT name, color FROM ab_tags WHERE profile_id = $1 ORDER BY name
	`, scope.GUID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch address book tags: %w", err)
	}
	defer rows.Close()

	tags := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.Name, &t.Color); err != nil {
			return nil, fmt.Errorf("failed to scan address book tag: %w", err)
		}
		tags = append(tags, t)
	}

	return tags, rows.Err()
}

// AddTag creates a tag, or recolours it if the client re-adds one it already has.
func (s *Service) AddTag(ctx context.Context, userID int64, guid uuid.UUID, t Tag) error {
	scope, err := s.requireWritable(ctx, userID, guid)
	if err != nil {
		return err
	}
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("tag name is required")
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO ab_tags (profile_id, name, color)
		VALUES ($1, $2, $3)
		ON CONFLICT (profile_id, name) DO UPDATE SET color = EXCLUDED.color
	`, scope.GUID, t.Name, t.Color)
	if err != nil {
		return fmt.Errorf("failed to add address book tag: %w", err)
	}

	return nil
}

// RenameTag renames a tag and rewrites it across every peer that carries it.
// Both statements run in one transaction so a peer can never keep a tag name
// that no longer exists.
func (s *Service) RenameTag(ctx context.Context, userID int64, guid uuid.UUID, oldName, newName string) error {
	scope, err := s.requireWritable(ctx, userID, guid)
	if err != nil {
		return err
	}
	if strings.TrimSpace(oldName) == "" || strings.TrimSpace(newName) == "" {
		return fmt.Errorf("both the old and new tag names are required")
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE ab_tags SET name = $3 WHERE profile_id = $1 AND name = $2
	`, scope.GUID, oldName, newName)
	if err != nil {
		return fmt.Errorf("failed to rename address book tag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	_, err = tx.Exec(ctx, `
		UPDATE ab_peers SET tags = array_replace(tags, $2, $3), updated_at = now()
		WHERE profile_id = $1 AND $2 = ANY(tags)
	`, scope.GUID, oldName, newName)
	if err != nil {
		return fmt.Errorf("failed to rewrite the tag on peers: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit tag rename: %w", err)
	}

	return nil
}

// UpdateTagColor changes a tag's colour.
func (s *Service) UpdateTagColor(ctx context.Context, userID int64, guid uuid.UUID, t Tag) error {
	scope, err := s.requireWritable(ctx, userID, guid)
	if err != nil {
		return err
	}

	tag, err := s.db.Exec(ctx, `
		UPDATE ab_tags SET color = $3 WHERE profile_id = $1 AND name = $2
	`, scope.GUID, t.Name, t.Color)
	if err != nil {
		return fmt.Errorf("failed to update address book tag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

// DeleteTags removes tags and strips them from every peer that carries them.
func (s *Service) DeleteTags(ctx context.Context, userID int64, guid uuid.UUID, names []string) error {
	scope, err := s.requireWritable(ctx, userID, guid)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}

	tx, err := s.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		DELETE FROM ab_tags WHERE profile_id = $1 AND name = ANY($2)
	`, scope.GUID, names)
	if err != nil {
		return fmt.Errorf("failed to delete address book tags: %w", err)
	}

	for _, name := range names {
		_, err = tx.Exec(ctx, `
			UPDATE ab_peers SET tags = array_remove(tags, $2), updated_at = now()
			WHERE profile_id = $1 AND $2 = ANY(tags)
		`, scope.GUID, name)
		if err != nil {
			return fmt.Errorf("failed to strip the tag from peers: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit tag deletion: %w", err)
	}

	return nil
}
