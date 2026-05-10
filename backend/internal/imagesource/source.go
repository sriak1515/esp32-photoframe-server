// Package imagesource defines the contract for synthetic image sources —
// things that produce a fresh image.Image on demand (AI generation, fractal
// zoom, DLA growth, …) rather than pulling from the photo library.
//
// A Source declares which source identifier(s) it handles via Names(), and
// is invoked through Fetch with a Request that carries the device, the
// chosen source name, and the target dimensions (already adjusted for the
// device's display orientation). It returns a Response carrying the rendered
// image plus a flag telling the handler whether the image should bypass
// post-processing (overlay rendering and epaper-image-convert dithering).
//
// DB-backed sources (gallery, immich, synology, …) intentionally stay
// outside this abstraction — their request shape is more involved (collage
// composition, exclude-ID lists, history tracking) and they live in the
// handler.
package imagesource

import (
	"fmt"
	"image"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
)

// Request carries everything a source needs to render one frame for a
// specific device. Width / Height are logical (oriented) dimensions — the
// dimensions the source should render at. NativeWidth / NativeHeight are
// the panel's physical layout; the handler uses them to decide whether the
// returned image needs rotating before being shipped (only relevant for
// bypass sources, since the post-processing pipeline handles rotation
// itself).
//
// ExcludeIDs is the device's recent-history list. DB-backed sources use it
// to avoid serving the same photo twice in a row; synthetic sources ignore
// it.
type Request struct {
	Device       *model.Device
	Source       string
	Width        int
	Height       int
	NativeWidth  int
	NativeHeight int
	Orientation  string
	ExcludeIDs   []uint
}

// Response is what a Source produces.
type Response struct {
	// Image is the rendered frame at Request.Width × Request.Height.
	Image image.Image

	// SkipPostProcessing tells the handler to bypass overlay rendering and
	// epaper-image-convert. The handler will encode the image as PNG and
	// send it directly. Used by sources that already produce panel-clean
	// pixels (palette-direct generators, image-processing demos, …).
	SkipPostProcessing bool

	// ImageIDs are the model.Image IDs that contributed to this frame, used
	// by the handler to write device-history rows. Empty for synthetic
	// sources that don't store images in the DB.
	ImageIDs []uint

	// PhotoTakenAt is the original capture time of the served photo,
	// surfaced so the handler can render the photo-date overlay. Nil for
	// sources where it doesn't apply.
	PhotoTakenAt *time.Time
}

// Source is the plugin interface every image source implements. Each Source
// owns exactly one source name — the registry is intentionally flat, so the
// full set of available sources is just `len(registry.sources)`.
type Source interface {
	// Name returns the source identifier this plugin handles.
	Name() string

	// Fetch produces one image for the given request.
	Fetch(req *Request) (*Response, error)
}

// Registry is a name → Source lookup table the image handler consults.
type Registry struct {
	sources map[string]Source
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{sources: map[string]Source{}}
}

// Register adds a source. If a name is already registered, it is overwritten.
func (r *Registry) Register(s Source) {
	r.sources[s.Name()] = s
}

// Has reports whether the given source name is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.sources[name]
	return ok
}

// Names returns every registered source name, in unspecified order.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.sources))
	for k := range r.sources {
		out = append(out, k)
	}
	return out
}

// Fetch dispatches to the registered Source for `name`.
func (r *Registry) Fetch(name string, req *Request) (*Response, error) {
	s, ok := r.sources[name]
	if !ok {
		return nil, fmt.Errorf("imagesource: no source registered for %q", name)
	}
	return s.Fetch(req)
}
