package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/go-logr/logr"

	"github.com/kedacore/http-add-on/interceptor/metrics"
	"github.com/kedacore/http-add-on/pkg/k8s"
	"github.com/kedacore/http-add-on/pkg/queue"
	"github.com/kedacore/http-add-on/pkg/util"
)

type Counting struct {
	queueCounter    queue.Counter
	upstreamHandler http.Handler
}

func NewCountingMiddleware(queueCounter queue.Counter, upstreamHandler http.Handler) *Counting {
	return &Counting{
		queueCounter:    queueCounter,
		upstreamHandler: upstreamHandler,
	}
}

var _ http.Handler = (*Counting)(nil)

func (cm *Counting) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r = util.RequestWithLoggerWithName(r, "CountingMiddleware")
	ctx := r.Context()
	logger := util.LoggerFromContext(ctx)

	_, err := getHost(r)
	if err != nil {
		logger.Error(err, "not forwarding request")
		w.WriteHeader(400)
		if _, err := w.Write([]byte("Host not found, not forwarding request")); err != nil {
			logger.Error(err, "could not write error message to client")
		}
		return
	}

	defer cm.countAsync(ctx)()

	cm.upstreamHandler.ServeHTTP(w, r)
}

func (cm *Counting) countAsync(ctx context.Context) func() {
	signaler := util.NewSignaler()

	go cm.count(ctx, signaler)

	return func() {
		go signaler.Signal()
	}
}

func (cm *Counting) count(ctx context.Context, signaler util.Signaler) {
	logger := util.LoggerFromContext(ctx)
	httpso := util.HTTPSOFromContext(ctx)

	key := k8s.NamespacedNameFromObject(httpso).String()

	if !cm.inc(logger, key) {
		return
	}

	if err := signaler.Wait(ctx); err != nil && err != context.Canceled {
		logger.Error(err, "failed to wait signal")
	}

	cm.dec(logger, key)
}

func (cm *Counting) inc(logger logr.Logger, key string) bool {
	if err := cm.queueCounter.Increase(key, 1); err != nil {
		logger.Error(err, "error incrementing queue counter", "key", key)

		return false
	}

	metrics.RecordPendingRequestCount(key, int64(1))

	return true
}

func (cm *Counting) dec(logger logr.Logger, key string) bool {
	if cm.queueCounter.ShouldPostponeResize() && cm.queueCounter.Count(key) == 1 {
		cm.queueCounter.PostponeResize(key, time.Now().Add(cm.queueCounter.PostponeDuration()))
		logger.Info("postponed resizing queue", "key", key)
		return false
	}
	if err := cm.queueCounter.Decrease(key, 1); err != nil {
		logger.Error(err, "error decrementing queue counter", "key", key)

		return false
	}

	metrics.RecordPendingRequestCount(key, int64(-1))

	return true
}
