package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/fluxcd/pkg/runtime/events"
	"github.com/fluxcd/pkg/runtime/logger"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

const (
	SignatureHeader = "X-Signature"
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

	if err := h.verifyXSignature(r.Context(), pipeline, r.Header, body); err != nil {
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
		rw.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(rw, err.Error())
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

	strat, err := h.stratReg.Get(*promotionSpec)
	if err != nil {
		return nil, fmt.Errorf("error getting strategy from registry: %w", err)
	}
	return strat.Promote(ctx, *promotionSpec, prom)
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

func (h DefaultPromotionHandler) verifyXSignature(ctx context.Context, p pipelinev1alpha1.Pipeline, header http.Header, body []byte) error {
	// If not secret defined just ignore the X-Signature checking
	if p.Spec.AppRef.SecretRef == nil {
		return nil
	}

	if len(header[SignatureHeader]) == 0 {
		return errors.New("no X-Signature header provided")
	}

	s := &corev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:      p.Spec.AppRef.SecretRef.Name,
			Namespace: p.Namespace,
		},
	}

	if err := h.c.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
		return fmt.Errorf("failed fetching Secret %s/%s: %w", s.Namespace, s.Name, err)
	}

	key := s.Data["hmac-key"]
	if len(key) == 0 {
		return fmt.Errorf("no 'hmac-key' field present in %s/%s Spec.AppRef.SecretRef", p.Namespace, s.Name)
	}

	if err := verifySignature(header[SignatureHeader][0], body, key); err != nil {
		return fmt.Errorf("failed verifying X-Signature header: %s", err)
	}

	return nil
}

func verifySignature(sig string, payload, key []byte) error {
	sigHdr := strings.Split(sig, "=")
	if len(sigHdr) != 2 {
		return fmt.Errorf("invalid signature value")
	}

	var newF func() hash.Hash

	switch sigHdr[0] {
	case "sha224":
		newF = sha256.New224
	case "sha256":
		newF = sha256.New
	case "sha384":
		newF = sha512.New384
	case "sha512":
		newF = sha512.New
	default:
		return fmt.Errorf("unsupported signature algorithm %q", sigHdr[0])
	}

	mac := hmac.New(newF, key)
	if _, err := mac.Write(payload); err != nil {
		return fmt.Errorf("error MAC'ing payload: %w", err)
	}

	sum := fmt.Sprintf("%x", mac.Sum(nil))
	if sum != sigHdr[1] {
		return fmt.Errorf("HMACs don't match: %#v != %#v", sum, sigHdr[1])
	}

	return nil
}
