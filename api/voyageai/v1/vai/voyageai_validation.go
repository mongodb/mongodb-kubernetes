package vai

import (
	"fmt"
)

// Validate performs basic validation of the VoyageAI resource.
func (v *VoyageAI) Validate() error {
	if v.Spec.Model == "" {
		return fmt.Errorf("spec.model must be set")
	}
	return nil
}
