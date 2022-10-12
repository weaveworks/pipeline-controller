package server

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"golang.org/x/net/context"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

type PromotionServer struct {
	log              logr.Logger
	c                client.Client
	addr             string
	listener         net.Listener
	promHandler      http.Handler
	promEndpointName string
	stratReg         strategy.StrategyRegistry
}

type Opt func(s *PromotionServer) error

var (
	ErrClientCantBeNil       = fmt.Errorf("client can't be nil")
	DefaultListenAddr        = "127.0.0.1:8080"
	DefaultPromotionEndpoint = "/promotion"
	DefaultStratReg          = strategy.StrategyRegistry{"nop": strategy.Nop{}}
)

func NewPromotionServer(c client.Client, opts ...Opt) (*PromotionServer, error) {
	if c == nil {
		return nil, ErrClientCantBeNil
	}

	s := &PromotionServer{
		c: c,
	}

	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
	}

	// set defaults

	if s.log.GetSink() == nil {
		s.log = stdr.New(log.New(os.Stdout, "", log.Lshortfile))
	}

	if s.addr == "" {
		s.addr = DefaultListenAddr
	}

	if s.stratReg == nil {
		s.stratReg = DefaultStratReg
	}

	if s.promHandler == nil {
		s.promHandler = NewDefaultPromotionHandler(
			s.log.WithName("handler"),
			s.stratReg,
			s.c,
		)
	}
	if s.promEndpointName == "" {
		s.promEndpointName = DefaultPromotionEndpoint
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
