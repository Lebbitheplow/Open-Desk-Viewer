package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/monitoring"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// DeviceState represents the state of a device
type DeviceState string

const (
	StateDiscovered     DeviceState = "DISCOVERED"
	StateProvisioning   DeviceState = "PROVISIONING"
	StateActive         DeviceState = "ACTIVE"
	StateDisabled       DeviceState = "DISABLED"
	StateDecommissioned DeviceState = "DECOMMISSIONED"
)

// DeviceConnectivity represents device connectivity status
type DeviceConnectivity string

const (
	ConnectivityOnline  DeviceConnectivity = "ONLINE"
	ConnectivityStale   DeviceConnectivity = "STALE"
	ConnectivityOffline DeviceConnectivity = "OFFLINE"
	ConnectivityUnknown DeviceConnectivity = "UNKNOWN"
)

// TelemetryService handles device heartbeat and sysinfo
type Service struct {
	db  *postgres.Pool
	cfg *config.Config
	// monitoring records connectivity transitions. Nil is allowed and means
	// "change the state, keep no history": the worker must not fail to start
	// because an optional feature is unwired.
	monitoring *monitoring.Service
	fleet      *fleet.Service
}

// NewService creates a new telemetry service. The fleet service is injected for
// the reason given on enrollment.NewService.
func NewService(db *postgres.Pool, cfg *config.Config, fleetService *fleet.Service, monitor *monitoring.Service) *Service {
	return &Service{
		db:         db,
		cfg:        cfg,
		monitoring: monitor,
		fleet:      fleetService,
	}
}

// ProcessHeartbeat records liveness for a device that has already proved who it
// is.
//
// It used to take a self-asserted rustdesk_id and register any id it had not
// seen, which is how an unauthenticated endpoint became a way to insert fleet
// rows and squat device ids. The caller now authenticates the device first and
// passes its real id; an id with no credential goes to RecordObservation
// instead and enters nothing.
func (s *Service) ProcessHeartbeat(ctx context.Context, deviceID uuid.UUID) error {
	// A device that was stale or offline and is now speaking is a recovery, and
	// is recorded and notified here rather than waiting up to a minute for the
	// worker to notice. RecordHeartbeatRecovery updates last_seen_at in the same
	// statement, so the ordinary case below is skipped only when it has already
	// been done.
	if s.monitoring != nil {
		recovery, err := s.monitoring.RecordHeartbeatRecovery(ctx, deviceID)
		if err != nil {
			return fmt.Errorf("failed to update device: %w", err)
		}
		if recovery != nil {
			if err := s.monitoring.Enqueue(ctx, recovery.EventType(), recovery.Payload()); err != nil {
				// The device is up and recorded as up, which is the part that
				// matters. Losing the notification is worth a log line, not a
				// failed heartbeat.
				log.Error().Err(err).Msg("failed to enqueue a device recovery notification")
			}
			return nil
		}
	}

	_, err := s.db.Exec(ctx, `
		UPDATE devices
		SET last_seen_at = now(),
		    connectivity = $1,
		    updated_at = now()
		WHERE id = $2
	`, ConnectivityOnline, deviceID)

	if err != nil {
		return fmt.Errorf("failed to update device: %w", err)
	}

	return nil
}

// RecordObservation notes that some id tried to report in without a credential.
//
// This is the replacement for auto-registration. The sighting is worth keeping:
// it is how an operator sees a device that was provisioned but never enrolled,
// or one whose credential was revoked and is still trying. It is an upsert
// keyed by the id, so a flood costs one row rather than one row per request.
func (s *Service) RecordObservation(ctx context.Context, rustdeskID, deviceUUID, clientIP string) error {
	var ip *string
	if clientIP != "" {
		ip = &clientIP
	}
	var deviceUUIDArg *string
	if deviceUUID != "" {
		deviceUUIDArg = &deviceUUID
	}

	_, err := s.db.Exec(ctx, `
		INSERT INTO device_observations (rustdesk_id, device_uuid, client_ip)
		VALUES ($1, $2, $3)
		ON CONFLICT (rustdesk_id) DO UPDATE
		SET last_seen_at = now(),
		    sightings = device_observations.sightings + 1,
		    device_uuid = COALESCE(EXCLUDED.device_uuid, device_observations.device_uuid),
		    client_ip = COALESCE(EXCLUDED.client_ip, device_observations.client_ip)
	`, rustdeskID, deviceUUIDArg, ip)

	return err
}

// ProcessSysinfo processes device system information for an authenticated
// device.
//
// The serial is filled in only when the device has none. openapi.yaml has
// always documented this field and nothing read it, so a device enrolled before
// serials existed would otherwise stay unsearchable forever: it never
// re-enrolls, and enrollment is the only other place the serial is written.
// Not overwritten, because a serial that changes is hardware being swapped, and
// silently replacing the identifier a technician searches by is worse than
// leaving the old one for a human to correct.
func (s *Service) ProcessSysinfo(ctx context.Context, deviceID uuid.UUID, hostname, version, os, serial string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE devices
		SET hostname = COALESCE(NULLIF($1, ''), hostname),
		    client_version = COALESCE(NULLIF($2, ''), client_version),
		    os = COALESCE(NULLIF($3, ''), os),
		    serial_number = COALESCE(serial_number, NULLIF($5, '')),
		    updated_at = now()
		WHERE id = $4
	`, hostname, version, os, deviceID, serial)

	if err != nil {
		return fmt.Errorf("failed to update device sysinfo: %w", err)
	}

	return nil
}

// Strategy is the configuration the platform pushes to a device through the
// heartbeat response.
type Strategy struct {
	ConfigOptions map[string]string
	ModifiedAt    int64
}

// TakePendingDisconnects returns the connection ids the platform wants this
// device to drop, and marks them delivered in the same statement.
//
// Delivered means "handed to the device", not "the session ended". The stock
// client terminates the ids it is given (sync.rs:251-255), but nothing
// acknowledges it, so a disconnect that races a network drop is delivered to
// nobody. That is why revocation is described as taking effect at the next
// heartbeat rather than instantly, and why it is paired with rotating the
// device's password rather than relying on this alone.
func (s *Service) TakePendingDisconnects(ctx context.Context, deviceID uuid.UUID) ([]int32, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE device_disconnect_requests
		SET delivered_at = now()
		WHERE device_id = $1 AND delivered_at IS NULL
		RETURNING conn_id
	`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Non-nil, because the client parses this field and an explicit empty array
	// is what says "nothing to do".
	connIDs := []int32{}
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		connIDs = append(connIDs, id)
	}

	return connIDs, rows.Err()
}

// GetStrategy returns the device's configuration and the timestamp that
// identifies its version. A nil result means there is nothing configured.
func (s *Service) GetStrategy(ctx context.Context, deviceID uuid.UUID) (*Strategy, error) {
	var st Strategy
	err := s.db.QueryRow(ctx, `
		SELECT config_options, modified_at FROM device_strategies WHERE device_id = $1
	`, deviceID).Scan(&st.ConfigOptions, &st.ModifiedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if st.ConfigOptions == nil {
		st.ConfigOptions = map[string]string{}
	}
	return &st, nil
}

// HeartbeatWorker runs periodic heartbeat cleanup
func (s *Service) HeartbeatWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(s.cfg.WorkerIntervalHeartbeatCheckSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.recomputeConnectivity(ctx)
		}
	}
}

// recomputeConnectivity updates device connectivity status based on
// last_seen_at, and records each change as an event.
//
// The recomputation itself is unchanged in what it decides. What changed with
// item 6.5 is that the two UPDATEs now go through monitoring.RecordAndNotify,
// which writes a device_connectivity_events row for every device it moves and
// enqueues a notification. The platform knew a device had gone offline and kept
// it in a single column that the next change overwrote; nobody could ask when,
// or for how long, or be told about it.
//
// The offline pass runs first. Running stale first would move a long-dead
// device to STALE and then immediately to OFFLINE in the same tick, producing
// two events and two alerts for one thing happening.
func (s *Service) recomputeConnectivity(ctx context.Context) {
	if s.monitoring == nil {
		// A deployment that has not wired monitoring still gets the state
		// changes, just no history. This is the pre-6.5 behaviour and exists so
		// the worker cannot fail to start over an optional feature.
		s.recomputeConnectivityWithoutHistory(ctx)
		return
	}

	offline, err := s.monitoring.RecordAndNotify(ctx,
		string(ConnectivityOffline), int64(s.cfg.DeviceOfflineAfterSeconds),
		string(ConnectivityStale), string(ConnectivityOnline))
	if err != nil {
		log.Error().Err(err).Msg("Failed to update offline devices")
	}

	stale, err := s.monitoring.RecordAndNotify(ctx,
		string(ConnectivityStale), int64(s.cfg.DeviceStaleAfterSeconds),
		string(ConnectivityOnline))
	if err != nil {
		log.Error().Err(err).Msg("Failed to update stale devices")
	}

	if offline > 0 || stale > 0 {
		log.Info().Int("offline", offline).Int("stale", stale).Msg("device connectivity changed")
	}
}

// recomputeConnectivityWithoutHistory is the original behaviour, kept for a
// worker built without a monitoring service.
func (s *Service) recomputeConnectivityWithoutHistory(ctx context.Context) {
	staleSeconds := int64(s.cfg.DeviceStaleAfterSeconds)
	offlineSeconds := int64(s.cfg.DeviceOfflineAfterSeconds)

	_, err := s.db.Exec(ctx, `
		UPDATE devices
		SET connectivity = $1, updated_at = now()
		WHERE last_seen_at IS NOT NULL
		  AND now() - last_seen_at > make_interval(secs => $2)
		  AND connectivity != $3
	`, ConnectivityStale, staleSeconds, ConnectivityOffline)

	if err != nil {
		log.Error().Err(err).Msg("Failed to update stale devices")
	}

	_, err = s.db.Exec(ctx, `
		UPDATE devices
		SET connectivity = $1, updated_at = now()
		WHERE last_seen_at IS NOT NULL
		  AND now() - last_seen_at > make_interval(secs => $2)
		  AND connectivity = $3
	`, ConnectivityOffline, offlineSeconds, ConnectivityStale)

	if err != nil {
		log.Error().Err(err).Msg("Failed to update offline devices")
	}
}

// NotificationWorker delivers the outbox.
//
// Separate from the heartbeat worker and on its own interval, because the two
// fail differently: a slow receiver must not delay the connectivity
// recomputation that produces the events it is delivering.
func (s *Service) NotificationWorker(ctx context.Context) {
	if s.monitoring == nil {
		return
	}

	client := &http.Client{Timeout: 15 * time.Second}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			delivered, err := s.monitoring.DeliverPending(ctx, client)
			if err != nil {
				log.Error().Err(err).Msg("Failed to deliver notifications")
			}
			if delivered > 0 {
				log.Info().Int("delivered", delivered).Msg("notifications delivered")
			}
		}
	}
}

// CleanupWorker runs periodic cleanup tasks
func (s *Service) CleanupWorker(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupExpiredSessions(ctx)
			s.cleanupOldAuditEvents(ctx)
		}
	}
}

// cleanupExpiredSessions removes expired client sessions
func (s *Service) cleanupExpiredSessions(ctx context.Context) {
	_, err := s.db.Exec(ctx, `
		DELETE FROM client_sessions 
		WHERE expires_at < now()
	`)
	if err != nil {
		log.Error().Err(err).Msg("Failed to cleanup expired sessions")
	}
}

// cleanupOldAuditEvents removes audit events older than the retention period.
//
// audit_events is append-only: a trigger refuses UPDATE and DELETE unless the
// transaction has announced itself as the retention pass. This is the only
// place in the codebase that may set it, and it sets it with SET LOCAL so the
// exemption dies with the transaction rather than lingering on a pooled
// connection.
func (s *Service) cleanupOldAuditEvents(ctx context.Context) {
	tx, err := s.db.Tx(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to start the audit retention transaction")
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SET LOCAL odv.audit_retention = 'on'`); err != nil {
		log.Error().Err(err).Msg("Failed to mark the audit retention transaction")
		return
	}

	tag, err := tx.Exec(ctx, `
		DELETE FROM audit_events
		WHERE created_at < now() - make_interval(days := $1)
	`, s.cfg.AuditRetentionDays)
	if err != nil {
		log.Error().Err(err).Msg("Failed to cleanup old audit events")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to commit the audit retention transaction")
		return
	}

	// Deletion from the audit log is itself worth a line in the log, since it
	// is the one operation that legitimately removes evidence.
	if rows := tag.RowsAffected(); rows > 0 {
		log.Info().
			Int64("deleted", rows).
			Int("retention_days", s.cfg.AuditRetentionDays).
			Msg("audit retention: removed events past the retention period")
	}
}
