package leveltriggered

import "context"

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
	ctx context.Context
	run func(context.Context)
}

type runner struct {
	rootContext context.Context
	tostart     chan runWithContext
	ready       chan struct{}
}

func newRunner() *runner {
	return &runner{
		tostart: make(chan runWithContext),
		ready:   make(chan struct{}),
	}
}

func (r *runner) run(fn func(ctx context.Context)) context.CancelFunc {
	<-r.ready // wait until there's a root context
	ctx, cancel := context.WithCancel(r.rootContext)
	r.tostart <- runWithContext{
		run: fn,
		ctx: ctx,
	}
	return cancel
}

// Start makes this a manager.Runnable so it can be registered with
// the manager and use its root context.
func (r *runner) Start(ctx context.Context) error {
	r.rootContext = ctx
	close(r.ready) // broadcast that things can be run
	for {
		select {
		case randc := <-r.tostart:
			go randc.run(randc.ctx)
		case <-r.rootContext.Done():
			return nil
		}
	}
}
