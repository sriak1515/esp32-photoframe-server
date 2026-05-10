package fractal

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func decodeState(t *testing.T, b []byte) state {
	t.Helper()
	var s state
	assert.NoError(t, json.Unmarshal(b, &s))
	return s
}

func TestGenerate_FromEmptyState(t *testing.T) {
	img, st, err := Generate(120, 80, nil)
	assert.NoError(t, err)
	assert.NotNil(t, img)
	assert.Equal(t, 120, img.Bounds().Dx())
	assert.Equal(t, 80, img.Bounds().Dy())
	assert.NotEmpty(t, st)

	s := decodeState(t, st)
	assert.Equal(t, initCX, s.CX)
	assert.Equal(t, initCY, s.CY)
	// First call advances zoom by one step.
	assert.InDelta(t, initZoom*zoomStep, s.Zoom, 1e-6)
	assert.Equal(t, 1, s.Frame)
}

func TestGenerate_AdvancesAcrossCalls(t *testing.T) {
	_, s1, err := Generate(80, 60, nil)
	assert.NoError(t, err)
	_, s2, err := Generate(80, 60, s1)
	assert.NoError(t, err)

	a := decodeState(t, s1)
	b := decodeState(t, s2)
	assert.Greater(t, b.Zoom, a.Zoom)
	assert.Equal(t, a.Frame+1, b.Frame)
}

func TestGenerate_ResetsPastMaxZoom(t *testing.T) {
	// Hand-crafted state already past the zoom ceiling.
	st, err := json.Marshal(state{CX: 0, CY: 0, Zoom: maxZoom * 10, Frame: 9999})
	assert.NoError(t, err)

	_, out, err := Generate(60, 60, st)
	assert.NoError(t, err)

	got := decodeState(t, out)
	// After reset the next call advances by one step from the initial seed.
	assert.InDelta(t, initZoom*zoomStep, got.Zoom, 1e-6)
	assert.Equal(t, 1, got.Frame)
}

func TestGenerate_BadStateRecovers(t *testing.T) {
	_, st, err := Generate(40, 40, []byte("not-json"))
	assert.NoError(t, err)
	got := decodeState(t, st)
	assert.InDelta(t, initZoom*zoomStep, got.Zoom, 1e-6)
}

func TestGenerate_OutputIsGrayscale(t *testing.T) {
	// We render escape-time as smooth grayscale and let the dithering
	// pipeline pick panel shades. Each pixel's RGB channels should match.
	img, _, err := Generate(120, 80, nil)
	assert.NoError(t, err)
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bch, _ := img.At(x, y).RGBA()
			if r != g || g != bch {
				t.Fatalf("pixel (%d,%d) is not grayscale: R=%d G=%d B=%d", x, y, r, g, bch)
			}
		}
	}
}
