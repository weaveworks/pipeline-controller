package retry

type ExponentialWith func(opt *ExponentialOptions)

func WithRetries(retries int) ExponentialWith {
	return func(opt *ExponentialOptions) {
		opt.retries = retries
	}
}

func WithDelayBase(delaySeconds float64) ExponentialWith {
	return func(opt *ExponentialOptions) {
		opt.delayBaseSeconds = delaySeconds
	}
}

func WithMaxDelay(maxSeconds float64) ExponentialWith {
	return func(opt *ExponentialOptions) {
		opt.maxDelaySeconds = maxSeconds
	}
}

func WithErrorHandler(errorHandlerFn ErrorHandlerFn) ExponentialWith {
	return func(opt *ExponentialOptions) {
		opt.errorHandlerFn = errorHandlerFn
	}
}

func WithFn(fn RetryFn) ExponentialWith {
	return func(opt *ExponentialOptions) {
		opt.fn = fn
	}
}
