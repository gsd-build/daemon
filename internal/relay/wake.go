package relay

import (
	"context"
	"time"
)

// WakeDetector sends on the returned channel when a sleep/wake cycle
// is detected. Uses a clock-gap heuristic: if the time between ticks
// exceeds 3x the tick interval, the system likely slept.
func WakeDetector(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		const interval = 5 * time.Second
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		lastTick := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				gap := now.Sub(lastTick)
				if gap > 3*interval {
					select {
					case ch <- struct{}{}:
					default:
					}
				}
				lastTick = now
			}
		}
	}()
	return ch
}
