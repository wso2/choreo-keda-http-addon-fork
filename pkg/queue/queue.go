package queue

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// CountReader represents the size of a virtual HTTP queue, possibly
// distributed across multiple HTTP server processes. It only can access
// the current size of the queue, not any other information about requests.
//
// It is concurrency safe.
type CountReader interface {
	// Current returns the current count of pending requests
	// for the given hostname
	Current() (*Counts, error)
	// Count returns the current count of pending requests for the
	// given hostname
	Count(host string) int
	// PostponeDuration returns the duration to postpone the resize
	PostponeDuration() time.Duration
	// ShouldPostponeResize returns if the resize should be postponed
	ShouldPostponeResize() bool
}

// QueueCounter represents a virtual HTTP queue, possibly distributed across
// multiple HTTP server processes. It can only increase or decrease the
// size of the queue or read the current size of the queue, but not read
// or modify any other information about it.
//
// Both the mutation and read functionality is concurrency safe, but
// the read functionality is point-in-time only
type Counter interface {
	CountReader
	// Increase increases the queue size by delta for the given host.
	Increase(host string, delta int) error
	// Decrease decreases the queue size by delta for the given host.
	Decrease(host string, delta int) error
	// EnsureKey ensures that host is represented in this counter.
	EnsureKey(host string, window, granularity time.Duration)
	// UpdateBuckets update request backets if there are changes
	UpdateBuckets(host string, window, granularity time.Duration)
	// RemoveKey tries to remove the given host and its
	// associated counts from the queue. returns true if it existed,
	// false otherwise.
	RemoveKey(host string) bool
	// PostponeResize sets the last request time for the given host
	PostponeResize(host string, time time.Time)
	// ProcessPostponedResizes processes the postponed resizes
	ProcessPostponedResizes(sleep time.Duration)
}

// Memory implements Counter and CountReader
var _ Counter = (*Memory)(nil)
var _ CountReader = (*Memory)(nil)

// Memory is a Counter implementation that
// holds the HTTP queue in memory only. Always use
// NewMemory to create one of these.
type Memory struct {
	concurrentMap    map[string]int
	rpsMap           map[string]*RequestsBuckets
	postponedResizes map[string]time.Time
	postponeDuration time.Duration
	shouldPostpone   bool
	mut              *sync.RWMutex
	logger           logr.Logger
}

// NewMemoryQueue creates a new empty in-memory queue
func NewMemory(postponeDuration time.Duration, shouldPostpone bool, logger logr.Logger) *Memory {
	lock := new(sync.RWMutex)
	return &Memory{
		concurrentMap:    make(map[string]int),
		rpsMap:           make(map[string]*RequestsBuckets),
		postponedResizes: make(map[string]time.Time),
		postponeDuration: postponeDuration,
		shouldPostpone:   shouldPostpone,
		mut:              lock,
		logger:           logger,
	}
}

// Increase changes the size of the queue adding delta
func (r *Memory) Increase(host string, delta int) error {
	r.mut.Lock()
	defer r.mut.Unlock()
	r.concurrentMap[host] += delta
	r.rpsMap[host].Record(time.Now(), delta)
	return nil
}

// Decrease changes the size of the queue reducing delta
func (r *Memory) Decrease(host string, delta int) error {
	r.mut.Lock()
	defer r.mut.Unlock()
	r.concurrentMap[host] -= delta
	return nil
}

func (r *Memory) EnsureKey(host string, window, granularity time.Duration) {
	r.mut.Lock()
	defer r.mut.Unlock()
	_, ok := r.concurrentMap[host]
	if !ok {
		r.concurrentMap[host] = 0
	}
	_, ok = r.rpsMap[host]
	if !ok {
		r.rpsMap[host] = NewRequestsBuckets(window, granularity)
	}
}

func (r *Memory) UpdateBuckets(host string, window, granularity time.Duration) {
	r.EnsureKey(host, window, granularity)
	r.mut.Lock()
	defer r.mut.Unlock()
	buckets, ok := r.rpsMap[host]
	if ok &&
		(buckets.window != window ||
			buckets.granularity != granularity) {
		r.rpsMap[host] = NewRequestsBuckets(window, granularity)
	}
}

func (r *Memory) RemoveKey(host string) bool {
	r.mut.Lock()
	defer r.mut.Unlock()
	_, concurrentOk := r.concurrentMap[host]
	delete(r.concurrentMap, host)
	_, rpsOk := r.rpsMap[host]
	delete(r.rpsMap, host)
	return concurrentOk && rpsOk
}

// Current returns the current size of the queue.
func (r *Memory) Current() (*Counts, error) {
	r.mut.RLock()
	defer r.mut.RUnlock()
	cts := NewCounts()
	for key, concurrency := range r.concurrentMap {
		rpsItem, ok := r.rpsMap[key]
		if !ok {
			return nil, fmt.Errorf(fmt.Sprintf("rps map doesn't contain the key '%s'", key))
		}
		cts.Counts[key] = Count{
			Concurrency: concurrency,
			RPS:         rpsItem.WindowAverage(time.Now()),
		}
	}
	return cts, nil
}

func (r *Memory) Count(host string) int {
	r.mut.Lock()
	defer r.mut.Unlock()
	count, ok := r.concurrentMap[host]
	if !ok {
		return 0
	}
	return count
}

func (r *Memory) PostponeResize(host string, time time.Time) {
	r.mut.Lock()
	defer r.mut.Unlock()
	r.postponedResizes[host] = time
}

func (r *Memory) PostponeDuration() time.Duration {
	return r.postponeDuration
}

func (r *Memory) ProcessPostponedResizes(sleep time.Duration) {
	for {
		time.Sleep(sleep)
		r.mut.Lock()
		hostsToModify := make([]string, 0)
		for host, resizeTime := range r.postponedResizes {
			if resizeTime.Before(time.Now()) {
				r.logger.Info("processing postponed resize", "host", host, "resizeTime", resizeTime, "count", r.concurrentMap[host])
				if r.concurrentMap[host] == 1 {
					hostsToModify = append(hostsToModify, host)
				}
			}
		}
		r.mut.Unlock()

		// Perform modifications outside of the lock
		r.mut.Lock()
		for _, host := range hostsToModify {
			r.concurrentMap[host] = 0
			delete(r.postponedResizes, host)
		}
		r.mut.Unlock()
	}
}

func (r *Memory) ShouldPostponeResize() bool {
	return r.shouldPostpone
}
