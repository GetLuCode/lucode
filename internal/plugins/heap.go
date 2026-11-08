package plugins

import (
	"runtime"
	"runtime/metrics"
	"sync"
	"time"
)

// The heap watchdog. The process has one heap, so it can only see that the heap grew while an
// invocation was running and stop that invocation: it is coarse, and two plugins running at once
// share the blame. It keeps a runaway plugin from taking the hub down; it is not an exact quota.

const (
	defaultHeapCap   = 256 << 20
	heapSampleEvery  = 10 * time.Millisecond
	heapMinGCBetween = time.Second
)

type heapWatcher struct {
	mu     sync.Mutex
	active map[*heapEntry]bool
	lastGC time.Time
}

type heapEntry struct {
	start uint64
	limit uint64
	stop  func()
}

var heapWatch = &heapWatcher{active: map[*heapEntry]bool{}}

var heapSamples = []metrics.Sample{
	{Name: "/memory/classes/heap/objects:bytes"},
	{Name: "/memory/classes/heap/stacks:bytes"},
}

func heapNow() uint64 {
	s := make([]metrics.Sample, len(heapSamples))
	copy(s, heapSamples)
	metrics.Read(s)
	var total uint64
	for _, x := range s {
		if x.Value.Kind() == metrics.KindUint64 {
			total += x.Value.Uint64()
		}
	}
	return total
}

// watchHeap calls stop if the heap grows past cap while this invocation runs.
func watchHeap(limit uint64, stop func()) func() {
	if limit == 0 {
		limit = defaultHeapCap
	}
	e := &heapEntry{start: heapNow(), limit: limit, stop: stop}
	w := heapWatch
	w.mu.Lock()
	first := len(w.active) == 0
	w.active[e] = true
	w.mu.Unlock()
	if first {
		go w.loop()
	}
	return func() {
		w.mu.Lock()
		delete(w.active, e)
		w.mu.Unlock()
	}
}

// loop runs while any invocation does. Before blaming anyone it collects once, so garbage from
// elsewhere in the process does not stop a plugin.
func (w *heapWatcher) loop() {
	for {
		time.Sleep(heapSampleEvery)
		w.mu.Lock()
		if len(w.active) == 0 {
			w.mu.Unlock()
			return
		}
		now := heapNow()
		over, farOver := false, false
		for e := range w.active {
			over = over || (now > e.start && now-e.start > e.limit)
			farOver = farOver || (now > e.start && now-e.start > 2*e.limit)
		}
		// far past the cap there is no time for a collection: this may be all live
		collect := over && !farOver && time.Since(w.lastGC) > heapMinGCBetween
		if collect {
			w.lastGC = time.Now()
		}
		w.mu.Unlock()
		if !over {
			continue
		}
		if collect {
			runtime.GC()
			now = heapNow()
		}
		w.mu.Lock()
		for e := range w.active {
			if now > e.start && now-e.start > e.limit {
				e.stop()
			}
		}
		w.mu.Unlock()
	}
}
