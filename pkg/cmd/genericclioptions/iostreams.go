// Package genericclioptions provides shared CLI I/O stream plumbing for the
// openshift-acme command-line tools.
package genericclioptions

import (
	"io"
)

// IOStreams is a structure containing all standard streams.
type IOStreams struct {
	// In think, os.Stdin
	In io.Reader
	// Out think, os.Stdout
	Out io.Writer
	// ErrOut think, os.Stderr
	ErrOut io.Writer
}
