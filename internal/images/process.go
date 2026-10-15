package images

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // registers the PNG decoder for image.Decode
	"io"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder for image.Decode

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// Limits of ARCHITECTURE §5.
const (
	MaxUploadBytes   = 10 << 20 // 10 MiB
	MaxPixels        = 40_000_000
	ThumbnailMaxSide = 640
	thumbnailQuality = 82
)

// Processed is a validated upload with its thumbnail, ready to be stored.
type Processed struct {
	// Original is the upload without embedded metadata (EXIF, GPS, XMP, IPTC,
	// comments), losslessly: see Strip. SHA256 is computed over these bytes.
	Original  []byte
	MediaType string // image/jpeg, image/png or image/webp
	Ext       string // jpg, png or webp
	// Width and Height are the size as displayed, i.e. with the EXIF
	// orientation of JPEGs applied (a rotated photo swaps them).
	Width, Height int
	SHA256        [sha256.Size]byte
	Thumbnail     []byte // always JPEG
}

// format describes one accepted image type.
type format struct {
	mediaType string
	ext       string
	decoder   string // name registered with the image package
}

// Process validates an upload and builds its thumbnail. The type comes from
// the leading bytes, never from the filename or a client Content-Type. The
// dimensions are read (image.DecodeConfig) before the pixels are decoded, so a
// small file that expands to a huge bitmap is refused without allocating it.
//
// The stored original has its metadata removed (Strip), the thumbnail is
// re-encoded from pixels and has none either.
//
// Errors are domain errors: 413 over 10 MiB, 415 for anything but JPEG, PNG
// and WebP (SVG included), 422 for a corrupt file or one over 40 megapixels.
func Process(r io.Reader) (*Processed, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err) // includes *http.MaxBytesError, mapped to 413
	}
	if len(data) > MaxUploadBytes {
		return nil, apperr.PayloadTooLarge(MaxUploadBytes)
	}
	f, ok := sniff(data)
	if !ok {
		return nil, apperr.UnsupportedMediaType("not a JPEG, PNG or WebP image")
	}

	cfg, name, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || name != f.decoder {
		return nil, invalidFile("is not a valid " + f.decoder + " image")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, invalidFile("has no pixels")
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return nil, invalidFile("is larger than 40 megapixels")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, invalidFile("is not a valid " + f.decoder + " image")
	}

	orientation := 1
	if f.decoder == "jpeg" {
		orientation = jpegOrientation(data)
	}
	thumb, err := makeThumbnail(img, orientation)
	if err != nil {
		return nil, err
	}
	w, h := cfg.Width, cfg.Height
	if orientation >= 5 {
		w, h = h, w
	}
	// The orientation was read from the upload above; Strip keeps it as the only EXIF tag.
	clean, err := Strip(data, f.mediaType)
	if err != nil {
		return nil, invalidFile("is not a valid " + f.decoder + " image")
	}
	return &Processed{
		Original: clean, MediaType: f.mediaType, Ext: f.ext, Width: w, Height: h,
		SHA256: sha256.Sum256(clean), Thumbnail: thumb,
	}, nil
}

func invalidFile(msg string) error {
	return apperr.Validation(apperr.FieldError{Field: "file", Message: msg})
}

// sniff recognises the three accepted types by their magic bytes.
func sniff(b []byte) (format, bool) {
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return format{"image/jpeg", "jpg", "jpeg"}, true
	case bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return format{"image/png", "png", "png"}, true
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return format{"image/webp", "webp", "webp"}, true
	}
	return format{}, false
}

// makeThumbnail scales img so its longest side is at most ThumbnailMaxSide
// (never enlarging), applies the EXIF orientation and encodes a JPEG. Encoding
// from decoded pixels drops every metadata block (EXIF, GPS, ICC, XMP).
func makeThumbnail(img image.Image, orientation int) ([]byte, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	tw, th := w, h
	if long := max(w, h); long > ThumbnailMaxSide {
		tw, th = max(1, w*ThumbnailMaxSide/long), max(1, h*ThumbnailMaxSide/long)
	}
	// JPEG has no alpha: paint on white so transparent PNG/WebP pixels do not turn black.
	small := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.Draw(small, small.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(small, small.Bounds(), img, b, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, orient(small, orientation), &jpeg.Options{Quality: thumbnailQuality}); err != nil {
		return nil, fmt.Errorf("encode thumbnail: %w", err)
	}
	return buf.Bytes(), nil
}

// orient returns img rotated or mirrored according to EXIF orientation 1-8
// (1 = as stored). Other values leave it as is.
func orient(src *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var dx, dy int
			switch o {
			case 2: // mirror horizontally
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertically
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // rotate 90 clockwise
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 90 counter-clockwise
				dx, dy = y, w-1-x
			}
			dst.SetRGBA(dx, dy, src.RGBAAt(x, y))
		}
	}
	return dst
}

// jpegOrientation reads the EXIF orientation (1-8) from a JPEG's APP1 segment
// and returns 1 when there is none or the data is malformed. It walks only the
// marker segments before the image data and reads one tag, which is all that is
// needed here (see D-064).
func jpegOrientation(b []byte) int {
	i := 2 // after SOI
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xFF { // fill byte
			i++
			continue
		}
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2 // standalone markers carry no length
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // start of scan / end of image
			return 1
		}
		size := int(b[i+2])<<8 | int(b[i+3])
		if size < 2 || i+2+size > len(b) {
			return 1
		}
		if marker == 0xE1 {
			if o := exifOrientation(b[i+4 : i+2+size]); o != 0 {
				return o
			}
		}
		i += 2 + size
	}
	return 1
}

// exifOrientation parses an APP1 payload ("Exif\0\0" + TIFF) and returns the
// value of tag 0x0112 in the first IFD, or 0 when absent or malformed.
func exifOrientation(p []byte) int {
	if !bytes.HasPrefix(p, []byte("Exif\x00\x00")) {
		return 0
	}
	t := p[6:]
	if len(t) < 8 {
		return 0
	}
	var u16 func([]byte) int
	var u32 func([]byte) int
	switch string(t[:2]) {
	case "II":
		u16 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 }
		u32 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24 }
	case "MM":
		u16 = func(b []byte) int { return int(b[0])<<8 | int(b[1]) }
		u32 = func(b []byte) int { return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3]) }
	default:
		return 0
	}
	if u16(t[2:]) != 42 {
		return 0
	}
	ifd := u32(t[4:])
	if ifd < 8 || ifd+2 > len(t) {
		return 0
	}
	n := u16(t[ifd:])
	for k := range n {
		e := ifd + 2 + k*12
		if e+12 > len(t) {
			return 0
		}
		if u16(t[e:]) == 0x0112 { // Orientation, type SHORT, count 1: the value sits in the first 2 bytes
			if o := u16(t[e+8:]); o >= 1 && o <= 8 {
				return o
			}
			return 0
		}
	}
	return 0
}
