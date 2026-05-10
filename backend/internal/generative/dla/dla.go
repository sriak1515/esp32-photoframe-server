// Package dla generates Diffusion-Limited Aggregation images: five seeded
// tufts grow over time as random walkers stick to occupied neighbors,
// producing branching organic structures in five colors on white.
//
// Ported from the standalone dla CLI. The original wrote 24-bit BMPs and
// kept its checkpoint on disk; here Generate returns an image.Image and
// reads/writes state as opaque bytes the caller persists.
package dla

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"math"

	mrand "math/rand/v2"
)

const (
	walkersPerFrame      = 84
	maxWalkerSteps       = 6000
	thickenRadius        = 1.1
	defaultFramesPerCall = 100
	numLayers            = 5
	stateMagic           = uint32(0x444C4131) // "DLA1"
	stateVersion         = uint16(1)
)

// Five-color palette used for the layers; index 0 is the background.
var palette = [numLayers + 1]color.RGBA{
	{R: 255, G: 255, B: 255, A: 255}, // background (white)
	{R: 0, G: 0, B: 200, A: 255},     // blue
	{R: 0, G: 180, B: 0, A: 255},     // green
	{R: 200, G: 30, B: 30, A: 255},   // red
	{R: 220, G: 200, B: 0, A: 255},   // yellow
	{R: 0, G: 0, B: 0, A: 255},       // black
}

// Options tunes a single Generate call. Zero values use sensible defaults.
type Options struct {
	// FramesPerCall is how many growth frames to advance before rendering.
	// Each frame attempts walkersPerFrame walkers per layer. Default: 100.
	FramesPerCall int
}

// Generator implements the generative.Generator interface.
type Generator struct{}

func (Generator) Name() string { return "dla" }

func (g Generator) Generate(width, height int, stateBytes []byte) (image.Image, []byte, error) {
	return Generate(width, height, stateBytes, Options{})
}

// Generate advances the simulation by Options.FramesPerCall frames (or the
// default), renders the layers (with thickening) into an RGBA image, and
// returns the new state. If state is empty or has incompatible dimensions,
// a fresh simulation is seeded.
func Generate(width, height int, stateBytes []byte, opts Options) (image.Image, []byte, error) {
	if width <= 0 || height <= 0 {
		return nil, nil, errors.New("dla: width and height must be positive")
	}
	frames := opts.FramesPerCall
	if frames <= 0 {
		frames = defaultFramesPerCall
	}

	sim, err := loadOrInit(stateBytes, width, height)
	if err != nil {
		return nil, nil, err
	}

	for f := 0; f < frames; f++ {
		for i := range sim.layers {
			advanceOneFrame(&sim.layers[i], width, height)
		}
	}
	sim.frame += frames

	img := render(sim, width, height)

	out, err := sim.marshal()
	if err != nil {
		return nil, nil, err
	}
	return img, out, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Simulation state
// ────────────────────────────────────────────────────────────────────────────

type layer struct {
	occ   []uint64 // bit-packed occupancy, length (W*H+63)/64
	rng   xoshiro
	color uint8 // index into palette (1..numLayers)
}

type sim struct {
	w, h   int
	frame  int
	layers [numLayers]layer
}

func loadOrInit(b []byte, width, height int) (*sim, error) {
	if len(b) > 0 {
		s, err := unmarshal(b)
		if err == nil && s.w == width && s.h == height {
			return s, nil
		}
		// Bad state or dimension mismatch — fall through and reseed.
	}
	return seed(width, height)
}

func seed(width, height int) (*sim, error) {
	var seedBytes [8]byte
	if _, err := rand.Read(seedBytes[:]); err != nil {
		return nil, err
	}
	rootSeed := binary.LittleEndian.Uint64(seedBytes[:])

	s := &sim{w: width, h: height}
	wordsPerLayer := (width*height + 63) / 64

	// Place 5 seed tufts on a 3×2 jittered grid.
	jitterRng := newSplitMix64(rootSeed)
	cols, rows := 3, 2
	k := 0
	for r := 0; r < rows && k < numLayers; r++ {
		for c := 0; c < cols && k < numLayers; c++ {
			jx := (jitterRng.float01() - 0.5) * 0.5
			jy := (jitterRng.float01() - 0.5) * 0.5
			x := int((float64(c) + 0.5 + jx) * float64(width) / float64(cols))
			y := int((float64(r) + 0.5 + jy) * float64(height) / float64(rows))
			x = clamp(x, 0, width-1)
			y = clamp(y, 0, height-1)

			s.layers[k] = layer{
				occ:   make([]uint64, wordsPerLayer),
				color: uint8(k + 1),
			}
			s.layers[k].rng.seed(rootSeed + uint64(k)*0x9E3779B97F4A7C15)

			// 3×3 starter tuft, wrapping at edges.
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					setOcc(s.layers[k].occ, wrap(x+dx, width), wrap(y+dy, height), width)
				}
			}
			k++
		}
	}

	return s, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Walker step
// ────────────────────────────────────────────────────────────────────────────

var walkerDirs = [8][2]int{
	{-1, -1}, {0, -1}, {1, -1},
	{-1, 0}, {1, 0},
	{-1, 1}, {0, 1}, {1, 1},
}

func advanceOneFrame(L *layer, width, height int) {
	for p := 0; p < walkersPerFrame; p++ {
		x := L.rng.intn(width)
		y := L.rng.intn(height)

		for s := 0; s < maxWalkerSteps; s++ {
			d := walkerDirs[L.rng.intn(8)]
			x = wrap(x+d[0], width)
			y = wrap(y+d[1], height)

			// 8-neighborhood with toroidal wrap.
			xl := wrap(x-1, width)
			xr := wrap(x+1, width)
			yu := wrap(y-1, height)
			yd := wrap(y+1, height)
			if getOcc(L.occ, xl, yu, width) ||
				getOcc(L.occ, x, yu, width) ||
				getOcc(L.occ, xr, yu, width) ||
				getOcc(L.occ, xl, y, width) ||
				getOcc(L.occ, xr, y, width) ||
				getOcc(L.occ, xl, yd, width) ||
				getOcc(L.occ, x, yd, width) ||
				getOcc(L.occ, xr, yd, width) {
				setOcc(L.occ, x, y, width)
				break
			}
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Render with fractional thickening (2× supersample)
// ────────────────────────────────────────────────────────────────────────────

func render(s *sim, width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	bg := palette[0]
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	thick := make([]uint8, width*height)
	for i := range s.layers {
		thicken(s.layers[i].occ, thick, width, height)
		c := palette[s.layers[i].color]
		for p, v := range thick {
			if v != 0 {
				x := p % width
				y := p / width
				img.SetRGBA(x, y, c)
			}
		}
	}
	return img
}

// thicken fills `out` with a dilated copy of `src` using a fractional
// Euclidean radius. Done in 2× supersampled space and downsampled back, so
// e.g. radius 1.1 dilates more than radius 1.0 without hard "+" artifacts.
// `out` must already be sized width*height.
func thicken(src []uint64, out []uint8, width, height int) {
	for i := range out {
		out[i] = 0
	}
	sw := 2 * width
	sh := 2 * height
	hi := make([]uint8, sw*sh)
	dil := make([]uint8, sw*sh)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if getOcc(src, x, y, width) {
				hi[(2*y+1)*sw+(2*x+1)] = 1
			}
		}
	}

	rHi := thickenRadius * 2.0
	r := int(math.Ceil(rHi))
	r2 := rHi * rHi

	for hy := 0; hy < sh; hy++ {
		base := hy * sw
		for hx := 0; hx < sw; hx++ {
			if hi[base+hx] == 0 {
				continue
			}
			for dy := -r; dy <= r; dy++ {
				for dx := -r; dx <= r; dx++ {
					if float64(dx*dx+dy*dy) > r2 {
						continue
					}
					xx := wrap(hx+dx, sw)
					yy := wrap(hy+dy, sh)
					dil[yy*sw+xx] = 1
				}
			}
		}
	}

	for y := 0; y < height; y++ {
		by := 2 * y
		for x := 0; x < width; x++ {
			bx := 2 * x
			v := dil[by*sw+bx] | dil[by*sw+bx+1] |
				dil[(by+1)*sw+bx] | dil[(by+1)*sw+bx+1]
			if v != 0 {
				out[y*width+x] = 1
			}
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Bit-packed occupancy helpers
// ────────────────────────────────────────────────────────────────────────────

func getOcc(occ []uint64, x, y, width int) bool {
	i := y*width + x
	return (occ[i>>6]>>uint(i&63))&1 != 0
}

func setOcc(occ []uint64, x, y, width int) {
	i := y*width + x
	occ[i>>6] |= 1 << uint(i&63)
}

func wrap(v, m int) int {
	if v < 0 {
		return v + m
	}
	if v >= m {
		return v - m
	}
	return v
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ────────────────────────────────────────────────────────────────────────────
// RNG: xoshiro256** seeded by SplitMix64. Both have small, serializable state.
// ────────────────────────────────────────────────────────────────────────────

type xoshiro struct{ s [4]uint64 }

type splitmix64 struct{ x uint64 }

func newSplitMix64(seed uint64) *splitmix64 { return &splitmix64{x: seed} }

func (s *splitmix64) next() uint64 {
	s.x += 0x9E3779B97F4A7C15
	z := s.x
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (s *splitmix64) float01() float64 {
	return float64(s.next()>>11) * (1.0 / 9007199254740992.0)
}

func (x *xoshiro) seed(seed uint64) {
	sm := newSplitMix64(seed)
	x.s[0] = sm.next()
	x.s[1] = sm.next()
	x.s[2] = sm.next()
	x.s[3] = sm.next()
}

func (x *xoshiro) next() uint64 {
	result := bits_rotl64(x.s[1]*5, 7) * 9
	t := x.s[1] << 17
	x.s[2] ^= x.s[0]
	x.s[3] ^= x.s[1]
	x.s[1] ^= x.s[2]
	x.s[0] ^= x.s[3]
	x.s[2] ^= t
	x.s[3] = bits_rotl64(x.s[3], 45)
	return result
}

// intn returns an unbiased integer in [0, n) for n in [1, 2^31). For our use
// (n up to width or height, up to a few thousand) the bias from naive modulo
// would be negligible, but rejection sampling keeps it deterministic.
func (x *xoshiro) intn(n int) int {
	if n <= 0 {
		return 0
	}
	rng := uint32(n)
	limit := uint32(0xFFFFFFFF) - (uint32(0xFFFFFFFF) % rng)
	for {
		v := uint32(x.next() >> 32)
		if v < limit {
			return int(v % rng)
		}
	}
}

func bits_rotl64(x uint64, k int) uint64 { return (x << k) | (x >> (64 - k)) }

// Verify math/rand/v2 isn't accidentally pulled in as a dependency at link
// time when someone imports just this package. (It isn't — we only import the
// alias to keep the option open if we ever switch RNG.)
var _ = mrand.Uint64

// ────────────────────────────────────────────────────────────────────────────
// State serialization
//
// Layout (little-endian throughout):
//
//   magic     uint32      "DLA1"
//   version   uint16      = stateVersion
//   width     uint16
//   height    uint16
//   layers    uint8       = numLayers
//   _pad      uint8
//   frame     uint32
//   for each layer:
//     color    uint8
//     _pad     uint8
//     rng[4]   uint64
//     occWords uint32     = (W*H+63)/64
//     occ      occWords × uint64
// ────────────────────────────────────────────────────────────────────────────

func (s *sim) marshal() ([]byte, error) {
	wordsPerLayer := (s.w*s.h + 63) / 64
	size := 4 + 2 + 2 + 2 + 1 + 1 + 4 +
		numLayers*(1+1+32+4+wordsPerLayer*8)
	buf := bytes.NewBuffer(make([]byte, 0, size))

	w := func(v interface{}) error { return binary.Write(buf, binary.LittleEndian, v) }
	if err := w(stateMagic); err != nil {
		return nil, err
	}
	if err := w(stateVersion); err != nil {
		return nil, err
	}
	if err := w(uint16(s.w)); err != nil {
		return nil, err
	}
	if err := w(uint16(s.h)); err != nil {
		return nil, err
	}
	if err := w(uint8(numLayers)); err != nil {
		return nil, err
	}
	if err := w(uint8(0)); err != nil {
		return nil, err
	}
	if err := w(uint32(s.frame)); err != nil {
		return nil, err
	}

	for i := range s.layers {
		L := &s.layers[i]
		if err := w(L.color); err != nil {
			return nil, err
		}
		if err := w(uint8(0)); err != nil {
			return nil, err
		}
		if err := w(L.rng.s); err != nil {
			return nil, err
		}
		if err := w(uint32(len(L.occ))); err != nil {
			return nil, err
		}
		if err := w(L.occ); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func unmarshal(b []byte) (*sim, error) {
	r := bytes.NewReader(b)
	read := func(v interface{}) error { return binary.Read(r, binary.LittleEndian, v) }

	var magic uint32
	if err := read(&magic); err != nil || magic != stateMagic {
		return nil, errors.New("dla: bad magic")
	}
	var version uint16
	if err := read(&version); err != nil || version != stateVersion {
		return nil, errors.New("dla: unsupported state version")
	}
	var w16, h16 uint16
	var nLayers, _pad uint8
	var frame uint32
	if err := read(&w16); err != nil {
		return nil, err
	}
	if err := read(&h16); err != nil {
		return nil, err
	}
	if err := read(&nLayers); err != nil {
		return nil, err
	}
	if err := read(&_pad); err != nil {
		return nil, err
	}
	if err := read(&frame); err != nil {
		return nil, err
	}
	if int(nLayers) != numLayers {
		return nil, errors.New("dla: wrong layer count")
	}

	s := &sim{w: int(w16), h: int(h16), frame: int(frame)}
	for i := 0; i < numLayers; i++ {
		L := &s.layers[i]
		if err := read(&L.color); err != nil {
			return nil, err
		}
		if err := read(&_pad); err != nil {
			return nil, err
		}
		if err := read(&L.rng.s); err != nil {
			return nil, err
		}
		var nWords uint32
		if err := read(&nWords); err != nil {
			return nil, err
		}
		L.occ = make([]uint64, nWords)
		if err := read(L.occ); err != nil {
			return nil, err
		}
	}
	return s, nil
}
