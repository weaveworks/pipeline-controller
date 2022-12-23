package ratelimiter

import "time"

type Limit struct {
	Created time.Time
	Key     string
	Hits    int
}

func newLimit(key string) *Limit {
	return &Limit{Created: time.Now(), Key: key, Hits: 0}
}

func (limit *Limit) Hit() {
	limit.Hits += 1
}
