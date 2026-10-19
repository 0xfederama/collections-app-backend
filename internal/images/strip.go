package images

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Strip removes embedded metadata (EXIF, GPS, XMP, IPTC, comments, text chunks)
// from a JPEG, PNG or WebP without re-encoding: the pixel data is copied
// byte for byte, only the metadata containers are left out. The input is parsed
// strictly; on any malformed layout it returns an error and callers must keep
// the original bytes.
//
// What stays: everything needed to decode and display the picture, including
// ICC colour profiles, JFIF, the Adobe APP14 colour marker (JPEG) and animation
// chunks. A JPEG whose EXIF orientation is not 1 gets back a minimal EXIF
// segment holding only that tag, so it is still displayed the same way.
// Bytes after the end of the image (after EOI, IEND or the RIFF end) are copied
// untouched.
func Strip(data []byte, mediaType string) ([]byte, error) {
	switch mediaType {
	case "image/jpeg":
		return stripJPEG(data)
	case "image/png":
		return stripPNG(data)
	case "image/webp":
		return stripWebP(data)
	}
	return nil, fmt.Errorf("strip: unsupported media type %q", mediaType)
}

var errMalformed = errors.New("strip: malformed file")

func malformed(what string) error { return fmt.Errorf("%w: %s", errMalformed, what) }

// stripJPEG walks the marker segments before the scan and drops APP1 (EXIF,
// XMP), APP13 (IPTC), COM and every other APPn except APP0 (JFIF), the ICC
// profile in APP2 and the Adobe marker in APP14. From SOS on it copies verbatim.
func stripJPEG(b []byte) ([]byte, error) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, malformed("not a JPEG")
	}
	orientation := jpegOrientation(b)
	out := make([]byte, 0, len(b))
	out = append(out, 0xFF, 0xD8)
	exifDone := orientation <= 1
	putExif := func() {
		if !exifDone {
			out = append(out, orientationSegment(orientation)...)
			exifDone = true
		}
	}

	i := 2
	for {
		if i+2 > len(b) {
			return nil, malformed("jpeg ends before the scan")
		}
		if b[i] != 0xFF {
			return nil, malformed("jpeg marker expected")
		}
		marker := b[i+1]
		switch {
		case marker == 0xFF: // fill byte
			i++
			continue
		case marker == 0x00 || marker == 0xD8:
			return nil, malformed("unexpected jpeg marker")
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7): // standalone, no length
			putExif()
			out = append(out, b[i:i+2]...)
			i += 2
			continue
		case marker == 0xD9: // EOI before any scan: nothing to display
			return nil, malformed("jpeg has no scan")
		}
		if i+4 > len(b) {
			return nil, malformed("truncated jpeg segment")
		}
		size := int(b[i+2])<<8 | int(b[i+3])
		if size < 2 || i+2+size > len(b) {
			return nil, malformed("bad jpeg segment length")
		}
		if marker == 0xDA { // start of scan: the rest is entropy-coded data
			putExif()
			return append(out, b[i:]...), nil
		}
		seg := b[i : i+2+size]
		payload := b[i+4 : i+2+size]
		if keepJPEGSegment(marker, payload) {
			if marker != 0xE0 {
				putExif() // after the JFIF APP0, before anything else
			}
			out = append(out, seg...)
		}
		i += 2 + size
	}
}

func keepJPEGSegment(marker byte, payload []byte) bool {
	switch {
	case marker == 0xE0: // JFIF
		return true
	case marker == 0xE2: // keep ICC profiles, drop MPF and the rest
		return bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00"))
	case marker == 0xEE: // Adobe colour transform, needed to decode CMYK/YCCK
		return bytes.HasPrefix(payload, []byte("Adobe"))
	case marker >= 0xE1 && marker <= 0xEF, marker == 0xFE: // other APPn, COM
		return false
	}
	return true
}

// orientationSegment builds an APP1 EXIF segment with one IFD entry: Orientation.
func orientationSegment(o int) []byte {
	tiff := []byte("MM")
	tiff = binary.BigEndian.AppendUint16(tiff, 42)
	tiff = binary.BigEndian.AppendUint32(tiff, 8) // first IFD offset
	tiff = binary.BigEndian.AppendUint16(tiff, 1) // one entry
	tiff = binary.BigEndian.AppendUint16(tiff, 0x0112)
	tiff = binary.BigEndian.AppendUint16(tiff, 3) // SHORT
	tiff = binary.BigEndian.AppendUint32(tiff, 1)
	tiff = binary.BigEndian.AppendUint16(tiff, uint16(o)) //nolint:gosec // 1-8, checked by exifOrientation
	tiff = append(tiff, 0, 0)
	tiff = binary.BigEndian.AppendUint32(tiff, 0) // no next IFD
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2)) //nolint:gosec // 34
	return append(seg, payload...)
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}

// stripPNG copies the chunks except eXIf, tEXt, zTXt, iTXt and tIME. CRCs are
// copied as they are. Everything after IEND is kept untouched.
func stripPNG(b []byte) ([]byte, error) {
	if !bytes.HasPrefix(b, pngSignature) {
		return nil, malformed("not a PNG")
	}
	out := make([]byte, 0, len(b))
	out = append(out, pngSignature...)
	i := len(pngSignature)
	for {
		if i+12 > len(b) {
			return nil, malformed("truncated png chunk")
		}
		n := int64(binary.BigEndian.Uint32(b[i:]))
		end := int64(i) + 12 + n
		if n > 1<<31-1 || end > int64(len(b)) {
			return nil, malformed("bad png chunk length")
		}
		typ := string(b[i+4 : i+8])
		for _, c := range []byte(typ) {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
				return nil, malformed("bad png chunk type")
			}
		}
		switch typ {
		case "eXIf", "tEXt", "zTXt", "iTXt", "tIME":
		default:
			out = append(out, b[i:end]...)
		}
		i = int(end)
		if typ == "IEND" {
			return append(out, b[i:]...), nil
		}
	}
}

// stripWebP copies the RIFF chunks except EXIF and XMP, rewrites the RIFF size
// and clears the EXIF and XMP flags of a VP8X chunk. Chunks are padded to even
// sizes. Bytes after the RIFF container are kept untouched.
func stripWebP(b []byte) ([]byte, error) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return nil, malformed("not a WebP")
	}
	riffEnd := 8 + int64(binary.LittleEndian.Uint32(b[4:8]))
	if riffEnd < 12 || riffEnd > int64(len(b)) {
		return nil, malformed("bad riff size")
	}
	body := b[12:riffEnd]
	chunks := make([]byte, 0, len(body))
	seen := 0
	for i := 0; i < len(body); {
		if i+8 > len(body) {
			return nil, malformed("truncated webp chunk header")
		}
		fourcc := string(body[i : i+4])
		n := int(binary.LittleEndian.Uint32(body[i+4:]))
		payloadEnd := i + 8 + n
		if n < 0 || payloadEnd > len(body) {
			return nil, malformed("bad webp chunk length")
		}
		next := payloadEnd + n%2
		if next > len(body) { // a missing pad byte on the very last chunk is tolerated
			if payloadEnd != len(body) {
				return nil, malformed("bad webp chunk padding")
			}
			next = payloadEnd
		}
		seen++
		switch fourcc {
		case "EXIF", "XMP ":
		default:
			start := len(chunks)
			chunks = append(chunks, body[i:payloadEnd]...)
			if n%2 == 1 {
				chunks = append(chunks, 0)
			}
			if fourcc == "VP8X" {
				if n < 10 {
					return nil, malformed("short VP8X chunk")
				}
				chunks[start+8] &^= 0x08 | 0x04 // EXIF and XMP flags
			}
		}
		i = next
	}
	if seen == 0 {
		return nil, malformed("webp has no chunks")
	}
	out := make([]byte, 0, 12+len(chunks)+len(b)-int(riffEnd))
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(4+len(chunks))) //nolint:gosec // smaller than the input, which is capped at 10 MiB
	out = append(out, "WEBP"...)
	out = append(out, chunks...)
	return append(out, b[riffEnd:]...), nil
}
