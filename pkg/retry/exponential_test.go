package retry_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/weaveworks/pipeline-controller/pkg/retry"
)

func TestExponential(t *testing.T) {
	tests := []struct {
		name              string
		opts              []retry.ExponentialWith
		wantErr           bool
		numberOfErrCalled int
		minTime           float64
		maxTime           float64
	}{
		{
			name:              "with defaults",
			opts:              []retry.ExponentialWith{},
			wantErr:           true,
			numberOfErrCalled: 0,
			minTime:           0,
			maxTime:           1.1,
		},
		{
			name: "with fn",
			opts: []retry.ExponentialWith{
				retry.WithFn(func() error {
					return fmt.Errorf("some error")
				}),
				retry.WithDelayBase(1.1),
				retry.WithMaxDelay(2),
			},
			wantErr:           true,
			numberOfErrCalled: 3,
			minTime:           2,
			maxTime:           3.1,
		},
		{
			name: "with error handler",
			opts: []retry.ExponentialWith{
				retry.WithFn(func() error {
					return fmt.Errorf("some error")
				}),
				retry.WithErrorHandler(func(e error) bool {
					return false
				}),
				retry.WithRetries(2),
				retry.WithMaxDelay(4),
			},
			wantErr:           true,
			numberOfErrCalled: 0,
			minTime:           2,
			maxTime:           3.1,
		},
		{
			name: "no retry",
			opts: []retry.ExponentialWith{
				retry.WithFn(func() error {
					return fmt.Errorf("some error")
				}),
				retry.WithDelayBase(1),
				retry.WithRetries(0),
			},
			wantErr:           true,
			numberOfErrCalled: 1,
			minTime:           0,
			maxTime:           3.1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errCalled := 0
			errFn := func(_ error) bool {
				errCalled++
				return false
			}
			start := time.Now()
			// Make sure it's called first and allow test case to override this.
			opts := []retry.ExponentialWith{retry.WithErrorHandler(errFn)}
			opts = append(opts, tt.opts...)
			if err := retry.Exponential(opts...); (err != nil) != tt.wantErr {
				t.Errorf("Exponential() error = %v, wantErr %v", err, tt.wantErr)
			}
			execTime := time.Since(start)
			if tt.minTime != 0 && float64(execTime) < tt.minTime*float64(time.Second) {
				t.Errorf("Exponential() expected to run longer then %.2fs, done in %.2fs", tt.minTime, execTime.Seconds())
			}
			if tt.maxTime != 0 && float64(execTime) > tt.maxTime*float64(time.Second) {
				t.Errorf("Exponential() expected to be done under %.2fs, done in %.2fs", tt.maxTime, execTime.Seconds())
			}
			if errCalled != tt.numberOfErrCalled {
				t.Errorf("Error handler of Exponential() expected to be called %d times, it was called %d times", tt.numberOfErrCalled, errCalled)
			}
		})
	}
}
