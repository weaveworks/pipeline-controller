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

func (s *introspectableStrategy) Promote(ctx context.Context, prom strategy.Promotion) (*strategy.PromotionResult, error) {
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

func createTestPipeline(g *WithT) (v1alpha1.Pipeline, func()) {
	targets := []v1alpha1.Target{}

	ns := "default"
	name := "app"

	targets = append(targets, v1alpha1.Target{
		Namespace: ns,
	})

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
	g.Expect(k8sClient.Create(context.Background(), &pipeline)).To(Succeed())

	return pipeline, func() {
		g.Expect(k8sClient.Delete(context.Background(), &pipeline)).To(Succeed())
	}
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
	g.Expect(resp.Code).To(Equal(http.StatusUnprocessableEntity))
}

func TestPostWithIncompatibleBody(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{}), nil, nil)
	resp := requestTo(g, h, http.MethodPost, "/ns/app/env", []byte("incompatible"))
	g.Expect(resp.Code).To(Equal(http.StatusUnprocessableEntity))
}

func TestPostWithUnknownPipeline(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/ns/app/env", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusBadRequest))
}

func TestInvolvedObjectDoesntMatch(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	_, cleanup := createTestPipeline(g)
	defer cleanup()
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
	_, cleanup := createTestPipeline(g)
	defer cleanup()
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/no-targets", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusBadRequest))
	g.Expect(resp.Body.String()).To(Equal("cannot promote beyond last environment no-targets"))
}

func TestPromotionToEnvWithoutTarget(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	_, cleanup := createTestPipeline(g)
	defer cleanup()
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/prod", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusBadRequest))
	g.Expect(resp.Body.String()).To(Equal("environment no-targets has no targets"))
}

func TestPromotionFromUnknownEnv(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	_, cleanup := createTestPipeline(g)
	defer cleanup()
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/foo", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusBadRequest))
	g.Expect(resp.Body.String()).To(Equal("app default/app has no environment foo defined"))
}

func TestPromotionStarted(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	_, cleanup := createTestPipeline(g)
	defer cleanup()

	strat := introspectableStrategy{
		location: "success",
	}
	stratReg := map[string]strategy.Strategy{
		"nop": &strat,
	}
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), stratReg, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/dev", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusCreated))
	g.Expect(resp.Body.String()).To(Equal(""))
	g.Expect(resp.Header().Get("location")).To(Equal("success"))
	expectedProm := strategy.Promotion{
		AppNS:   "default",
		AppName: "app",
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
	_, cleanup := createTestPipeline(g)
	defer cleanup()
	strat := introspectableStrategy{
		err: fmt.Errorf("this didn't work"),
	}
	stratReg := map[string]strategy.Strategy{
		"nop": &strat,
	}
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), stratReg, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/dev", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusInternalServerError))
	g.Expect(resp.Body.String()).To(Equal("promotion failed: this didn't work"))
	expectedProm := strategy.Promotion{
		AppNS:   "default",
		AppName: "app",
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
	_, cleanup := createTestPipeline(g)
	defer cleanup()
	strat := introspectableStrategy{}
	stratReg := map[string]strategy.Strategy{
		"nop": &strat,
	}
	h := server.NewDefaultPromotionHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), stratReg, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/dev", marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusNoContent))
	g.Expect(resp.Body.String()).To(Equal(""))
	g.Expect(resp.Header()).NotTo(HaveKey("location"))
	expectedProm := strategy.Promotion{
		AppNS:   "default",
		AppName: "app",
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
