package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// quadrants are unmistakable, which the real phone photo's four corners are not:
// they are all the same wall colour, so only a synthetic fixture can tell the
// eight orientations apart (spike S12).
var quadrants = []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {255, 255, 0, 255}}

func quadrantImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := 0
			switch {
			case y >= h/2 && x >= w/2:
				i = 3
			case y >= h/2:
				i = 2
			case x >= w/2:
				i = 1
			}
			img.SetRGBA(x, y, quadrants[i])
		}
	}
	return img
}

func jpegOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 98}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// injectOrientation writes a minimal APP1 EXIF segment carrying only tag 0x0112.
func injectOrientation(t *testing.T, jpegBytes []byte, o int) []byte {
	t.Helper()
	var tiff bytes.Buffer
	tiff.WriteString("MM")
	binary.Write(&tiff, binary.BigEndian, uint16(42))
	binary.Write(&tiff, binary.BigEndian, uint32(8))
	binary.Write(&tiff, binary.BigEndian, uint16(1))
	binary.Write(&tiff, binary.BigEndian, uint16(0x0112))
	binary.Write(&tiff, binary.BigEndian, uint16(3))
	binary.Write(&tiff, binary.BigEndian, uint32(1))
	binary.Write(&tiff, binary.BigEndian, uint16(o))
	binary.Write(&tiff, binary.BigEndian, uint16(0))
	binary.Write(&tiff, binary.BigEndian, uint32(0))

	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
	out := append([]byte{}, jpegBytes[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, jpegBytes[2:]...)
}

// TestEveryOrientationLandsTheRightQuadrant is the decisive orientation test:
// each of the eight values must move every quadrant to the corner EXIF says,
// and 5..8 must exchange the dimensions.
func TestEveryOrientationLandsTheRightQuadrant(t *testing.T) {
	source := jpegOf(t, quadrantImage(400, 300))
	want := map[int]map[string]int{
		1: {"tl": 0, "tr": 1, "bl": 2, "br": 3},
		2: {"tr": 0, "tl": 1, "br": 2, "bl": 3},
		3: {"br": 0, "bl": 1, "tr": 2, "tl": 3},
		4: {"bl": 0, "br": 1, "tl": 2, "tr": 3},
		5: {"tl": 0, "bl": 1, "tr": 2, "br": 3},
		6: {"tr": 0, "br": 1, "tl": 2, "bl": 3},
		7: {"br": 0, "tr": 1, "bl": 2, "tl": 3},
		8: {"bl": 0, "tl": 1, "br": 2, "tr": 3},
	}
	for o := 1; o <= 8; o++ {
		body := injectOrientation(t, source, o)
		if got := Orientation(body); got != o {
			t.Fatalf("orientation %d read back as %d", o, got)
		}
		thumb, err := Thumbnail(bytes.NewReader(body), 0)
		if err != nil {
			t.Fatalf("orientation %d: %v", o, err)
		}
		if thumb.Orientation != o {
			t.Errorf("orientation %d: Thumb.Orientation = %d", o, thumb.Orientation)
		}
		// 400x300 is already under the long edge, so nothing is scaled; only the
		// rotation can change the dimensions.
		if o >= 5 && !(thumb.Width == 300 && thumb.Height == 400) {
			t.Errorf("orientation %d: thumbnail %dx%d, want 300x400", o, thumb.Width, thumb.Height)
		}
		if o < 5 && !(thumb.Width == 400 && thumb.Height == 300) {
			t.Errorf("orientation %d: thumbnail %dx%d, want 400x300", o, thumb.Width, thumb.Height)
		}

		// Decode the thumbnail we actually produced and read the corners.
		img, err := jpeg.Decode(bytes.NewReader(thumb.JPEG))
		if err != nil {
			t.Fatalf("orientation %d: decoding the thumbnail: %v", o, err)
		}
		b := img.Bounds()
		found := map[string]int{
			"tl": classOf(img.At(b.Min.X+20, b.Min.Y+20)),
			"tr": classOf(img.At(b.Max.X-20, b.Min.Y+20)),
			"bl": classOf(img.At(b.Min.X+20, b.Max.Y-20)),
			"br": classOf(img.At(b.Max.X-20, b.Max.Y-20)),
		}
		for corner, index := range want[o] {
			if found[corner] != index {
				t.Errorf("orientation %d: %s holds quadrant %d, want %d (all: %v)",
					o, corner, found[corner], index, found)
			}
		}
	}
}

func classOf(c color.Color) int {
	r, g, _, _ := c.RGBA()
	switch {
	case r > 0x8000 && g > 0x8000:
		return 3 // yellow
	case r > 0x8000:
		return 0 // red
	case g > 0x8000:
		return 1 // green
	default:
		return 2 // blue
	}
}

// A photo bigger than the long edge is scaled down and keeps its aspect ratio.
func TestThumbnailScalesToTheLongEdge(t *testing.T) {
	body := jpegOf(t, quadrantImage(3200, 2400))
	thumb, err := Thumbnail(bytes.NewReader(body), 0)
	if err != nil {
		t.Fatal(err)
	}
	if thumb.Width != ThumbLongEdge || thumb.Height != ThumbLongEdge*2400/3200 {
		t.Errorf("thumbnail = %dx%d, want %dx%d", thumb.Width, thumb.Height, ThumbLongEdge, ThumbLongEdge*2400/3200)
	}
	if thumb.SrcWidth != 3200 || thumb.SrcHeight != 2400 {
		t.Errorf("source recorded as %dx%d", thumb.SrcWidth, thumb.SrcHeight)
	}
}

// A portrait photo is fitted by its height, not its width.
func TestThumbnailFitsTheLongEdgeEitherWayUp(t *testing.T) {
	body := jpegOf(t, quadrantImage(1200, 3000))
	thumb, err := Thumbnail(bytes.NewReader(body), 0)
	if err != nil {
		t.Fatal(err)
	}
	if thumb.Height != ThumbLongEdge || thumb.Width != ThumbLongEdge*1200/3000 {
		t.Errorf("thumbnail = %dx%d, want %dx%d", thumb.Width, thumb.Height, ThumbLongEdge*1200/3000, ThumbLongEdge)
	}
}

// An image already smaller than the long edge is not upscaled: a 300 px icon
// stays a 300 px icon rather than being blown up and blurred.
func TestSmallImagesAreNotUpscaled(t *testing.T) {
	body := jpegOf(t, quadrantImage(300, 200))
	thumb, err := Thumbnail(bytes.NewReader(body), 0)
	if err != nil {
		t.Fatal(err)
	}
	if thumb.Width != 300 || thumb.Height != 200 {
		t.Errorf("thumbnail = %dx%d, want 300x200 untouched", thumb.Width, thumb.Height)
	}
}

// The pixel cap is checked from DecodeConfig, before a decode allocates
// anything (spike S12: 1.6 MB of RSS per megapixel, so the check is the guard).
func TestThePixelCapRefusesBeforeDecoding(t *testing.T) {
	// A deliberately tiny cap, because a real 50 MP fixture would itself cost
	// 240 MB to build. The real number is asserted separately, below.
	if err := checkPixels(400, 300, 1000); !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("checkPixels(400x300, cap 1000) = %v, want ErrTooManyPixels", err)
	}
	if err := checkPixels(100, 100, 10_000); err != nil {
		t.Errorf("checkPixels(exactly the cap) = %v, want nil", err)
	}
	// The number S12 chose, because 8064x6048 = 48.77 MP is an iPhone's 48 MP
	// photo and must be inside the cap.
	if err := checkPixels(8064, 6048, MaxPixels); err != nil {
		t.Errorf("an iPhone's 48 MP photo is over the cap: %v", err)
	}
	// A 108 MP phone photo is outside it: stored, badged, not decoded.
	if err := checkPixels(12000, 9000, MaxPixels); !errors.Is(err, ErrTooManyPixels) {
		t.Errorf("a 108 MP photo passed the cap: %v", err)
	}
}

// A body that is not an image is an error the caller stores around, never a
// panic and never a silent empty thumbnail.
func TestNotAnImageIsAnError(t *testing.T) {
	for name, body := range map[string][]byte{
		"empty":     {},
		"html":      []byte("<html>not an image</html>"),
		"truncated": []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10},
	} {
		if _, err := Thumbnail(bytes.NewReader(body), 0); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := Thumbnail(nil, 0); err == nil {
		t.Error("a nil reader must be an error")
	}
}

// PNG is decoded and re-encoded as JPEG, which is what the pipeline stores: one
// thumbnail format regardless of what was uploaded.
func TestPNGBecomesJPEG(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, quadrantImage(400, 300)); err != nil {
		t.Fatal(err)
	}
	thumb, err := Thumbnail(bytes.NewReader(buf.Bytes()), 0)
	if err != nil {
		t.Fatalf("Thumbnail(png): %v", err)
	}
	if _, format, err := image.Decode(bytes.NewReader(thumb.JPEG)); err != nil || format != "jpeg" {
		t.Errorf("thumbnail format = %q, %v; want jpeg", format, err)
	}
	// A PNG has no EXIF, so no orientation is applied and nothing is rotated.
	if thumb.Orientation != 1 {
		t.Errorf("orientation = %d for a PNG, want 1", thumb.Orientation)
	}
}

// EXIF parsing is bounds-checked: this input is whatever a stranger's server
// sent, and a panicking thumbnail would take the worker with it.
func FuzzOrientation(f *testing.F) {
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x10, 'E', 'x', 'i', 'f', 0, 0, 'M', 'M', 0, 42})
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x01})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, body []byte) {
		o := Orientation(body)
		if o < 1 || o > 8 {
			t.Fatalf("Orientation returned %d, which is not an EXIF value", o)
		}
	})
}
