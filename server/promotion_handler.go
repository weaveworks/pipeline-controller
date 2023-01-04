package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"regexp"

	"github.com/fluxcd/pkg/runtime/events"
	"github.com/fluxcd/pkg/runtime/logger"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/pkg/retry"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

const (
	SignatureHeader = "X-Signature"
)

type DefaultPromotionHandler struct {
	log      logr.Logger
	c        client.Client
	stratReg strategy.StrategyRegistry
	retry    RetryOpts
}

func NewDefaultPromotionHandler(log logr.Logger, stratReg strategy.StrategyRegistry, c client.Client, retryOpts RetryOpts) DefaultPromotionHandler {
	return DefaultPromotionHandler{
		log:      log,
		c:        c,
		stratReg: stratReg,
		retry:    retryOpts,
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

	promotion := strategy.Promotion{
		PipelineNamespace: pathMatches[1],
		PipelineName:      pathMatches[2],
	}
	env := pathMatches[3]

	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.log.V(logger.DebugLevel).Error(err, "reading request body")
		rw.WriteHeader(http.StatusBadRequest)
		return
	}

	var pipeline pipelinev1alpha1.Pipeline
	if err := h.c.Get(r.Context(), client.ObjectKey{Namespace: promotion.PipelineNamespace, Name: promotion.PipelineName}, &pipeline); err != nil {
		h.log.V(logger.InfoLevel).Info("could not fetch Pipeline object", "error", err)
		if k8serrors.IsNotFound(err) {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err := verifyXSignature(r.Context(), h.c, pipeline, r.Header, body); err != nil {
		h.log.V(logger.DebugLevel).Error(err, "failed verifying X-Signature header")
		rw.WriteHeader(http.StatusUnauthorized)
		return
	}

	var ev events.Event
	if err := json.Unmarshal(body, &ev); err != nil {
		h.log.V(logger.DebugLevel).Info("failed decoding request body")
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	promotion.Version = ev.Metadata["revision"]
	if promotion.Version == "" {
		rw.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprintf(rw, "event has no 'revision' in the metadata field.")
		return
	}

	promEnv, err := lookupNextEnvironment(pipeline, env, ev.InvolvedObject)
	if err != nil {
		h.log.Error(err, "error looking up next environment")
		rw.WriteHeader(http.StatusUnprocessableEntity)
		template.HTMLEscape(rw, []byte(err.Error()))
		return
	}
	promotion.Environment = *promEnv

	promSpec := pipeline.Spec.GetPromotion(promEnv.Name)

	if promSpec == nil {
		h.log.Error(err, "no promotion configured in Pipeline resource")
		rw.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(rw, "error promoting application, please consult the promotion server's logs")
		return
	}

	if promSpec.Manual {
		if err := h.setWaitingApproval(r.Context(), pipeline, promEnv.Name, promotion.Version); err != nil {
			h.log.Error(err, "error setting waiting approval", "env", promEnv.Name)
			rw.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(rw, "error promoting application, please consult the promotion server's logs")
			return
		}

		rw.WriteHeader(http.StatusNoContent)
		return
	}

	h.log.Info("promoting app", "app", pipeline.Spec.AppRef, "source environment", env, "target environment", promotion.Environment.Name)

	res, err := h.promote(r.Context(), promSpec, promotion)
	if err != nil {
		h.log.Error(err, "error promoting application")
		rw.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(rw, "error promoting application, please consult the promotion server's logs")
		return
	}

	if res.Location != "" {
		rw.Header().Add("Location", res.Location)
		rw.WriteHeader(http.StatusCreated)
	} else {
		rw.WriteHeader(http.StatusNoContent)
	}
}

func (h DefaultPromotionHandler) setWaitingApproval(ctx context.Context, pipeline pipelinev1alpha1.Pipeline, env string, revision string) error {
	h.log.Info("set waiting approval to", "pipeline", pipeline.Name, "env", env)
	if err := h.c.Get(ctx, client.ObjectKeyFromObject(&pipeline), &pipeline); err != nil {
		return err
	}

	pipeline.Status.SetWaitingApproval(env, revision)

	return h.c.Status().Update(ctx, &pipeline)
}

func (h DefaultPromotionHandler) promote(ctx context.Context, promotionSpec *pipelinev1alpha1.Promotion, prom strategy.Promotion) (*strategy.PromotionResult, error) {
	if promotionSpec == nil {
		return nil, fmt.Errorf("no promotion configured in Pipeline resource")
	}

	var res *strategy.PromotionResult

	err := retry.Exponential(
		retry.WithRetries(h.retry.Threshold),
		retry.WithDelayBase(float64(h.retry.Delay)),
		retry.WithMaxDelay(float64(h.retry.MaxDelay)),
		retry.WithErrorHandler(func(err error) bool {
			h.log.Error(err, "retry promotion")

			return false
		}),
		retry.WithFn(func() error {
			strat, err := h.stratReg.Get(*promotionSpec)
			if err != nil {
				return fmt.Errorf("error getting strategy from registry: %w", err)
			}

			res, err = strat.Promote(ctx, *promotionSpec, prom)
			return err
		}),
	)

	return res, err
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
		return nil, fmt.Errorf("involved object does not match Pipeline definition")
	}
	return promEnv, nil
}
