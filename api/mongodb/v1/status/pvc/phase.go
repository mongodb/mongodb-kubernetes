package pvc

type Phase string

const (
	PhaseNoAction    Phase = "PVC Resize - NoAction"
	PhasePVCResize   Phase = "PVC Resize - PVC Is Resizing"
	PhaseSTSOrphaned Phase = "PVC Resize - STS has been orphaned"
)
