package imageops

import (
	"fmt"
	"os/exec"
)

// AutoOrient rewrites the file at path so its EXIF orientation is baked
// into the pixel grid (and the orientation tag normalized to 1). After
// this, readers that don't honor EXIF — Go's image.Decode, the
// thumbnail generator, the device renderer — see the photo the right
// way up. Required for iPhone JPEGs, which encode portrait shots as
// landscape pixels plus an Orientation=6 tag.
//
// Requires `magick` (ImageMagick) on PATH; no-op semantics if the file
// already has Orientation=1.
func AutoOrient(path string) error {
	cmd := exec.Command("magick", path, "-auto-orient", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("magick auto-orient %s: %w (output: %s)", path, err, string(output))
	}
	return nil
}
