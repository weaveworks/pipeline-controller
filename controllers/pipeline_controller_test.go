package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/conditions"
	. "github.com/onsi/gomega"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
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
	g := newGomegaWithT(t)
	ctx := context.Background()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pipeline-system",
		},
	}
	g.Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	pcSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "pipeline-system",
			Name:      "pipeline-controller-manager",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 8082,
					Name: "promhook",
				},
			},
		},
	}
	g.Expect(k8sClient.Create(ctx, pcSvc)).To(Succeed())
	pcSvc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{
		{Hostname: "foo"},
	}
	g.Expect(k8sClient.Status().Update(ctx, pcSvc)).To(Succeed())

	t.Run("sets cluster not found condition", func(t *testing.T) {
		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)

		pipeline := newPipeline(ctx, g, name, ns.Name, []*clusterctrlv1alpha1.GitopsCluster{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wrong-cluster",
				Namespace: ns.Name,
			},
		}})

		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionFalse, v1alpha1.TargetClusterNotFoundReason)
	})

	t.Run("sets reconciliation succeeded condition", func(t *testing.T) {
		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)

		gc := testingutils.NewGitopsCluster(ctx, g, k8sClient, name, ns.Name, kubeConfig)
		apimeta.SetStatusCondition(&gc.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "test"})
		g.Expect(k8sClient.Status().Update(ctx, gc)).To(Succeed())

		pipeline := newPipeline(ctx, g, name, ns.Name, []*clusterctrlv1alpha1.GitopsCluster{gc})

		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)
	})

	t.Run("sets reconciliation succeeded condition without clusterRef", func(t *testing.T) {
		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)

		pipeline := newPipeline(ctx, g, name, ns.Name, nil)

		checkReadyCondition(ctx, g, client.ObjectKeyFromObject(pipeline), metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)
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
