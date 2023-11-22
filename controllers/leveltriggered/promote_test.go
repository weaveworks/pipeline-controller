package leveltriggered

import (
	"context"
	"fmt"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/fluxcd/pkg/apis/meta"
	. "github.com/onsi/gomega"
	pipelineconditions "github.com/weaveworks/pipeline-controller/pkg/conditions"
	"go.uber.org/mock/gomock"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

func TestPromotionAlgorithm(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	ctx := context.TODO()

	devNs := testingutils.NewNamespace(ctx, g, k8sClient)
	stagingNs := testingutils.NewNamespace(ctx, g, k8sClient)
	prodNs := testingutils.NewNamespace(ctx, g, k8sClient)

	appName := "app-" + rand.String(5)
	devApp := createApp(ctx, k8sClient, g, appName, devNs.Name)
	setAppRevisionAndReadyStatus(ctx, g, devApp, "v1.0.0")

	stagingApp := createApp(ctx, k8sClient, g, appName, stagingNs.Name)
	setAppRevisionAndReadyStatus(ctx, g, stagingApp, "v1.0.0")

	prodApp := createApp(ctx, k8sClient, g, appName, prodNs.Name)
	setAppRevisionAndReadyStatus(ctx, g, prodApp, "v1.0.0")

	mockStrategy := installMockStrategy(t, pipelineReconciler)
	mockStrategy.EXPECT().Handles(gomock.Any()).Return(true).AnyTimes()

	// These operations act to assert the property that _a promotion
	// at a particular revision will not be retried_. This property
	// holds only if the test cases do not move the pipeline back to a
	// prior revision. This is something that could happen in normal
	// operation (i.e., that property would not hold); but avoided in
	// these test cases, without loss of generality.

	type promotionAt struct {
		env, version string
	}

	promoted := map[promotionAt]bool{} // environment+version -> absent=not attempted|false=waiting|true=done
	completePromotion := func(env, version string) {
		done, ok := promoted[promotionAt{env, version}]
		if !ok {
			panic("completePromotion called for unstarted promotion. This suggests a faulty test case.")
		}
		if done {
			panic("completePromotion called for completed promotion. This suggests a faulty test case")
		}
		switch env {
		case "staging":
			setAppRevision(ctx, g, stagingApp, version)
		case "prod":
			setAppRevision(ctx, g, prodApp, version)
		default:
			panic("Unexpected environment. Make sure to setup the pipeline properly in the test.")
		}
		fmt.Printf("[DEBUG] complete promotion %s->%s\n", env, version)
		promoted[promotionAt{env, version}] = true
	}
	startPromotion := func(env, version string) {
		if _, ok := promoted[promotionAt{env, version}]; ok {
			panic("attempting to replay a promotion " + env + "->" + version + "; this indicates bad code")
		}
		fmt.Printf("[DEBUG] start promotion %s->%s\n", env, version)
		promoted[promotionAt{env, version}] = false
	}
	isPromotionStarted := func(env, version string) bool {
		done, ok := promoted[promotionAt{env, version}]
		return ok && !done
	}
	mockStrategy.EXPECT().
		Promote(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		Do(func(ctx context.Context, p v1alpha1.Promotion, prom strategy.Promotion) {
			startPromotion(prom.Environment.Name, prom.Version)
		})

	checkAndCompletePromotion := func(g Gomega, env, version string) {
		g.Eventually(func() bool {
			return isPromotionStarted(env, version)
		}).Should(BeTrue())
		completePromotion(env, version)
	}

	t.Run("promotes revision to all environments", func(t *testing.T) {
		g := testingutils.NewGomegaWithT(t)
		name := "pipeline-" + rand.String(5)
		managementNs := testingutils.NewNamespace(ctx, g, k8sClient)

		pipeline := &v1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: managementNs.Name,
			},
			Spec: v1alpha1.PipelineSpec{
				AppRef: v1alpha1.LocalAppReference{
					APIVersion: "helm.toolkit.fluxcd.io/v2beta1",
					Kind:       "HelmRelease",
					Name:       appName,
				},
				Environments: []v1alpha1.Environment{
					{
						Name: "dev",
						Targets: []v1alpha1.Target{
							{Namespace: devNs.Name},
						},
					},
					{
						Name: "staging",
						Targets: []v1alpha1.Target{
							{Namespace: stagingNs.Name},
						},
					},
					{
						Name: "prod",
						Targets: []v1alpha1.Target{
							{Namespace: prodNs.Name},
						},
					},
				},
				Promotion: &v1alpha1.Promotion{
					Strategy: v1alpha1.Strategy{
						Notification: &v1alpha1.NotificationPromotion{},
					},
				},
			},
		}

		g.Expect(k8sClient.Create(ctx, pipeline)).To(Succeed())
		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), meta.ReadyCondition, metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)

		versionToPromote := "v1.0.1"

		// Bumping dev revision to trigger the promotion
		setAppRevisionAndReadyStatus(ctx, g, devApp, versionToPromote)
		checkAndCompletePromotion(g, "staging", versionToPromote)
		checkAndCompletePromotion(g, "prod", versionToPromote)

		// checks if the revision of all target status is v1.0.1
		var p *v1alpha1.Pipeline
		g.Eventually(func() bool {
			p = getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))

			for _, env := range p.Spec.Environments {
				if !checkAllTargetsRunRevision(p.Status.Environments[env.Name], versionToPromote) {
					return false
				}

				if !checkAllTargetsAreReady(p.Status.Environments[env.Name]) {
					return false
				}
			}

			return true
		}, "5s", "0.2s").Should(BeTrue())

		// success through to prod
		p = getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))
		checkPromotionSuccess(g, p, versionToPromote, "prod")

		t.Run("triggers another promotion if the app is updated again", func(t *testing.T) {
			const newVersion = "v1.0.2"
			g := testingutils.NewGomegaWithT(t)
			// Bumping dev revision to trigger the promotion
			setAppRevisionAndReadyStatus(ctx, g, devApp, newVersion)
			checkAndCompletePromotion(g, "staging", newVersion)
			checkAndCompletePromotion(g, "prod", newVersion)

			// checks if the revision of all target status is the new version
			g.Eventually(func() bool {
				p := getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))

				for _, env := range p.Spec.Environments {
					if !checkAllTargetsRunRevision(p.Status.Environments[env.Name], newVersion) {
						return false
					}
				}
				return true
			}, "5s", "0.2s").Should(BeTrue())

			p := getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))
			checkPromotionSuccess(g, p, newVersion, "prod")
		})
	})

	t.Run("sets PipelinePending condition", func(t *testing.T) {
		g := testingutils.NewGomegaWithT(t)
		name := "pipeline-" + rand.String(5)

		managementNs := testingutils.NewNamespace(ctx, g, k8sClient)
		devNs := testingutils.NewNamespace(ctx, g, k8sClient)
		devNs2 := testingutils.NewNamespace(ctx, g, k8sClient)
		stagingNs := testingutils.NewNamespace(ctx, g, k8sClient)

		pipeline := &v1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: managementNs.Name,
			},
			Spec: v1alpha1.PipelineSpec{
				AppRef: v1alpha1.LocalAppReference{
					APIVersion: "helm.toolkit.fluxcd.io/v2beta1",
					Kind:       "HelmRelease",
					Name:       name,
				},
				Environments: []v1alpha1.Environment{
					{
						Name: "dev",
						Targets: []v1alpha1.Target{
							{Namespace: devNs.Name},
							{Namespace: devNs2.Name},
						},
					},
					{
						Name: "staging",
						Targets: []v1alpha1.Target{
							{Namespace: stagingNs.Name},
						},
					},
				},
				Promotion: &v1alpha1.Promotion{
					Strategy: v1alpha1.Strategy{
						Notification: &v1alpha1.NotificationPromotion{},
					},
				},
			},
		}

		devApp := createApp(ctx, k8sClient, g, name, devNs.Name)
		setAppRevisionAndReadyStatus(ctx, g, devApp, "v1.0.0")

		devApp2 := createApp(ctx, k8sClient, g, name, devNs2.Name)

		stagingApp := createApp(ctx, k8sClient, g, name, stagingNs.Name)
		setAppRevisionAndReadyStatus(ctx, g, stagingApp, "v1.0.0")

		g.Expect(k8sClient.Create(ctx, pipeline)).To(Succeed())
		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), meta.ReadyCondition, metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)
		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), pipelineconditions.PromotionPendingCondition, metav1.ConditionTrue, v1alpha1.EnvironmentNotReadyReason)

		setAppRevision(ctx, g, devApp2, "v1.0.0")
		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), pipelineconditions.PromotionPendingCondition, metav1.ConditionTrue, v1alpha1.EnvironmentNotReadyReason)

		setAppStatusReadyCondition(ctx, g, devApp2)

		g.Eventually(func() bool {
			p := getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))
			return apimeta.FindStatusCondition(p.Status.Conditions, pipelineconditions.PromotionPendingCondition) == nil
		}).Should(BeTrue())
	})
}

func checkPromotionSuccess(g Gomega, pipeline *v1alpha1.Pipeline, revision, lastEnv string) {
	for _, env := range pipeline.Spec.Environments[1:] { // no promotion in the first env
		prom := pipeline.Status.Environments[env.Name].Promotion
		g.Expect(prom).NotTo(BeNil())
		g.Expect(prom.LastAttemptedTime).NotTo(BeZero())
		g.Expect(prom.Succeeded).To(BeTrue())
		g.Expect(prom.Revision).To(Equal(revision))
		if env.Name == lastEnv {
			return
		}
	}
}

func setAppRevisionAndReadyStatus(ctx context.Context, g Gomega, hr *helmv2.HelmRelease, revision string) {
	setAppRevision(ctx, g, hr, revision)
	setAppStatusReadyCondition(ctx, g, hr)
}

func setAppRevision(ctx context.Context, g Gomega, hr *helmv2.HelmRelease, revision string) {
	hr.Status.LastAppliedRevision = revision
	g.Expect(k8sClient.Status().Update(ctx, hr)).To(Succeed())
}

func setAppStatusReadyCondition(ctx context.Context, g Gomega, hr *helmv2.HelmRelease) {
	apimeta.SetStatusCondition(&hr.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "test"})
	g.Expect(k8sClient.Status().Update(ctx, hr)).To(Succeed())
}

func installMockStrategy(t *testing.T, r *PipelineReconciler) *strategy.MockStrategy {
	r.stratReg = strategy.StrategyRegistry{}
	mockCtrl := gomock.NewController(t)
	mockStrategy := strategy.NewMockStrategy(mockCtrl)

	r.stratReg.Register(mockStrategy)

	return mockStrategy
}
