package ratelimiter_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/weaveworks/pipeline-controller/pkg/ratelimiter"
)

func TestLimiter(t *testing.T) {
	limiter := ratelimiter.New(
		ratelimiter.WithLimit(3),
		ratelimiter.WithDuration(time.Millisecond*500),
	)

	var (
		hit *ratelimiter.Limit
		err error
	)

	hit, err = limiter.Hit("127.0.0.1")
	assert.NoError(t, err, "127.0.0.1 shouldn't hit rate limit")
	assert.Equal(t, hit.Hits, 1)

	hit, err = limiter.Hit("127.0.0.1")
	assert.NoError(t, err, "127.0.0.1 shouldn't hit rate limit")
	assert.Equal(t, hit.Hits, 2)

	hit, err = limiter.Hit("127.0.0.1")
	assert.NoError(t, err, "127.0.0.1 shouldn't hit rate limit")
	assert.Equal(t, hit.Hits, 3)

	hit, err = limiter.Hit("127.0.0.1")
	assert.Error(t, err, "127.0.0.1 should hit rate limit")
	assert.Equal(t, hit.Hits, 4)

	hit, err = limiter.Hit("127.0.0.2")
	assert.NoError(t, err, "127.0.0.2 shouldn't hit rate limit")
	assert.Equal(t, hit.Hits, 1)

	time.Sleep(time.Second)

	hit, err = limiter.Hit("127.0.0.1")
	assert.NoError(t, err, "127.0.0.1 shouldn't hit rate limit")
	assert.Equal(t, hit.Hits, 1)
}
