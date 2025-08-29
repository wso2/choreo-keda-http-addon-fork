package routing

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	httpv1alpha1 "github.com/kedacore/http-add-on/operator/apis/http/v1alpha1"
	"github.com/kedacore/http-add-on/operator/generated/informers/externalversions"
	informershttpv1alpha1 "github.com/kedacore/http-add-on/operator/generated/informers/externalversions/http/v1alpha1"
	"github.com/kedacore/http-add-on/pkg/k8s"
	"github.com/kedacore/http-add-on/pkg/queue"
	"github.com/kedacore/http-add-on/pkg/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	errUnknownSharedIndexInformer = errors.New("informer is not cache.sharedIndexInformer")
	errStartedSharedIndexInformer = errors.New("sharedIndexInformer has started, run more than once is not allowed")
	errStoppedSharedIndexInformer = errors.New("sharedIndexInformer has stopped")
	errNotSyncedTable             = errors.New("table has not synced")
)

// TableConfig configures the routing table update strategy
type TableConfig struct {
	UseIncrementalUpdates bool
	UpdateChannelSize     int
	// Scale testing configuration
	MockObjectsCount  int  // Number of mock HTTPScaledObjects to add for testing
	EnableMockObjects bool // Enable mock object generation
}

// DefaultTableConfig returns the default configuration (legacy behavior)
func DefaultTableConfig() TableConfig {
	return TableConfig{
		UseIncrementalUpdates: false,
		UpdateChannelSize:     1000,
		MockObjectsCount:      0,
		EnableMockObjects:     false,
	}
}

// UpdateOperationType represents the type of update operation
type UpdateOperationType int

const (
	UpdateOperationAdd UpdateOperationType = iota
	UpdateOperationUpdate
	UpdateOperationDelete
)

// String returns the string representation of the operation type
func (op UpdateOperationType) String() string {
	switch op {
	case UpdateOperationAdd:
		return "add"
	case UpdateOperationUpdate:
		return "update"
	case UpdateOperationDelete:
		return "delete"
	default:
		return "unknown"
	}
}

// updateOperation represents an incremental update to the routing table
type updateOperation struct {
	operation UpdateOperationType            // "add", "update", "delete"
	oldHTTPSO *httpv1alpha1.HTTPScaledObject // for updates/deletes
	newHTTPSO *httpv1alpha1.HTTPScaledObject // for adds/updates
}

type Table interface {
	util.HealthChecker

	Start(ctx context.Context) error
	Route(req *http.Request) *httpv1alpha1.HTTPScaledObject
	HasSynced() bool
}

type table struct {
	httpScaledObjectInformer                 sharedIndexInformer
	httpScaledObjectEventHandlerRegistration cache.ResourceEventHandlerRegistration
	httpScaledObjects                        map[types.NamespacedName]*httpv1alpha1.HTTPScaledObject
	httpScaledObjectsMutex                   sync.RWMutex
	memoryHolder                             util.AtomicValue[TableMemory]
	memorySignaler                           util.Signaler // Legacy signaler for rebuild approach
	queueCounter                             queue.Counter

	// Configuration
	config TableConfig

	// New fields for incremental updates (only used when config.UseIncrementalUpdates = true)
	incrementalUpdateChan  chan updateOperation
	incrementalInitialized atomic.Bool
}

func NewTable(sharedInformerFactory externalversions.SharedInformerFactory, namespace string, counter queue.Counter) (Table, error) {
	return NewTableWithConfig(sharedInformerFactory, namespace, counter, NewTableConfigFromEnv())
}

func NewTableWithConfig(sharedInformerFactory externalversions.SharedInformerFactory, namespace string, counter queue.Counter, config TableConfig) (Table, error) {
	httpScaledObjects := informershttpv1alpha1.New(sharedInformerFactory, namespace, nil).HTTPScaledObjects()

	t := table{
		httpScaledObjects: make(map[types.NamespacedName]*httpv1alpha1.HTTPScaledObject),
		memorySignaler:    util.NewSignaler(), // Legacy signaler
		config:            config,
	}

	// Initialize incremental update channel if enabled
	if config.UseIncrementalUpdates {
		t.incrementalUpdateChan = make(chan updateOperation, config.UpdateChannelSize)
	}

	informer, ok := httpScaledObjects.Informer().(sharedIndexInformer)
	if !ok {
		return nil, errUnknownSharedIndexInformer
	}
	t.httpScaledObjectInformer = informer

	registration, err := informer.AddEventHandler(&t)
	if err != nil {
		return nil, err
	}
	t.httpScaledObjectEventHandlerRegistration = registration
	t.queueCounter = counter
	return &t, nil
}

func (t *table) runInformer(ctx context.Context) error {
	if t.httpScaledObjectInformer.HasStarted() {
		return errStartedSharedIndexInformer
	}

	t.httpScaledObjectInformer.Run(ctx.Done())

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return errStoppedSharedIndexInformer
	}
}

func (t *table) refreshMemory(ctx context.Context) error {
	// wait for event handler to be synced before first computation of routes
	for !t.httpScaledObjectEventHandlerRegistration.HasSynced() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			continue
		}
	}

	for {
		m := t.newMemoryFromHTTPSOs()
		t.memoryHolder.Set(m)
		if err := t.memorySignaler.Wait(ctx); err != nil {
			return err
		}
	}
}

func (t *table) newMemoryFromHTTPSOs() TableMemory {
	// Make a fast copy of the map while holding the lock briefly
	t.httpScaledObjectsMutex.RLock()
	mapSnapshot := make(map[types.NamespacedName]*httpv1alpha1.HTTPScaledObject, len(t.httpScaledObjects))
	for key, httpso := range t.httpScaledObjects {
		mapSnapshot[key] = httpso // Fast pointer copy
	}
	t.httpScaledObjectsMutex.RUnlock() // Release lock immediately!

	// Build memory from snapshot without holding lock
	tm := NewTableMemory()
	for _, newHTTPSO := range mapSnapshot {
		tm = tm.Remember(newHTTPSO)
	}

	return tm
}

// New incremental update methods
func (t *table) runIncrementalMemoryUpdater(ctx context.Context) error {
	// Wait for event handler to be synced before first computation of routes
	for !t.httpScaledObjectEventHandlerRegistration.HasSynced() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			continue
		}
	}

	// Initial full build from existing map
	t.httpScaledObjectsMutex.RLock()
	initialMemory := t.buildMemoryFromMap(t.httpScaledObjects)
	t.httpScaledObjectsMutex.RUnlock()

	t.memoryHolder.Set(initialMemory)
	t.incrementalInitialized.Store(true)

	log.Printf("Initial routing table built with %d HTTPScaledObjects", len(t.httpScaledObjects))

	// Process incremental updates
	for {
		select {
		case update := <-t.incrementalUpdateChan:
			err := t.applyIncrementalUpdate(update)
			if err != nil {
				log.Printf("OnIncrementalUpdate error: %v", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *table) buildMemoryFromMap(httpsoMap map[types.NamespacedName]*httpv1alpha1.HTTPScaledObject) TableMemory {
	tm := NewTableMemory()
	for _, httpso := range httpsoMap {
		tm = tm.Remember(httpso)
	}
	// Add mock objects for scale testing if configured
	if t.config.EnableMockObjects && t.config.MockObjectsCount > 0 {
		log.Printf("Adding %d mock HTTPScaledObjects for scale testing", t.config.MockObjectsCount)
		start := time.Now()

		mockObjects := t.generateMockHTTPScaledObjects(t.config.MockObjectsCount)
		for _, mockHttpso := range mockObjects {
			tm = tm.Remember(mockHttpso)
		}

		duration := time.Since(start)
		log.Printf("Added %d mock objects in %v (avg: %v per object)",
			t.config.MockObjectsCount, duration, duration/time.Duration(t.config.MockObjectsCount))
	}
	return tm
}

func (t *table) applyIncrementalUpdate(update updateOperation) error {
	currentMemory := t.memoryHolder.Get()
	var newMemory TableMemory

	switch update.operation {
	case UpdateOperationAdd:
		newMemory = currentMemory.Remember(update.newHTTPSO)
	case UpdateOperationUpdate:
		// Remove old, add new
		if update.oldHTTPSO != nil {
			oldKey := *k8s.NamespacedNameFromObject(update.oldHTTPSO)
			currentMemory = currentMemory.Forget(&oldKey)
		}
		newMemory = currentMemory.Remember(update.newHTTPSO)
	case UpdateOperationDelete:
		if update.oldHTTPSO != nil {
			oldKey := *k8s.NamespacedNameFromObject(update.oldHTTPSO)
			newMemory = currentMemory.Forget(&oldKey)
		} else {
			return fmt.Errorf("delete operation requires oldHTTPSO")
		}
	default:
		return fmt.Errorf("unknown operation: %s", update.operation)
	}

	// Atomic update
	t.memoryHolder.Set(newMemory)
	return nil
}

// Helper method to send updates based on configuration
func (t *table) sendUpdate(update updateOperation) {
	if t.config.UseIncrementalUpdates {
		// Send incremental update (non-blocking)
		select {
		case t.incrementalUpdateChan <- update:
			// Sent successfully
		default:
			// Channel full - this shouldn't happen with proper sizing
			log.Printf("Warning: Incremental update channel full, skipping update for %s", update.operation)
		}
	} else {
		// Use legacy signaler
		t.memorySignaler.Signal()
	}
}

var _ Table = (*table)(nil)

func (t *table) Start(ctx context.Context) error {
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(util.ApplyContext(t.runInformer, ctx))

	// Choose memory update strategy based on configuration
	if t.config.UseIncrementalUpdates {
		eg.Go(util.ApplyContext(t.runIncrementalMemoryUpdater, ctx))
	} else {
		eg.Go(util.ApplyContext(t.refreshMemory, ctx))
	}

	return eg.Wait()
}

func (t *table) Route(req *http.Request) *httpv1alpha1.HTTPScaledObject {
	if req == nil {
		return nil
	}

	tm := t.memoryHolder.Get()
	if tm == nil {
		return nil
	}

	key := NewKeyFromRequest(req)
	log.Printf("Routing key: %s", key.String())
	return tm.Route(key)
}

func (t *table) HasSynced() bool {
	if t.config.UseIncrementalUpdates {
		return t.incrementalInitialized.Load()
	} else {
		tm := t.memoryHolder.Get()
		return tm != nil
	}
}

var _ cache.ResourceEventHandler = (*table)(nil)

func (t *table) OnAdd(obj interface{}, _ bool) {
	httpScaledObject, ok := obj.(*httpv1alpha1.HTTPScaledObject)
	if !ok {
		return
	}
	key := *k8s.NamespacedNameFromObject(httpScaledObject)

	window := time.Minute
	granualrity := time.Second
	if httpScaledObject.Spec.ScalingMetric != nil &&
		httpScaledObject.Spec.ScalingMetric.Rate != nil {
		window = httpScaledObject.Spec.ScalingMetric.Rate.Window.Duration
		granualrity = httpScaledObject.Spec.ScalingMetric.Rate.Granularity.Duration
	}
	t.queueCounter.EnsureKey(key.String(), window, granualrity)

	t.httpScaledObjectsMutex.Lock()
	t.httpScaledObjects[key] = httpScaledObject
	t.httpScaledObjectsMutex.Unlock()

	// Send update based on configuration
	update := updateOperation{
		operation: UpdateOperationAdd,
		newHTTPSO: httpScaledObject,
	}
	t.sendUpdate(update)
}

func (t *table) OnUpdate(oldObj interface{}, newObj interface{}) {
	oldHTTPSO, ok := oldObj.(*httpv1alpha1.HTTPScaledObject)
	if !ok {
		return
	}
	oldKey := *k8s.NamespacedNameFromObject(oldHTTPSO)

	newHTTPSO, ok := newObj.(*httpv1alpha1.HTTPScaledObject)
	if !ok {
		return
	}
	newKey := *k8s.NamespacedNameFromObject(newHTTPSO)

	window := time.Minute
	granualrity := time.Second
	if newHTTPSO.Spec.ScalingMetric != nil &&
		newHTTPSO.Spec.ScalingMetric.Rate != nil {
		window = newHTTPSO.Spec.ScalingMetric.Rate.Window.Duration
		granualrity = newHTTPSO.Spec.ScalingMetric.Rate.Granularity.Duration
	}
	t.queueCounter.UpdateBuckets(newKey.String(), window, granualrity)

	mustDelete := oldKey != newKey

	t.httpScaledObjectsMutex.Lock()
	t.httpScaledObjects[newKey] = newHTTPSO

	if mustDelete {
		delete(t.httpScaledObjects, oldKey)
		t.queueCounter.RemoveKey(oldKey.String())
	}
	t.httpScaledObjectsMutex.Unlock()

	// Send update based on configuration
	update := updateOperation{
		operation: UpdateOperationUpdate,
		oldHTTPSO: oldHTTPSO,
		newHTTPSO: newHTTPSO,
	}
	t.sendUpdate(update)
}

func (t *table) OnDelete(obj interface{}) {
	httpScaledObject, ok := obj.(*httpv1alpha1.HTTPScaledObject)
	if !ok {
		return
	}
	key := *k8s.NamespacedNameFromObject(httpScaledObject)

	t.httpScaledObjectsMutex.Lock()
	delete(t.httpScaledObjects, key)
	t.httpScaledObjectsMutex.Unlock()

	t.queueCounter.RemoveKey(key.String())

	// Send update based on configuration
	update := updateOperation{
		operation: UpdateOperationDelete,
		oldHTTPSO: httpScaledObject,
	}
	t.sendUpdate(update)
}

var _ util.HealthChecker = (*table)(nil)

func (t *table) HealthCheck(_ context.Context) error {
	if !t.HasSynced() {
		return errNotSyncedTable
	}

	return nil
}

// generateMockHTTPScaledObjects creates mock HTTPScaledObjects for scale testing
func (t *table) generateMockHTTPScaledObjects(count int) []*httpv1alpha1.HTTPScaledObject {
	objects := make([]*httpv1alpha1.HTTPScaledObject, count)

	for i := 0; i < count; i++ {
		name := fmt.Sprintf("mock-httpso-%d", i)
		namespace := fmt.Sprintf("mock-namespace-%d", i%100) // Distribute across 100 namespaces
		host := fmt.Sprintf("mock-app-%d.example.com", i)

		httpso := &httpv1alpha1.HTTPScaledObject{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         namespace,
				CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Duration(i) * time.Second)),
				// Add a label to identify mock objects
				Labels: map[string]string{
					"mock-object": "true",
					"scale-test":  "true",
				},
			},
			Spec: httpv1alpha1.HTTPScaledObjectSpec{
				Hosts: []string{host},
				PathPrefixes: []string{
					fmt.Sprintf("/api/v1/mock-%d", i),
					fmt.Sprintf("/mock-app-%d", i),
					fmt.Sprintf("/test/%d", i%10), // Create some path conflicts
				},
				ScaleTargetRef: httpv1alpha1.ScaleTargetRef{
					Name:    fmt.Sprintf("mock-deployment-%d", i),
					Service: fmt.Sprintf("mock-service-%d", i),
					Port:    8080,
				},
				// Add some variety in configurations
				ScalingMetric: &httpv1alpha1.ScalingMetricSpec{
					Rate: &httpv1alpha1.RateMetricSpec{
						Window:      metav1.Duration{Duration: time.Duration(30+i%60) * time.Second},
						Granularity: metav1.Duration{Duration: time.Duration(1+i%5) * time.Second},
					},
				},
			},
		}

		objects[i] = httpso
	}

	return objects
}
