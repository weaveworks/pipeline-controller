package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	. "github.com/onsi/gomega"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultTimeout  = time.Second * 10
	defaultInterval = time.Millisecond * 250
)

func TestReconcile(t *testing.T) {
	g := NewGomegaWithT(t)
	ctx := context.Background()

	t.Run("sets cluster not found condition", func(t *testing.T) {
		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)

		pipeline := newPipeline(ctx, g, name, ns.Name, []*clusterctrlv1alpha1.GitopsCluster{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wrong-cluster",
				Namespace: ns.Name,
			},
		}})

		checkReadyCondition(ctx, g, pipeline, metav1.ConditionFalse, v1alpha1.TargetClusterNotFoundReason)
	})

	t.Run("sets reconciliation succeeded condition", func(t *testing.T) {
		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)

		gc := testingutils.NewGitopsCluster(ctx, g, k8sClient, name, ns.Name, kubeConfig)
		apimeta.SetStatusCondition(&gc.Status.Conditions, v1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "test"})
		g.Expect(k8sClient.Status().Update(ctx, gc)).To(Succeed())

		pipeline := newPipeline(ctx, g, name, ns.Name, []*clusterctrlv1alpha1.GitopsCluster{gc})

		checkReadyCondition(ctx, g, pipeline, metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)
	})
}

func checkReadyCondition(ctx context.Context, g Gomega, pipeline *v1alpha1.Pipeline, status metav1.ConditionStatus, reason string) {
	createdPipeline := &v1alpha1.Pipeline{}
	g.Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pipeline), createdPipeline)
		if err != nil {
			return false
		}

		condition := apimeta.FindStatusCondition(createdPipeline.Status.Conditions, meta.ReadyCondition)
		if condition != nil {
			return condition.Status == status && condition.Reason == reason
		}

		return false
	}, defaultTimeout, defaultInterval).Should(BeTrue())
}

func newPipeline(ctx context.Context, g Gomega, name string, ns string, clusters []*clusterctrlv1alpha1.GitopsCluster) *v1alpha1.Pipeline {
	targets := []v1alpha1.Target{}

	for _, c := range clusters {
		targets = append(targets, v1alpha1.Target{
			Namespace: ns,
			ClusterRef: v1alpha1.CrossNamespaceClusterReference{
				Kind:      "GitopsCluster",
				Name:      c.Name,
				Namespace: c.Namespace,
			},
		})
	}

	pipeline := v1alpha1.Pipeline{
		ObjectMeta: v1.ObjectMeta{
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