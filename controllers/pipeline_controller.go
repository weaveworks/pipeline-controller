package controllers

import (
	"context"
	"fmt"

	helmctrlv2beta1 "github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/fluxcd/pkg/apis/meta"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

// PipelineReconciler reconciles a Pipeline object
type PipelineReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	targetScheme   *runtime.Scheme
	ControllerName string
}

func NewPipelineReconciler(c client.Client, s *runtime.Scheme, controllerName string) *PipelineReconciler {
	targetScheme := runtime.NewScheme()
	helmctrlv2beta1.AddToScheme(targetScheme)
	return &PipelineReconciler{
		Client:         c,
		Scheme:         s,
		targetScheme:   targetScheme,
		ControllerName: controllerName,
	}
}

//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines/finalizers,verbs=update
//+kubebuilder:rbac:groups=gitops.weave.works,resources=gitopsclusters,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

func (r *PipelineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.WithValues("pipeline", req.NamespacedName.String()).Info("starting reconciliation")

	var pipeline v1alpha1.Pipeline
	if err := r.Get(ctx, req.NamespacedName, &pipeline); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Examine if the object is under deletion
	if !pipeline.ObjectMeta.DeletionTimestamp.IsZero() {
		// Stop reconciliation as the object is being deleted
		return ctrl.Result{}, nil
	}

	envsStatuses := []v1alpha1.EnvironmentStatus{}

	for _, env := range pipeline.Spec.Environments {
		envStatus := v1alpha1.EnvironmentStatus{Name: env.Name}

		for _, target := range env.Targets {
			cluster, err := r.getCluster(ctx, pipeline, target.ClusterRef)
			if err != nil {
				if apierrors.IsNotFound(err) {
					if err := r.setStatusCondition(ctx, pipeline, fmt.Sprintf("Target cluster '%s' not found", target.ClusterRef.String()),
						v1alpha1.TargetClusterNotFoundReason); err != nil {
						return ctrl.Result{}, err
					}
					// do not requeue immediately, when the cluster is created the watcher should trigger a reconciliation
					return ctrl.Result{RequeueAfter: v1alpha1.DefaultRequeueInterval}, nil
				}
				return ctrl.Result{}, err
			}

			secretKey, err := deriveSecretKey(*cluster)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed deriving kubeconfig Secret name: %w", err)
			}
			var kubeconfigSecret corev1.Secret
			if err := r.Get(ctx, secretKey, &kubeconfigSecret); err != nil {
				if apierrors.IsNotFound(err) {
					if err := r.setStatusCondition(ctx, pipeline, fmt.Sprintf("Secret for target cluster '%s' not found", target.ClusterRef.String()),
						v1alpha1.TargetClusterSecretNotFoundReason); err != nil {
						return ctrl.Result{}, err
					}
					// do not requeue immediately, when the cluster is created the watcher should trigger a reconciliation
					return ctrl.Result{RequeueAfter: v1alpha1.DefaultRequeueInterval}, nil
				}
				return ctrl.Result{}, err
			}

			clientCfg, err := clientcmd.NewClientConfigFromBytes(kubeconfigSecret.Data["value"])
			if err != nil {
				return ctrl.Result{}, err
			}
			restCfg, err := clientCfg.ClientConfig()
			if err != nil {
				return ctrl.Result{}, err
			}
			targetClient, err := client.New(restCfg, client.Options{
				Scheme: r.targetScheme,
			})
			if err != nil {
				return ctrl.Result{}, err
			}

			logger.Info("Listing HelmReleases", "pipeline", pipeline.Name, "namespace", target.Namespace)

			var hrs helmctrlv2beta1.HelmReleaseList
			if err := targetClient.List(ctx, &hrs,
				client.InNamespace(target.Namespace),
				client.MatchingLabels{
					"pipelines.wego.weave.works/pipeline": pipeline.Name,
				}); err != nil {
				if err := r.setStatusCondition(ctx, pipeline, fmt.Sprintf("Failed listing HelmReleases in '%s': %s", target.String(), err),
					v1alpha1.TargetClusterSecretNotFoundReason); err != nil {
					return ctrl.Result{Requeue: true}, err
				}
				return ctrl.Result{}, err
			}
			logger.Info("Got HelmReleases", "target", target, "HelmReleaseList", hrs)

			envStatus.TargetsStatus = append(envStatus.TargetsStatus,
				v1alpha1.TargetStatus{Namespace: target.Namespace, ClusterRef: target.ClusterRef, Workloads: helmReleaseListToWorkloads(hrs)})
		}

		envsStatuses = append(envsStatuses, envStatus)
	}

	pipeline.Status.Environments = envsStatuses

	newCondition := metav1.Condition{
		Type:    meta.ReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  v1alpha1.ReconciliationSucceededReason,
		Message: trimString("All clusters checked", v1alpha1.MaxConditionMessageLength),
	}
	pipeline.Status.ObservedGeneration = pipeline.Generation
	apimeta.SetStatusCondition(&pipeline.Status.Conditions, newCondition)
	if err := r.patchStatus(ctx, client.ObjectKeyFromObject(&pipeline), pipeline.Status); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	return ctrl.Result{}, nil
}

func deriveSecretKey(cluster clusterctrlv1alpha1.GitopsCluster) (types.NamespacedName, error) {
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

	return types.NamespacedName{},
		fmt.Errorf("cluster %s doesn't have a secretRef or capiClusterRef set", client.ObjectKeyFromObject(&cluster).String())
}

func (r *PipelineReconciler) setStatusCondition(ctx context.Context, p v1alpha1.Pipeline, msg, reason string) error {
	newCondition := metav1.Condition{
		Type:    meta.ReadyCondition,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: trimString(msg, v1alpha1.MaxConditionMessageLength),
	}
	p.Status.ObservedGeneration = p.Generation
	apimeta.SetStatusCondition(&p.Status.Conditions, newCondition)
	if err := r.patchStatus(ctx, client.ObjectKeyFromObject(&p), p.Status); err != nil {
		return fmt.Errorf("failed patching Pipeline: %w", err)
	}
	return nil
}

func (r *PipelineReconciler) patchStatus(ctx context.Context, n types.NamespacedName, newStatus v1alpha1.PipelineStatus) error {
	var pipeline v1alpha1.Pipeline
	if err := r.Get(ctx, n, &pipeline); err != nil {
		return err
	}

	patch := client.MergeFrom(pipeline.DeepCopy())
	pipeline.Status = newStatus
	return r.Status().Patch(ctx, &pipeline, patch, client.FieldOwner(r.ControllerName))
}

func (r *PipelineReconciler) getCluster(ctx context.Context, p v1alpha1.Pipeline, clusterRef v1alpha1.CrossNamespaceSourceReference) (*clusterctrlv1alpha1.GitopsCluster, error) {
	cluster := &clusterctrlv1alpha1.GitopsCluster{}
	namespace := clusterRef.Namespace
	if clusterRef.Namespace == "" {
		namespace = p.Namespace
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: clusterRef.Name}, cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PipelineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	const (
		gitopsClusterIndexKey string = ".metadata.gitopsCluster"
	)
	// Index the Pipelines by the GitopsCluster references they (may) point at.
	if err := mgr.GetCache().IndexField(context.TODO(), &v1alpha1.Pipeline{}, gitopsClusterIndexKey,
		r.indexBy("GitopsCluster")); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Pipeline{}).
		Watches(
			&source.Kind{Type: &clusterctrlv1alpha1.GitopsCluster{}},
			handler.EnqueueRequestsFromMapFunc(r.requestsForRevisionChangeOf(gitopsClusterIndexKey)),
		).
		Complete(r)
}

func trimString(str string, limit int) string {
	if len(str) <= limit {
		return str
	}

	return str[0:limit] + "..."
}

func helmReleaseListToWorkloads(hrs helmctrlv2beta1.HelmReleaseList) []v1alpha1.CrossNamespaceSourceReference {
	workloads := []v1alpha1.CrossNamespaceSourceReference{}
	for _, hr := range hrs.Items {
		workloads = append(workloads, v1alpha1.CrossNamespaceSourceReference{
			APIVersion: hr.APIVersion,
			Kind:       hr.Kind,
			Name:       hr.Name,
			Namespace:  hr.Namespace,
		})
	}

	return workloads
}
