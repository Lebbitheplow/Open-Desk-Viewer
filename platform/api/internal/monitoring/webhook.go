package monitoring

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Webhook delivery, item 6.6.
//
// Webhooks rather than email, and the choice is worth stating because the plan
// left it open. Email needs an SMTP server, credentials, a sender domain with
// SPF and DKIM, and something to do about bounces; none of that exists in this
// deployment and all of it has to be right before the first message is useful.
// A webhook needs a URL, and an operator who wants email points one at a thing
// that sends email, which they already have.

const (
	// maxAttempts before a delivery is abandoned. Six attempts on the backoff
	// below spans about an hour, which covers a receiver restart without
	// retrying into next week.
	maxAttempts = 6

	// deliveryTimeout per attempt. A receiver that takes longer than this is
	// not going to be made reliable by waiting.
	deliveryTimeout = 10 * time.Second

	// batchSize per pass. Bounded so one backlog cannot hold the worker's
	// connection for minutes.
	batchSize = 50
)

// backoff returns the delay before attempt n. Exponential from 30 seconds,
// capped at 30 minutes: the point is to survive a receiver restart, not to
// keep trying until somebody notices.
func backoff(attempt int) time.Duration {
	d := 30 * time.Second << attempt
	if d > 30*time.Minute {
		return 30 * time.Minute
	}
	return d
}

type pendingDelivery struct {
	id        int64
	targetID  uuid.UUID
	url       string
	secret    *string
	eventType string
	payload   []byte
	attempts  int
}

// DeliverPending sends everything due and returns how many were delivered.
//
// Claiming is the first thing it does, in the same statement that selects, so
// two worker replicas cannot both send the same notification. Without that,
// scaling the worker to two would double every alert.
func (s *Service) DeliverPending(ctx context.Context, client *http.Client) (delivered int, err error) {
	rows, err := s.db.Query(ctx, `
		UPDATE notification_deliveries d
		SET attempts = d.attempts + 1,
		    next_attempt_at = now() + interval '1 hour'
		FROM notification_targets t
		WHERE d.id IN (
			SELECT id FROM notification_deliveries
			WHERE delivered_at IS NULL AND abandoned_at IS NULL AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		  AND t.id = d.target_id
		RETURNING d.id, t.id, t.url, t.secret, d.event_type, d.payload, d.attempts
	`, batchSize)
	if err != nil {
		return 0, err
	}

	var batch []pendingDelivery
	for rows.Next() {
		var p pendingDelivery
		if err := rows.Scan(&p.id, &p.targetID, &p.url, &p.secret, &p.eventType, &p.payload, &p.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// The HTTP calls happen after the rows are read, not inside the loop over
	// them: holding a pool connection open across somebody else's network is
	// how twenty slow receivers become an exhausted pool.
	for _, p := range batch {
		if sendErr := s.send(ctx, client, p); sendErr != nil {
			s.recordFailure(ctx, p, sendErr)
			continue
		}
		s.recordSuccess(ctx, p)
		delivered++
	}

	return delivered, nil
}

func (s *Service) send(ctx context.Context, client *http.Client, p pendingDelivery) error {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(p.payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OpenDeskViewer")
	// So a receiver can route without parsing, and can tell a redelivery from a
	// new event.
	req.Header.Set("X-ODV-Event", p.eventType)
	req.Header.Set("X-ODV-Delivery", fmt.Sprint(p.id))

	if p.secret != nil && *p.secret != "" {
		mac := hmac.New(sha256.New, []byte(*p.secret))
		mac.Write(p.payload)
		req.Header.Set("X-ODV-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Drained rather than ignored, so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("receiver answered %d", resp.StatusCode)
	}
	return nil
}

func (s *Service) recordSuccess(ctx context.Context, p pendingDelivery) {
	if _, err := s.db.Exec(ctx, `
		UPDATE notification_deliveries SET delivered_at = now(), last_error = NULL WHERE id = $1
	`, p.id); err != nil {
		log.Error().Err(err).Int64("delivery", p.id).Msg("failed to mark a notification delivered")
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE notification_targets
		SET last_success_at = now(), consecutive_failures = 0, updated_at = now()
		WHERE id = $1
	`, p.targetID); err != nil {
		log.Error().Err(err).Msg("failed to record notification target health")
	}
}

func (s *Service) recordFailure(ctx context.Context, p pendingDelivery, cause error) {
	// Abandon rather than retry forever. An abandoned row is kept, not deleted:
	// "which alerts never arrived" is a question an operator has to be able to
	// answer after an incident.
	abandon := p.attempts >= maxAttempts

	if _, err := s.db.Exec(ctx, `
		UPDATE notification_deliveries
		SET last_error = $2,
		    next_attempt_at = now() + make_interval(secs => $3),
		    abandoned_at = CASE WHEN $4::boolean THEN now() ELSE NULL END
		WHERE id = $1
	`, p.id, cause.Error(), backoff(p.attempts).Seconds(), abandon); err != nil {
		log.Error().Err(err).Int64("delivery", p.id).Msg("failed to record a notification failure")
	}

	if _, err := s.db.Exec(ctx, `
		UPDATE notification_targets
		SET last_failure_at = now(), last_failure_reason = $2,
		    consecutive_failures = consecutive_failures + 1, updated_at = now()
		WHERE id = $1
	`, p.targetID, cause.Error()); err != nil {
		log.Error().Err(err).Msg("failed to record notification target health")
	}

	event := log.Warn()
	if abandon {
		event = log.Error()
	}
	event.Err(cause).
		Int64("delivery", p.id).
		Int("attempts", p.attempts).
		Bool("abandoned", abandon).
		Str("event", p.eventType).
		Msg("notification delivery failed")
}
