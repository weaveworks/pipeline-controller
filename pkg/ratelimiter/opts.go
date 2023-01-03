package ratelimiter

import "time"

type LimiterOpt func(*Limiter)

func WithLimit(limit int) LimiterOpt {
	return func(rl *Limiter) {
		rl.Limit = limit
	}
}

func WithDuration(duration time.Duration) LimiterOpt {
	return func(rl *Limiter) {
		rl.Duration = duration
	}
}
