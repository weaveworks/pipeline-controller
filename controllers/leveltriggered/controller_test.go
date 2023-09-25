package leveltriggered

import (
	"context"
	"fmt"
	"testing"
	"time"

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

	t.Run("sets cluster not found -> unready -> ready condition", func(_ *testing.T) {
		name := "pipeline-" + rand.String(5)
		clusterName := "cluster-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)

		pipeline := newPipeline(ctx, g, name, ns.Name, []*clusterctrlv1alpha1.GitopsCluster{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: ns.Name,
			},
		}})

		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionFalse, v1alpha1.TargetClusterNotFoundReason)
		g.Eventually(fetchEventsFor(ns.Name, name), time.Second, time.Millisecond*100).Should(Not(BeEmpty()))
		events := fetchEventsFor(ns.Name, name)()
		g.Expect(events).ToNot(BeEmpty())
		g.Expect(events[0].reason).To(Equal("GetClusterError"))
		g.Expect(events[0].message).To(ContainSubstring(fmt.Sprintf("GitopsCluster.gitops.weave.works %q not found", clusterName)))

		// make an unready cluster and see if it notices
		gc := testingutils.NewGitopsCluster(ctx, g, k8sClient, clusterName, ns.Name, kubeConfig)
		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionFalse, v1alpha1.TargetClusterNotReadyReason)

		// make the cluster ready, check that the controller is now happy with the pipeline
		apimeta.SetStatusCondition(&gc.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "test"})
		g.Expect(k8sClient.Status().Update(ctx, gc)).To(Succeed())
		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)
	})

	t.Run("sets reconciliation succeeded condition", func(_ *testing.T) {
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

		pipeline := newPipeline(ctx, g, name, ns.Name, nil)

		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)

		g.Eventually(fetchEventsFor(ns.Name, name), time.Second, time.Millisecond*100).Should(Not(BeEmpty()))

		events := fetchEventsFor(ns.Name, name)()
		g.Expect(events).ToNot(BeEmpty())
		g.Expect(events[0].reason).To(Equal("Updated"))
		g.Expect(events[0].message).To(ContainSubstring("Updated pipeline"))
	})
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
				Kind: "HelmRelease",
				Name: name,
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

func fetchEventsFor(ns string, name string) func() []testEvent {
	key := fmt.Sprintf("%s/%s", ns, name)
	return func() []testEvent {
		return eventRecorder.Events(key)
	}
}
