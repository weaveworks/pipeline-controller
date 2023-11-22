package leveltriggered

import (
	"context"
	"fmt"

	"github.com/fluxcd/pkg/runtime/patch"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/pkg/conditions"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

// PipelineReconciler reconciles a Pipeline object
type PipelineReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ControllerName string
	caches         *caches
	recorder       record.EventRecorder
	stratReg       strategy.StrategyRegistry

	appEvents chan event.GenericEvent
}

func NewPipelineReconciler(c client.Client, s *runtime.Scheme, controllerName string, eventRecorder record.EventRecorder, stratReg strategy.StrategyRegistry) *PipelineReconciler {
	appEvents := make(chan event.GenericEvent)

	// this is empty because we're going to use unstructured.Unstructured objects to support arbitrary types.
	// If something changed and we wanted typed objects, this scheme would need to have those registered.
	targetScheme := runtime.NewScheme()

	pc := &PipelineReconciler{
		Client:         c,
		Scheme:         s,
		recorder:       eventRecorder,
		ControllerName: controllerName,
		stratReg:       stratReg,
		caches:         newCaches(appEvents, targetScheme),
		appEvents:      appEvents,
	}
	return pc
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

	patcher := patch.NewSerialPatcher(&pipeline, r.Client)
	withFieldOwner := patch.WithFieldOwner(r.ControllerName)

	envStatuses := map[string]*v1alpha1.EnvironmentStatus{}
	var unready bool

	for _, env := range pipeline.Spec.Environments {
		envStatus, ok := pipeline.Status.Environments[env.Name]
		if !ok {
			envStatus = &v1alpha1.EnvironmentStatus{}
		}
		envStatus.Targets = make([]v1alpha1.TargetStatus, len(env.Targets))
		envStatuses[env.Name] = envStatus

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

			targetObj, err := targetObject(&pipeline, &target)
			if err != nil {
				targetStatus.Error = fmt.Sprintf("target spec could not be interpreted as an object: %s", err.Error())
				unready = true
				continue
			}

			// it's OK if clusterObject is still `nil` -- that represents the local cluster.
			clusterClient, ok, err := r.caches.watchTargetAndGetReader(ctx, clusterObject, targetObj)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !ok {
				targetStatus.Error = "Target cluster client is not synced"
				unready = true
				continue
			}

			targetKey := client.ObjectKeyFromObject(targetObj)
			// look up the actual application
			err = clusterClient.Get(ctx, targetKey, targetObj)
			if err != nil {
				r.emitEventf(
					&pipeline,
					corev1.EventTypeWarning,
					"GetAppError", "Failed to get application object %s%s/%s for pipeline %s/%s: %s",
					clusterPrefix(target.ClusterRef), targetKey.Namespace, targetKey.Name,
					pipeline.GetNamespace(), pipeline.GetName(),
					err,
				)

				targetStatus.Error = err.Error()
				unready = true
				continue
			}
			setTargetStatus(targetStatus, targetObj)
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
	if err := patcher.Patch(ctx, &pipeline, withFieldOwner); err != nil {
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

	firstEnvStatus, ok := pipeline.Status.Environments[firstEnv.Name]
	if !ok {
		return ctrl.Result{}, fmt.Errorf("did not find status for environment listed first %q", firstEnv.Name)
	}
	latestRevision := checkAllTargetsHaveSameRevision(firstEnvStatus)
	if latestRevision == "" {
		// not all targets have the same revision, or have no revision set, so we can't proceed
		setPendingCondition(&pipeline, v1alpha1.EnvironmentNotReadyReason, "Waiting for all targets to have the same revision")
		if err := patcher.Patch(ctx, &pipeline, withFieldOwner); err != nil {
			return ctrl.Result{Requeue: true}, fmt.Errorf("error setting pending condition: %w", err)
		}

		return ctrl.Result{}, nil
	}

	if !checkAllTargetsAreReady(firstEnvStatus) {
		// not all targets are ready, so we can't proceed
		setPendingCondition(&pipeline, v1alpha1.EnvironmentNotReadyReason, "Waiting for all targets to be ready")
		if err := patcher.Patch(ctx, &pipeline, withFieldOwner); err != nil {
			return ctrl.Result{}, fmt.Errorf("error setting pending condition: %w", err)
		}

		return ctrl.Result{}, nil
	}

	if removePendingCondition(&pipeline) {
		if err := patcher.Patch(ctx, &pipeline, withFieldOwner); err != nil {
			return ctrl.Result{}, fmt.Errorf("error removing pending condition: %w", err)
		}
	}

	for _, env := range pipeline.Spec.Environments[1:] {
		envStatus, ok := pipeline.Status.Environments[env.Name]
		if !ok {
			return ctrl.Result{}, fmt.Errorf("environment in spec %q does not have a calculated status", env.Name)
		}

		// if all targets run the latest revision and are ready, we can skip this environment
		if checkAllTargetsRunRevision(envStatus, latestRevision) && checkAllTargetsAreReady(pipeline.Status.Environments[env.Name]) {
			continue
		}

		// otherwise: if there's a promotion recorded, we can stop here.
		if envStatus.Promotion != nil && envStatus.Promotion.Revision == latestRevision {
			logger.Info("promotion already recorded", "env", env.Name, "revision", latestRevision)
			break
		}

		// other-otherwise: attempt a promotion
		promoteErr := r.promoteLatestRevision(ctx, pipeline, env, latestRevision)
		logger.Info("promoting env", "env", env.Name, "revision", latestRevision)
		err := setPromotionStatus(&pipeline, env.Name, latestRevision, promoteErr)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error recording promotion status: %w", err)
		}

		break
	}
	if err := patcher.Patch(ctx, &pipeline, withFieldOwner); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func setPromotionStatus(pipeline *v1alpha1.Pipeline, env, revision string, promErr error) error {
	envStatus, ok := pipeline.Status.Environments[env]
	if !ok {
		return fmt.Errorf("environment %q not found in status", env)
	}
	var prom v1alpha1.PromotionStatus
	prom.Revision = revision
	prom.Succeeded = (promErr == nil)
	prom.LastAttemptedTime = metav1.Now()
	envStatus.Promotion = &prom
	pipeline.Status.Environments[env] = envStatus
	return nil
}

func setPendingCondition(pipeline *v1alpha1.Pipeline, reason, message string) {
	condition := metav1.Condition{
		Type:    conditions.PromotionPendingCondition,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	}
	apimeta.SetStatusCondition(&pipeline.Status.Conditions, condition)
}

func removePendingCondition(pipeline *v1alpha1.Pipeline) bool {
	ok := apimeta.FindStatusCondition(pipeline.Status.Conditions, conditions.PromotionPendingCondition) != nil
	if ok {
		apimeta.RemoveStatusCondition(&pipeline.Status.Conditions, conditions.PromotionPendingCondition)
	}
	return ok
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

// clusterPrefix returns a string naming the cluster containing an app, to prepend to the usual namespace/name format of the app object itself.
// So that it can be empty, the separator is include in the return value.
func clusterPrefix(ref *v1alpha1.CrossNamespaceClusterReference) string {
	if ref == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s:", ref.Namespace, ref.Name)
}

// targetObject returns a target object for a target spec, ready to be queried. The Pipeline is passed in as well as the Target,
// since the definition can be spread between these specs. This is coupled with setTargetStatus because the concrete types returned here
// must be handled by setTargetStatus.
func targetObject(pipeline *v1alpha1.Pipeline, target *v1alpha1.Target) (client.Object, error) {
	var obj unstructured.Unstructured
	gv, err := schema.ParseGroupVersion(pipeline.Spec.AppRef.APIVersion)
	if err != nil {
		return nil, err
	}
	gvk := gv.WithKind(pipeline.Spec.AppRef.Kind)
	obj.GetObjectKind().SetGroupVersionKind(gvk)
	obj.SetName(pipeline.Spec.AppRef.Name)
	obj.SetNamespace(target.Namespace)
	return &obj, nil
}

// setTargetStatus gets the relevant status from the app object given, and records it in the TargetStatus.
func setTargetStatus(status *v1alpha1.TargetStatus, targetObject client.Object) {
	switch obj := targetObject.(type) {
	case *unstructured.Unstructured:
		// this assumes it's a Flux-like object; specifically with
		//   - a Ready condition
		//   - a .status.lastAppliedRevision
		conds, ok, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
		if !ok || err != nil {
			status.Ready = false
			return
		}
		status.Ready = conditions.IsReadyUnstructured(conds)

		lastAppliedRev, ok, err := unstructured.NestedString(obj.Object, "status", "lastAppliedRevision")
		if !ok || err != nil {
			// It's not an error to lack a Ready condition (new objects will lack any conditions), and it's not an error to lack a lastAppliedRevision
			// (maybe it hasn't got that far yet); but it is an error to have a ready condition of true and lack a lastAppliedRevision, since that means
			// the object is not a usable target.
			if status.Ready {
				status.Error = "unable to find .status.lastAppliedRevision in ready target object"
				status.Ready = false
				return
			}
		} else {
			status.Revision = lastAppliedRev
		}
	default:
		status.Error = "unable to determine ready status for object"
		status.Ready = false
	}
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

// == Setup of indices and static watchers ==

// SetupWithManager sets up the controller with the Manager.
func (r *PipelineReconciler) SetupWithManager(mgr ctrl.Manager) error {

	// let the `caches` object set up its own indexing etc.
	if err := r.caches.setupWithManager(mgr); err != nil {
		return nil
	}

	const (
		gitopsClusterIndexKey string = ".spec.environment.ClusterRef" // this is arbitrary, but let's make it suggest what it's indexing.
	)
	// Index the Pipelines by the GitopsCluster references they (may) point at.
	if err := mgr.GetCache().IndexField(context.TODO(), &v1alpha1.Pipeline{}, gitopsClusterIndexKey, r.indexClusterKind("GitopsCluster")); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	if r.recorder == nil {
		r.recorder = mgr.GetEventRecorderFor(r.ControllerName)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Pipeline{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&clusterctrlv1alpha1.GitopsCluster{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForCluster(gitopsClusterIndexKey)),
		).
		WatchesRawSource(&source.Channel{Source: r.appEvents}, &handler.EnqueueRequestForObject{}).
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
