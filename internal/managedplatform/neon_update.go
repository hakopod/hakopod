package managedplatform

import (
	"fmt"
	"reflect"
)

// ValidateNeonResourceUpdate keeps durable identity and membership fixed while
// a reviewed revision changes component CPU and memory allocations.
func ValidateNeonResourceUpdate(previous, current Spec) error {
	if previous.Kind != "neon" || current.Kind != "neon" || previous.Neon == nil || current.Neon == nil {
		return fmt.Errorf("Neon updates require the complete previous specification")
	}
	previous.Resources, current.Resources = nil, nil
	if !reflect.DeepEqual(previous, current) {
		return fmt.Errorf("Neon updates support CPU and memory changes only; restore into a new platform to change storage or topology")
	}
	return nil
}
