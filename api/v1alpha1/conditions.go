package v1alpha1

// Reasons are provided as utility, and are not part of the declarative API.
const (
	// TargetClusterNotFoundReason signals a failure to locate a cluster resource on the management cluster.
	TargetClusterNotFoundReason string = "TargetClusterNotFound"
	// TargetClusterNotReadyReason signals that a cluster pointed to by a Pipeline is not ready.
	TargetClusterNotReadyReason string = "TargetClusterNotReady"
	// ReconciliationSucceededReason signals that a Pipeline has been successfully reconciled.
	ReconciliationSucceededReason string = "ReconciliationSucceeded"
)
