package bus

import (
	"context"
	"log"
	"time"
)

// StartMaintenance runs bounded housekeeping for an owning service. Stop it
// before closing the runtime. Library callers can instead invoke maintenance
// through their own lifecycle without opening another daemon or scheduler.
func (r *Runtime) StartMaintenance() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				run, stop := context.WithTimeout(ctx, 2*time.Minute)
				_, err := r.store.CollectAttachmentGarbage(run)
				stop()
				if err != nil && ctx.Err() == nil {
					log.Print("Agent Intercom file maintenance failed; it will retry on the next run")
				}
				timer.Reset(time.Hour)
			}
		}
	}()
	return func() { cancel(); <-done }
}
