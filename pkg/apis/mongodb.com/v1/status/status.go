package status

type Writer interface {
	// UpdateStatus updates the status of the object
	UpdateStatus(phase Phase, statusOptions ...Option)

	// SetWarnings sets the warnings for the object
	SetWarnings([]Warning)
}
type Reader interface {
	// GetStatus returns the status of the object
	GetStatus() interface{}
}
