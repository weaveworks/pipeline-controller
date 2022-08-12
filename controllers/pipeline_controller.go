package controllers

import (
	"context"
	"fmt"

	"github.com/fluxcd/pkg/apis/meta"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	ControllerName string
}

//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines/finalizers,verbs=update

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

	for _, env := range pipeline.Spec.Environments {
		for _, target := range env.Targets {
			cluster, err := r.getCluster(ctx, pipeline, target.ClusterRef)
			if err != nil {
				if apierrors.IsNotFound(err) {
					msg := fmt.Sprintf("Target cluster '%s' not found", target.ClusterRef.String())
					newCondition := metav1.Condition{
						Type:    meta.ReadyCondition,
						Status:  metav1.ConditionFalse,
						Reason:  v1alpha1.TargetClusterNotFoundReason,
						Message: trimString(msg, v1alpha1.MaxConditionMessageLength),
					}
					pipeline.Status.ObservedGeneration = pipeline.Generation
					apimeta.SetStatusCondition(&pipeline.Status.Conditions, newCondition)
					if err := r.patchStatus(ctx, req, pipeline.Status); err != nil {
						return ctrl.Result{Requeue: true}, err
					}
					logger.Info(msg)
					// do not requeue immediately, when the cluster is created the watcher should trigger a reconciliation
					return ctrl.Result{RequeueAfter: v1alpha1.DefaultRequeueInterval}, nil
				}

				return ctrl.Result{Requeue: true}, err
			}
			logger.Info("got cluster", "cluster", cluster)
		}
	}

	newCondition := metav1.Condition{
		Type:    meta.ReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  v1alpha1.ReconciliationSucceededReason,
		Message: trimString("All clusters checked", v1alpha1.MaxConditionMessageLength),
	}
	pipeline.Status.ObservedGeneration = pipeline.Generation
	apimeta.SetStatusCondition(&pipeline.Status.Conditions, newCondition)
	if err := r.patchStatus(ctx, req, pipeline.Status); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	return ctrl.Result{}, nil
}

func (r *PipelineReconciler) patchStatus(ctx context.Context, req ctrl.Request, newStatus v1alpha1.PipelineStatus) error {
	var pipeline v1alpha1.Pipeline
	if err := r.Get(ctx, req.NamespacedName, &pipeline); err != nil {
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
