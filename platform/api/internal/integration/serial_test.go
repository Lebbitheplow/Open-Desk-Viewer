package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The device identification workflow: a device names itself after the serial it
// reports, a technician finds it by that serial, and an external system that
// knows the serial can rename the device to whatever the customer calls it.
//
// Every piece of this existed before and none of it was connected.
// fleet.GenerateDeviceName, fleet.SearchDevices and DEVICE_NAME_TEMPLATE had
// zero callers, devices.serial_number was never written, and the client sent no
// serial at all, so every enrolled device was named its nine-digit RustDesk id.

// The fixture's customer is 'Acme' and the token names no location, so
// {customer}-{location}-{serial} renders as "Acme--<serial>".
func TestEnrollmentNamesTheDeviceFromItsSerial(t *testing.T) {
	f := newFixture(t)
	devices := newDeviceServer(t, f)

	token := f.issueToken(t, nil, nil)
	w := devices.post(t, "/api/enroll", fmt.Sprintf(
		`{"token":%q,"id":"760001001","hostname":"tablet-7","os":"Android 13","serial":"SN-ABC-001"}`,
		token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	deviceID := decodeJSON(t, w)["device_id"].(string)

	var name string
	var serial *string
	if err := f.db.QueryRow(context.Background(),
		`SELECT name, serial_number FROM devices WHERE id = $1`, deviceID).Scan(&name, &serial); err != nil {
		t.Fatalf("failed to read the device: %v", err)
	}

	if serial == nil || *serial != "SN-ABC-001" {
		t.Errorf("serial_number = %v, want SN-ABC-001. Nothing wrote this column before.", serial)
	}
	// The name has to carry the serial. Which template a deployment uses is its
	// own business; that the serial is in the name is the requirement.
	if name == "760001001" {
		t.Errorf("device is named its RustDesk id (%q), which is the thing a "+
			"technician cannot search for", name)
	}
	if !strings.Contains(name, "SN-ABC-001") {
		t.Errorf("name = %q, want it to carry the serial SN-ABC-001", name)
	}
}

// Without a serial there is nothing better than the hostname. This is the
// desktop case and it must keep working.
func TestEnrollmentWithoutASerialFallsBackToHostname(t *testing.T) {
	f := newFixture(t)
	devices := newDeviceServer(t, f)

	token := f.issueToken(t, nil, nil)
	w := devices.post(t, "/api/enroll", fmt.Sprintf(
		`{"token":%q,"id":"760001002","hostname":"workstation-3"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	deviceID := decodeJSON(t, w)["device_id"].(string)

	var name string
	if err := f.db.QueryRow(context.Background(),
		`SELECT name FROM devices WHERE id = $1`, deviceID).Scan(&name); err != nil {
		t.Fatalf("failed to read the device: %v", err)
	}
	if name != "workstation-3" {
		t.Errorf("name = %q, want the hostname", name)
	}
}

// The whole external-system workflow in one test: find by serial, rename, and
// confirm the name is what both surfaces now report.
//
// Before this, an external system could do neither half. There was no way to
// turn a serial into a device id, because PATCH keys on our internal uuid and
// nothing searched serial_number.
func TestExternalSystemFindsBySerialAndRenames(t *testing.T) {
	f := newFixture(t)
	devices := newDeviceServer(t, f)
	portal := newV1Server(t, f, f.adminID)

	token := f.issueToken(t, nil, nil)
	w := devices.post(t, "/api/enroll", fmt.Sprintf(
		`{"token":%q,"id":"760001003","hostname":"tablet-9","serial":"SN-XYZ-042"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}

	// Step one: the caller knows a serial and nothing else.
	found := portal.do(t, http.MethodGet, "/api/v1/devices?serial=SN-XYZ-042", "")
	list := decodeList(t, found)
	if list.Total != 1 {
		t.Fatalf("serial lookup returned %d devices, want exactly the one", list.Total)
	}
	var device struct {
		ID           string  `json:"id"`
		Name         string  `json:"name"`
		SerialNumber *string `json:"serial_number"`
	}
	if err := json.Unmarshal(list.Data[0], &device); err != nil {
		t.Fatalf("failed to decode the device: %v", err)
	}
	if device.SerialNumber == nil || *device.SerialNumber != "SN-XYZ-042" {
		t.Errorf("serial_number = %v, want it returned in the list so the caller "+
			"can confirm it matched the right device", device.SerialNumber)
	}

	// Step two: rename it to whatever the customer's own system calls it.
	renamed := portal.do(t, http.MethodPatch, "/api/v1/devices/"+device.ID,
		`{"name":"Ward 3 Bedside 12"}`)
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename got %d: %s", renamed.Code, renamed.Body.String())
	}

	// Step three: a technician searching the customer's name finds it.
	byName := portal.do(t, http.MethodGet, "/api/v1/devices?q=Ward+3", "")
	if got := decodeList(t, byName).Total; got != 1 {
		t.Errorf("searching the assigned name returned %d devices, want 1", got)
	}
	// And the serial still finds it, because renaming does not erase identity.
	bySerial := portal.do(t, http.MethodGet, "/api/v1/devices?serial=SN-XYZ-042", "")
	if got := decodeList(t, bySerial).Total; got != 1 {
		t.Errorf("the serial no longer finds the device after a rename (%d results)", got)
	}
}

// A name assigned through the API is the customer's own identifier for the
// machine. Re-enrollment happens whenever a device is re-imaged or its token is
// re-redeemed, and regenerating the name there would silently undo the rename
// that the external system exists to perform.
func TestReenrollmentKeepsAnAPIAssignedName(t *testing.T) {
	f := newFixture(t)
	devices := newDeviceServer(t, f)
	portal := newV1Server(t, f, f.adminID)

	token := f.issueToken(t, nil, nil)
	body := fmt.Sprintf(
		`{"token":%q,"id":"760001004","hostname":"tablet-4","serial":"SN-KEEP-001"}`, token.Token)
	w := devices.post(t, "/api/enroll", body)
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	deviceID := decodeJSON(t, w)["device_id"].(string)

	renamed := portal.do(t, http.MethodPatch, "/api/v1/devices/"+deviceID,
		`{"name":"Reception iPad"}`)
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename got %d: %s", renamed.Code, renamed.Body.String())
	}

	// Re-enrol with a new serial: the serial is a fact about the hardware and
	// updates, the name is a decision and does not.
	again := devices.post(t, "/api/enroll", fmt.Sprintf(
		`{"token":%q,"id":"760001004","hostname":"tablet-4","serial":"SN-KEEP-002"}`, token.Token))
	if again.Code != http.StatusOK {
		t.Fatalf("re-enrollment got %d: %s", again.Code, again.Body.String())
	}

	var name string
	var serial *string
	if err := f.db.QueryRow(context.Background(),
		`SELECT name, serial_number FROM devices WHERE id = $1`, deviceID).Scan(&name, &serial); err != nil {
		t.Fatalf("failed to read the device: %v", err)
	}
	if name != "Reception iPad" {
		t.Errorf("name = %q, want the API-assigned name to survive re-enrollment", name)
	}
	if serial == nil || *serial != "SN-KEEP-002" {
		t.Errorf("serial_number = %v, want the updated hardware serial", serial)
	}
}

// A device that enrolled before serials existed never re-enrolls on its own, so
// enrollment alone would leave the installed fleet permanently unsearchable.
// The sysinfo upload backfills it. openapi.yaml has documented that field since
// the first spec and nothing read it.
func TestSysinfoBackfillsAMissingSerialButDoesNotOverwriteOne(t *testing.T) {
	f := newFixture(t)
	devices := newDeviceServer(t, f)

	token := f.issueToken(t, nil, nil)
	w := devices.post(t, "/api/enroll", fmt.Sprintf(
		`{"token":%q,"id":"760001005","hostname":"tablet-5"}`, token.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("enrollment got %d: %s", w.Code, w.Body.String())
	}
	enrolled := decodeJSON(t, w)
	secret := enrolled["device_token"].(string)
	deviceID := enrolled["device_id"].(string)

	serialOf := func(t *testing.T) *string {
		t.Helper()
		var serial *string
		if err := f.db.QueryRow(context.Background(),
			`SELECT serial_number FROM devices WHERE id = $1`, deviceID).Scan(&serial); err != nil {
			t.Fatalf("failed to read the device: %v", err)
		}
		return serial
	}

	if got := serialOf(t); got != nil {
		t.Fatalf("enrolled without a serial but the column holds %v", *got)
	}

	sysinfo := func(serial string) {
		t.Helper()
		body := fmt.Sprintf(
			`{"id":"760001005","uuid":"760001005","hostname":"tablet-5","device_token":%q,"serial":%q}`,
			secret, serial)
		if resp := devices.post(t, "/api/sysinfo", body); resp.Code != http.StatusOK {
			t.Fatalf("sysinfo got %d: %s", resp.Code, resp.Body.String())
		}
	}

	sysinfo("SN-BACKFILL-1")
	if got := serialOf(t); got == nil || *got != "SN-BACKFILL-1" {
		t.Fatalf("serial_number = %v, want the backfilled value", got)
	}

	// A serial that changes is hardware being swapped. Silently replacing the
	// identifier a technician searches by is worse than leaving a human to
	// correct it, so the second report must not take.
	sysinfo("SN-DIFFERENT-2")
	if got := serialOf(t); got == nil || *got != "SN-BACKFILL-1" {
		t.Errorf("serial_number = %v, want the original; sysinfo must not "+
			"overwrite a serial the device already has", got)
	}
}
