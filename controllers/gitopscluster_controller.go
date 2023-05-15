package controllers

/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	gitopsv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/pkg/gitopscluster"
)

const (
	// SecretNameIndexKey is the key used for indexing secret
	// resources based on their name.
	SecretNameIndexKey string = "SecretNameIndexKey"
	// CAPIClusterNameIndexKey is the key used for indexing CAPI cluster
	// resources based on their name.
	CAPIClusterNameIndexKey string = "CAPIClusterNameIndexKey"

	// MissingSecretRequeueTime is the period after which a secret will be
	// checked if it doesn't exist.
	MissingSecretRequeueTime = time.Second * 30

	// SecretChecksumKey is the key used to store the checksum of the secret
	SecretChecksumKey string = "gitops.weave.works/secret-checksum"

	// GitopsClusterFinalizer is the finalizer used by Pipeline controller
	GitopsClusterFinalizer string = "gitops.weave.works/pipelines"
)

// GitopsClusterReconciler reconciles a GitopsCluster object
type GitopsClusterReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	clientsManager gitopscluster.RestConfigManager
}

// NewGitopsClusterReconciler creates and returns a configured
// reconciler ready for use.
func NewGitopsClusterReconciler(c client.Client, s *runtime.Scheme, clientsManager gitopscluster.RestConfigManager) *GitopsClusterReconciler {
	return &GitopsClusterReconciler{
		Client:         c,
		Scheme:         s,
		clientsManager: clientsManager,
	}
}

// +kubebuilder:rbac:groups=gitops.weave.works,resources=gitopsclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gitops.weave.works,resources=gitopsclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gitops.weave.works,resources=gitopsclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;watch;list
// +kubebuilder:rbac:groups="cluster.x-k8s.io",resources=clusters,verbs=get;watch;list

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *GitopsClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the Cluster
	cluster := &gitopsv1alpha1.GitopsCluster{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// examine DeletionTimestamp to determine if object is under deletion
	if cluster.ObjectMeta.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(cluster, GitopsClusterFinalizer) {
			controllerutil.AddFinalizer(cluster, GitopsClusterFinalizer)
			if err := r.Update(ctx, cluster); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(cluster, GitopsClusterFinalizer) {
			r.clientsManager.Delete(cluster.Name, cluster.Namespace)

			// remove our finalizer from the list and update it.
			controllerutil.RemoveFinalizer(cluster, GitopsClusterFinalizer)
			if err := r.Update(ctx, cluster); err != nil {
				return ctrl.Result{}, err
			}
		}

		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	secretData, err := r.restConfigSecretData(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	secretMd5 := fmt.Sprintf("%x", md5.Sum(secretData))

	if cluster.Annotations[SecretChecksumKey] == secretMd5 {
		_, err := r.clientsManager.Get(cluster.Name, cluster.Namespace)
		if err == nil {
			return ctrl.Result{}, nil
		}

		client, err := r.getClusterRestConfig(ctx, cluster)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get cluster client cluster=%s namespace=%s: %w", cluster.Name, cluster.Namespace, err)
		}

		r.clientsManager.Add(cluster.Name, cluster.Namespace, client)
		return ctrl.Result{}, nil
	}

	if cluster.Annotations == nil {
		cluster.Annotations = map[string]string{}
	}

	cluster.Annotations[SecretChecksumKey] = secretMd5

	if err := r.Update(ctx, cluster); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update cluster secret checksum cluster=%s namespace=%s: %w", cluster.Name, cluster.Namespace, err)
	}

	client, err := r.getClusterRestConfig(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get cluster client cluster=%s namespace=%s: %w", cluster.Name, cluster.Namespace, err)
	}

	r.clientsManager.Add(cluster.Name, cluster.Namespace, client)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GitopsClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetCache().IndexField(context.TODO(), &gitopsv1alpha1.GitopsCluster{}, SecretNameIndexKey, r.indexGitopsClusterBySecretName); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	if err := mgr.GetCache().IndexField(context.TODO(), &gitopsv1alpha1.GitopsCluster{}, CAPIClusterNameIndexKey, r.indexGitopsClusterByCAPIClusterName); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	builder := ctrl.NewControllerManagedBy(mgr).
		For(&gitopsv1alpha1.GitopsCluster{}).
		Watches(
			&source.Kind{Type: &corev1.Secret{}},
			handler.EnqueueRequestsFromMapFunc(r.requestsForSecretChange),
		)

	return builder.Complete(r)
}

func (r *GitopsClusterReconciler) indexGitopsClusterBySecretName(o client.Object) []string {
	c, ok := o.(*gitopsv1alpha1.GitopsCluster)
	if !ok {
		panic(fmt.Sprintf("Expected a GitopsCluster, got %T", o))
	}

	if c.Spec.SecretRef != nil {
		return []string{c.Spec.SecretRef.Name}
	}

	return nil
}

func (r *GitopsClusterReconciler) indexGitopsClusterByCAPIClusterName(o client.Object) []string {
	c, ok := o.(*gitopsv1alpha1.GitopsCluster)
	if !ok {
		panic(fmt.Sprintf("Expected a GitopsCluster, got %T", o))
	}

	if c.Spec.CAPIClusterRef != nil {
		return []string{c.Spec.CAPIClusterRef.Name}
	}

	return nil
}

func (r *GitopsClusterReconciler) requestsForSecretChange(o client.Object) []ctrl.Request {
	secret, ok := o.(*corev1.Secret)
	if !ok {
		panic(fmt.Sprintf("Expected a Secret but got a %T", o))
	}

	ctx := context.Background()
	var list gitopsv1alpha1.GitopsClusterList
	if err := r.Client.List(ctx, &list, client.MatchingFields{SecretNameIndexKey: secret.GetName()}); err != nil {
		return nil
	}

	var reqs []ctrl.Request
	for _, i := range list.Items {
		name := client.ObjectKey{Namespace: i.Namespace, Name: i.Name}
		reqs = append(reqs, ctrl.Request{NamespacedName: name})
	}
	return reqs
}

func (r *GitopsClusterReconciler) requestsForCAPIClusterChange(o client.Object) []ctrl.Request {
	cluster, ok := o.(*clusterv1.Cluster)
	if !ok {
		panic(fmt.Sprintf("Expected a CAPI Cluster but got a %T", o))
	}

	ctx := context.Background()
	var list gitopsv1alpha1.GitopsClusterList
	if err := r.Client.List(ctx, &list, client.MatchingFields{CAPIClusterNameIndexKey: cluster.GetName()}); err != nil {
		return nil
	}

	var reqs []ctrl.Request
	for _, i := range list.Items {
		name := client.ObjectKey{Namespace: i.Namespace, Name: i.Name}
		reqs = append(reqs, ctrl.Request{NamespacedName: name})
	}
	return reqs
}

func (r *GitopsClusterReconciler) getClusterRestConfig(ctx context.Context, cluster *gitopsv1alpha1.GitopsCluster) (*rest.Config, error) {
	config, err := r.restConfigFromSecret(ctx, cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to get rest config from secret: %w", err)
	}

	return config, nil
}

func (r *GitopsClusterReconciler) restConfigFromSecret(ctx context.Context, cluster *gitopsv1alpha1.GitopsCluster) (*rest.Config, error) {
	log := log.FromContext(ctx)

	data, err := r.restConfigSecretData(ctx, cluster)
	if err != nil {
		return nil, err
	}

	restCfg, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		log.Error(err, "unable to create kubconfig from GitOps Cluster secret data", "cluster", cluster.Name)

		return nil, err
	}

	return restCfg, nil
}

func (r *GitopsClusterReconciler) restConfigSecretData(ctx context.Context, cluster *gitopsv1alpha1.GitopsCluster) ([]byte, error) {
	log := log.FromContext(ctx)

	var secretRef string

	if cluster.Spec.CAPIClusterRef != nil {
		secretRef = fmt.Sprintf("%s-kubeconfig", cluster.Spec.CAPIClusterRef.Name)
	}

	if secretRef == "" && cluster.Spec.SecretRef != nil {
		secretRef = cluster.Spec.SecretRef.Name
	}

	if secretRef == "" {
		return nil, errors.New("no secret ref found")
	}

	key := types.NamespacedName{
		Name:      secretRef,
		Namespace: cluster.Namespace,
	}

	var secret v1.Secret
	if err := r.Get(ctx, key, &secret); err != nil {
		log.Error(err, "unable to fetch secret for GitOps Cluster", "cluster", cluster.Name)

		return nil, err
	}

	var data []byte

	for k := range secret.Data {
		if k == "value" || k == "value.yaml" {
			data = secret.Data[k]

			break
		}
	}

	if len(data) == 0 {
		return nil, errors.New("no data present in cluster secret")
	}

	return data, nil
}
