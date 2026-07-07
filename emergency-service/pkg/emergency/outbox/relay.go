// Package outbox contains the relay that ships transactionally-queued audit
// events from emergency.audit_outbox to audit-service. Delivery is at-least-once;
// audit-service dedupes by event_id.
package outbox

import (
	"context"
	"log"
	"time"

	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/repository"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/service"
)

// Relay periodically ships undelivered outbox rows to audit-service.
type Relay struct {
	repo      repository.EmergencyRepository
	shipper   service.AuditShipper
	interval  time.Duration
	batchSize int
}

// NewRelay builds a relay. interval is the poll period; batchSize caps rows per tick.
func NewRelay(repo repository.EmergencyRepository, shipper service.AuditShipper, interval time.Duration, batchSize int) *Relay {
	return &Relay{repo: repo, shipper: shipper, interval: interval, batchSize: batchSize}
}

// Run blocks, shipping batches every interval until ctx is cancelled. Start it in
// its own goroutine.
func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	log.Printf("emergency audit relay: started (interval=%s, batch=%d)", r.interval, r.batchSize)
	for {
		select {
		case <-ctx.Done():
			log.Println("emergency audit relay: stopped")
			return
		case <-ticker.C:
			r.shipBatch(ctx)
		}
	}
}

func (r *Relay) shipBatch(ctx context.Context) {
	rows, err := r.repo.FetchUnshippedOutbox(ctx, r.batchSize)
	if err != nil {
		log.Printf("emergency audit relay: fetch failed: %v", err)
		return
	}

	for _, row := range rows {
		if err := r.shipper.Ship(ctx, row.Payload); err != nil {
			if mErr := r.repo.MarkOutboxFailed(ctx, row.ID, err.Error()); mErr != nil {
				log.Printf("emergency audit relay: mark-failed error for %s: %v", row.ID, mErr)
			}
			continue
		}
		if err := r.repo.MarkOutboxShipped(ctx, row.ID); err != nil {
			// Shipped but not marked — a later retry re-ships and audit-service
			// dedupes by event_id, so this is safe.
			log.Printf("emergency audit relay: mark-shipped error for %s: %v", row.ID, err)
		}
	}
}
