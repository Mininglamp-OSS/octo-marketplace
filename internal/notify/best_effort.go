package notify

import (
	"context"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"go.uber.org/zap"
)

// BestEffortTimeout bounds post-commit notification work. Matches trackInstall
// in internal/service/expert/install.go: tight enough that a stalled
// octo-server cannot hold anything long enough to matter, because the primary
// transaction has already committed and the client already has its response.
const BestEffortTimeout = 2 * time.Second

// BestEffort runs fn asynchronously on a context detached from any request.
//
// Two properties matter, and both were bugs in an earlier implementation:
//
//  1. DETACHED. A client that disconnects the instant after the submit
//     transaction commits must still get its approval card dispatched. The
//     context here descends from context.Background(), so no caller
//     cancellation or request deadline can reach fn.
//  2. OFF THE REQUEST GOROUTINE. The ENTIRE dispatch — including any octo-server
//     lookup it needs — belongs in here. Leaving even one lookup on the request
//     path makes every submit pay a full octo-server round trip.
//
// fn's failure is logged and never returned: the primary write already
// succeeded, and admins can still see the request in the web review queue. A
// panic in fn is recovered so a notification bug cannot take down the process.
//
// desc is a short operation label for logs; do not put user content or any
// token in it.
func BestEffort(desc string, fn func(ctx context.Context) error) {
	if fn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), BestEffortTimeout)
	go func() {
		// LIFO: recover runs before cancel, so a panic still releases the ctx.
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				logging.Error("notify_best_effort_panic",
					zap.String("operation", desc),
					zap.Any("panic", r),
				)
			}
		}()
		if err := fn(ctx); err != nil {
			logging.Warn("notify_best_effort_failed",
				zap.String("operation", desc),
				logging.ErrorField(err),
			)
		}
	}()
}
