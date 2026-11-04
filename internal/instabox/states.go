package instabox

// OperationState is extensible; unknown upstream states remain unchanged.
type OperationState string

const (
	// OperationStateQueued means the operation is waiting to run.
	OperationStateQueued OperationState = "queued"
	// OperationStateRunning means the operation is in progress.
	OperationStateRunning OperationState = "running"
	// OperationStateSucceeded means the operation completed successfully.
	OperationStateSucceeded OperationState = "succeeded"
	// OperationStateFailed means the operation failed.
	OperationStateFailed OperationState = "failed"
)
