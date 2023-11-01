package leveltriggered

import (
	"context"
	"fmt"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"
	kustomv1 "github.com/fluxcd/kustomize-controller/api/v1"

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

func deleteObjectCleanup(ctx context.Context, g Gomega, obj client.Object) func() {
	return func() {
		println("[DEBUG] cleanup", obj.GetNamespace(), obj.GetName())
		g.Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
	}
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()

	t.Run("sets cluster not found and cluster unready condition", func(t *testing.T) {
		g := testingutils.NewGomegaWithT(t)
		name := "pipeline-" + rand.String(5)
		clusterName := "cluster-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)
		t.Cleanup(deleteObjectCleanup(ctx, g, ns))

		pipeline := newPipeline(name, ns.Name, []*clusterctrlv1alpha1.GitopsCluster{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: ns.Name,
			},
		}})
		g.Expect(k8sClient.Create(ctx, pipeline)).To(Succeed())

		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), meta.ReadyCondition, metav1.ConditionFalse, v1alpha1.TargetNotReadableReason)
		g.Eventually(fetchEventsFor(ns.Name, name), time.Second, time.Millisecond*100).Should(Not(BeEmpty()))
		events := fetchEventsFor(ns.Name, name)()
		g.Expect(events).ToNot(BeEmpty())
		g.Expect(events[0].reason).To(Equal("GetClusterError"))
		g.Expect(events[0].message).To(ContainSubstring(fmt.Sprintf("GitopsCluster.gitops.weave.works %q not found", clusterName)))

		// make an unready cluster and see if it notices
		testingutils.NewGitopsCluster(ctx, g, k8sClient, clusterName, ns.Name, kubeConfig)
		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), meta.ReadyCondition, metav1.ConditionFalse, v1alpha1.TargetNotReadableReason)
	})

	t.Run("sets reconciliation succeeded condition without clusterRef", func(t *testing.T) {
		g := testingutils.NewGomegaWithT(t)
		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)
		t.Cleanup(deleteObjectCleanup(ctx, g, ns))

		_ = createApp(ctx, k8sClient, g, name, ns.Name) // the name of the pipeline is also used as the name in the appRef, in newPipeline(...)

		pipeline := newPipeline(name, ns.Name, nil)
		g.Expect(k8sClient.Create(ctx, pipeline)).To(Succeed())

		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), meta.ReadyCondition, metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)

		g.Eventually(fetchEventsFor(ns.Name, name), time.Second, time.Millisecond*100).Should(Not(BeEmpty()))

		events := fetchEventsFor(ns.Name, name)()
		g.Expect(events).ToNot(BeEmpty())
		g.Expect(events[0].reason).To(Equal("Updated"))
		g.Expect(events[0].message).To(ContainSubstring("Updated pipeline"))
	})

	t.Run("app status is recorded faithfully", func(t *testing.T) {
		g := testingutils.NewGomegaWithT(t)
		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)
		t.Cleanup(deleteObjectCleanup(ctx, g, ns))

		pipeline := newPipeline(name, ns.Name, nil)
		g.Expect(k8sClient.Create(ctx, pipeline)).To(Succeed())

		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), meta.ReadyCondition, metav1.ConditionFalse, v1alpha1.TargetNotReadableReason)

		hr := createApp(ctx, k8sClient, g, name, ns.Name) // the name of the pipeline is also used as the name in the appRef, in newPipeline(...)
		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), meta.ReadyCondition, metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)

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

	t.Run("works with a Kustomization", func(t *testing.T) {
		g := testingutils.NewGomegaWithT(t)
		ctx := context.TODO()

		const appRevision = "1.0.1"

		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)
		t.Cleanup(deleteObjectCleanup(ctx, g, ns))

		pipeline := newPipeline(name, ns.Name, nil)
		pipeline.Spec.AppRef.APIVersion = "kustomize.toolkit.fluxcd.io/v1"
		pipeline.Spec.AppRef.Kind = "Kustomization"
		g.Expect(k8sClient.Create(ctx, pipeline)).To(Succeed())

		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), meta.ReadyCondition, metav1.ConditionFalse, v1alpha1.TargetNotReadableReason)

		// the application hasn't been created, so we expect "not found"
		p := getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))
		g.Expect(getTargetStatus(g, p, "test", 0).Ready).NotTo(BeTrue())
		g.Expect(getTargetStatus(g, p, "test", 0).Error).To(ContainSubstring("not found"))

		// FIXME create the app
		app := kustomv1.Kustomization{
			Spec: kustomv1.KustomizationSpec{
				SourceRef: kustomv1.CrossNamespaceSourceReference{
					Kind: "OCIRepository",
					Name: "dummy",
				},
			},
		}
		app.Name = name
		app.Namespace = ns.Name
		g.Expect(k8sClient.Create(ctx, &app)).To(Succeed())

		// now it's there, the pipeline can be reconciled fully
		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), meta.ReadyCondition, metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)
		// .. and the target status will be unready, but no error
		p = getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))
		g.Expect(getTargetStatus(g, p, "test", 0).Ready).NotTo(BeTrue())
		g.Expect(getTargetStatus(g, p, "test", 0).Error).To(BeEmpty())

		app.Status.LastAppliedRevision = appRevision
		apimeta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "test"})
		g.Expect(k8sClient.Status().Update(ctx, &app)).To(Succeed())

		var targetStatus v1alpha1.TargetStatus
		g.Eventually(func() bool {
			p := getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))
			targetStatus = getTargetStatus(g, p, "test", 0)
			return targetStatus.Ready
		}, "5s", "0.2s").Should(BeTrue())
		g.Expect(targetStatus.Revision).To(Equal(appRevision))

		// TODO possibly: check the events too, as it used to.
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

func checkCondition(ctx context.Context, g Gomega, n types.NamespacedName, conditionType string, status metav1.ConditionStatus, reason string) {
	pipeline := &v1alpha1.Pipeline{}
	assrt := g.EventuallyWithOffset(1, func() metav1.Condition {
		err := k8sClient.Get(ctx, n, pipeline)
		if err != nil {
			return metav1.Condition{}
		}

		cond := apimeta.FindStatusCondition(pipeline.Status.Conditions, conditionType)
		if cond == nil {
			return metav1.Condition{}
		}

		return *cond
	}, defaultTimeout, defaultInterval)

	cond := metav1.Condition{
		Type:   conditionType,
		Status: status,
		Reason: reason,
	}

	assrt.Should(conditions.MatchCondition(cond))
}

// newPipeline creates a new pipeline value using the name and namespace given, and HelmRelease targets in the clusters given;
// or if no clusters are supplied, one target in the same namespace as the pipeline.
func newPipeline(name string, ns string, clusters []*clusterctrlv1alpha1.GitopsCluster) *v1alpha1.Pipeline {
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

	return &pipeline
}

// createApp creates a minimal application object using the client given, and returns a pointer to the object.
// Use client.Status().Update(...) to make it look ready.
func createApp(ctx context.Context, client client.Client, g Gomega, name, namespace string) *helmv2.HelmRelease {
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
	g.ExpectWithOffset(1, client.Create(ctx, hr)).To(Succeed())
	return hr
}

func fetchEventsFor(ns string, name string) func() []testEvent {
	key := fmt.Sprintf("%s/%s", ns, name)
	return func() []testEvent {
		return eventRecorder.Events(key)
	}
}
