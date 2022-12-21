package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/fluxcd/pkg/runtime/logger"
	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

type DefaultApprovalHandler struct {
	log      logr.Logger
	c        client.Client
	stratReg strategy.StrategyRegistry
}

func NewDefaultApprovalHandler(log logr.Logger, stratReg strategy.StrategyRegistry, c client.Client) DefaultApprovalHandler {
	return DefaultApprovalHandler{
		log:      log,
		c:        c,
		stratReg: stratReg,
	}
}

func (h DefaultApprovalHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		rw.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	pathPattern := regexp.MustCompile("([^/]+)/([^/]+)/([^/]+)/([^/]+)")
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
	revision := pathMatches[4]

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

	waitingApproval := pipeline.Status.GetWaitingApproval(env)

	if waitingApproval.Revision != revision {
		h.log.V(logger.InfoLevel).Info("pipeline is not waiting approval for", "env", env, "revision", revision)
		rw.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(rw, "failed approving promotion")
		return
	}

	promotion.Version = waitingApproval.Revision

	promEnv, err := lookupEnvironment(pipeline, env)
	if err != nil {
		rw.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(rw, err.Error())
		return
	}
	promotion.Environment = promEnv

	promSpec := pipeline.Spec.GetPromotion(promEnv.Name)

	if promSpec == nil {
		h.log.Error(err, "no promotion configured in Pipeline resource")
		rw.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(rw, "error promoting application, please consult the promotion server's logs")
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

	// Reseting waiting approval after promotion is done.
	if err := h.resetWaitingApproval(r.Context(), pipeline, env); err != nil {
		h.log.Error(err, "error resetting waiting approval")
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

func (h DefaultApprovalHandler) promote(ctx context.Context, promotionSpec *pipelinev1alpha1.Promotion, prom strategy.Promotion) (*strategy.PromotionResult, error) {
	if promotionSpec == nil {
		return nil, fmt.Errorf("no promotion configured in Pipeline resource")
	}

	strat, err := h.stratReg.Get(*promotionSpec)
	if err != nil {
		return nil, fmt.Errorf("error getting strategy from registry: %w", err)
	}
	return strat.Promote(ctx, *promotionSpec, prom)
}

// lookupEnvironment searches the pipeline for the given environment name.
func lookupEnvironment(pipeline pipelinev1alpha1.Pipeline, env string) (pipelinev1alpha1.Environment, error) {
	for _, e := range pipeline.Spec.Environments {
		if e.Name == env {
			return e, nil
		}
	}

	return pipelinev1alpha1.Environment{}, fmt.Errorf("app %s/%s has no environment %s defined", pipeline.Namespace, pipeline.Name, env)
}

func (h DefaultApprovalHandler) resetWaitingApproval(ctx context.Context, pipeline pipelinev1alpha1.Pipeline, env string) error {
	h.log.Info("resetting waiting approval for", "pipeline", pipeline.Name, "env", env)
	if err := h.c.Get(ctx, client.ObjectKeyFromObject(&pipeline), &pipeline); err != nil {
		return err
	}

	pipeline.Status.ResetWaitingApproval(env)

	return h.c.Status().Update(ctx, &pipeline)
}
