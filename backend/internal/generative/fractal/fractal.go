// Package fractal generates a deep zoom into the Mandelbrot set, one frame
// per Generate call. State carries the current zoom / center / frame counter
// so successive calls walk further into the fractal.
//
// Ported from the standalone fractalgen CLI. The original rendered a 2-color
// BMP (foreground/background, default white-on-black); here we emit a smooth
// grayscale RGBA so the Spectra 6 dithering pipeline can render the
// Mandelbrot's escape-time gradient as black/white shading on the panel.
package fractal

import (
	"encoding/json"
	"image"
	"image/color"
	"math"
)

const (
	initCX      = -0.743643887037158
	initCY      = 0.131825904205311
	initZoom    = 100.0
	zoomStep    = 1.25
	maxZoom     = 1e11
	minVariance = 0.002
	minGradient = 0.010
)

type state struct {
	CX    float64 `json:"cx"`
	CY    float64 `json:"cy"`
	Zoom  float64 `json:"zoom"`
	Frame int     `json:"frame"`
}

func initialState() state {
	return state{CX: initCX, CY: initCY, Zoom: initZoom}
}

func loadState(b []byte) state {
	if len(b) == 0 {
		return initialState()
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil || s.Zoom == 0 {
		return initialState()
	}
	return s
}

// Generator implements the generative.Generator interface.
type Generator struct{}

func (Generator) Name() string { return "fractal" }

func (g Generator) Generate(width, height int, stateBytes []byte) (image.Image, []byte, error) {
	return Generate(width, height, stateBytes)
}

// Generate renders one Mandelbrot frame and advances state by one zoom step.
// When the visible structure is exhausted (low variance/gradient) or the zoom
// limit is reached, state silently resets to the initial center and zoom; the
// returned image is always valid.
func Generate(width, height int, stateBytes []byte) (image.Image, []byte, error) {
	s := loadState(stateBytes)

	img, variance, gradient := renderFrame(s, width, height)

	if s.Zoom > maxZoom || variance < minVariance || gradient < minGradient {
		s = initialState()
		img, _, _ = renderFrame(s, width, height)
	}

	s.Zoom *= zoomStep
	s.Frame++
	nb, err := json.Marshal(s)
	if err != nil {
		return nil, nil, err
	}
	return img, nb, nil
}

func renderFrame(s state, width, height int) (img *image.RGBA, variance, gradient float64) {
	maxIter := int(math.Min(4096, math.Max(256,
		256+math.Pow(math.Log10(s.Zoom), 1.6)*120)))

	n := width * height
	nu := make([]float64, n)
	scale := 4.0 / (float64(width) * s.Zoom)
	x0 := s.CX - float64(width)*0.5*scale
	y0 := s.CY - float64(height)*0.5*scale

	var sum, sumSq, maxNu float64
	for y := 0; y < height; y++ {
		ci := y0 + float64(y)*scale
		for x := 0; x < width; x++ {
			cr := x0 + float64(x)*scale
			zr, zi := 0.0, 0.0
			zr2, zi2 := 0.0, 0.0
			iter := 0
			for zr2+zi2 < 16.0 && iter < maxIter {
				zi = 2*zr*zi + ci
				zr = zr2 - zi2 + cr
				zr2 = zr * zr
				zi2 = zi * zi
				iter++
			}
			v := 0.0
			if iter < maxIter {
				v = float64(iter) + 1.0 - math.Log2(math.Log(math.Sqrt(zr2+zi2)))
			}
			i := y*width + x
			nu[i] = v
			sum += v
			sumSq += v * v
			if v > maxNu {
				maxNu = v
			}
		}
	}

	mean := sum / float64(n)
	variance = sumSq/float64(n) - mean*mean

	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			i := y*width + x
			dx := nu[i+1] - nu[i-1]
			dy := nu[i+width] - nu[i-width]
			gradient += math.Hypot(dx, dy)
		}
	}
	gradient /= float64(n)

	img = image.NewRGBA(image.Rect(0, 0, width, height))
	if maxNu == 0 {
		// Fully inside the set — solid black is fine; the dithering
		// pipeline will leave it at the panel's measured black.
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i+3] = 255
		}
		return
	}

	// Smooth grayscale, gamma-compressed so escape-time bands have visual
	// depth instead of all bunching near the dark end.
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			i := y*width + x
			t := math.Sqrt(nu[i] / maxNu)
			g := uint8(math.Min(255, t*255))
			img.SetRGBA(x, y, color.RGBA{R: g, G: g, B: g, A: 255})
		}
	}
	return
}
