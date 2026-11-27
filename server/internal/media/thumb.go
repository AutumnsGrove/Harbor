package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"

	// Decoders. JPEG, PNG and GIF are the standard library's; WebP decodes only,
	// and there is no encoder for it (spike S12).
	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// The numbers come from spike S12 (docs/plans/spikes.md), not from taste:
// decoding costs 1.6 MB of resident memory per megapixel on both the potato and
// the Mac, and an iPhone's 48 MP photo is 8064x6048 = 48.77 MP, so a 48 MP cap
// would refuse the exact photo the cap exists for.
const (
	// MaxPixels is the decode ceiling: 50 MP covers every consumer phone's
	// full-resolution photo at ~80 MB and 4.2 s on the potato. Above it the blob
	// is stored and left undescribed with a badge — never lost, never decoded.
	MaxPixels int64 = 50_000_000
	// ThumbLongEdge is the long edge of a generated thumbnail.
	ThumbLongEdge = 1600
	// ThumbQuality is the JPEG quality of a generated thumbnail.
	ThumbQuality = 85
)

// ErrTooManyPixels is returned for an image past MaxPixels. It is not a
// failure to store: the original is kept and the item is badged.
var ErrTooManyPixels = errors.New("media: image is larger than the pixel cap")

// Thumb is a generated thumbnail and what was learned making it.
type Thumb struct {
	// JPEG is the encoded thumbnail, oriented the way EXIF says to display it.
	JPEG []byte
	// Width and Height are the thumbnail's dimensions.
	Width, Height int
	// SrcWidth and SrcHeight are the stored image's, before orientation: what a
	// media row records about the original.
	SrcWidth, SrcHeight int
	// Orientation is the EXIF value that was applied, 1..8.
	Orientation int
}

// Thumbnail decodes one image and returns a JPEG thumbnail of it, upright.
//
// The order matters and is the cheap one: DecodeConfig first (7–10 MB, no
// pixels), then the cap, then the decode, then scale, then rotate — so the
// rotation costs the thumbnail's pixels rather than the original's.
//
// A format the standard library cannot decode (HEIC) is an error here and the
// caller stores the original anyway: a file Harbor cannot read is still her
// file.
func Thumbnail(r io.Reader, longEdge int) (*Thumb, error) {
	if r == nil {
		return nil, errors.New("media: no input")
	}
	// One buffered pass, because the header and the pixels have to be read from
	// the same bytes and the body may not be seekable.
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("media: reading the image header: %w", err)
	}
	if err := checkPixels(cfg.Width, cfg.Height, MaxPixels); err != nil {
		return nil, err
	}

	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("media: decoding the image: %w", err)
	}
	orientation := Orientation(body)

	if longEdge <= 0 {
		longEdge = ThumbLongEdge
	}
	scaled := scale(img, longEdge)
	upright := rotate(scaled, orientation)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, upright, &jpeg.Options{Quality: ThumbQuality}); err != nil {
		return nil, fmt.Errorf("media: encoding the thumbnail: %w", err)
	}
	return &Thumb{
		JPEG:        buf.Bytes(),
		Width:       upright.Bounds().Dx(),
		Height:      upright.Bounds().Dy(),
		SrcWidth:    cfg.Width,
		SrcHeight:   cfg.Height,
		Orientation: orientation,
	}, nil
}

// checkPixels is the cap, split out so it can be tested without building a
// 50 MP fixture (which would itself cost 240 MB).
func checkPixels(w, h int, cap int64) error {
	pixels := int64(w) * int64(h)
	if pixels > cap {
		return fmt.Errorf("%w: %dx%d is %.1f MP, cap %d MP",
			ErrTooManyPixels, w, h, float64(pixels)/1e6, cap/1_000_000)
	}
	return nil
}

// scale fits an image into longEdge without upscaling it.
func scale(src image.Image, longEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= longEdge && h <= longEdge {
		return src
	}
	nw, nh := longEdge, h*longEdge/w
	if h > w {
		nw, nh = w*longEdge/h, longEdge
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// Orientation reads the EXIF Orientation tag (0x0112) out of the first APP1
// segment, or returns 1 when the file has no EXIF or a value outside 1..8.
//
// It is written here rather than taken from a library because it is fifty lines
// and spike S12 verified it against all eight values on a real phone photo: one
// more dependency in a distroless image would cost more than it saves.
func Orientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xDA { // start of scan: no more metadata follows
			return 1
		}
		size := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if size < 2 {
			return 1 // a malformed segment length would loop forever
		}
		end := i + 2 + size
		if end > len(data) {
			end = len(data)
		}
		seg := data[i+4 : end]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:4]) == "Exif" && seg[4] == 0 && seg[5] == 0 {
			if o := tiffOrientation(seg[6:]); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
		i += 2 + size
	}
	return 1
}

// tiffOrientation walks IFD0 of a TIFF block looking for tag 0x0112. It is
// bounds-checked throughout: this input is whatever a stranger's server sent.
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	ifd := int(bo.Uint32(tiff[4:8]))
	if ifd < 0 || ifd+2 > len(tiff) {
		return 1
	}
	n := int(bo.Uint16(tiff[ifd : ifd+2]))
	for k := 0; k < n; k++ {
		e := ifd + 2 + k*12
		if e+12 > len(tiff) {
			return 1
		}
		if bo.Uint16(tiff[e:e+2]) == 0x0112 {
			return int(bo.Uint16(tiff[e+8 : e+10]))
		}
	}
	return 1
}

// rotate applies one of the eight EXIF orientations. 1..4 keep the dimensions
// and mirror or turn in place; 5..8 exchange width and height.
func rotate(src image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
