package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/fluxcd/pkg/runtime/events"
	"github.com/fluxcd/pkg/runtime/logger"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

type DefaultPromotionHandler struct {
	log      logr.Logger
	c        client.Client
	stratReg strategy.StrategyRegistry
}

func NewDefaultPromotionHandler(log logr.Logger, stratReg strategy.StrategyRegistry, c client.Client) DefaultPromotionHandler {
	return DefaultPromotionHandler{
		log:      log,
		c:        c,
		stratReg: stratReg,
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

	appNS, appName, env := pathMatches[1], pathMatches[2], pathMatches[3]
	promotion := strategy.Promotion{
		AppNS:   appNS,
		AppName: appName,
	}

	var ev events.Event
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		h.log.V(logger.DebugLevel).Info("failed decoding request body")
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	promotion.Version = ev.Metadata["revision"]

	var pipeline pipelinev1alpha1.Pipeline
	if err := h.c.Get(r.Context(), client.ObjectKey{Namespace: promotion.AppNS, Name: promotion.AppName}, &pipeline); err != nil {
		h.log.V(logger.DebugLevel).Info("could not fetch Pipeline object", "error", err)
		if errors.IsNotFound(err) {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}

	promEnv, err := lookupNextEnvironment(pipeline, env, ev.InvolvedObject)
	if err != nil {
		rw.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(rw, err.Error())
		return
	}

	promotion.Environment = *promEnv

	h.log.Info("promoting app", "app", pipeline.Spec.AppRef, "source environment", env, "target environment", promotion.Environment.Name)

	requestedStrategy := "nop" // this will later be derived from the Pipeline spec
	strat, ok := h.stratReg[requestedStrategy]
	if !ok {
		rw.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(rw, "unknown promotion strategy %q requested.", requestedStrategy)
		return
	}
	res, err := strat.Promote(r.Context(), promotion)
	if err != nil {
		h.log.Error(err, "promotion failed")
		rw.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(rw, "promotion failed")
		return
	}

	if res.Location != "" {
		rw.Header().Add("Location", res.Location)
		rw.WriteHeader(http.StatusCreated)
	} else {
		rw.WriteHeader(http.StatusNoContent)
	}
}

// lookupNextEnvironment searches the pipeline for the given environment name and returns the subsequent environment. The given pipeline's appRef
// needs to match the given object reference and the environment pointed to by "env" needs to have at least one target with the appRef's namespace.
// This ensures that promotion can only be triggered by objects residing in a namespace that is part of an environment's target.
func lookupNextEnvironment(pipeline pipelinev1alpha1.Pipeline, env string, appRef corev1.ObjectReference) (*pipelinev1alpha1.Environment, error) {
	var sourceEnv *pipelinev1alpha1.Environment
	var promEnv *pipelinev1alpha1.Environment
	for idx, pEnv := range pipeline.Spec.Environments {
		if pEnv.Name == env {
			if idx == len(pipeline.Spec.Environments)-1 {
				return nil, fmt.Errorf("cannot promote beyond last environment %s", pEnv.Name)
			}
			sourceEnv = &pipeline.Spec.Environments[idx]
			promEnv = &pipeline.Spec.Environments[idx+1]
			break
		}
	}
	if promEnv == nil {
		return nil, fmt.Errorf("app %s/%s has no environment %s defined", pipeline.Namespace, pipeline.Name, env)
	}
	if len(promEnv.Targets) == 0 {
		return nil, fmt.Errorf("environment %s has no targets", promEnv.Name)
	}
	if pipeline.Spec.AppRef.APIVersion != appRef.APIVersion ||
		pipeline.Spec.AppRef.Kind != appRef.Kind ||
		pipeline.Spec.AppRef.Name != appRef.Name ||
		!namespaceInTargets(sourceEnv.Targets, appRef.Namespace) {
		return nil, fmt.Errorf("involved object doesn't match Pipeline definition")
	}
	return promEnv, nil
}
