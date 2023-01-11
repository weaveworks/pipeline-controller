package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/pkg/ratelimiter"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

const (
	DefaultRateLimitCount    = 20
	DefaultRateLimitInterval = 30
	DefaultRetryDelay        = 2
	DefaultRetryMaxDelay     = 20
	DefaultRetryThreshold    = 3
)

type PromotionServer struct {
	log                  logr.Logger
	c                    client.Client
	addr                 string
	listener             net.Listener
	promHandler          http.Handler
	promEndpointName     string
	approvalHandler      http.Handler
	approvalEndpointName string
	stratReg             strategy.StrategyRegistry
	rateLimit            rateLimit
	retry                RetryOpts
}

type rateLimit struct {
	count    int
	interval time.Duration
}

type RetryOpts struct {
	Delay     int
	MaxDelay  int
	Threshold int
}

type Opt func(s *PromotionServer) error

var (
	ErrClientCantBeNil       = fmt.Errorf("client can't be nil")
	DefaultListenAddr        = "127.0.0.1:8080"
	DefaultPromotionEndpoint = "/promotion"
	DefaultApprovalEndpoint  = "/approval"
)

func NewPromotionServer(c client.Client, opts ...Opt) (*PromotionServer, error) {
	if c == nil {
		return nil, ErrClientCantBeNil
	}

	s := &PromotionServer{
		c: c,
		rateLimit: rateLimit{
			count:    DefaultRateLimitCount,
			interval: time.Second * DefaultRateLimitInterval,
		},
	}

	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
	}
	setDefaults(s)

	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("failed creating listener: %w", err)
	}
	s.listener = listener

	return s, nil
}

func WithRateLimit(count int, interval time.Duration) Opt {
	return func(s *PromotionServer) error {
		s.rateLimit.count = count
		s.rateLimit.interval = interval

		return nil
	}
}

func setDefaults(s *PromotionServer) {
	if s.log.GetSink() == nil {
		s.log = stdr.New(log.New(os.Stdout, "", log.Lshortfile))
	}

	if s.addr == "" {
		s.addr = DefaultListenAddr
	}

	if s.promHandler == nil {
		s.promHandler = NewDefaultPromotionHandler(
			s.log.WithName("handler"),
			s.stratReg,
			s.c,
			s.retry,
		)
	}
	if s.promEndpointName == "" {
		s.promEndpointName = DefaultPromotionEndpoint
	}

	if s.approvalHandler == nil {
		s.approvalHandler = NewDefaultApprovalHandler(
			s.log.WithName("handler"),
			s.stratReg,
			s.c,
		)
	}
	if s.approvalEndpointName == "" {
		s.approvalEndpointName = DefaultApprovalEndpoint
	}
}

func getRealIP(r *http.Request) string {
	address := r.Header.Get("X-Real-IP")
	if address == "" {
		address = r.Header.Get("X-Forwarder-For")
	}
	if address == "" {
		address = r.RemoteAddr
	}

	if strings.Contains(address, ":") {
		address = strings.Split(address, ":")[0]
	}

	return address
}

func (s PromotionServer) rateLimitMiddleware(limiter *ratelimiter.Limiter, h http.Handler) http.Handler {
	log := s.log.WithValues("kind", "promotion webhook rate limiter")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getRealIP(r)
		if limit, err := limiter.Hit(ip); err != nil {
			log.Error(err, "rate limit hit", "ip", ip)
			w.Header().Add("Retry-After", limit.Created.Add(limiter.Duration).Format(time.RFC1123))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}

		h.ServeHTTP(w, r)
	})
}

func (s PromotionServer) Start(ctx context.Context) error {
	promPathPrefix := "/promotion/"
	approvalPathPrefix := "/approval/"

	limiter := ratelimiter.New(
		ratelimiter.WithLimit(s.rateLimit.count),
		ratelimiter.WithDuration(s.rateLimit.interval),
	)

	mux := http.NewServeMux()
	mux.Handle(promPathPrefix,
		s.rateLimitMiddleware(
			limiter,
			http.StripPrefix(s.promEndpointName, s.promHandler),
		),
	)
	mux.Handle(approvalPathPrefix,
		s.rateLimitMiddleware(
			limiter,
			http.StripPrefix(s.approvalEndpointName, s.approvalHandler),
		),
	)
	mux.Handle("/healthz", healthz.CheckHandler{Checker: healthz.Ping})

	srv := http.Server{
		Addr:    s.listener.Addr().String(),
		Handler: mux,
	}

	go func() {
		log := s.log.WithValues("kind", "promotion webhook", "path", promPathPrefix, "addr", s.listener.Addr())
		log.Info("Starting server")
		if err := srv.Serve(s.listener); err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			log.Error(err, "failed serving")
		}
	}()

	<-ctx.Done()

	limiter.Shutdown()

	return srv.Shutdown(ctx)
}

// namespaceInTargets returns true if the given namespace name is declared in at least one of the given targets.
func namespaceInTargets(targets []pipelinev1alpha1.Target, namespace string) bool {
	for _, t := range targets {
		if t.Namespace == namespace {
			return true
		}
	}
	return false
}
