package sub2pool

import (
	"context"
	"log/slog"

	"github.com/bejix/upstream-ops/backend/storage"
)

type runnerTargetLister interface {
	List() ([]storage.UpstreamSyncTarget, error)
}

// Runner is the scheduler adapter. Disabled targets are never read or written;
// pending notifications are retried before a new enabled cycle starts.
type Runner struct {
	targets runnerTargetLister
	service *Service
	log     *slog.Logger
}

func NewRunner(targets runnerTargetLister, service *Service, log *slog.Logger) *Runner {
	return &Runner{targets: targets, service: service, log: log}
}

func (r *Runner) RunAllEnabled(ctx context.Context) {
	if r == nil || r.targets == nil || r.service == nil {
		return
	}
	targets, err := r.targets.List()
	if err != nil {
		r.warn("list Sub2 pool targets failed", "err", err)
		return
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		if !target.Enabled {
			continue
		}
		if _, err := r.service.DispatchPending(ctx, target.ID, 20); err != nil && !isPublicError(err, ErrUnavailable.Code) {
			r.warn("dispatch Sub2 pool outbox failed", "target", target.ID, "err", err)
		}
		status, err := r.service.GetAutomation(target.ID)
		if err != nil {
			r.warn("read Sub2 pool automation failed", "target", target.ID, "err", err)
			continue
		}
		if !status.Enabled {
			if _, _, err := r.service.SnapshotPreview(ctx, target.ID); err != nil {
				r.warn("refresh Sub2 pool snapshot cache failed", "target", target.ID, "err", err)
			}
			continue
		}
		result, err := r.service.Run(ctx, target.ID)
		if err != nil {
			r.service.RecordAutomationFailure(target.ID, err)
			r.warn("run Sub2 pool automation failed", "target", target.ID, "err", err)
			continue
		}
		if r.log != nil {
			r.log.Info(
				"Sub2 pool automation finished",
				"target", target.ID,
				"changes", len(result.Preview.Changes),
				"guards", len(result.Preview.Guards),
				"notification", result.NotificationStatus,
			)
		}
	}
}

func (r *Runner) warn(message string, args ...any) {
	if r.log != nil {
		r.log.Warn(message, args...)
	}
}
