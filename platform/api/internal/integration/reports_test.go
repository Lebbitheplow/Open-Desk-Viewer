package integration

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/OpenDeskViewer/platform/api/internal/apiv1"
)

// Reporting, item 6.7.
//
// These check the two things a report can be wrong about in a way nobody
// notices: whether the numbers are real, and whether the CSV a recipient opens
// says the same thing as the JSON the portal renders.

func decodeCSV(t *testing.T, body string) [][]string {
	t.Helper()

	records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v\n%s", err, body)
	}
	return records
}

type reportJSON struct {
	Report  string           `json:"report"`
	Columns []string         `json:"columns"`
	Total   int              `json:"total"`
	Data    []map[string]any `json:"data"`
}

func runReport(t *testing.T, s *v1Server, name, query string) reportJSON {
	t.Helper()

	w := s.do(t, http.MethodGet, "/api/v1/reports/"+name+query, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%s got %d: %s", name, w.Code, w.Body.String())
	}
	var out reportJSON
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s did not return JSON: %v", name, err)
	}
	return out
}

// The inventory reports every device, and the column an inventory is usually
// run to find: a device that exists and holds no credential.
func TestDeviceInventoryReport(t *testing.T) {
	f := newFixture(t)
	s := newDeviceServer(t, f)
	admin := newV1Server(t, f, f.adminID)

	// One enrolled device among the five seeded ones, which are not.
	const id = "960000001"
	enrollDevice(t, f, s, id)

	report := runReport(t, admin, "device-inventory", "")
	if report.Total < 6 {
		t.Fatalf("the inventory has %d rows; five devices are seeded and one was enrolled", report.Total)
	}

	enrolled, unenrolled := 0, 0
	for _, row := range report.Data {
		if row["enrolled"] == true {
			enrolled++
		} else {
			unenrolled++
		}
	}
	if enrolled != 1 {
		t.Errorf("%d devices report as enrolled, want the one that redeemed a token", enrolled)
	}
	if unenrolled < 5 {
		t.Errorf("%d devices report as unenrolled, want the five seeded ones", unenrolled)
	}
}

// The CSV and the JSON are two renderings of one query, so they have to agree.
// A report where the download says something different from the screen is worse
// than no download.
func TestCSVAndJSONAgree(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	for _, name := range []string{"device-inventory", "session-history", "access-review"} {
		t.Run(name, func(t *testing.T) {
			asJSON := runReport(t, admin, name, "")

			w := admin.do(t, http.MethodGet, "/api/v1/reports/"+name+"?format=csv", "")
			if w.Code != http.StatusOK {
				t.Fatalf("csv got %d: %s", w.Code, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
				t.Errorf("Content-Type = %q", ct)
			}
			if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
				t.Errorf("Content-Disposition = %q; a report is a download", cd)
			}

			records := decodeCSV(t, w.Body.String())
			if len(records) == 0 {
				t.Fatal("the CSV has no header row")
			}
			if strings.Join(records[0], ",") != strings.Join(asJSON.Columns, ",") {
				t.Errorf("the CSV header is %v and the JSON columns are %v", records[0], asJSON.Columns)
			}
			if len(records)-1 != asJSON.Total {
				t.Errorf("the CSV has %d data rows and the JSON reports %d", len(records)-1, asJSON.Total)
			}
		})
	}
}

// The access review has to agree with what the API enforces. A review derived
// from the group tables by hand is how somebody signs off on a picture that
// differs from reality.
func TestAccessReviewMatchesWhatIsEnforced(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin := newV1Server(t, f, f.adminID)

	report := runReport(t, admin, "access-review", "")

	// tech1 reaches deviceGroup1's three active devices; tech2 reaches
	// deviceGroup2's one. Those are the numbers the resolver produces.
	byEmail := map[string]float64{}
	for _, row := range report.Data {
		email, _ := row["email"].(string)
		devices, _ := row["devices"].(float64)
		byEmail[email] += devices
	}

	for _, tc := range []struct {
		email  string
		userID int64
	}{
		{"tech1@example.com", f.tech1ID},
		{"tech2@example.com", f.tech2ID},
	} {
		enforced, err := newResolver(f).GetAccessibleDevices(ctx, tc.userID)
		if err != nil {
			t.Fatal(err)
		}
		if got := int(byEmail[tc.email]); got != len(enforced) {
			t.Errorf("the review says %s reaches %d devices; the resolver grants %d",
				tc.email, got, len(enforced))
		}
	}
}

// Running a report is evidence, so producing it is itself recorded.
func TestRunningAReportIsAudited(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	runReport(t, admin, "access-review", "")

	var events int
	if err := f.db.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM audit_events
		WHERE event_type = 'report.generated'
		  AND metadata->>'resource_id' = 'access-review'
		  AND user_id = $1
	`, f.adminID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("%d report.generated events, want 1", events)
	}
}

// A Read Only auditor is exactly who runs these; a technician is not.
func TestReportAccess(t *testing.T) {
	f := newFixture(t)
	auditorID := f.newUser(t, "sub-reviewer", "reviewer@example.com", apiv1.RoleReadOnly)
	auditor := newV1Server(t, f, auditorID)
	tech := newV1Server(t, f, f.tech1ID)

	if w := auditor.do(t, http.MethodGet, "/api/v1/reports/access-review", ""); w.Code != http.StatusOK {
		t.Errorf("the auditor got %d: %s", w.Code, w.Body.String())
	}
	if w := tech.do(t, http.MethodGet, "/api/v1/reports/access-review", ""); w.Code != http.StatusForbidden {
		t.Errorf("the technician got %d, want 403", w.Code)
	}
}

func TestUnknownReportIs404(t *testing.T) {
	f := newFixture(t)
	admin := newV1Server(t, f, f.adminID)

	w := admin.do(t, http.MethodGet, "/api/v1/reports/everything", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("an unknown report got %d, want 404: %s", w.Code, w.Body.String())
	}
}
