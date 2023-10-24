package leveltriggered

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCacheGC(t *testing.T) {
	// Check that when we stop using a {cluster, GVK}, the cache for that is (eventually) dismantled.
	// To do this, we need introspection into the caches. I've done this simply by introducing an
	// internal method which says exactly which {cluster, GVK} types have caches.

	g := testingutils.NewGomegaWithT(t)

	expectedCacheKey := clusterAndGVK{
		ObjectKey: client.ObjectKey{},
		GroupVersionKind: schema.GroupVersionKind{
			Group:   "test.weave.works",
			Version: "v1alpha1",
			Kind:    "Fake",
		},
	}

	// This is a minor cheat, since it looks into a private field of the controller. But, it should be
	// relatively easy to replace if that detail changed.
	getCacheKeys := func() []clusterAndGVK {
		return pipelineReconciler.caches.getCacheKeys()
	}

	// First, let's check the initial state. Other tests may leave detritus, so this just checks we
	// don't have the fake application that's for this specific test.
	g.Expect(getCacheKeys()).ToNot(ContainElements(expectedCacheKey))

	ctx := context.TODO()
	ns := testingutils.NewNamespace(ctx, g, k8sClient)

	// First scenario: the target doesn't exist (this checks that, for
	// example, the cache doesn't have to be operating for it to be
	// collected).

	newPipeline := func(name string, targetName string) *v1alpha1.Pipeline {
		p := &v1alpha1.Pipeline{
			Spec: v1alpha1.PipelineSpec{
				AppRef: v1alpha1.LocalAppReference{
					APIVersion: "test.weave.works/v1alpha1",
					Kind:       "Fake",
					Name:       targetName,
				},
				Environments: []v1alpha1.Environment{
					{
						Name: "test",
						Targets: []v1alpha1.Target{
							{
								Namespace: ns.Name,
							},
						},
					},
				},
			},
		}
		p.Name = name
		p.Namespace = ns.Name
		return p
	}

	pipeline := newPipeline(rand.String(5), rand.String(5))
	g.Expect(k8sClient.Create(ctx, pipeline)).To(Succeed())

	// Now there's a pipeline that mentions the fake kind -- did it create a cache for it?
	g.Eventually(getCacheKeys).Should(ContainElement(expectedCacheKey))

	g.Expect(k8sClient.Delete(ctx, pipeline)).To(Succeed())

	// Now there's no pipeline using the fake kind, so it should get cleaned up.
	g.Eventually(getCacheKeys, "5s", "0.5s").ShouldNot(ContainElement(expectedCacheKey))

	// Second scenario: the target exists. This is more or less the happy path: you have a pipeline, it's working, you delete it.

	newFake := func(targetName string) client.Object {
		var fake unstructured.Unstructured
		fake.SetAPIVersion("test.weave.works/v1alpha1")
		fake.SetKind("Fake")
		fake.SetNamespace(ns.Name)
		fake.SetName(targetName)
		return &fake
	}

	target := newFake(rand.String(5))
	g.Expect(k8sClient.Create(ctx, target)).To(Succeed())

	pipeline = newPipeline(rand.String(5), target.GetName())
	g.Expect(k8sClient.Create(ctx, pipeline)).To(Succeed())

	// Now there's a pipeline that mentions the fake kind -- did it create a cache for it?
	g.Eventually(getCacheKeys).Should(ContainElement(expectedCacheKey))

	// Delete the pipeline, leave the target there
	g.Expect(k8sClient.Delete(ctx, pipeline)).To(Succeed())

	// Now there's no pipeline using the fake kind, so it should get cleaned up even though the target is still there.
	g.Eventually(getCacheKeys, "5s", "0.5s").ShouldNot(ContainElement(expectedCacheKey))

	g.Expect(k8sClient.Delete(ctx, target)).To(Succeed())

	// Third scenario: two pipelines refer to the same {cluser, type}. Checks that the cache won't get deleted if
	// there's still something referring to it.
	g.Expect(getCacheKeys()).ToNot(ContainElement(expectedCacheKey))

	target = newFake(rand.String(5))
	p1 := newPipeline(rand.String(5), target.GetName())
	g.Expect(k8sClient.Create(ctx, p1)).To(Succeed())
	p2 := newPipeline(rand.String(5), target.GetName())
	g.Expect(k8sClient.Create(ctx, p2)).To(Succeed())

	// Confirm there's a cache for the target type
	g.Eventually(getCacheKeys).Should(ContainElement(expectedCacheKey))

	// Delete one pipeline, there should still be a cache.
	g.Expect(k8sClient.Delete(ctx, p1)).To(Succeed())

	// make sure it's gone in the controller's cached client
	g.Eventually(func() bool {
		var p v1alpha1.Pipeline
		err := pipelineReconciler.Get(ctx, client.ObjectKeyFromObject(p1), &p)
		return err != nil && client.IgnoreNotFound(err) == nil
	}).Should(BeTrue())
	// This is a minor cheat on top of a cheat, using an internal API, because I can't wait around for something to _not_ happen.
	g.Expect(pipelineReconciler.caches.isCacheUsed(expectedCacheKey)).To(BeTrue())
}
