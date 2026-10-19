package images_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"reflect"
	"testing"

	_ "golang.org/x/image/webp" // registers the WebP decoder for image.Decode

	"github.com/0xfederama/collections-app-backend/internal/images"
)

// latitudeSecs is the GPS "seconds" rational 1234567/100; its denominator bytes
// are a marker nothing else in the fixtures contains.
var latitudeMarker = []byte{0x00, 0x12, 0xD6, 0x87}

// gpsExif builds an APP1 EXIF payload with the orientation tag and a GPS IFD
// holding a latitude (big-endian TIFF).
func gpsExif(orientation uint16) []byte {
	be := binary.BigEndian
	t := []byte("MM")
	t = be.AppendUint16(t, 42)
	t = be.AppendUint32(t, 8)
	// IFD0 at 8: Orientation and the GPS IFD pointer (at 38).
	t = be.AppendUint16(t, 2)
	t = be.AppendUint16(t, 0x0112)
	t = be.AppendUint16(t, 3)
	t = be.AppendUint32(t, 1)
	t = be.AppendUint16(t, orientation)
	t = append(t, 0, 0)
	t = be.AppendUint16(t, 0x8825)
	t = be.AppendUint16(t, 4)
	t = be.AppendUint32(t, 1)
	t = be.AppendUint32(t, 38)
	t = be.AppendUint32(t, 0)
	// GPS IFD at 38: GPSLatitudeRef "N" and GPSLatitude (rationals at 68).
	t = be.AppendUint16(t, 2)
	t = be.AppendUint16(t, 0x0001)
	t = be.AppendUint16(t, 2)
	t = be.AppendUint32(t, 2)
	t = append(t, 'N', 0, 0, 0)
	t = be.AppendUint16(t, 0x0002)
	t = be.AppendUint16(t, 5)
	t = be.AppendUint32(t, 3)
	t = be.AppendUint32(t, 68)
	t = be.AppendUint32(t, 0)
	for _, r := range [][2]uint32{{45, 1}, {30, 1}, {1234567, 100}} {
		t = be.AppendUint32(t, r[0])
		t = be.AppendUint32(t, r[1])
	}
	return append([]byte("Exif\x00\x00"), t...)
}

func jpegSegment(marker byte, payload []byte) []byte {
	seg := []byte{0xFF, marker}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2))
	return append(seg, payload...)
}

// insertAfterSOI puts segments right after the JPEG start marker.
func insertAfterSOI(jpg []byte, segs ...[]byte) []byte {
	out := append([]byte{}, jpg[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, jpg[2:]...)
}

const iccPayload = "ICC_PROFILE\x00\x01\x01fake-profile-bytes"

// dirtyJPEG is a JPEG carrying EXIF with GPS, XMP, IPTC, a comment, an ICC
// profile and an MPF block.
func dirtyJPEG(base []byte, orientation uint16) []byte {
	return insertAfterSOI(base,
		jpegSegment(0xE1, gpsExif(orientation)),
		jpegSegment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>")),
		jpegSegment(0xED, []byte("Photoshop 3.0\x008BIM-iptc")),
		jpegSegment(0xFE, []byte("GPS 45.5N shot at home")),
		jpegSegment(0xE2, []byte(iccPayload)),
		jpegSegment(0xE2, []byte("MPF\x00multi-picture")),
	)
}

func sha(b []byte) [sha256.Size]byte { return sha256.Sum256(b) }

func pngChunk(typ string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	out = append(out, typ...)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(typ), data...)))
}

func patternPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 16, 9))
	for x := range 16 {
		for y := range 9 {
			img.Set(x, y, color.NRGBA{R: uint8(x * 16), G: uint8(y * 28), B: 7, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// dirtyPNG inserts metadata chunks before the first IDAT.
func dirtyPNG(t *testing.T) (dirty, clean []byte) {
	t.Helper()
	clean = patternPNG(t)
	at := bytes.Index(clean, []byte("IDAT")) - 4
	extra := bytes.Join([][]byte{
		pngChunk("eXIf", gpsExif(1)),
		pngChunk("tEXt", []byte("Comment\x00GPS 45.5N")),
		pngChunk("iTXt", []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00<x:xmpmeta/>")),
		pngChunk("tIME", []byte{0x07, 0xea, 1, 2, 3, 4, 5}),
		pngChunk("zTXt", []byte("Software\x00\x00x")),
	}, nil)
	dirty = append(append(append([]byte{}, clean[:at]...), extra...), clean[at:]...)
	return dirty, clean
}

func riffChunk(fourcc string, payload []byte) []byte {
	out := []byte(fourcc)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(payload)))
	out = append(out, payload...)
	if len(payload)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

func riffFile(chunks ...[]byte) []byte {
	body := []byte("WEBP")
	for _, c := range chunks {
		body = append(body, c...)
	}
	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(len(body)))
	return append(out, body...)
}

// vp8x is a VP8X chunk for a 1x1 canvas with the given flag byte.
func vp8x(flags byte) []byte {
	return riffChunk("VP8X", []byte{flags, 0, 0, 0, 0, 0, 0, 0, 0, 0})
}

// vp8l is the image chunk of webp1x1.
func vp8l() []byte { return riffChunk("VP8L", webp1x1[20:33]) }

func TestF005B1_ProcessStripsJPEGMetadata(t *testing.T) {
	base := jpegBytes(t, 40, 30)
	p, err := images.Process(bytes.NewReader(dirtyJPEG(base, 1)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range [][]byte{
		[]byte("Exif"), []byte("http://ns.adobe.com/xap"), []byte("Photoshop"), []byte("GPS"),
		[]byte("shot at home"), []byte("MPF"), latitudeMarker,
	} {
		if bytes.Contains(p.Original, s) {
			t.Errorf("original still contains %q", s)
		}
	}
	if !bytes.Contains(p.Original, []byte(iccPayload)) {
		t.Error("ICC profile must be kept")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(p.Original))
	if err != nil || cfg.Width != 40 || cfg.Height != 30 {
		t.Fatalf("stripped original: %+v %v", cfg, err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(p.Original)); err != nil {
		t.Fatal(err)
	}
	if p.Width != 40 || p.Height != 30 {
		t.Fatalf("%dx%d", p.Width, p.Height)
	}
	if got := sha(p.Original); got != p.SHA256 {
		t.Fatal("sha256 must cover the stripped bytes")
	}
}

func TestF005B1_ProcessKeepsJPEGOrientation(t *testing.T) {
	base := jpegBytes(t, 60, 20)
	for _, o := range []uint16{2, 3, 5, 6, 8} {
		t.Run(string(rune('0'+o)), func(t *testing.T) {
			want, err := images.Process(bytes.NewReader(withOrientation(base, o, false)))
			if err != nil {
				t.Fatal(err)
			}
			p, err := images.Process(bytes.NewReader(dirtyJPEG(base, o)))
			if err != nil {
				t.Fatal(err)
			}
			if got := images.JPEGOrientation(p.Original); got != int(o) {
				t.Fatalf("orientation of the stored original = %d, want %d", got, o)
			}
			if p.Width != want.Width || p.Height != want.Height {
				t.Fatalf("%dx%d, want %dx%d", p.Width, p.Height, want.Width, want.Height)
			}
			if !bytes.Equal(p.Thumbnail, want.Thumbnail) {
				t.Fatal("thumbnail changed")
			}
			if bytes.Contains(p.Original, latitudeMarker) || bytes.Contains(p.Original, []byte("GPS")) {
				t.Fatal("GPS data survived")
			}
			if !bytes.Contains(p.Original, []byte(iccPayload)) {
				t.Error("ICC profile must be kept")
			}
			// Stripping again changes nothing.
			again, err := images.Strip(p.Original, "image/jpeg")
			if err != nil || !bytes.Equal(again, p.Original) {
				t.Fatalf("not idempotent: %v", err)
			}
		})
	}
}

func TestF005B1_StripJPEGKeepsJFIFFirst(t *testing.T) {
	base := jpegBytes(t, 20, 20)
	jfif := jpegSegment(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"))
	in := insertAfterSOI(base, jfif, jpegSegment(0xE1, gpsExif(6)))
	out, err := images.Strip(in, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out[2:], jfif) {
		t.Fatal("JFIF APP0 must stay right after SOI")
	}
	if images.JPEGOrientation(out) != 6 {
		t.Fatal("orientation lost")
	}
	if bytes.Index(out, []byte("Exif")) < bytes.Index(out, []byte("JFIF")) {
		t.Fatal("EXIF must come after JFIF")
	}
}

func TestF005B1_ProcessStripsPNGMetadata(t *testing.T) {
	dirty, clean := dirtyPNG(t)
	p, err := images.Process(bytes.NewReader(dirty))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"eXIf", "tEXt", "iTXt", "tIME", "zTXt", "GPS", "xmpmeta"} {
		if bytes.Contains(p.Original, []byte(k)) {
			t.Errorf("original still contains %q", k)
		}
	}
	if !bytes.Equal(p.Original, clean) {
		t.Fatal("expected exactly the chunks png.Encode wrote")
	}
	want, _ := png.Decode(bytes.NewReader(clean))
	got, err := png.Decode(bytes.NewReader(p.Original))
	if err != nil || !reflect.DeepEqual(want, got) {
		t.Fatalf("pixels differ: %v", err)
	}
}

func TestF005B1_ProcessStripsWebPMetadata(t *testing.T) {
	exif := riffChunk("EXIF", append([]byte("Exif\x00\x00"), 'x')) // odd size: padded
	xmp := riffChunk("XMP ", []byte("<x:xmpmeta/>"))
	icc := riffChunk("ICCP", []byte("profile!"))
	tests := []struct {
		name         string
		dirty, clean []byte
	}{
		{"exif and xmp", riffFile(vp8x(0x0C), vp8l(), exif, xmp), riffFile(vp8x(0), vp8l())},
		{"icc kept, flag kept", riffFile(vp8x(0x2C), icc, vp8l(), exif, xmp), riffFile(vp8x(0x20), icc, vp8l())},
		{"metadata before image", riffFile(vp8x(0x0C), xmp, exif, vp8l()), riffFile(vp8x(0), vp8l())},
		{"no vp8x", riffFile(vp8l(), exif), riffFile(vp8l())},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := images.Process(bytes.NewReader(tc.dirty))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(p.Original, tc.clean) {
				t.Fatalf("got % x\nwant % x", p.Original, tc.clean)
			}
			if bytes.Contains(p.Original, []byte("EXIF")) || bytes.Contains(p.Original, []byte("XMP ")) {
				t.Fatal("metadata chunk survived")
			}
			if got := binary.LittleEndian.Uint32(p.Original[4:8]); int(got) != len(p.Original)-8 {
				t.Fatalf("riff size %d, file %d", got, len(p.Original))
			}
			if _, _, err := image.Decode(bytes.NewReader(p.Original)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestF005B1_StripLeavesCleanFilesIdentical(t *testing.T) {
	tests := []struct {
		name, mediaType string
		data            []byte
	}{
		{"jpeg", "image/jpeg", jpegBytes(t, 30, 20)},
		{"png", "image/png", patternPNG(t)},
		{"webp", "image/webp", webp1x1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := images.Strip(tc.data, tc.mediaType)
			if err != nil || !bytes.Equal(out, tc.data) {
				t.Fatalf("changed a clean file: %v", err)
			}
		})
	}
}

func TestF005B1_StripRejectsMalformed(t *testing.T) {
	jpg := jpegBytes(t, 30, 20)
	pg := patternPNG(t)
	tests := []struct {
		name, mediaType string
		data            []byte
	}{
		{"empty jpeg", "image/jpeg", nil},
		{"garbage jpeg", "image/jpeg", []byte("not a jpeg at all")},
		{"jpeg cut before the scan", "image/jpeg", jpg[:30]},
		{"jpeg segment longer than the file", "image/jpeg", append(append([]byte{}, jpg[:2]...), 0xFF, 0xE1, 0x7F, 0xFF, 'E')},
		{"jpeg segment length 1", "image/jpeg", append(append([]byte{}, jpg[:2]...), 0xFF, 0xE1, 0x00, 0x01, 'E', 'x')},
		{"jpeg without marker", "image/jpeg", append(append([]byte{}, jpg[:2]...), 1, 2, 3, 4, 5)},
		{"truncated png", "image/png", pg[:len(pg)/2]},
		{"png without IEND", "image/png", pg[:len(pg)-12]},
		{"png chunk longer than the file", "image/png", append(append([]byte{}, pg[:8]...), 0x7F, 0xFF, 0xFF, 0xFF, 'I', 'H', 'D', 'R')},
		{"png signature only", "image/png", pg[:8]},
		{"truncated webp", "image/webp", webp1x1[:20]},
		{"webp riff size too large", "image/webp", append(append([]byte{}, webp1x1[:4]...), 0xFF, 0, 0, 0, 'W', 'E', 'B', 'P')},
		{"webp chunk too long", "image/webp", riffFile(append([]byte("VP8L"), 0xFF, 0xFF, 0, 0, 1))},
		{"webp short VP8X", "image/webp", riffFile(riffChunk("VP8X", []byte{0x0C, 0}))},
		{"webp no chunks", "image/webp", riffFile()},
		{"unknown media type", "image/gif", []byte("GIF89a")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := images.Strip(tc.data, tc.mediaType); err == nil {
				t.Fatalf("want an error, got %d bytes", len(out))
			}
		})
	}
}
