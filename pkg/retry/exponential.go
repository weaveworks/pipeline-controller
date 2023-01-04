package retry

import (
	"math"
	"time"
)

type RetryError struct {
	message string
}

func (e RetryError) Error() string {
	return e.message
}

type RetryFn func() error
type ErrorHandlerFn func(error) bool

type ExponentialOptions struct {
	retries          int
	delayBaseSeconds float64
	maxDelaySeconds  float64
	fn               RetryFn
	errorHandlerFn   ErrorHandlerFn
}

func Exponential(withOpts ...ExponentialWith) error {
	opts := ExponentialOptions{
		retries:          3,
		delayBaseSeconds: 2,
		maxDelaySeconds:  30,
	}

	for _, fn := range withOpts {
		fn(&opts)
	}

	if opts.fn == nil {
		return RetryError{message: "fn is not defined, nothing to retry"}
	}

	var (
		err error
		try int = 0
	)

	for {
		try++

		wait := math.Pow(opts.delayBaseSeconds, float64(try))
		if wait > opts.maxDelaySeconds {
			wait = opts.maxDelaySeconds
		}

		err = opts.fn()
		if err == nil {
			return nil
		}

		if opts.errorHandlerFn != nil && opts.errorHandlerFn(err) {
			return nil
		}

		if try >= opts.retries {
			break
		}

		time.Sleep(time.Second * time.Duration(wait))
	}

	return err
}
