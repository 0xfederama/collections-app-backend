package images_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/images"
)

// webp1x1 is a 1x1 lossless WebP (the smallest valid file), kept as bytes
// because the standard library and x/image only decode WebP.
var webp1x1 = []byte{
	'R', 'I', 'F', 'F', 0x1a, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P', '8', 'L', 0x0d, 0, 0, 0,
	0x2f, 0, 0, 0, 0x10, 0x07, 0x10, 0x11, 0x11, 0x88, 0x88, 0xfe, 0x07, 0x00,
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withOrientation inserts an EXIF APP1 segment holding only the orientation
// tag right after the JPEG start marker. bigEndian picks the TIFF byte order.
func withOrientation(jpg []byte, o uint16, bigEndian bool) []byte {
	var bo binary.AppendByteOrder = binary.LittleEndian
	hdr := "II"
	if bigEndian {
		bo, hdr = binary.BigEndian, "MM"
	}
	tiff := []byte(hdr)
	tiff = bo.AppendUint16(tiff, 42)
	tiff = bo.AppendUint32(tiff, 8) // first IFD offset
	tiff = bo.AppendUint16(tiff, 1) // one entry
	tiff = bo.AppendUint16(tiff, 0x0112)
	tiff = bo.AppendUint16(tiff, 3) // SHORT
	tiff = bo.AppendUint32(tiff, 1)
	tiff = bo.AppendUint16(tiff, o)
	tiff = append(tiff, 0, 0)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	seg = append(seg, payload...)
	return append(append(append([]byte{}, jpg[:2]...), seg...), jpg[2:]...)
}

// hugePNG is a header-only PNG claiming w x h pixels: DecodeConfig accepts it
// without any pixel data, which is exactly the decompression-bomb shape.
func hugePNG(w, h uint32) []byte {
	chunk := func(kind string, data []byte) []byte {
		out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
		out = append(out, kind...)
		out = append(out, data...)
		return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(kind), data...)))
	}
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 2, 0, 0, 0) // 8-bit RGB
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, chunk("IHDR", ihdr)...)
	return append(out, chunk("IDAT", nil)...)
}

func TestAT06_ProcessAcceptsJPEGPNGWebP(t *testing.T) {
	tests := []struct {
		name, mediaType, ext string
		data                 []byte
		w, h                 int
	}{
		{"jpeg", "image/jpeg", "jpg", jpegBytes(t, 30, 20), 30, 20},
		{"png", "image/png", "png", pngBytes(t, 12, 8), 12, 8},
		{"webp", "image/webp", "webp", webp1x1, 1, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := images.Process(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatal(err)
			}
			if p.MediaType != tc.mediaType || p.Ext != tc.ext || p.Width != tc.w || p.Height != tc.h {
				t.Fatalf("got %s %s %dx%d", p.MediaType, p.Ext, p.Width, p.Height)
			}
			if !bytes.Equal(p.Original, tc.data) {
				t.Fatal("original must be stored unchanged")
			}
			if _, err := jpeg.Decode(bytes.NewReader(p.Thumbnail)); err != nil {
				t.Fatalf("thumbnail is not a JPEG: %v", err)
			}
		})
	}
}

func TestAT06_ProcessRejects(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	tests := []struct {
		name string
		data []byte
		want string // "415", "413" or "422:<field>"
	}{
		{"svg", svg, "415"},
		{"text", []byte("hello, I am not a picture"), "415"},
		{"empty", nil, "415"},
		{"gif", []byte("GIF89a\x01\x00\x01\x00"), "415"},
		{"jpeg magic, garbage body", append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{7}, 50)...), "422:file"},
		{"truncated png", pngBytes(t, 40, 40)[:40], "422:file"},
		{"over 10 MiB", append(pngBytes(t, 4, 4), make([]byte, images.MaxUploadBytes)...), "413"},
		{"48 megapixels", hugePNG(8000, 6000), "422:file"},
		{"just over 40 megapixels", hugePNG(40_000_001, 1), "422:file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := images.Process(bytes.NewReader(tc.data))
			var tooLarge *apperr.PayloadTooLargeError
			var media *apperr.UnsupportedMediaTypeError
			var invalid *apperr.ValidationError
			switch {
			case tc.want == "413" && errors.As(err, &tooLarge):
			case tc.want == "415" && errors.As(err, &media):
			case errors.As(err, &invalid) && tc.want == "422:"+invalid.Errors[0].Field:
			default:
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

func TestAT06_ProcessAcceptsExactlyTenMiB(t *testing.T) {
	// A valid PNG padded with trailing bytes up to the limit is accepted; one byte more is not.
	base := pngBytes(t, 4, 4)
	if _, err := images.Process(bytes.NewReader(append(base, make([]byte, images.MaxUploadBytes-len(base))...))); err != nil {
		t.Fatalf("10 MiB: %v", err)
	}
	var tooLarge *apperr.PayloadTooLargeError
	if _, err := images.Process(bytes.NewReader(append(base, make([]byte, images.MaxUploadBytes-len(base)+1)...))); !errors.As(err, &tooLarge) {
		t.Fatalf("10 MiB + 1: %v", err)
	}
}

func TestThumbnailSize(t *testing.T) {
	tests := []struct {
		name           string
		w, h           int
		thumbW, thumbH int
	}{
		{"landscape is scaled", 1600, 800, 640, 320},
		{"portrait is scaled", 400, 1600, 160, 640},
		{"small image is not enlarged", 100, 50, 100, 50},
		{"exactly 640", 640, 480, 640, 480},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := images.Process(bytes.NewReader(pngBytes(t, tc.w, tc.h)))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(p.Thumbnail))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Width != tc.thumbW || cfg.Height != tc.thumbH {
				t.Fatalf("thumbnail %dx%d, want %dx%d", cfg.Width, cfg.Height, tc.thumbW, tc.thumbH)
			}
		})
	}
}

func TestThumbnailAppliesExifOrientationAndStripsMetadata(t *testing.T) {
	base := jpegBytes(t, 60, 20)
	for _, o := range []uint16{1, 2, 3, 4, 5, 6, 7, 8} {
		for _, be := range []bool{false, true} {
			p, err := images.Process(bytes.NewReader(withOrientation(base, o, be)))
			if err != nil {
				t.Fatalf("orientation %d: %v", o, err)
			}
			wantW, wantH := 60, 20
			if o >= 5 {
				wantW, wantH = 20, 60 // a rotated photo is displayed with width and height swapped
			}
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(p.Thumbnail))
			if err != nil {
				t.Fatal(err)
			}
			if p.Width != wantW || p.Height != wantH || cfg.Width != wantW || cfg.Height != wantH {
				t.Fatalf("orientation %d: stored %dx%d thumbnail %dx%d, want %dx%d", o, p.Width, p.Height, cfg.Width, cfg.Height, wantW, wantH)
			}
			if bytes.Contains(p.Thumbnail, []byte("Exif")) {
				t.Fatalf("orientation %d: thumbnail still carries EXIF", o)
			}
			// The stored original keeps only a minimal EXIF with the orientation (F005-B1).
			if hasExif := bytes.Contains(p.Original, []byte("Exif")); hasExif != (o > 1) {
				t.Fatalf("orientation %d: original has EXIF = %v, want %v", o, hasExif, o > 1)
			}
		}
	}
}

func TestThumbnailOrientation6RotatesPixels(t *testing.T) {
	// Stored image: left half red, right half blue. Orientation 6 means "rotate 90° clockwise to display":
	// the left (red) half ends up on top.
	src := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for x := range 40 {
		for y := range 20 {
			c := color.RGBA{R: 255, A: 255}
			if x >= 20 {
				c = color.RGBA{B: 255, A: 255}
			}
			src.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	p, err := images.Process(bytes.NewReader(withOrientation(buf.Bytes(), 6, false)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := jpeg.Decode(bytes.NewReader(p.Thumbnail))
	if err != nil {
		t.Fatal(err)
	}
	topR, _, topB, _ := out.At(10, 5).RGBA()
	botR, _, botB, _ := out.At(10, 35).RGBA()
	if topR <= 0xC000 || topB >= 0x4000 || botB <= 0xC000 || botR >= 0x4000 {
		t.Fatalf("top should be red and bottom blue; top r=%x b=%x, bottom r=%x b=%x", topR, topB, botR, botB)
	}
}

func TestMalformedExifFallsBackToUpright(t *testing.T) {
	base := jpegBytes(t, 30, 10)
	// APP1 claiming Exif but with a truncated TIFF header, and an orientation of 99.
	bad := append(append([]byte{}, base[:2]...), 0xFF, 0xE1, 0x00, 0x0A, 'E', 'x', 'i', 'f', 0, 0, 'I', 'I')
	bad = append(bad, base[2:]...)
	for name, data := range map[string][]byte{"truncated": bad, "value 99": withOrientation(base, 99, false)} {
		p, err := images.Process(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.Width != 30 || p.Height != 10 {
			t.Fatalf("%s: %dx%d", name, p.Width, p.Height)
		}
	}
}
