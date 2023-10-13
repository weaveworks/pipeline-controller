package leveltriggered

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
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/pkg/conditions"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

// PipelineReconciler reconciles a Pipeline object
type PipelineReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	targetScheme   *runtime.Scheme
	ControllerName string
	recorder       record.EventRecorder
	stratReg       strategy.StrategyRegistry
}

func NewPipelineReconciler(c client.Client, s *runtime.Scheme, controllerName string, stratReg strategy.StrategyRegistry) *PipelineReconciler {
	targetScheme := runtime.NewScheme()

	return &PipelineReconciler{
		Client:         c,
		Scheme:         s,
		targetScheme:   targetScheme,
		ControllerName: controllerName,
		stratReg:       stratReg,
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

	envStatuses := map[string]*v1alpha1.EnvironmentStatus{}
	var unready bool

	for _, env := range pipeline.Spec.Environments {
		var envStatus v1alpha1.EnvironmentStatus
		envStatus.Targets = make([]v1alpha1.TargetStatus, len(env.Targets))
		envStatuses[env.Name] = &envStatus

		for i, target := range env.Targets {
			targetStatus := &envStatus.Targets[i]
			targetStatus.ClusterAppRef.LocalAppReference = pipeline.Spec.AppRef

			var clusterObject *clusterctrlv1alpha1.GitopsCluster
			if target.ClusterRef != nil {
				// record the fact of the remote cluster in the target status
				targetStatus.ClusterAppRef.ClusterRef = target.ClusterRef

				// even though we could just get the client from our list of clients, we check that the cluster object exists
				// every time. We might have been queued because of a cluster disappearing.

				var err error
				clusterObject, err = r.getCluster(ctx, pipeline, *target.ClusterRef)
				if err != nil {
					// emit the event whatever problem there was
					r.emitEventf(
						&pipeline,
						corev1.EventTypeWarning,
						"GetClusterError", "Failed to get cluster %s/%s for pipeline %s/%s: %s",
						target.ClusterRef.Namespace, target.ClusterRef.Name,
						pipeline.GetNamespace(), pipeline.GetName(),
						err,
					)

					// not found -- fine, maybe things are happening out of order; make a note and wait until the cluster exists (or something else happens).
					if apierrors.IsNotFound(err) {
						targetStatus.Error = err.Error()
						unready = true
						continue
					}

					// some other error -- this _is_ unexpected, so return it to controller-runtime.
					return ctrl.Result{}, err
				}

				if !conditions.IsReady(clusterObject.Status.Conditions) {
					msg := fmt.Sprintf("Target cluster '%s' not ready", target.ClusterRef.String())
					targetStatus.Error = msg
					unready = true
					continue
				}
			}

			// it's OK if this is `nil` -- that represents the local cluster.
			clusterClient, err := r.getClusterClient(clusterObject)
			if err != nil {
				return ctrl.Result{}, err
			}

			// look up the actual application
			var app helmv2.HelmRelease // FIXME this can be other kinds!
			appKey := targetObjectKey(&pipeline, &target)
			err = clusterClient.Get(ctx, appKey, &app)
			if err != nil {
				r.emitEventf(
					&pipeline,
					corev1.EventTypeWarning,
					"GetAppError", "Failed to get application object %s%s/%s for pipeline %s/%s: %s",
					clusterPrefix(target.ClusterRef), appKey.Namespace, appKey.Name,
					pipeline.GetNamespace(), pipeline.GetName(),
					err,
				)

				targetStatus.Error = err.Error()
				unready = true
				continue
			}
			setTargetStatus(targetStatus, &app)
		}
	}

	var readyCondition metav1.Condition
	if unready {
		readyCondition = metav1.Condition{
			Type:    conditions.ReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.TargetNotReadableReason,
			Message: "One or more targets was not reachable or not present",
		}
	} else {
		readyCondition = metav1.Condition{
			Type:    conditions.ReadyCondition,
			Status:  metav1.ConditionTrue,
			Reason:  v1alpha1.ReconciliationSucceededReason,
			Message: trimString("All clusters checked", v1alpha1.MaxConditionMessageLength),
		}
	}

	pipeline.Status.ObservedGeneration = pipeline.Generation
	apimeta.SetStatusCondition(&pipeline.Status.Conditions, readyCondition)
	pipeline.Status.Environments = envStatuses
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

	firstEnv := pipeline.Spec.Environments[0]

	latestRevision := checkAllTargetsHaveSameRevision(pipeline.Status.Environments[firstEnv.Name])
	if latestRevision == "" {
		// not all targets have the same revision, or have no revision set, so we can't proceed
		if err := r.setPendingCondition(ctx, pipeline, v1alpha1.EnvironmentNotReadyReason, "Waiting for all targets to have the same revision"); err != nil {
			return ctrl.Result{Requeue: true}, fmt.Errorf("error setting pending condition: %w", err)
		}

		return ctrl.Result{}, nil
	}

	if !checkAllTargetsAreReady(pipeline.Status.Environments[firstEnv.Name]) {
		// not all targets are ready, so we can't proceed
		if err := r.setPendingCondition(ctx, pipeline, v1alpha1.EnvironmentNotReadyReason, "Waiting for all targets to be ready"); err != nil {
			return ctrl.Result{}, fmt.Errorf("error setting pending condition: %w", err)
		}

		return ctrl.Result{}, nil
	}

	if err := r.removePendingCondition(ctx, pipeline); err != nil {
		return ctrl.Result{}, fmt.Errorf("error removing pending condition: %w", err)
	}

	for _, env := range pipeline.Spec.Environments[1:] {
		// if all targets run the latest revision and are ready, we can skip this environment
		if checkAllTargetsRunRevision(pipeline.Status.Environments[env.Name], latestRevision) && checkAllTargetsAreReady(pipeline.Status.Environments[env.Name]) {
			continue
		}

		if checkAnyTargetHasRevision(pipeline.Status.Environments[env.Name], latestRevision) {
			return ctrl.Result{}, nil
		}

		err := r.promoteLatestRevision(ctx, pipeline, env, latestRevision)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error promoting new version: %w", err)
		}

		break
	}

	return ctrl.Result{}, nil
}

func (r *PipelineReconciler) setPendingCondition(ctx context.Context, pipeline v1alpha1.Pipeline, reason, message string) error {
	condition := metav1.Condition{
		Type:    conditions.PromotionPendingCondition,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	}

	apimeta.SetStatusCondition(&pipeline.Status.Conditions, condition)

	if err := r.patchStatus(ctx, client.ObjectKeyFromObject(&pipeline), pipeline.Status); err != nil {
		return err
	}

	return nil
}

func (r *PipelineReconciler) removePendingCondition(ctx context.Context, pipeline v1alpha1.Pipeline) error {
	apimeta.RemoveStatusCondition(&pipeline.Status.Conditions, conditions.PromotionPendingCondition)

	if err := r.patchStatus(ctx, client.ObjectKeyFromObject(&pipeline), pipeline.Status); err != nil {
		return err
	}

	return nil
}

func (r *PipelineReconciler) promoteLatestRevision(ctx context.Context, pipeline v1alpha1.Pipeline, env v1alpha1.Environment, revision string) error {
	// none of the current strategies are idepontent, using it now to keep the ball rolling, but we need to implement
	// strategies that are.

	promotion := pipeline.Spec.GetPromotion(env.Name)
	if promotion == nil {
		return nil
	}

	strat, err := r.stratReg.Get(*promotion)
	if err != nil {
		return fmt.Errorf("error getting strategy from registry: %w", err)
	}

	prom := strategy.Promotion{
		PipelineName:      pipeline.Name,
		PipelineNamespace: pipeline.Namespace,
		Environment:       env,
		Version:           revision,
	}

	_, err = strat.Promote(ctx, *pipeline.Spec.Promotion, prom)

	return err
}

func checkAnyTargetHasRevision(env *v1alpha1.EnvironmentStatus, revision string) bool {
	for _, target := range env.Targets {
		if target.Revision == revision {
			return true
		}
	}

	return false
}

func checkAllTargetsRunRevision(env *v1alpha1.EnvironmentStatus, revision string) bool {
	for _, target := range env.Targets {
		if target.Revision != revision {
			return false
		}
	}

	return true
}

// checkAllTargetsHaveSameRevision returns a revision if all targets in the environment have the same,
// non-empty revision, and an empty string otherwise.
func checkAllTargetsHaveSameRevision(env *v1alpha1.EnvironmentStatus) string {
	if len(env.Targets) == 0 {
		return ""
	}

	revision := env.Targets[0].Revision
	for _, target := range env.Targets {
		if target.Revision != revision {
			return ""
		}
	}

	return revision
}

func checkAllTargetsAreReady(env *v1alpha1.EnvironmentStatus) bool {
	for _, target := range env.Targets {
		if !target.Ready {
			return false
		}
	}

	return true
}

// getClusterClient retrieves or creates a client for the cluster in question. A `nil` value for the argument indicates the local cluster.
func (r *PipelineReconciler) getClusterClient(cluster *clusterctrlv1alpha1.GitopsCluster) (client.Client, error) {
	if cluster == nil {
		return r.Client, nil
	}
	// TODO future: get the secret via the cluster object, connect to remote cluster
	return nil, fmt.Errorf("remote clusters not supported yet")
}

// targetObjectKey returns the object key (namespaced name) for a target. The Pipeline is passed in as well as the Target, since the definition can be spread between these specs.
func targetObjectKey(pipeline *v1alpha1.Pipeline, target *v1alpha1.Target) client.ObjectKey {
	key := client.ObjectKey{}
	key.Name = pipeline.Spec.AppRef.Name
	key.Namespace = target.Namespace
	return key
}

// clusterPrefix returns a string naming the cluster containing an app, to prepend to the usual namespace/name format of the app object itself. So that it can be empty, the separator is include in the return value.
func clusterPrefix(ref *v1alpha1.CrossNamespaceClusterReference) string {
	if ref == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s:", ref.Namespace, ref.Name)
}

// setTargetStatus gets the relevant status from the app object given, and records it in the TargetStatus.
func setTargetStatus(status *v1alpha1.TargetStatus, target *helmv2.HelmRelease) {
	status.Revision = target.Status.LastAppliedRevision
	status.Ready = conditions.IsReady(target.Status.Conditions)
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

// SetupWithManager sets up the controller with the Manager.
func (r *PipelineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	const (
		gitopsClusterIndexKey string = ".spec.environment.ClusterRef" // this is arbitrary, but let's make it suggest what it's indexing.
	)
	// Index the Pipelines by the GitopsCluster references they (may) point at.
	if err := mgr.GetCache().IndexField(context.TODO(), &v1alpha1.Pipeline{}, gitopsClusterIndexKey,
		r.indexClusterKind("GitopsCluster")); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	// Index the Pipelines by the application references they point at.
	if err := mgr.GetCache().IndexField(context.TODO(), &v1alpha1.Pipeline{}, applicationKey /* <- from indexing.go */, r.indexApplication); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	if r.recorder == nil {
		r.recorder = mgr.GetEventRecorderFor(r.ControllerName)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Pipeline{}).
		Watches(
			&source.Kind{Type: &clusterctrlv1alpha1.GitopsCluster{}},
			handler.EnqueueRequestsFromMapFunc(r.requestsForCluster(gitopsClusterIndexKey)),
		).
		Watches(
			&source.Kind{Type: &helmv2.HelmRelease{}},
			handler.EnqueueRequestsFromMapFunc(r.requestsForApplication),
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
