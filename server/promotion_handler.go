package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/fluxcd/pkg/runtime/events"
	"github.com/fluxcd/pkg/runtime/logger"
	"github.com/go-logr/logr"
	"golang.org/x/net/context"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

type DefaultPromotionHandler struct {
	log          logr.Logger
	promStrategy Strategy
	c            client.Client
}

func NewDefaultPromotionHandler(log logr.Logger, strat Strategy, c client.Client) DefaultPromotionHandler {
	return DefaultPromotionHandler{
		log:          log,
		promStrategy: strat,
		c:            c,
	}
}

func (h DefaultPromotionHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		rw.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	pathPattern := regexp.MustCompile("([^/]+)/([^/]+)/([^/]+)")
	pathMatches := pathPattern.FindStringSubmatch(r.URL.Path)
	if pathMatches == nil {
		h.log.V(logger.DebugLevel).Info("request for unknown path", "path", r.URL.Path)
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
		h.log.V(logger.DebugLevel).Info("failed decoding request body")
		rw.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	promotion.Version = ev.Metadata["revision"]

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// fetch pipeline

	var pipeline pipelinev1alpha1.Pipeline
	if err := h.c.Get(ctx, client.ObjectKey{Namespace: promotion.AppNS, Name: promotion.AppName}, &pipeline); err != nil {
		h.log.V(logger.DebugLevel).Info("could not fetch Pipeline object")
		rw.WriteHeader(http.StatusBadRequest)
		return
	}

	// find the target environment

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
	if pipeline.Spec.AppRef.APIVersion != ev.InvolvedObject.APIVersion ||
		pipeline.Spec.AppRef.Kind != ev.InvolvedObject.Kind ||
		pipeline.Spec.AppRef.Name != ev.InvolvedObject.Name ||
		!namespaceInTargets(sourceEnv.Targets, ev.InvolvedObject.Namespace) {
		rw.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprintf(rw, "involved object doesn't match Pipeline definition")
		return
	}

	promotion.Environment = *promEnv

	h.log.Info("promoting app")

	res, err := h.promStrategy.Promote(ctx, promotion)
	if err != nil {
		h.log.Error(err, "promotion failed")
		rw.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(rw, "promotion failed: %s", err)
		return
	}

	if res.Location != "" {
		rw.Header().Add("Location", res.Location)
		rw.WriteHeader(http.StatusCreated)
	} else {
		rw.WriteHeader(http.StatusNoContent)
	}
}
