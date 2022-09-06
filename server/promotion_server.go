package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/fluxcd/pkg/runtime/events"
	"github.com/go-logr/logr"
	"golang.org/x/net/context"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

type PromotionServer struct {
	log          logr.Logger
	c            client.Client
	addr         string
	promStrategy Strategy
}

type NopStrategy struct{}

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

	return s, nil
}

func (s PromotionServer) ListenAndServe(stopCh <-chan struct{}) {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		panic(err)
	}

	hndlr := http.NewServeMux()
	pathPattern := regexp.MustCompile("/promotion/([^/]+)/([^/]+)/([^/]+)")
	hndlr.HandleFunc("/promotion/", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			rw.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		pathMatches := pathPattern.FindStringSubmatch(r.URL.Path)
		if pathMatches == nil {
			s.log.Info("request for unknown path", "path", r.URL.Path)
			http.NotFound(rw, r)
			return
		}

		// extract pipeline and environment identifiers from URL path

		appNS, appName, env := func(parts []string) (string, string, string) {
			return pathMatches[1], pathMatches[2], pathMatches[3]
		}(pathMatches)
		promotion := Promotion{
			AppNS:   appNS,
			AppName: appName,
		}

		// extract target app version from event payload

		var ev events.Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			rw.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		promotion.Version = ev.Metadata["revision"]

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// fetch pipeline

		var pipeline pipelinev1alpha1.Pipeline
		if err := s.c.Get(ctx, client.ObjectKey{Namespace: promotion.AppNS, Name: promotion.AppName}, &pipeline); err != nil {
			s.log.Info("could not fetch Pipeline object")
			rw.WriteHeader(http.StatusBadRequest)
			return
		}

		// find the target environment and Git spec

		var sourceEnv *pipelinev1alpha1.Environment
		var promEnv *pipelinev1alpha1.Environment
		for idx, pEnv := range pipeline.Spec.Environments {
			if pEnv.Name == env {
				if idx == len(pipeline.Spec.Environments)-1 {
					rw.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(rw, "cannot promote beyond last environment %s", pEnv.Name)
					return
				}
				sourceEnv = &pipeline.Spec.Environments[idx]
				promEnv = &pipeline.Spec.Environments[idx+1]
			}
		}
		if promEnv == nil {
			rw.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(rw, "app %s/%s has no environment %s defined", promotion.AppNS, promotion.AppName, env)
			return
		}
		if len(promEnv.Targets) == 0 {
			rw.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(rw, "environment %s has no targets", promEnv.Name)
			return
		}
		if promEnv.GitSpec == nil {
			rw.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(rw, "environment %s has no Git spec, promotion cannot happen", promEnv.Name)
			return
		}
		if pipeline.Spec.AppRef.APIVersion != ev.InvolvedObject.APIVersion ||
			pipeline.Spec.AppRef.Kind != ev.InvolvedObject.Kind ||
			pipeline.Spec.AppRef.Name != ev.InvolvedObject.Name ||
			!namespaceInTargets(sourceEnv.Targets, ev.InvolvedObject.Namespace) {
			rw.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprintf(rw, "involved object doesn't match Pipeline definition")
			return
		}

		promotion.Environment = *promEnv

		log := s.log.WithValues("promotion", promotion)
		log.Info("promoting app")

		res, err := s.promStrategy.Promote(ctx, promotion)
		if err != nil {
			log.Error(err, "promotion failed")
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}

		if res.Location != "" {
			rw.Header().Add("Location", res.Location)
			rw.WriteHeader(http.StatusCreated)
		} else {
			rw.WriteHeader(http.StatusNoContent)
		}
	})

	srv := http.Server{
		Addr:    listener.Addr().String(),
		Handler: hndlr,
	}

	go func() {
		if err := srv.Serve(listener); err != nil {
			panic(err)
		}
	}()

	<-stopCh

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		panic(err)
	}
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
