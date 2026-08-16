package integration

import (
	"context"
	"testing"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/peers"
	"github.com/google/uuid"
)

func newResolver(f *fixture) *access.SQLResolver {
	return access.NewSQLResolver(f.db, &config.Config{})
}

func TestIsAdminOrManager(t *testing.T) {
	f := newFixture(t)
	r := newResolver(f)
	ctx := context.Background()

	isAdmin, err := r.IsAdminOrManager(ctx, f.adminID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isAdmin {
		t.Error("expected the Administrator to be recognised as admin")
	}

	isAdmin, err = r.IsAdminOrManager(ctx, f.tech1ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isAdmin {
		t.Error("expected a Technician not to be recognised as admin")
	}
}

// This is the query that used to return an empty slice for every caller.
func TestGetAccessibleDevices(t *testing.T) {
	f := newFixture(t)
	r := newResolver(f)
	ctx := context.Background()

	tech1, err := r.GetAccessibleDevices(ctx, f.tech1ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tech1) != 3 {
		t.Fatalf("expected the technician to reach 3 devices, got %d", len(tech1))
	}
	if !contains(tech1, f.device1) || !contains(tech1, f.device2) || !contains(tech1, f.device3) {
		t.Error("expected the technician to reach every device in their own support group")
	}
	if contains(tech1, f.device4) {
		t.Error("expected the technician not to reach another group's device")
	}
	if contains(tech1, f.discovered) {
		t.Error("expected a DISCOVERED device to stay invisible")
	}

	tech2, err := r.GetAccessibleDevices(ctx, f.tech2ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tech2) != 1 || !contains(tech2, f.device4) {
		t.Errorf("expected the second technician to reach exactly their own device, got %d", len(tech2))
	}

	admin, err := r.GetAccessibleDevices(ctx, f.adminID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(admin) != 4 {
		t.Errorf("expected the admin to reach all 4 active devices, got %d", len(admin))
	}
	if contains(admin, f.discovered) {
		t.Error("expected a DISCOVERED device to stay invisible even to an admin")
	}
}

func TestCanAccessDevice(t *testing.T) {
	f := newFixture(t)
	r := newResolver(f)
	ctx := context.Background()

	cases := []struct {
		name   string
		userID int64
		device uuid.UUID
		want   bool
	}{
		{"technician, own group", f.tech1ID, f.device1, true},
		{"technician, other group", f.tech1ID, f.device4, false},
		{"technician, discovered device", f.tech1ID, f.discovered, false},
		{"admin, any group", f.adminID, f.device4, true},
		{"admin, discovered device", f.adminID, f.discovered, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.CanAccessDevice(ctx, tc.userID, tc.device)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

// The paginated variant used to pass the page size where the query expected the
// user id, so a technician was filtered by the wrong value entirely.
func TestGetAccessibleDevicesPaginated(t *testing.T) {
	f := newFixture(t)
	r := newResolver(f)
	ctx := context.Background()

	page1, total, err := r.GetAccessibleDevicesPaginated(ctx, f.tech1ID, 0, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 {
		t.Errorf("expected a total of 3, got %d", total)
	}
	if len(page1) != 2 {
		t.Errorf("expected 2 rows on the first page, got %d", len(page1))
	}

	page2, total, err := r.GetAccessibleDevicesPaginated(ctx, f.tech1ID, 2, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 {
		t.Errorf("expected a total of 3, got %d", total)
	}
	if len(page2) != 1 {
		t.Errorf("expected 1 row on the second page, got %d", len(page2))
	}

	// The two pages together must be the whole set, with no overlap.
	seen := map[uuid.UUID]bool{}
	for _, id := range append(append([]uuid.UUID{}, page1...), page2...) {
		if seen[id] {
			t.Error("expected no device to appear on both pages")
		}
		seen[id] = true
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 distinct devices across the pages, got %d", len(seen))
	}
}

// ListAccessiblePeers used to return no rows at all, and its technician query
// referenced columns that do not exist on devices.
func TestListAccessiblePeers(t *testing.T) {
	f := newFixture(t)
	svc := peers.NewService(f.db, newResolver(f))
	ctx := context.Background()

	list, total, err := svc.ListAccessiblePeers(ctx, f.tech1ID, 0, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 {
		t.Errorf("expected a total of 3, got %d", total)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 peers, got %d", len(list))
	}

	// One row per device even though the device is in a group, and the joined
	// columns have to actually resolve.
	first := list[0]
	if first.Name == "" {
		t.Error("expected the device name to be populated")
	}
	if first.GroupName == nil || *first.GroupName != "Acme HQ" {
		t.Errorf("expected the device group name, got %v", first.GroupName)
	}
	if first.UserName == nil || *first.UserName != "Acme" {
		t.Errorf("expected the customer name, got %v", first.UserName)
	}
	if first.State != "ACTIVE" {
		t.Errorf("expected an ACTIVE device, got %q", first.State)
	}

	admin, total, err := svc.ListAccessiblePeers(ctx, f.adminID, 0, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 4 || len(admin) != 4 {
		t.Errorf("expected the admin to see 4 peers, got %d (total %d)", len(admin), total)
	}
}

func TestListAccessiblePeersPaginates(t *testing.T) {
	f := newFixture(t)
	svc := peers.NewService(f.db, newResolver(f))
	ctx := context.Background()

	page, total, err := svc.ListAccessiblePeers(ctx, f.tech1ID, 1, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 {
		t.Errorf("expected the total to count every accessible device, got %d", total)
	}
	if len(page) != 2 {
		t.Errorf("expected the page size to be honoured, got %d rows", len(page))
	}
}

func TestListPeerIDsAccessible(t *testing.T) {
	f := newFixture(t)
	svc := peers.NewService(f.db, newResolver(f))
	ctx := context.Background()

	ids, err := svc.ListPeerIDsAccessible(ctx, f.tech1ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("expected 3 accessible peer ids, got %d", len(ids))
	}
}

func TestListDeviceGroups(t *testing.T) {
	f := newFixture(t)
	svc := peers.NewService(f.db, newResolver(f))

	// Call with pagination params (current=1, pageSize=100)
	groups, total, err := svc.ListDeviceGroups(context.Background(), f.tech1ID, 1, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 device group, got %d", len(groups))
	}
	if groups[0].Name != "Acme HQ" {
		t.Errorf("expected 'Acme HQ', got %q", groups[0].Name)
	}
	if total != 1 {
		t.Fatalf("expected total=1, got %d", total)
	}
}

// A freshly registered device has NULL last_seen_at, client_version, os and
// hostname. Those are scanned into non-pointer Go fields, so before the select
// list used COALESCE this errored, which meant /api/heartbeat registered a
// device on its first call and then failed on that call and every later one.
func TestGetDeviceByRustdeskIDReadsAFreshlyRegisteredDevice(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	svc := fleet.NewService(f.db, &config.Config{})

	if _, err := svc.RegisterDevice(ctx, fleet.Device{
		RustdeskID: "900000001",
		UUID:       "uuid-900000001",
		State:      "DISCOVERED",
	}); err != nil {
		t.Fatalf("failed to register: %v", err)
	}

	device, err := svc.GetDeviceByRustdeskID(ctx, "900000001")
	if err != nil {
		t.Fatalf("failed to read back a device that was just registered: %v", err)
	}
	// Compared as an instant, not by year: the epoch renders as 1969 in any
	// timezone west of UTC.
	if !device.LastSeenAt.Equal(time.Unix(0, 0)) {
		t.Errorf("LastSeenAt = %v, want the epoch for a device never seen", device.LastSeenAt)
	}
}
