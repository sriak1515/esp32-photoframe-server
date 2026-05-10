// Package generative defines the contract for procedural image sources
// (fractals, DLA, etc.) that produce a new image per call and persist a small
// chunk of state between calls so each call advances the visual sequence.
package generative

import "image"

// Generator produces successive frames of a procedural sequence. State is
// opaque to callers — the service layer just persists whatever bytes are
// returned and hands them back on the next call.
type Generator interface {
	// Name returns the stable identifier used in device settings (e.g.
	// "fractal", "dla").
	Name() string

	// Generate renders one image at the given dimensions, given the previous
	// state. On first call (or when state is nil/empty) the implementation
	// initializes from a fresh seed. The returned []byte is the new state to
	// persist for the next call.
	Generate(width, height int, state []byte) (image.Image, []byte, error)
}
