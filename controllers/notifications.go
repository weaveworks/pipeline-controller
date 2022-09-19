package controllers

import (
	"context"
	"fmt"

	ncv1beta1 "github.com/fluxcd/notification-controller/api/v1beta1"
	"github.com/fluxcd/pkg/apis/meta"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

func deriveSecretKey(cluster *clusterctrlv1alpha1.GitopsCluster) (types.NamespacedName, error) {
	if cluster.Spec.SecretRef != nil {
		return types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      cluster.Spec.SecretRef.Name,
		}, nil
	}
	if cluster.Spec.CAPIClusterRef != nil {
		return types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      fmt.Sprintf("%s-%s", cluster.Spec.CAPIClusterRef.Name, "kubeconfig"),
		}, nil
	}

	return types.NamespacedName{}, fmt.Errorf("cluster doesn't have a secretRef or capiClusterRef set")
}

// getTargetClient returns a Client for interacting with the given cluster. If cluster is nil then a Client for
// the cluster running PipelineReconciler is returned.
func (r *PipelineReconciler) getTargetClient(ctx context.Context, cluster *clusterctrlv1alpha1.GitopsCluster) (client.Client, error) {
	if cluster == nil {
		return r.Client, nil
	}
	secretKey, err := deriveSecretKey(cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to derive kubeconfig Secret name: %w", err)
	}
	var kubeconfigSecret corev1.Secret
	if err := r.Get(ctx, secretKey, &kubeconfigSecret); err != nil {
		return nil, fmt.Errorf("failed to retrieve kubeconfig secret: %w", err)
	}

	clientCfg, err := clientcmd.NewClientConfigFromBytes(kubeconfigSecret.Data["value"])
	if err != nil {
		return nil, fmt.Errorf("failed to generate client config from Secret data: %w", err)
	}
	restCfg, err := clientCfg.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to derive REST client config: %w", err)
	}
	targetClient, err := client.New(restCfg, client.Options{
		Scheme: r.targetScheme,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create client for cluster: %w", err)
	}

	return targetClient, nil
}

func (r *PipelineReconciler) upsertNotifications(ctx context.Context, cluster *clusterctrlv1alpha1.GitopsCluster, target v1alpha1.Target,
	env v1alpha1.Environment, pipeline v1alpha1.Pipeline) error {
	tc, err := r.getTargetClient(ctx, cluster)
	if err != nil {
		return fmt.Errorf("failed to get target cluster client: %w", err)
	}

	addressType := ExternalAddress
	if cluster == nil {
		addressType = InternalAddress
	}
	svcAddr, err := r.getPipelineServiceAddr(ctx, addressType)
	if err != nil {
		return fmt.Errorf("failed to determine webhook address: %w", err)
	}

	provider := &ncv1beta1.Provider{
		TypeMeta: metav1.TypeMeta{
			APIVersion: ncv1beta1.GroupVersion.String(),
			Kind:       ncv1beta1.ProviderKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: target.Namespace,
			Name:      "promotion",
		},
		Spec: ncv1beta1.ProviderSpec{
			Type:    ncv1beta1.GenericProvider,
			Address: fmt.Sprintf("%s/promotion/%s/%s/%s", svcAddr, pipeline.Namespace, pipeline.Name, env.Name),
		},
	}
	if err := tc.Patch(ctx, provider, client.Apply,
		client.FieldOwner("pipeline-controller"),
		client.ForceOwnership,
	); err != nil {

		return fmt.Errorf("failed to create Provider: %w", err)
	}

	alert := &ncv1beta1.Alert{
		TypeMeta: metav1.TypeMeta{
			APIVersion: ncv1beta1.GroupVersion.String(),
			Kind:       ncv1beta1.AlertKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: target.Namespace,
			Name:      "promotion",
		},
		Spec: ncv1beta1.AlertSpec{
			EventSources: []ncv1beta1.CrossNamespaceObjectReference{
				{
					APIVersion: pipeline.Spec.AppRef.APIVersion,
					Kind:       pipeline.Spec.AppRef.Kind,
					Name:       pipeline.Spec.AppRef.Name,
					Namespace:  target.Namespace,
				},
			},
			ProviderRef: meta.LocalObjectReference{
				Name: "promotion",
			},
			ExclusionList: []string{
				".*upgrade.*has.*started",
				".*is.*not.*ready",
				"^Dependencies.*",
			},
		},
	}
	if err := tc.Patch(ctx, alert, client.Apply,
		client.FieldOwner("pipeline-controller"),
		client.ForceOwnership,
	); err != nil {
		return fmt.Errorf("failed to create Alert: %w", err)
	}

	return nil

}

type AddressType int

const (
	ExternalAddress AddressType = iota
	InternalAddress
)

func (r *PipelineReconciler) getPipelineServiceAddr(ctx context.Context, t AddressType) (string, error) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "pipeline-system",
			Name:      "pipeline-controller-manager",
		},
	}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(svc), svc); err != nil {
		return "", fmt.Errorf("failed to retrieve Service object: %w", err)
	}

	var svcPort int32
	for _, port := range svc.Spec.Ports {
		if port.Name == "promhook" {
			svcPort = port.Port
		}
	}
	if svcPort == 0 {
		return "", fmt.Errorf("failed to determine target port: Service %s/%s has no port 'promhook' defined", svc.Namespace, svc.Name)
	}

	var svcHost string
	switch t {
	case InternalAddress:
		svcHost = fmt.Sprintf("%s.%s", svc.Name, svc.Namespace)
	case ExternalAddress:
		for _, ingress := range svc.Status.LoadBalancer.Ingress {
			if ingress.Hostname != "" {
				svcHost = ingress.Hostname
				break
			}
			if ingress.IP != "" {
				svcHost = ingress.IP
				break
			}
		}
	default:
		return "", fmt.Errorf("unknown address type %q requested", t)
	}

	return fmt.Sprintf("http://%s:%d", svcHost, svcPort), nil
}
