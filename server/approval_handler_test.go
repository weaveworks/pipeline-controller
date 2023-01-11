package server_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/logger"
	. "github.com/onsi/gomega"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	"github.com/weaveworks/pipeline-controller/server"
	"github.com/weaveworks/pipeline-controller/server/strategy"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestApprovalGet(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	h := server.DefaultApprovalHandler{}
	resp := requestTo(g, h, http.MethodGet, "/", nil, nil)
	g.Expect(resp.Code).To(Equal(http.StatusMethodNotAllowed))
}

func TestApprovalPostWithWrongPath(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	h := server.NewDefaultApprovalHandler(logger.NewLogger(logger.Options{}), nil, nil)
	resp := requestTo(g, h, http.MethodPost, "/", nil, nil)
	g.Expect(resp.Code).To(Equal(http.StatusNotFound))
}

func TestApprovalPostWithUnknownPipeline(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	h := server.NewDefaultApprovalHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/ns/app/env", nil, marshalEvent(g, createEvent()))
	g.Expect(resp.Code).To(Equal(http.StatusNotFound))
}

func TestApprovalVerifyXSignature(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)

	pipeline := createTestPipelineWithPromotion(g, t)
	pipeline = setWaitingApproval(g, t, pipeline)
	secret := createHmacSecret(g, t, pipeline)

	pipeline.Spec.Promotion.Strategy.SecretRef = &meta.LocalObjectReference{
		Name: secret.Name,
	}
	g.Expect(k8sClient.Update(context.Background(), &pipeline)).To(Succeed())

	makeSignedReq := func(hmac string) *httptest.ResponseRecorder {
		header := http.Header{
			server.SignatureHeader: []string{fmt.Sprintf("sha256=%s", hmac)},
		}
		strat := introspectableStrategy{
			location: "success",
		}
		stratReg := strategy.StrategyRegistry{&strat}
		h := server.NewDefaultApprovalHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), stratReg, k8sClient)
		return requestTo(g, h, http.MethodPost, "/default/app/prod/5.0.0", header, []byte(""))
	}

	t.Run("succeeds with proper hmac", func(t *testing.T) {
		mac := hmac.New(sha256.New, secret.Data["hmac-key"])
		_, err := mac.Write([]byte(""))
		g.Expect(err).NotTo(HaveOccurred())
		sum := fmt.Sprintf("%x", mac.Sum(nil))

		resp := makeSignedReq(sum)
		g.Expect(resp.Code).To(Equal(http.StatusCreated))
	})

	t.Run("fails with invalid hmac", func(t *testing.T) {
		resp := makeSignedReq("invalid")
		g.Expect(resp.Code).To(Equal(http.StatusUnauthorized))
	})
}

func TestApprovalValidateRevision(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	p := createTestPipelineWithPromotion(g, t)

	setWaitingApproval(g, t, p)

	h := server.NewDefaultApprovalHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/prod/4.0.0", nil, nil)

	g.Expect(resp.Code).To(Equal(http.StatusUnprocessableEntity))
}

func TestApprovalValidatePromSpecPresence(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	p := createTestPipeline(g, t)

	setWaitingApproval(g, t, p)

	h := server.NewDefaultApprovalHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), nil, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/prod/4.0.0", nil, nil)

	g.Expect(resp.Code).To(Equal(http.StatusUnprocessableEntity))
}

func TestApprovalPromotionHappens(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	p := createTestPipelineWithPromotion(g, t)

	setWaitingApproval(g, t, p)

	strat := introspectableStrategy{
		location: "success",
	}
	stratReg := strategy.StrategyRegistry{&strat}

	h := server.NewDefaultApprovalHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), stratReg, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/prod/5.0.0", nil, nil)
	g.Expect(resp.Code).To(Equal(http.StatusCreated))
	g.Expect(resp.Body.String()).To(Equal(""))
	g.Expect(resp.Header().Get("location")).To(Equal("success"))
}

func TestApprovalResetsWaitingApproval(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	p := createTestPipelineWithPromotion(g, t)

	setWaitingApproval(g, t, p)

	strat := introspectableStrategy{
		location: "success",
	}
	stratReg := strategy.StrategyRegistry{&strat}

	h := server.NewDefaultApprovalHandler(logger.NewLogger(logger.Options{LogLevel: "trace"}), stratReg, k8sClient)
	resp := requestTo(g, h, http.MethodPost, "/default/app/prod/5.0.0", nil, nil)
	g.Expect(resp.Code).To(Equal(http.StatusCreated))
	g.Expect(resp.Body.String()).To(Equal(""))
	g.Expect(resp.Header().Get("location")).To(Equal("success"))

	updatedPipeline := v1alpha1.Pipeline{}

	g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&p), &updatedPipeline)).To(Succeed())

	g.Expect(updatedPipeline.Status.Environments["prod"].WaitingApproval.Revision).To(Equal(""))
}

func setWaitingApproval(g *WithT, t *testing.T, p v1alpha1.Pipeline) v1alpha1.Pipeline {
	g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&p), &p)).To(Succeed())

	// Update status simulating a promotion has happened and set the waiting approval status
	p.Status.Environments = map[string]*v1alpha1.EnvironmentStatus{
		"prod": {
			WaitingApproval: v1alpha1.WaitingApproval{
				Revision: "5.0.0",
			},
		},
	}
	g.Expect(k8sClient.Status().Update(context.Background(), &p)).To(Succeed())

	return p
}
