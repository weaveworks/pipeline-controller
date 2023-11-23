package leveltriggered

import (
	"context"
	"github.com/go-logr/logr"
	"sync"
)

// This is a dead simple way to run things using a manager's context as a base, so that they will
// get shut down when the manager does. It must be constructed with `newRunner`, and added to a manager:
//
//     r := newRunner()
//     mgr.Add(r)
//
// then you can use it to run funcs:
//
//     cancel := r.run(func(context.Context))
//
// The func will be run with its own context derived from the root context supplied by the manager,
// with the cancel func returned to the caller as shown. This way you can cancel the context yourself,
// or let it be canceled when the manager shuts down.
//
// It'll deadlock if you call `run` before adding it to a manager (or otherwise calling `Start`).

type runWithContext struct {
	name string
	ctx  context.Context
	do   func(context.Context)
}

type runner struct {
	log         logr.Logger
	rootContext context.Context
	tostart     chan runWithContext
	ready       chan struct{}
}

func newRunner(log logr.Logger) *runner {
	return &runner{
		log:     log,
		tostart: make(chan runWithContext),
		ready:   make(chan struct{}),
	}
}

func (r *runner) run(name string, fn func(ctx context.Context)) context.CancelFunc {
	<-r.ready // wait until there's a root context
	ctx, cancel := context.WithCancel(r.rootContext)
	r.tostart <- runWithContext{
		name: name,
		do:   fn,
		ctx:  ctx,
	}
	return cancel
}

// Start makes this a manager.Runnable so it can be registered with
// the manager and use its root context.
func (r *runner) Start(ctx context.Context) error {
	r.rootContext = ctx
	close(r.ready) // broadcast that things can be run
	var wg sync.WaitGroup
loop:
	for {
		select {
		case randc := <-r.tostart:
			r.log.Info("starting child", "name", randc.name)
			wg.Add(1)
			go func(rc runWithContext) {
				defer wg.Done()
				rc.do(rc.ctx)
				r.log.Info("child exited", "name", rc.name)
			}(randc)
		case <-r.rootContext.Done():
			break loop
		}
	}
	r.log.Info("Stopping and waiting for children")
	wg.Wait()
	r.log.Info("All children stopped; runner exit")
	return nil
}
