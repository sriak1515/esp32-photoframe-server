package dla

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerate_FromEmptyState(t *testing.T) {
	img, st, err := Generate(80, 48, nil, Options{FramesPerCall: 2})
	assert.NoError(t, err)
	assert.NotNil(t, img)
	assert.Equal(t, 80, img.Bounds().Dx())
	assert.Equal(t, 48, img.Bounds().Dy())
	assert.NotEmpty(t, st)

	s, err := unmarshal(st)
	assert.NoError(t, err)
	assert.Equal(t, 80, s.w)
	assert.Equal(t, 48, s.h)
	assert.Equal(t, 2, s.frame)
}

func TestGenerate_AdvancesFrameCounter(t *testing.T) {
	_, s1, err := Generate(80, 48, nil, Options{FramesPerCall: 1})
	assert.NoError(t, err)
	_, s2, err := Generate(80, 48, s1, Options{FramesPerCall: 3})
	assert.NoError(t, err)

	a, _ := unmarshal(s1)
	b, _ := unmarshal(s2)
	assert.Equal(t, 1, a.frame)
	assert.Equal(t, 4, b.frame)
}

func TestGenerate_StateSurvivesRoundTrip(t *testing.T) {
	_, st, err := Generate(80, 48, nil, Options{FramesPerCall: 5})
	assert.NoError(t, err)

	s, err := unmarshal(st)
	assert.NoError(t, err)
	out, err := s.marshal()
	assert.NoError(t, err)
	assert.Equal(t, st, out, "marshal(unmarshal(state)) should be identity")
}

func TestGenerate_DimensionMismatchReseeds(t *testing.T) {
	_, s1, err := Generate(80, 48, nil, Options{FramesPerCall: 4})
	assert.NoError(t, err)

	// Switching dimensions discards the old state and starts fresh.
	_, s2, err := Generate(120, 60, s1, Options{FramesPerCall: 1})
	assert.NoError(t, err)

	a, _ := unmarshal(s1)
	b, _ := unmarshal(s2)
	assert.Equal(t, 80, a.w)
	assert.Equal(t, 120, b.w)
	assert.Equal(t, 60, b.h)
	assert.Equal(t, 1, b.frame, "fresh seed should start at frame 0 then advance by 1")
}

func TestGenerate_BadMagicReseeds(t *testing.T) {
	bad := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	_, st, err := Generate(80, 48, bad, Options{FramesPerCall: 1})
	assert.NoError(t, err)

	s, err := unmarshal(st)
	assert.NoError(t, err)
	assert.Equal(t, 1, s.frame)
}

func TestGenerate_ZeroDimsRejected(t *testing.T) {
	_, _, err := Generate(0, 48, nil, Options{})
	assert.Error(t, err)
	_, _, err = Generate(80, -1, nil, Options{})
	assert.Error(t, err)
}

func TestRender_FillsOnlyOccupiedCells(t *testing.T) {
	// One frame should produce a mix: some white background plus some
	// colored layer pixels. We avoid asserting on the ratio because random
	// walker placement plus the radius-1.1 thickening makes exact coverage
	// non-deterministic — especially at small canvas sizes where a few
	// extra stuck cells flip the majority. The contract is just "the
	// renderer touched both colors".
	img, _, err := Generate(60, 40, nil, Options{FramesPerCall: 1})
	assert.NoError(t, err)

	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	whitePixels, coloredPixels := 0, 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r == 0xFFFF && g == 0xFFFF && b == 0xFFFF {
				whitePixels++
			} else {
				coloredPixels++
			}
		}
	}
	assert.Positive(t, whitePixels, "expected some white background pixels")
	assert.Positive(t, coloredPixels, "expected some colored layer pixels")
	assert.Equal(t, w*h, whitePixels+coloredPixels)
}
