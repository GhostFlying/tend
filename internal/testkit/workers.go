package testkit

import (
	"context"
	"sync"

	"github.com/GhostFlying/tend/internal/harness"
)

// WorkerScript is what a fake worker does.
type WorkerScript struct {
	Events []harness.WorkerEvent
	Result harness.WorkerResult
	Err    error
	// Gate, when set, holds the worker after its events until the gate is
	// closed or the worker is stopped.
	Gate <-chan struct{}
}

// Workers is a scriptable harness.Workers. Script decides what each worker
// does; it is called once per start.
type Workers struct {
	Script func(spec harness.WorkerSpec) WorkerScript

	mu      sync.Mutex
	started []harness.WorkerSpec
	stopped []harness.WorkerSpec
}

// Start implements harness.Workers.
func (w *Workers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	w.mu.Lock()
	w.started = append(w.started, spec)
	w.mu.Unlock()
	script := w.Script(spec)
	fw := &fakeWorker{events: make(chan harness.WorkerEvent), done: make(chan struct{})}
	go func() {
		defer close(fw.done)
		defer close(fw.events)
		for _, e := range script.Events {
			select {
			case fw.events <- e:
			case <-ctx.Done():
				fw.err = ctx.Err()
				w.recordStop(spec)
				return
			}
		}
		if script.Gate != nil {
			select {
			case <-script.Gate:
			case <-ctx.Done():
				fw.err = ctx.Err()
				w.recordStop(spec)
				return
			}
		}
		fw.res, fw.err = script.Result, script.Err
	}()
	return fw, nil
}

func (w *Workers) recordStop(spec harness.WorkerSpec) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = append(w.stopped, spec)
}

// Started returns the specs of every worker started so far.
func (w *Workers) Started() []harness.WorkerSpec {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]harness.WorkerSpec(nil), w.started...)
}

// Stopped returns the specs of workers that were stopped before they ended.
func (w *Workers) Stopped() []harness.WorkerSpec {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]harness.WorkerSpec(nil), w.stopped...)
}

type fakeWorker struct {
	events chan harness.WorkerEvent
	done   chan struct{}
	res    harness.WorkerResult
	err    error
}

func (f *fakeWorker) Events() <-chan harness.WorkerEvent { return f.events }

func (f *fakeWorker) Wait() (harness.WorkerResult, error) {
	<-f.done
	return f.res, f.err
}
