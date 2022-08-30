package testingutils

import (
	"context"

	. "github.com/onsi/gomega"

	"github.com/fluxcd/pkg/apis/meta"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func NewNamespace(ctx context.Context, g Gomega, k client.Client) *corev1.Namespace {
	ns := &corev1.Namespace{}
	ns.Name = "kube-test-" + rand.String(5)

	g.Expect(k.Create(ctx, ns)).To(Succeed(), "failed creating namespace")

	return ns
}

func NewGitopsCluster(ctx context.Context, g Gomega, k client.Client, name string, ns string, kubeConfig []byte) *clusterctrlv1alpha1.GitopsCluster {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Data: map[string][]byte{
			"value": kubeConfig,
		},
	}
	g.Expect(k.Create(ctx, secret)).To(Succeed())

	gc := &clusterctrlv1alpha1.GitopsCluster{
		TypeMeta: metav1.TypeMeta{
			APIVersion: clusterctrlv1alpha1.GroupVersion.String(),
			Kind:       "GitopsCluster",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: clusterctrlv1alpha1.GitopsClusterSpec{
			SecretRef: &meta.LocalObjectReference{
				Name: name,
			},
		},
	}
	g.Expect(k.Create(ctx, gc)).To(Succeed())

	return gc
}
