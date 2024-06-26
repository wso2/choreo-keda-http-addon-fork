package queue

import (
	"fmt"
	"sync"
	"time"
)

var _ Counter = &FakeCounter{}

type HostAndCount struct {
	Host  string
	Count int
}
type FakeCounter struct {
	mapMut        *sync.RWMutex
	RetMap        map[string]int
	ResizedCh     chan HostAndCount
	ResizeTimeout time.Duration
}

// ShouldPostponeResize implements Counter.
func (f *FakeCounter) ShouldPostponeResize() bool {
	panic("unimplemented")
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

func NewFakeCounter() *FakeCounter {
	return &FakeCounter{
		mapMut:        new(sync.RWMutex),
		RetMap:        map[string]int{},
		ResizedCh:     make(chan HostAndCount),
		ResizeTimeout: 1 * time.Second,
	}
}

func (f *FakeCounter) Resize(host string, i int) error {
	f.mapMut.Lock()
	f.RetMap[host] += i
	f.mapMut.Unlock()
	select {
	case f.ResizedCh <- HostAndCount{Host: host, Count: i}:
	case <-time.After(f.ResizeTimeout):
		return fmt.Errorf(
			"FakeCounter.Resize timeout after %s",
			f.ResizeTimeout,
		)
	}
	return nil
}

func (f *FakeCounter) Ensure(host string) {
	f.mapMut.Lock()
	defer f.mapMut.Unlock()
	f.RetMap[host] = 0
}

func (f *FakeCounter) Remove(host string) bool {
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
	current int
	err     error
}

// ShouldPostponeResize implements CountReader.
func (f *FakeCountReader) ShouldPostponeResize() bool {
	panic("unimplemented")
}

// Count implements CountReader.
func (f *FakeCountReader) Count(host string) int {
	panic("unimplemented")
}

// PostponeDuration implements CountReader.
func (f *FakeCountReader) PostponeDuration() time.Duration {
	panic("unimplemented")
}

func (f *FakeCountReader) Current() (*Counts, error) {
	ret := NewCounts()
	ret.Counts = map[string]int{
		"sample.com": f.current,
	}
	return ret, f.err
}
