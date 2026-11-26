package portrait

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// orientInternalTIFF is a big-endian TIFF whose IFD0 says orientation `o`.
func orientInternalTIFF(o uint16) []byte {
	b := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1}
	b = binary.BigEndian.AppendUint16(b, 0x0112)
	b = binary.BigEndian.AppendUint16(b, 3)
	b = binary.BigEndian.AppendUint32(b, 1)
	b = binary.BigEndian.AppendUint16(b, o)

	return append(b, 0, 0, 0, 0, 0, 0)
}

// orientInternalWebP is a WebP container with an EXIF chunk after a chunk of
// odd length, which is padded; no picture, since only the tag is read here.
func orientInternalWebP(exif []byte) []byte {
	chunk := func(kind string, body []byte) []byte {
		c := append([]byte(kind), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...)
		c = append(c, body...)
		if len(body)%2 == 1 {
			c = append(c, 0)
		}

		return c
	}
	body := append([]byte("WEBP"), chunk("VP8X", make([]byte, 9))...)
	body = append(body, chunk("EXIF", exif)...)

	return append(append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...), body...)
}

// TestTheOrientationOfAWebPIsRead: no encoder here writes a WebP, so the tag
// is read from a container made by hand -- written as the TIFF itself, and as
// some writers do, begun as a JPEG's segment is.
func TestTheOrientationOfAWebPIsRead(t *testing.T) {
	x := require.New(t)
	x.Equal(6, orientation(orientInternalWebP(orientInternalTIFF(6))))
	x.Equal(8, orientation(orientInternalWebP(append([]byte("Exif\x00\x00"), orientInternalTIFF(8)...))))
	x.Equal(1, orientation(orientInternalWebP(orientInternalTIFF(0))))
}

// TestWhatSaysItIsATagAndIsNotIsReadAsNone: the tag is read from bytes
// anybody wrote, before they are known to be an image, so every length and
// offset in them is a claim. Each cut short, and each claiming more than there
// is, is no tag, and none of it reads past the end.
func TestWhatSaysItIsATagAndIsNotIsReadAsNone(t *testing.T) {
	x := require.New(t)
	tiff := orientInternalTIFF(6)
	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe1}, binary.BigEndian.AppendUint16(nil, uint16(2+6+len(tiff)))...)
	jpeg = append(append(jpeg, "Exif\x00\x00"...), tiff...)
	webp := orientInternalWebP(tiff)
	x.Equal(6, orientation(jpeg))

	for _, whole := range [][]byte{jpeg, webp} {
		for n := range len(whole) {
			x.NotPanics(func() { orientation(whole[:n]) })
		}
	}

	// An IFD past the end, and an entry count larger than the TIFF.
	far := append([]byte{}, tiff...)
	binary.BigEndian.PutUint32(far[4:], 1<<30)
	x.Equal(0, tiffOrientation(far))
	many := append([]byte{}, tiff...)
	binary.BigEndian.PutUint16(many[8:], 0xffff)
	x.NotPanics(func() { tiffOrientation(many) })
	// A segment that says it is longer than the file.
	long := append([]byte{}, jpeg...)
	binary.BigEndian.PutUint16(long[4:], 0xffff)
	x.Equal(1, orientation(long))
}
