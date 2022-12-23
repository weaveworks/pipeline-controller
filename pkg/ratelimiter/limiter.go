package ratelimiter

import (
	"fmt"
	"sync"
	"time"
)

const (
	DefaultDuration = time.Second * 30
	DefaultLimit    = 20
)

type Limiter struct {
	sync.Mutex
	Limit    int
	Duration time.Duration

	limits  map[string]*Limit
	cleanup []func()
}

func New(opts ...LimiterOpt) *Limiter {
	limiter := Limiter{
		Limit:    DefaultLimit,
		Duration: DefaultDuration,
		limits:   map[string]*Limit{},
		cleanup:  []func(){},
	}

	for _, opt := range opts {
		opt(&limiter)
	}

	limiter.cleanup = append(limiter.cleanup, limiter.cleaner())

	return &limiter
}

func (limiter *Limiter) Shutdown() {
	for _, fn := range limiter.cleanup {
		fn()
	}
}

func (limiter *Limiter) Hit(key string) (*Limit, error) {
	limiter.Lock()

	limit, ok := limiter.limits[key]
	if !ok {
		limit = newLimit(key)
		limiter.limits[key] = limit
	}

	limit.Hit()

	if limit.Hits > limiter.Limit {
		limiter.Unlock()
		return limit, fmt.Errorf("%s has reached max requests %d", key, limiter.Limit)
	}

	limiter.Unlock()

	return limit, nil
}

func (limiter *Limiter) cleaner() func() {
	ticker := time.NewTicker(time.Millisecond * 500)

	go func() {
		for range ticker.C {
			base := time.Now().Add(-limiter.Duration)
			limiter.Lock()
			for key, value := range limiter.limits {
				if value.Created.Before(base) {
					delete(limiter.limits, key)
				}
			}
			limiter.Unlock()
		}
	}()

	return ticker.Stop
}
