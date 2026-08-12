package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/addressbook"
	"github.com/google/uuid"
)

func newAbService(f *fixture) *addressbook.Service {
	return addressbook.NewService(f.db, newResolver(f))
}

func TestPersonalProfileIsCreatedOnceAndReused(t *testing.T) {
	f := newFixture(t)
	svc := newAbService(f)
	ctx := context.Background()

	first, err := svc.ResolvePersonalProfile(ctx, f.tech1ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first.GUID == uuid.Nil {
		t.Fatal("expected a guid")
	}
	if first.Rule != addressbook.RuleFullControl {
		t.Errorf("expected the personal book to be writable, got rule %d", first.Rule)
	}

	// The partial unique index has to make this idempotent: the client calls
	// /api/ab/personal on every sign-in.
	second, err := svc.ResolvePersonalProfile(ctx, f.tech1ID)
	if err != nil {
		t.Fatalf("unexpected error on the second call: %v", err)
	}
	if second.GUID != first.GUID {
		t.Errorf("expected the same guid, got %s then %s", first.GUID, second.GUID)
	}

	var count int
	if err := f.db.QueryRow(ctx, `SELECT COUNT(*) FROM ab_profiles WHERE owner_user_id = $1`, f.tech1ID).Scan(&count); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly one personal book, got %d", count)
	}
}

func TestSharedProfilesAreScopedToSupportGroups(t *testing.T) {
	f := newFixture(t)
	svc := newAbService(f)
	ctx := context.Background()

	tech, total, err := svc.ListSharedProfiles(ctx, f.tech1ID, 0, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 1 || len(tech) != 1 {
		t.Fatalf("expected the technician to see 1 shared book, got %d (total %d)", len(tech), total)
	}
	if tech[0].Name != "Team One" {
		t.Errorf("expected 'Team One', got %q", tech[0].Name)
	}
	if tech[0].Rule != addressbook.RuleRead {
		t.Errorf("expected a shared book to be read-only, got rule %d", tech[0].Rule)
	}

	// An admin gets both support groups plus the fleet-wide book.
	admin, total, err := svc.ListSharedProfiles(ctx, f.adminID, 0, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 || len(admin) != 3 {
		t.Fatalf("expected the admin to see 3 shared books, got %d (total %d)", len(admin), total)
	}
	if admin[0].GUID != addressbook.FleetProfileGUID {
		t.Errorf("expected the fleet book first, got %s", admin[0].GUID)
	}
}

func TestSharedProfilesPaginate(t *testing.T) {
	f := newFixture(t)
	svc := newAbService(f)
	ctx := context.Background()

	page1, total, err := svc.ListSharedProfiles(ctx, f.adminID, 0, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 || len(page1) != 2 {
		t.Fatalf("expected 2 of 3 on the first page, got %d of %d", len(page1), total)
	}

	page2, _, err := svc.ListSharedProfiles(ctx, f.adminID, 2, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page2) != 1 {
		t.Errorf("expected 1 on the second page, got %d", len(page2))
	}

	// Past the end must be empty rather than an error or a panic.
	page3, _, err := svc.ListSharedProfiles(ctx, f.adminID, 10, 2)
	if err != nil {
		t.Fatalf("unexpected error past the end: %v", err)
	}
	if len(page3) != 0 {
		t.Errorf("expected no rows past the end, got %d", len(page3))
	}
}

func TestScopeRefusesAnotherGroupsBook(t *testing.T) {
	f := newFixture(t)
	svc := newAbService(f)
	ctx := context.Background()

	if _, err := svc.ResolveScope(ctx, f.tech1ID, f.group2); !errors.Is(err, addressbook.ErrForbidden) {
		t.Errorf("expected ErrForbidden for another group's book, got %v", err)
	}

	if _, err := svc.ResolveScope(ctx, f.tech1ID, addressbook.FleetProfileGUID); !errors.Is(err, addressbook.ErrForbidden) {
		t.Errorf("expected ErrForbidden for the fleet book, got %v", err)
	}

	scope, err := svc.ResolveScope(ctx, f.adminID, addressbook.FleetProfileGUID)
	if err != nil {
		t.Fatalf("expected the admin to reach the fleet book, got %v", err)
	}
	if !scope.Fleet {
		t.Error("expected the fleet scope")
	}

	// Another user's personal book is not addressable at all.
	personal, err := svc.ResolvePersonalProfile(ctx, f.tech2ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := svc.ResolveScope(ctx, f.tech1ID, personal.GUID); !errors.Is(err, addressbook.ErrForbidden) {
		t.Errorf("expected ErrForbidden for another user's personal book, got %v", err)
	}
}

// A shared book is a projection of the fleet, so the devices in it come from
// the support group rather than from stored rows.
func TestSharedBookListsTheGroupsDevices(t *testing.T) {
	f := newFixture(t)
	svc := newAbService(f)
	ctx := context.Background()

	list, total, err := svc.ListPeers(ctx, f.tech1ID, f.group1, 0, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 || len(list) != 3 {
		t.Fatalf("expected 3 devices, got %d (total %d)", len(list), total)
	}

	byID := map[string]addressbook.Peer{}
	for _, p := range list {
		byID[p.ID] = p
	}
	got, ok := byID["100000001"]
	if !ok {
		t.Fatal("expected the device to appear under its RustDesk id")
	}
	if got.Alias != "acme-hq-01" {
		t.Errorf("expected the managed name as the alias, got %q", got.Alias)
	}
	if got.DeviceGroupName != "Acme HQ" {
		t.Errorf("expected the device group name, got %q", got.DeviceGroupName)
	}
	if got.LoginName != "Acme" {
		t.Errorf("expected the customer name, got %q", got.LoginName)
	}
	if got.Platform != "Windows" {
		t.Errorf("expected the OS to be normalised to Windows, got %q", got.Platform)
	}

	// The unclaimed device must not leak into an address book.
	if _, found := byID["100000005"]; found {
		t.Error("expected a DISCOVERED device to stay out of the address book")
	}
}

func TestFleetBookListsEveryActiveDevice(t *testing.T) {
	f := newFixture(t)
	svc := newAbService(f)

	list, total, err := svc.ListPeers(context.Background(), f.adminID, addressbook.FleetProfileGUID, 0, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 4 || len(list) != 4 {
		t.Errorf("expected the fleet book to hold 4 active devices, got %d (total %d)", len(list), total)
	}
}

func TestSharedBooksAreReadOnly(t *testing.T) {
	f := newFixture(t)
	svc := newAbService(f)
	ctx := context.Background()

	err := svc.AddPeer(ctx, f.tech1ID, f.group1, addressbook.Peer{ID: "999"})
	if !errors.Is(err, addressbook.ErrReadOnly) {
		t.Errorf("expected ErrReadOnly when adding to a shared book, got %v", err)
	}

	err = svc.AddTag(ctx, f.tech1ID, f.group1, addressbook.Tag{Name: "x"})
	if !errors.Is(err, addressbook.ErrReadOnly) {
		t.Errorf("expected ErrReadOnly when tagging a shared book, got %v", err)
	}

	// Listing tags on a shared book is not an error, just empty.
	tags, err := svc.ListTags(ctx, f.tech1ID, f.group1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected no tags on a shared book, got %d", len(tags))
	}
}

func TestPersonalPeerLifecycle(t *testing.T) {
	f := newFixture(t)
	svc := newAbService(f)
	ctx := context.Background()

	profile, err := svc.ResolvePersonalProfile(ctx, f.tech1ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	guid := profile.GUID

	err = svc.AddPeer(ctx, f.tech1ID, guid, addressbook.Peer{
		ID:       "200000001",
		Hostname: "laptop",
		Platform: "Linux",
		Alias:    "My Laptop",
		Hash:     "secret-hash",
		Tags:     []string{"work"},
	})
	if err != nil {
		t.Fatalf("failed to add a peer: %v", err)
	}

	list, total, err := svc.ListPeers(ctx, f.tech1ID, guid, 0, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected 1 peer, got %d (total %d)", len(list), total)
	}
	if list[0].Alias != "My Laptop" || list[0].Hash != "secret-hash" {
		t.Errorf("expected the stored peer back, got %+v", list[0])
	}
	if len(list[0].Tags) != 1 || list[0].Tags[0] != "work" {
		t.Errorf("expected the tags array to round-trip, got %v", list[0].Tags)
	}

	// Re-adding the same id updates rather than failing.
	err = svc.AddPeer(ctx, f.tech1ID, guid, addressbook.Peer{ID: "200000001", Alias: "Renamed"})
	if err != nil {
		t.Fatalf("failed to re-add a peer: %v", err)
	}
	list, _, _ = svc.ListPeers(ctx, f.tech1ID, guid, 0, 100)
	if len(list) != 1 || list[0].Alias != "Renamed" {
		t.Errorf("expected the re-add to update in place, got %+v", list)
	}

	// A partial update must leave everything it omits alone.
	note := "on loan"
	err = svc.UpdatePeer(ctx, f.tech1ID, guid, addressbook.PeerUpdate{ID: "200000001", Note: &note})
	if err != nil {
		t.Fatalf("failed to update a peer: %v", err)
	}
	list, _, _ = svc.ListPeers(ctx, f.tech1ID, guid, 0, 100)
	if list[0].Note != "on loan" {
		t.Errorf("expected the note to be set, got %q", list[0].Note)
	}
	if list[0].Alias != "Renamed" {
		t.Errorf("expected the omitted alias to survive the update, got %q", list[0].Alias)
	}

	if err := svc.UpdatePeer(ctx, f.tech1ID, guid, addressbook.PeerUpdate{ID: "nope", Note: &note}); !errors.Is(err, addressbook.ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown peer, got %v", err)
	}

	if err := svc.DeletePeers(ctx, f.tech1ID, guid, []string{"200000001"}); err != nil {
		t.Fatalf("failed to delete a peer: %v", err)
	}
	_, total, _ = svc.ListPeers(ctx, f.tech1ID, guid, 0, 100)
	if total != 0 {
		t.Errorf("expected the peer to be gone, got a total of %d", total)
	}
}

func TestPersonalTagLifecycle(t *testing.T) {
	f := newFixture(t)
	svc := newAbService(f)
	ctx := context.Background()

	profile, err := svc.ResolvePersonalProfile(ctx, f.tech1ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	guid := profile.GUID

	// Colour is a Flutter ARGB value, which overflows a 32-bit column.
	const argb = int64(4294198070)
	if err := svc.AddTag(ctx, f.tech1ID, guid, addressbook.Tag{Name: "urgent", Color: argb}); err != nil {
		t.Fatalf("failed to add a tag: %v", err)
	}

	tags, err := svc.ListTags(ctx, f.tech1ID, guid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 1 || tags[0].Name != "urgent" || tags[0].Color != argb {
		t.Fatalf("expected the tag to round-trip, got %+v", tags)
	}

	err = svc.AddPeer(ctx, f.tech1ID, guid, addressbook.Peer{ID: "300000001", Tags: []string{"urgent"}})
	if err != nil {
		t.Fatalf("failed to add a peer: %v", err)
	}

	// Renaming has to rewrite the tag on every peer that carries it, or the
	// peer keeps a name that no longer exists.
	if err := svc.RenameTag(ctx, f.tech1ID, guid, "urgent", "critical"); err != nil {
		t.Fatalf("failed to rename a tag: %v", err)
	}
	tags, _ = svc.ListTags(ctx, f.tech1ID, guid)
	if len(tags) != 1 || tags[0].Name != "critical" {
		t.Errorf("expected the tag to be renamed, got %+v", tags)
	}
	list, _, _ := svc.ListPeers(ctx, f.tech1ID, guid, 0, 100)
	if len(list) != 1 || len(list[0].Tags) != 1 || list[0].Tags[0] != "critical" {
		t.Errorf("expected the rename to reach the peer, got %v", list[0].Tags)
	}

	if err := svc.UpdateTagColor(ctx, f.tech1ID, guid, addressbook.Tag{Name: "critical", Color: 42}); err != nil {
		t.Fatalf("failed to recolour a tag: %v", err)
	}
	tags, _ = svc.ListTags(ctx, f.tech1ID, guid)
	if tags[0].Color != 42 {
		t.Errorf("expected the colour to change, got %d", tags[0].Color)
	}

	if err := svc.UpdateTagColor(ctx, f.tech1ID, guid, addressbook.Tag{Name: "ghost"}); !errors.Is(err, addressbook.ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown tag, got %v", err)
	}

	// Deleting has to strip the tag from peers too.
	if err := svc.DeleteTags(ctx, f.tech1ID, guid, []string{"critical"}); err != nil {
		t.Fatalf("failed to delete a tag: %v", err)
	}
	tags, _ = svc.ListTags(ctx, f.tech1ID, guid)
	if len(tags) != 0 {
		t.Errorf("expected the tag to be gone, got %+v", tags)
	}
	list, _, _ = svc.ListPeers(ctx, f.tech1ID, guid, 0, 100)
	if len(list[0].Tags) != 0 {
		t.Errorf("expected the tag to be stripped from the peer, got %v", list[0].Tags)
	}
}

func TestPersonalPeersPaginate(t *testing.T) {
	f := newFixture(t)
	svc := newAbService(f)
	ctx := context.Background()

	profile, err := svc.ResolvePersonalProfile(ctx, f.tech1ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, id := range []string{"400000001", "400000002", "400000003"} {
		if err := svc.AddPeer(ctx, f.tech1ID, profile.GUID, addressbook.Peer{ID: id}); err != nil {
			t.Fatalf("failed to add a peer: %v", err)
		}
	}

	page, total, err := svc.ListPeers(ctx, f.tech1ID, profile.GUID, 2, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 {
		t.Errorf("expected a total of 3, got %d", total)
	}
	if len(page) != 1 {
		t.Errorf("expected 1 row on the last page, got %d", len(page))
	}
}
