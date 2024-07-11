package queue

import (
	"fmt"
	"sync"
	"time"
)

var _ Counter = (*FakeCounter)(nil)

type HostAndCount struct {
	Host  string
	Count int
}
type FakeCounter struct {
	mapMut        *sync.RWMutex
	RetMap        map[string]Count
	ResizedCh     chan HostAndCount
	ResizeTimeout time.Duration
}

// Count implements Counter.
func (f *FakeCounter) Count(host string) int {
	panic("unimplemented")
}

// PostponeDuration implements Counter.
func (f *FakeCounter) PostponeDuration() time.Duration {
	panic("unimplemented")
}

// PostponeResize implements Counter.
func (f *FakeCounter) PostponeResize(host string, time time.Time) {
	panic("unimplemented")
}

// ProcessPostponedResizes implements Counter.
func (f *FakeCounter) ProcessPostponedResizes(sleep time.Duration) {
	panic("unimplemented")
}

// ShouldPostponeResize implements Counter.
func (f *FakeCounter) ShouldPostponeResize() bool {
	panic("unimplemented")
}

func NewFakeCounter() *FakeCounter {
	return &FakeCounter{
		mapMut:        new(sync.RWMutex),
		RetMap:        map[string]Count{},
		ResizedCh:     make(chan HostAndCount),
		ResizeTimeout: 1 * time.Second,
	}
}

func (f *FakeCounter) Increase(host string, i int) error {
	f.mapMut.Lock()
	count := f.RetMap[host]
	count.Concurrency += i
	count.RPS += float64(i)
	f.RetMap[host] = count
	f.mapMut.Unlock()
	select {
	case f.ResizedCh <- HostAndCount{Host: host, Count: i}:
	case <-time.After(f.ResizeTimeout):
		return fmt.Errorf(
			"FakeCounter.Increase timeout after %s",
			f.ResizeTimeout,
		)
	}
	return nil
}

func (f *FakeCounter) Decrease(host string, i int) error {
	f.mapMut.Lock()
	count := f.RetMap[host]
	count.Concurrency -= i
	f.RetMap[host] = count
	f.mapMut.Unlock()
	select {
	case f.ResizedCh <- HostAndCount{Host: host, Count: i}:
	case <-time.After(f.ResizeTimeout):
		return fmt.Errorf(
			"FakeCounter.Decrease timeout after %s",
			f.ResizeTimeout,
		)
	}
	return nil
}

func (f *FakeCounter) EnsureKey(host string, _, _ time.Duration) {
	f.mapMut.Lock()
	defer f.mapMut.Unlock()
	f.RetMap[host] = Count{
		Concurrency: 0,
	}
}

func (f *FakeCounter) UpdateBuckets(_ string, _, _ time.Duration) {}

func (f *FakeCounter) RemoveKey(host string) bool {
	f.mapMut.Lock()
	defer f.mapMut.Unlock()
	_, ok := f.RetMap[host]
	delete(f.RetMap, host)
	return ok
}

func (f *FakeCounter) Current() (*Counts, error) {
	ret := NewCounts()
	f.mapMut.RLock()
	defer f.mapMut.RUnlock()
	retMap := f.RetMap
	ret.Counts = retMap
	return ret, nil
}

var _ CountReader = &FakeCountReader{}

type FakeCountReader struct {
	concurrency int
	rps         float64
	err         error
}

// Count implements CountReader.
func (f *FakeCountReader) Count(host string) int {
	panic("unimplemented")
}

// PostponeDuration implements CountReader.
func (f *FakeCountReader) PostponeDuration() time.Duration {
	panic("unimplemented")
}

// ShouldPostponeResize implements CountReader.
func (f *FakeCountReader) ShouldPostponeResize() bool {
	panic("unimplemented")
}

func (f *FakeCountReader) Current() (*Counts, error) {
	ret := NewCounts()
	ret.Counts = map[string]Count{
		"sample.com": {
			Concurrency: f.concurrency,
			RPS:         f.rps,
		},
	}
	return ret, f.err
}
