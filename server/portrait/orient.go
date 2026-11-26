package portrait

import (
	"bytes"
	"encoding/binary"
	"image"
)

// # The way up a photograph is seen
//
// A camera keeps the pixels as its sensor read them and says, in the EXIF
// orientation tag, how they are to be turned to be seen: a phone held upright
// writes a landscape image and 6, *turn it a quarter clockwise*. Every browser
// honours the tag, and `ts/lib/picture.tsx` draws an image larger than
// [MaxSide] to a canvas the way up it is seen before sending it; one within
// the bound arrives as it was taken, tag and all. So it is honoured here, and
// a photograph is the same portrait whichever way it arrived.
//
// Read where the formats roster takes keep it: a JPEG's APP1 segment, a PNG's
// eXIf chunk and a WebP's EXIF chunk. GIF has none. Of the TIFF inside, one
// tag of IFD0 is read and nothing else. An image whose tag is missing, 1, or
// a value the tag cannot have is the image as stored.
//
// # What a turn costs, and the bound
//
// [MaxSide] is held on the sides the header says, which are the stored
// pixels, before anything is decoded and before anything is turned. A quarter
// turn swaps the two sides, so it cannot carry an image past the bound in
// either direction. And it is not the image that is turned: the square in its
// middle is the same square either way up, so it is cropped and scaled as
// stored and each rendition, at most 128 pixels a side, is turned. Nothing is
// allocated for the turn that [MaxSide] has not already paid for.

// orientation is the EXIF orientation `b` says it is seen at, 1 to 8, or 1
// when it says none.
func orientation(b []byte) int {
	var tiff []byte
	switch {
	case bytes.HasPrefix(b, []byte{0xff, 0xd8}):
		tiff = jpegExif(b)
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		tiff = pngExif(b)
	case len(b) >= 12 && bytes.Equal(b[0:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		tiff = webpExif(b)
	}
	if o := tiffOrientation(tiff); o >= 1 && o <= 8 {
		return o
	}

	return 1
}

// jpegExif is the TIFF of a JPEG's EXIF segment, read from the segments
// before its scan.
func jpegExif(b []byte) []byte {
	at := 2
	for at+4 <= len(b) {
		if b[at] != 0xff {
			return nil
		}
		marker := b[at+1]
		if marker == 0xd9 || marker == 0xda {
			// The end, or the scan: what follows is not segments.
			return nil
		}
		if marker == 0xff || marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) {
			// Fill, and the markers that stand alone.
			at++
			if marker != 0xff {
				at++
			}

			continue
		}
		n := int(binary.BigEndian.Uint16(b[at+2:]))
		if n < 2 || at+2+n > len(b) {
			return nil
		}
		seg := b[at+4 : at+2+n]
		if marker == 0xe1 {
			if tiff, ok := bytes.CutPrefix(seg, []byte("Exif\x00\x00")); ok {
				return tiff
			}
		}
		at += 2 + n
	}

	return nil
}

// pngExif is a PNG's eXIf chunk.
func pngExif(b []byte) []byte {
	at := 8
	for at+12 <= len(b) {
		n := int(binary.BigEndian.Uint32(b[at:]))
		kind := string(b[at+4 : at+8])
		if n < 0 || n > len(b)-at-12 {
			return nil
		}
		switch kind {
		case "eXIf":
			return b[at+8 : at+8+n]
		case "IEND":
			return nil
		}
		at += 12 + n
	}

	return nil
}

// webpExif is a WebP's EXIF chunk, which some writers begin as a JPEG's
// segment is begun.
func webpExif(b []byte) []byte {
	at := 12
	for at+8 <= len(b) {
		n := int(binary.LittleEndian.Uint32(b[at+4:]))
		if n < 0 || n > len(b)-at-8 {
			return nil
		}
		if string(b[at:at+4]) == "EXIF" {
			tiff := b[at+8 : at+8+n]
			if rest, ok := bytes.CutPrefix(tiff, []byte("Exif\x00\x00")); ok {
				tiff = rest
			}

			return tiff
		}
		// A chunk is padded to an even length.
		at += 8 + n + n%2
	}

	return nil
}

// tiffOrientation is tag 0x0112 of the first IFD of a TIFF, or 0.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0
	}
	if order.Uint16(t[2:]) != 42 {
		return 0
	}
	ifd := order.Uint32(t[4:])
	if ifd > uint32(len(t)-2) {
		return 0
	}
	at := int(ifd)
	n := int(order.Uint16(t[at:]))
	at += 2
	for i := 0; i < n && at+12 <= len(t); i, at = i+1, at+12 {
		if order.Uint16(t[at:]) != 0x0112 {
			continue
		}
		// A SHORT, one of it, kept in the entry itself.
		if order.Uint16(t[at+2:]) != 3 || order.Uint32(t[at+4:]) != 1 {
			return 0
		}

		return int(order.Uint16(t[at+8:]))
	}

	return 0
}

// turned is the square `m` as orientation `o` says it is seen: `m` itself for
// 1, and otherwise a new image of the same size.
func turned(m *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return m
	}

	r := m.Bounds()
	side := r.Dx()
	last := side - 1
	out := image.NewRGBA(r)
	for y := range side {
		for x := range side {
			// Where in what is stored the pixel seen at (x, y) is.
			var sx, sy int
			switch o {
			case 2: // mirrored left to right
				sx, sy = last-x, y
			case 3: // turned half round
				sx, sy = last-x, last-y
			case 4: // mirrored top to bottom
				sx, sy = x, last-y
			case 5: // mirrored on the diagonal from the top left
				sx, sy = y, x
			case 6: // to be turned a quarter clockwise
				sx, sy = y, last-x
			case 7: // mirrored on the other diagonal
				sx, sy = last-y, last-x
			case 8: // to be turned a quarter anticlockwise
				sx, sy = last-y, x
			}
			i, j := out.PixOffset(r.Min.X+x, r.Min.Y+y), m.PixOffset(r.Min.X+sx, r.Min.Y+sy)
			copy(out.Pix[i:i+4], m.Pix[j:j+4])
		}
	}

	return out
}
