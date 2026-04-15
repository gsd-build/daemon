package crons

import (
	"time"

	"github.com/gorhill/cronexpr"
	protocol "github.com/gsd-build/protocol-go"
)

// BuildInventory converts local cron state into the wire inventory payload.
func BuildInventory(locals []LocalCron, now time.Time) []protocol.CronLocalState {
	items := make([]protocol.CronLocalState, 0, len(locals))
	for _, local := range locals {
		item := protocol.CronLocalState{
			ID:              local.Spec.ID,
			Name:            local.Spec.Name,
			CronExpression:  local.Spec.CronExpression,
			Enabled:         local.Spec.Enabled,
			SyncedAt:        local.SyncedAt.UTC().Format(time.RFC3339Nano),
			LocallyModified: local.LocallyModified,
		}
		if local.LastRunAt != nil {
			item.LastRunAt = local.LastRunAt.UTC().Format(time.RFC3339Nano)
		}
		if expr, err := cronexpr.Parse(local.Spec.CronExpression); err == nil {
			next := expr.Next(now.UTC())
			if !next.IsZero() {
				item.NextRunAt = next.Format(time.RFC3339Nano)
			}
		}
		items = append(items, item)
	}
	return items
}
