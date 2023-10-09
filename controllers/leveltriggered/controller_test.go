package leveltriggered

import (
	"context"
	"fmt"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/conditions"
	. "github.com/onsi/gomega"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
)

const (
	defaultTimeout  = time.Second * 10
	defaultInterval = time.Millisecond * 250
)

func TestReconcile(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	ctx := context.Background()

	t.Run("sets cluster not found and cluster unready condition", func(_ *testing.T) {
		name := "pipeline-" + rand.String(5)
		clusterName := "cluster-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)

		pipeline := newPipeline(ctx, g, name, ns.Name, []*clusterctrlv1alpha1.GitopsCluster{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: ns.Name,
			},
		}})

		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionFalse, v1alpha1.TargetNotReadableReason)
		g.Eventually(fetchEventsFor(ns.Name, name), time.Second, time.Millisecond*100).Should(Not(BeEmpty()))
		events := fetchEventsFor(ns.Name, name)()
		g.Expect(events).ToNot(BeEmpty())
		g.Expect(events[0].reason).To(Equal("GetClusterError"))
		g.Expect(events[0].message).To(ContainSubstring(fmt.Sprintf("GitopsCluster.gitops.weave.works %q not found", clusterName)))

		// make an unready cluster and see if it notices
		testingutils.NewGitopsCluster(ctx, g, k8sClient, clusterName, ns.Name, kubeConfig)
		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionFalse, v1alpha1.TargetNotReadableReason)
	})

	t.Run("sets reconciliation succeeded condition for remote cluster", func(t *testing.T) {
		t.Skip("remote clusters not supported yet")

		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)

		gc := testingutils.NewGitopsCluster(ctx, g, k8sClient, name, ns.Name, kubeConfig)
		apimeta.SetStatusCondition(&gc.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "test"})
		g.Expect(k8sClient.Status().Update(ctx, gc)).To(Succeed())

		pipeline := newPipeline(ctx, g, name, ns.Name, []*clusterctrlv1alpha1.GitopsCluster{gc})

		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)

		g.Eventually(fetchEventsFor(ns.Name, name), time.Second, time.Millisecond*100).Should(Not(BeEmpty()))

		events := fetchEventsFor(ns.Name, name)()
		g.Expect(events).ToNot(BeEmpty())
		g.Expect(events[0].reason).To(Equal("Updated"))
		g.Expect(events[0].message).To(ContainSubstring("Updated pipeline"))
	})

	t.Run("sets reconciliation succeeded condition without clusterRef", func(_ *testing.T) {
		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)

		_ = newApp(ctx, g, name, ns.Name) // the name of the pipeline is also used as the name in the appRef, in newPipeline(...)

		pipeline := newPipeline(ctx, g, name, ns.Name, nil)

		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)

		g.Eventually(fetchEventsFor(ns.Name, name), time.Second, time.Millisecond*100).Should(Not(BeEmpty()))

		events := fetchEventsFor(ns.Name, name)()
		g.Expect(events).ToNot(BeEmpty())
		g.Expect(events[0].reason).To(Equal("Updated"))
		g.Expect(events[0].message).To(ContainSubstring("Updated pipeline"))
	})

	t.Run("app status is recorded faithfully", func(t *testing.T) {
		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)
		pipeline := newPipeline(ctx, g, name, ns.Name, nil)
		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionFalse, v1alpha1.TargetNotReadableReason)

		hr := newApp(ctx, g, name, ns.Name) // the name of the pipeline is also used as the name in the appRef, in newPipeline(...)
		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)

		// we didn't set the app to be ready, so we should expect the target to be reported as unready
		p := getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))
		g.Expect(getTargetStatus(g, p, "test", 0).Ready).NotTo(BeTrue())

		// make the app ready, and check it's recorded as such in the pipeline status
		const appRevision = "v1.0.1"
		hr.Status.LastAppliedRevision = appRevision
		apimeta.SetStatusCondition(&hr.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "test"})
		g.Expect(k8sClient.Status().Update(ctx, hr)).To(Succeed())

		var targetStatus v1alpha1.TargetStatus
		g.Eventually(func() bool {
			p := getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))
			targetStatus = getTargetStatus(g, p, "test", 0)
			return targetStatus.Ready
		}, "5s", "0.2s").Should(BeTrue())
		g.Expect(targetStatus.Revision).To(Equal(appRevision))
	})
}

func getPipeline(ctx context.Context, g Gomega, key client.ObjectKey) *v1alpha1.Pipeline {
	var p v1alpha1.Pipeline
	g.Expect(k8sClient.Get(ctx, key, &p)).To(Succeed())
	return &p
}

func getTargetStatus(g Gomega, pipeline *v1alpha1.Pipeline, envName string, target int) v1alpha1.TargetStatus {
	g.ExpectWithOffset(1, pipeline.Status).NotTo(BeNil())
	g.ExpectWithOffset(1, pipeline.Status.Environments).To(HaveKey(envName))
	env := pipeline.Status.Environments[envName]
	g.ExpectWithOffset(1, len(env.Targets) > target).To(BeTrue())
	return env.Targets[target]
}

func checkReadyCondition(ctx context.Context, g Gomega, n types.NamespacedName, status metav1.ConditionStatus, reason string) {
	pipeline := &v1alpha1.Pipeline{}
	assrt := g.Eventually(func() []metav1.Condition {
		err := k8sClient.Get(ctx, n, pipeline)
		if err != nil {
			return nil
		}
		return pipeline.Status.Conditions
	}, defaultTimeout, defaultInterval)

	cond := metav1.Condition{
		Type:   meta.ReadyCondition,
		Status: status,
		Reason: reason,
	}

	assrt.Should(conditions.MatchConditions([]metav1.Condition{cond}))
}

func newPipeline(ctx context.Context, g Gomega, name string, ns string, clusters []*clusterctrlv1alpha1.GitopsCluster) *v1alpha1.Pipeline {
	targets := []v1alpha1.Target{}

	if len(clusters) > 0 {
		for _, c := range clusters {
			targets = append(targets, v1alpha1.Target{
				Namespace: ns,
				ClusterRef: &v1alpha1.CrossNamespaceClusterReference{
					Kind:      "GitopsCluster",
					Name:      c.Name,
					Namespace: c.Namespace,
				},
			})
		}
	} else {
		targets = append(targets, v1alpha1.Target{
			Namespace: ns,
		})
	}

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
					Name:    "test",
					Targets: targets,
				},
			},
		},
	}
	g.Expect(k8sClient.Create(ctx, &pipeline)).To(Succeed())

	return &pipeline
}

// newApp returns a minimal application object. Use client.Status().Update(...) to make it look ready.
func newApp(ctx context.Context, g Gomega, name, namespace string) *helmv2.HelmRelease {
	hr := &helmv2.HelmRelease{
		Spec: helmv2.HelmReleaseSpec{
			Chart: helmv2.HelmChartTemplate{
				Spec: helmv2.HelmChartTemplateSpec{
					SourceRef: helmv2.CrossNamespaceObjectReference{
						Name: "dummy",
					},
				},
			},
		},
	}
	hr.Name = name
	hr.Namespace = namespace
	g.ExpectWithOffset(1, k8sClient.Create(ctx, hr)).To(Succeed())
	return hr
}

func fetchEventsFor(ns string, name string) func() []testEvent {
	key := fmt.Sprintf("%s/%s", ns, name)
	return func() []testEvent {
		return eventRecorder.Events(key)
	}
}
