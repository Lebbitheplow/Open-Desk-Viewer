package telemetry

import (
	"context"
	"fmt"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
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
	db    *postgres.Pool
	cfg   *config.Config
	fleet *fleet.Service
}

// NewService creates a new telemetry service
func NewService(db *postgres.Pool, cfg *config.Config) *Service {
	return &Service{
		db:    db,
		cfg:   cfg,
		fleet: fleet.NewService(db, cfg),
	}
}

// ProcessHeartbeat processes a device heartbeat
func (s *Service) ProcessHeartbeat(ctx context.Context, deviceID, uuid string, online bool) error {
	device, err := s.fleet.GetDeviceByRustdeskID(ctx, deviceID)
	if err != nil {
		if err == fleet.ErrDeviceNotFound {
			_, err := s.fleet.RegisterDevice(ctx, fleet.Device{
				RustdeskID: deviceID,
				UUID:       uuid,
				State:      string(StateDiscovered),
			})
			if err != nil {
				return fmt.Errorf("failed to register device: %w", err)
			}
			device, err = s.fleet.GetDeviceByRustdeskID(ctx, deviceID)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	connectivity := ConnectivityOffline
	if online {
		connectivity = ConnectivityOnline
	} else {
		if time.Since(device.LastSeenAt) < time.Duration(s.cfg.DeviceStaleAfterSeconds)*time.Second {
			connectivity = ConnectivityStale
		}
	}

	_, err = s.db.Exec(ctx, `
		UPDATE devices 
		SET last_seen_at = now(), 
		    connectivity = $1,
		    updated_at = now()
		WHERE id = $2
	`, connectivity, device.ID)

	if err != nil {
		return fmt.Errorf("failed to update device: %w", err)
	}

	// Device group accessibility is handled by the access resolver
	// No need to explicitly mark devices when they come online

	return nil
}

// ProcessSysinfo processes device system information
func (s *Service) ProcessSysinfo(ctx context.Context, deviceID, uuid, hostname, version, os string) error {
	device, err := s.fleet.GetDeviceByRustdeskID(ctx, deviceID)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(ctx, `
		UPDATE devices 
		SET hostname = $1, 
		    client_version = $2, 
		    os = $3,
		    updated_at = now()
		WHERE id = $4
	`, hostname, version, os, device.ID)

	if err != nil {
		return fmt.Errorf("failed to update device sysinfo: %w", err)
	}

	return nil
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

// recomputeConnectivity updates device connectivity status based on last_seen_at
func (s *Service) recomputeConnectivity(ctx context.Context) {
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

// cleanupOldAuditEvents removes audit events older than retention period
func (s *Service) cleanupOldAuditEvents(ctx context.Context) {
	_, err := s.db.Exec(ctx, `
		DELETE FROM audit_events 
		WHERE created_at < now() - make_interval(days := $1)
	`, s.cfg.AuditRetentionDays)

	if err != nil {
		log.Error().Err(err).Msg("Failed to cleanup old audit events")
	}
}
