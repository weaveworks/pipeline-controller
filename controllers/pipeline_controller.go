package controllers

import (
	"context"
	"fmt"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"

	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/pkg/conditions"
	"github.com/weaveworks/pipeline-controller/pkg/gitopscluster"
)

const (
	PipelineLabelKeyEnabled   = "pipelines.weave.works/enabled"
	PipelineLabelKeyName      = "pipelines.weave.works/name"
	PipelineLabelKeyNamespace = "pipelines.weave.works/namespace"
	PipelineLabelKeyEnv       = "pipelines.weave.works/env"
)

// PipelineReconciler reconciles a Pipeline object
type PipelineReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	targetScheme      *runtime.Scheme
	ControllerName    string
	recorder          record.EventRecorder
	restConfigManager gitopscluster.RestConfigManager
}

func NewPipelineReconciler(
	c client.Client,
	s *runtime.Scheme,
	controllerName string,
	restConfigManager gitopscluster.RestConfigManager,
) *PipelineReconciler {
	targetScheme := runtime.NewScheme()

	return &PipelineReconciler{
		Client:            c,
		Scheme:            s,
		targetScheme:      targetScheme,
		ControllerName:    controllerName,
		restConfigManager: restConfigManager,
	}
}

//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines/finalizers,verbs=update
//+kubebuilder:rbac:groups=gitops.weave.works,resources=gitopsclusters,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

func (r *PipelineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("starting reconciliation")

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
			// check cluster only if ref is defined
			if target.ClusterRef != nil {
				cluster, err := r.getCluster(ctx, pipeline, *target.ClusterRef)
				if err != nil {
					if apierrors.IsNotFound(err) {
						if err := r.setStatusCondition(ctx, pipeline, fmt.Sprintf("Target cluster '%s' not found", target.ClusterRef.String()),
							v1alpha1.TargetClusterNotFoundReason); err != nil {
							return ctrl.Result{}, err
						}
						r.emitEventf(
							&pipeline,
							corev1.EventTypeWarning,
							"SetStatusConditionError", "Failed to set status for pipeline %s/%s: %s; requeue",
							pipeline.GetNamespace(), pipeline.GetName(),
							err,
						)
						// do not requeue immediately, when the cluster is created the watcher should trigger a reconciliation
						return ctrl.Result{RequeueAfter: v1alpha1.DefaultRequeueInterval}, nil
					}
					r.emitEventf(
						&pipeline,
						corev1.EventTypeWarning,
						"GetClusterError", "Failed to get cluster %s/%s for pipeline %s/%s: %s",
						target.ClusterRef.Namespace, target.ClusterRef.Name,
						pipeline.GetNamespace(), pipeline.GetName(),
						err,
					)
					return ctrl.Result{}, err
				}

				if !conditions.IsReady(cluster.Status.Conditions) {
					err := r.setStatusCondition(
						ctx, pipeline,
						fmt.Sprintf("Target cluster '%s' not ready", target.ClusterRef.String()),
						v1alpha1.TargetClusterNotReadyReason,
					)
					if err != nil {
						r.emitEventf(
							&pipeline,
							corev1.EventTypeWarning,
							"SetStatusConditionError", "Failed to set status for pipeline %s/%s: %s",
							pipeline.GetNamespace(), pipeline.GetName(),
							err,
						)
						return ctrl.Result{}, err
					}
					// do not requeue immediately, when the cluster is created the watcher should trigger a reconciliation
					return ctrl.Result{RequeueAfter: v1alpha1.DefaultRequeueInterval}, nil
				}
			}

			if err := r.setPipelineLabels(ctx, pipeline, env, target); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	newCondition := metav1.Condition{
		Type:    conditions.ReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  v1alpha1.ReconciliationSucceededReason,
		Message: trimString("All clusters checked", v1alpha1.MaxConditionMessageLength),
	}
	pipeline.Status.ObservedGeneration = pipeline.Generation
	apimeta.SetStatusCondition(&pipeline.Status.Conditions, newCondition)
	if err := r.patchStatus(ctx, client.ObjectKeyFromObject(&pipeline), pipeline.Status); err != nil {
		r.emitEventf(
			&pipeline,
			corev1.EventTypeWarning,
			"SetStatus", "Failed to patch status for pipeline %s/%s: %s",
			pipeline.GetNamespace(), pipeline.GetName(),
			err,
		)
		return ctrl.Result{Requeue: true}, err
	}
	r.emitEventf(
		&pipeline,
		corev1.EventTypeNormal,
		"Updated", "Updated pipeline %s/%s",
		pipeline.GetNamespace(), pipeline.GetName(),
	)

	return ctrl.Result{}, nil
}

func (r *PipelineReconciler) setStatusCondition(ctx context.Context, p v1alpha1.Pipeline, msg, reason string) error {
	newCondition := metav1.Condition{
		Type:    conditions.ReadyCondition,
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

func (r *PipelineReconciler) getCluster(ctx context.Context, p v1alpha1.Pipeline, clusterRef v1alpha1.CrossNamespaceClusterReference) (*clusterctrlv1alpha1.GitopsCluster, error) {
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

func (r *PipelineReconciler) setPipelineLabels(ctx context.Context, pipeline v1alpha1.Pipeline, env v1alpha1.Environment, target v1alpha1.Target) error {
	var (
		restConfig       *rest.Config
		err              error
		clusterName      string
		clusterNamespace string
	)

	if target.ClusterRef == nil {
		restConfig, err = r.restConfigManager.Get("local", "")
		if err != nil {
			return fmt.Errorf("failed to get rest config for local cluster: %w", err)
		}
		clusterName = "local"
		clusterNamespace = ""
	} else {
		restConfig, err = r.restConfigManager.Get(target.ClusterRef.Name, target.ClusterRef.Namespace)
		if err != nil {
			return fmt.Errorf("failed to get rest config for cluster %s/%s: %w", target.ClusterRef.Namespace, target.ClusterRef.Name, err)
		}
		clusterName = target.ClusterRef.Name
		clusterNamespace = target.ClusterRef.Namespace
	}

	clusterClient, err := client.New(restConfig, client.Options{
		Scheme: r.Scheme,
	})
	if err != nil {
		return fmt.Errorf("failed to create client for cluster %s/%s: %w", clusterNamespace, clusterName, err)
	}

	hr := helmv2.HelmRelease{}
	if err := clusterClient.Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: pipeline.Spec.AppRef.Name}, &hr); err != nil {
		return fmt.Errorf("failed to get HelmRelease %s/%s on cluster %s/%s: %w", pipeline.Namespace, pipeline.Spec.AppRef.Name, clusterNamespace, clusterName, err)
	}

	if hr.Labels == nil {
		hr.Labels = map[string]string{}
	}

	hr.Labels[PipelineLabelKeyEnabled] = "true"
	hr.Labels[PipelineLabelKeyName] = pipeline.Name
	hr.Labels[PipelineLabelKeyNamespace] = pipeline.Namespace
	hr.Labels[PipelineLabelKeyEnv] = env.Name

	if err := clusterClient.Update(ctx, &hr); err != nil {
		return fmt.Errorf("failed to update HelmRelease %s/%s on cluster %s/%s: %w", pipeline.Namespace, pipeline.Spec.AppRef.Name, clusterNamespace, clusterName, err)
	}

	return nil
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

	if r.recorder == nil {
		r.recorder = mgr.GetEventRecorderFor(r.ControllerName)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Pipeline{}).
		Watches(
			&source.Kind{Type: &clusterctrlv1alpha1.GitopsCluster{}},
			handler.EnqueueRequestsFromMapFunc(r.requestsForRevisionChangeOf(gitopsClusterIndexKey)),
		).
		Complete(r)
}

func (r *PipelineReconciler) emitEventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	if r.recorder == nil {
		return
	}

	r.recorder.Eventf(object, eventtype, reason, messageFmt, args...)
}

func trimString(str string, limit int) string {
	if len(str) <= limit {
		return str
	}

	return str[0:limit] + "..."
}
