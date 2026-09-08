//go:build !linux && !darwin && !netbsd

package cliprovider

import (
	"fmt"
	"os"
)

// openFileNoFollow fails closed on platforms where this package has no atomic,
// component-by-component no-follow traversal. In particular, maintained Go
// Windows APIs do not currently provide the required safe reparse-point walk;
// os.Root follows in-root reparse points and therefore cannot enforce this
// cross-request isolation property.
func openFileNoFollow(root, name string, beforeOpen func(string)) (*os.File, error) {
	return nil, fmt.Errorf("secure managed media opening is unsupported on this platform")
}
