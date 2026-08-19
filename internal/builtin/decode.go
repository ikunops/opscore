package builtin

import (
	"encoding/json"
	"fmt"

	"github.com/YuDong999/opscore/internal/core"
)

// DecodeInput converts the loosely-typed operation input (arriving as a JSON
// object from HTTP or as string flags from the CLI) into a strongly-typed
// request struct owned by the handler. This is the "Runtime Decode" step from
// Round6: the kernel still receives a struct, never a raw map, and a malformed
// payload fails fast with ErrInvalidInput before any plan is built.
func DecodeInput[T any](input map[string]any) (T, error) {
	var req T
	b, err := json.Marshal(input)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%w: %v", core.ErrInvalidInput, err)
	}
	if err := json.Unmarshal(b, &req); err != nil {
		var zero T
		return zero, fmt.Errorf("%w: %v", core.ErrInvalidInput, err)
	}
	return req, nil
}
