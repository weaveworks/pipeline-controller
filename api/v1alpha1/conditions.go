package v1alpha1

// Reasons are provided as utility, and are not part of the declarative API.

// Reasons used by the original controller.
const (
	// TargetClusterNotFoundReason signals a failure to locate a cluster resource on the management cluster.
	TargetClusterNotFoundReason string = "TargetClusterNotFound"
	// TargetClusterNotReadyReason signals that a cluster pointed to by a Pipeline is not ready.
	TargetClusterNotReadyReason string = "TargetClusterNotReady"
	// ReconciliationSucceededReason signals that a Pipeline has been successfully reconciled.
	ReconciliationSucceededReason string = "ReconciliationSucceeded"
	// EnvironmentNotReadyReason signals the environment is not ready.
	EnvironmentNotReadyReason string = "EnvironmentNotReady"
)

// Reasons used by the level-triggered controller.
const (
	// TargetNotReadableReason signals that an app object pointed to by a Pipeline cannot be read, either because it is not found, or it's on a cluster that cannot be reached.
	TargetNotReadableReason string = "TargetNotReadable"
)
