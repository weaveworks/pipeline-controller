package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/net/context"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

type PromotionServer struct {
	log              logr.Logger
	c                client.Client
	addr             string
	listener         net.Listener
	promHandler      http.Handler
	promStrategy     Strategy
	promEndpointName string
}

type NopStrategy struct{}

var defaultPromotionEndpoint = "/promotion"

func (n NopStrategy) Promote(_ context.Context, _ Promotion) (*PromotionResult, error) {
	return &PromotionResult{}, nil
}

type Opt func(s *PromotionServer) error

func NewPromotionServer(opts ...Opt) (*PromotionServer, error) {
	s := &PromotionServer{}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
	}

	// set defaults
	if s.promHandler == nil {
		s.promHandler = DefaultPromotionHandler{
			log:          s.log.WithName("handler"),
			promStrategy: s.promStrategy,
			c:            s.c,
		}
	}
	if s.promEndpointName == "" {
		s.promEndpointName = defaultPromotionEndpoint
	}

	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("failed creating listener: %w", err)
	}
	s.listener = listener

	return s, nil
}

func (s PromotionServer) Start(ctx context.Context) error {
	pathPrefix := "/promotion/"

	mux := http.NewServeMux()
	mux.Handle(pathPrefix, http.StripPrefix(s.promEndpointName, s.promHandler))

	srv := http.Server{
		Addr:    s.listener.Addr().String(),
		Handler: mux,
	}

	go func() {
		log := s.log.WithValues("kind", "promotion webhook", "path", pathPrefix, "addr", s.listener.Addr())
		log.Info("Starting server")
		if err := srv.Serve(s.listener); err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			log.Error(err, "failed serving")
		}
	}()

	<-ctx.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

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
