package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fluxcd/pkg/runtime/events"
	"github.com/fluxcd/pkg/runtime/logger"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	"github.com/weaveworks/pipeline-controller/server"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

func createEvent() events.Event {
	return events.Event{
		InvolvedObject: v1.ObjectReference{
			APIVersion: "helm.toolkit.fluxcd.io/v2beta1",
			Kind:       "HelmRelease",
			Name:       "app",
			Namespace:  "default",
		},
		Metadata: map[string]string{
			"revision": "5.0.0",
		},
	}
}

func marshalEvent(g *WithT, ev events.Event) []byte {
	b, err := json.Marshal(ev)
	g.Expect(err).NotTo(HaveOccurred())
	return b
}

type introspectableStrategy struct {
	promotion strategy.Promotion
	location  string
	err       error
}

func (s *introspectableStrategy) Handles(p v1alpha1.Promotion) bool {
	return true
}

func (s *introspectableStrategy) Promote(ctx context.Context, promSpec v1alpha1.Promotion, prom strategy.Promotion) (*strategy.PromotionResult, error) {
	s.promotion = prom
	if s.err != nil {
		return nil, s.err
	}
	return &strategy.PromotionResult{
		Location: s.location,
	}, nil
}

func requestTo(g *WithT, handler http.Handler, method, dest string, body []byte) *httptest.ResponseRecorder {
	req, err := http.NewRequest(method, dest, bytes.NewReader(body))
	g.Expect(err).NotTo(HaveOccurred())
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	return resp
}

func buildTestPipeline() v1alpha1.Pipeline {
	ns := "default"
	name := "app"

	targets := []v1alpha1.Target{{
		Namespace: ns,
	}}

	pipeline := v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: v1alpha1.PipelineSpec{
			AppRef: v1alpha1.LocalAppReference{
				APIVersion: "helm.toolkit.fluxcd.io/v2beta1",
				Kind:       "HelmRelease",
				Name:       name,
			},
			Environments: []v1alpha1.Environment{
				{
					Name:    "dev",
					Targets: targets,
				},
				{
					Name:    "prod",
					Targets: targets,
				},
				{
					Name:    "no-targets",
					Targets: []v1alpha1.Target{},
				},
			},
		},
	}

	return pipeline
}

func createTestPipeline(g *WithT, t *testing.T) v1alpha1.Pipeline {
	p := buildTestPipeline()
	return createPipeline(g, t, p)
}

func createTestPipelineWithPromotion(g *WithT, t *testing.T) v1alpha1.Pipeline {
	p := buildTestPipeline()
	p.Spec.Promotion = &v1alpha1.Promotion{
		PullRequest: &v1alpha1.PullRequestPromotion{
			URL: "foobar",
		},
	}
	return createPipeline(g, t, p)
}

func createPipeline(g *WithT, t *testing.T, pipeline v1alpha1.Pipeline) v1alpha1.Pipeline {
	g.Expect(k8sClient.Create(context.Background(), &pipeline)).To(Succeed())

	t.Cleanup(func() {
		g.Expect(k8sClient.Delete(context.Background(), &pipeline)).To(Succeed())
	})

	return pipeline
}

func TestGet(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	h := server.DefaultPromotionHandler{}
	resp := requestTo(g, h, http.MethodGet, "/", nil)
	g.Expect(resp.Code).To(Equal(http.StatusMethodNotAllowed))
}

func TestPostWithWrongPath(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{}), nil, nil)
	resp := requestTo(g, h, http.MethodPost, "/", nil)
	g.Expect(resp.Code).To(Equal(http.StatusNotFound))
}

func TestPostWithNoBody(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{}), nil, nil)
	resp := requestTo(g, h, http.MethodPost, "/ns/app/env", nil)
	g.Expect(resp.Code).To(Equal(http.StatusBadRequest))
}

func TestPostWithIncompatibleBody(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{}), nil, nil)
	resp := requestTo(g, h, http.MethodPost, "/ns/app/env", []byte("incompatible"))
	g.Expect(resp.Code).To(Equal(http.StatusBadRequest))
}

func TestPostWithUnknownPipeline(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/ns/app/env", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusNotFound))
}

func TestInvolvedObjectDoesntMatch(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	createTestPipeline(g, t)
	tests := []struct {
		name      string
		transform func(ev *events.Event)
	}{
		{
			name: "name",
			transform: func(ev *events.Event) {
				ev.InvolvedObject.Name = "foo"
			},
		},
		{
			name: "namespace",
			transform: func(ev *events.Event) {
				ev.InvolvedObject.Namespace = "foo"
			},
		},
		{
			name: "API version",
			transform: func(ev *events.Event) {
				ev.InvolvedObject.APIVersion = "foo"
			},
		},
		{
			name: "kind",
			transform: func(ev *events.Event) {
				ev.InvolvedObject.Kind = "foo"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
			ev := createEvent()
			tt.transform(&ev)
			resp := requestTo(g, h, http.MethodPost, "/default/app/dev", marshalEvent(g, ev))
			g.Expect(resp.Code).To(Equal(http.StatusUnprocessableEntity))
			g.Expect(resp.Body.String()).To(Equal("involved object doesn't match Pipeline definition"))
		})
	}
}

func TestPromotionBeyondLastEnv(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	createTestPipeline(g, t)
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/no-targets", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusUnprocessableEntity))
	g.Expect(resp.Body.String()).To(Equal("cannot promote beyond last environment no-targets"))
}

func TestPromotionToEnvWithoutTarget(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	createTestPipeline(g, t)
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/prod", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusUnprocessableEntity))
	g.Expect(resp.Body.String()).To(Equal("environment no-targets has no targets"))
}

func TestPromotionFromUnknownEnv(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	createTestPipeline(g, t)
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/foo", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusUnprocessableEntity))
	g.Expect(resp.Body.String()).To(Equal("app default/app has no environment foo defined"))
}

func TestPromotionWithNoMetadataInEvent(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	createTestPipeline(g, t)
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	ev := createEvent()
	ev.Metadata = nil
	resp := requestTo(g, h, http.MethodPost, "/default/app/foo", marshalEvent(g, ev))
	g.Expect(resp.Code).To(Equal(http.StatusUnprocessableEntity))
	g.Expect(resp.Body.String()).To(Equal("event has no 'revision' in the metadata field."))
}

func TestPromotionStarted(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	createTestPipelineWithPromotion(g, t)

	strat := introspectableStrategy{
		location: "success",
	}
	stratReg := strategy.StrategyRegistry{&strat}
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), stratReg, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/dev", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusCreated))
	g.Expect(resp.Body.String()).To(Equal(""))
	g.Expect(resp.Header().Get("location")).To(Equal("success"))
	expectedProm := strategy.Promotion{
		PipelineNamespace: "default",
		PipelineName:      "app",
		Environment: v1alpha1.Environment{
			Name: "prod",
			Targets: []v1alpha1.Target{
				{
					Namespace: "default",
				},
			},
		},
		Version: "5.0.0",
	}
	g.Expect(strat.promotion).To(Equal(expectedProm))
}

func TestPromotionFails(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	createTestPipelineWithPromotion(g, t)
	strat := introspectableStrategy{
		err: fmt.Errorf("this didn't work"),
	}
	stratReg := strategy.StrategyRegistry{&strat}
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), stratReg, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/dev", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusInternalServerError))
	g.Expect(resp.Body.String()).To(Equal("error promoting application, please consult the promotion server's logs"))
	expectedProm := strategy.Promotion{
		PipelineNamespace: "default",
		PipelineName:      "app",
		Environment: v1alpha1.Environment{
			Name: "prod",
			Targets: []v1alpha1.Target{
				{
					Namespace: "default",
				},
			},
		},
		Version: "5.0.0",
	}
	g.Expect(strat.promotion).To(Equal(expectedProm))
}

func TestPromotionWithoutLocation(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	createTestPipelineWithPromotion(g, t)
	strat := introspectableStrategy{}
	stratReg := strategy.StrategyRegistry{&strat}
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), stratReg, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/dev", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusNoContent))
	g.Expect(resp.Body.String()).To(Equal(""))
	g.Expect(resp.Header()).NotTo(HaveKey("location"))
	expectedProm := strategy.Promotion{
		PipelineNamespace: "default",
		PipelineName:      "app",
		Environment: v1alpha1.Environment{
			Name: "prod",
			Targets: []v1alpha1.Target{
				{
					Namespace: "default",
				},
			},
		},
		Version: "5.0.0",
	}
	g.Expect(strat.promotion).To(Equal(expectedProm))
}

func TestPromotionWithoutPromotionSpec(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	createTestPipeline(g, t)
	strat := introspectableStrategy{}
	stratReg := strategy.StrategyRegistry{&strat}
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), stratReg, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/dev", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusInternalServerError))
	g.Expect(resp.Body.String()).To(Equal("error promoting application, please consult the promotion server's logs"))
	expectedProm := strategy.Promotion{}
	g.Expect(strat.promotion).To(Equal(expectedProm))
}

func TestPromotionWithoutUnknownStrategy(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	createTestPipelineWithPromotion(g, t)
	stratReg := strategy.StrategyRegistry{}
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), stratReg, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/dev", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusInternalServerError))
	g.Expect(resp.Body.String()).To(Equal("error promoting application, please consult the promotion server's logs"))
}
