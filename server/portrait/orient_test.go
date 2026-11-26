package portrait_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/server/portrait"
)

var (
	orientRed    = color.NRGBA{0xff, 0, 0, 0xff}
	orientGreen  = color.NRGBA{0, 0xc0, 0, 0xff}
	orientBlue   = color.NRGBA{0, 0, 0xff, 0xff}
	orientYellow = color.NRGBA{0xff, 0xff, 0, 0xff}
)

// orientQuarters is a square image `side` pixels a side in four quarters of
// colour: top left, top right, bottom left, bottom right.
func orientQuarters(side int, tl, tr, bl, br color.Color) image.Image {
	return paint(side, side, func(x, y int) color.Color {
		switch {
		case x < side/2 && y < side/2:
			return tl
		case y < side/2:
			return tr
		case x < side/2:
			return bl
		default:
			return br
		}
	})
}

// orientTIFF is the TIFF an EXIF block is: a header in the byte order `order`
// says and an IFD0 of one entry, the orientation, a SHORT.
func orientTIFF(order binary.AppendByteOrder, o uint16) []byte {
	b := make([]byte, 0, 26)
	if order == binary.BigEndian {
		b = append(b, 'M', 'M')
	} else {
		b = append(b, 'I', 'I')
	}
	b = order.AppendUint16(b, 42)
	b = order.AppendUint32(b, 8)
	b = order.AppendUint16(b, 1)      // one entry
	b = order.AppendUint16(b, 0x0112) // Orientation
	b = order.AppendUint16(b, 3)      // SHORT
	b = order.AppendUint32(b, 1)      // one of them
	b = order.AppendUint16(b, o)
	b = order.AppendUint16(b, 0)
	b = order.AppendUint32(b, 0) // no IFD1

	return b
}

// orientJPEG is `m` as a JPEG that says it is seen at orientation `o`, in an
// APP1 segment after the start of the image, as a camera writes it.
func orientJPEG(t *testing.T, m image.Image, order binary.AppendByteOrder, o uint16) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, jpeg.Encode(&b, m, &jpeg.Options{Quality: 95}))
	raw := b.Bytes()

	exif := append([]byte("Exif\x00\x00"), orientTIFF(order, o)...)
	app1 := binary.BigEndian.AppendUint16([]byte{0xff, 0xe1}, uint16(2+len(exif)))
	app1 = append(app1, exif...)

	return append(append(append([]byte{}, raw[:2]...), app1...), raw[2:]...)
}

// orientPNG is `m` as a PNG with an eXIf chunk before its pixels.
func orientPNG(t *testing.T, m image.Image, o uint16) []byte {
	t.Helper()
	raw := encoded(t, m)

	tiff := orientTIFF(binary.BigEndian, o)
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(tiff)))
	body := append([]byte("eXIf"), tiff...)
	chunk = append(chunk, body...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(body))

	// After the signature and IHDR, which is 8 + 25 bytes.
	at := 8 + 25

	return append(append(append([]byte{}, raw[:at]...), chunk...), raw[at:]...)
}

// orientSeen is the four quarters of the portrait's 32, as a page draws it.
func orientSeen(t *testing.T, b []byte) [4]color.Color {
	t.Helper()
	p, err := portrait.Render(t.Context(), b)
	require.NoError(t, err)
	m := decoded(t, p.GetRenditions()[0])
	require.Equal(t, 32, m.Bounds().Dx())

	return [4]color.Color{m.At(8, 8), m.At(24, 8), m.At(8, 24), m.At(24, 24)}
}

// TestAPhotographIsRenderedTheWayUpItWasTaken: a camera keeps the pixels the
// way its sensor read them and says in the EXIF orientation tag how they are
// to be seen, and `Render` ignored the tag, so a phone's photograph held
// upright was drawn on its side. A page draws a picture larger than roster
// takes to a canvas first, the right way up, so the same phone gave two
// answers by how large the file was.
//
// Each of the eight orientations is an image stored so that, turned as its
// tag says, it is red, green / blue, yellow; what is kept is that. The tag is
// read where a JPEG keeps it (APP1, in either byte order) and where a PNG does
// (eXIf). No tag, the default, and a value the tag cannot have are the image
// as it is stored.
func TestAPhotographIsRenderedTheWayUpItWasTaken(t *testing.T) {
	r, g, b, y := orientRed, orientGreen, orientBlue, orientYellow
	upright := [4]color.Color{r, g, b, y}

	// How each orientation is stored, by hand from what the tag means: 2
	// mirrored left to right, 3 turned half round, 4 mirrored top to bottom,
	// 5 mirrored on the diagonal from the top left, 6 to be turned a quarter
	// clockwise, 7 mirrored on the other diagonal, 8 to be turned a quarter
	// anticlockwise.
	stored := map[uint16][4]color.Color{
		1: {r, g, b, y},
		2: {g, r, y, b},
		3: {y, b, g, r},
		4: {b, y, r, g},
		5: {r, b, g, y},
		6: {g, y, r, b},
		7: {y, g, b, r},
		8: {b, r, y, g},
	}

	for o, s := range stored {
		m := orientQuarters(32, s[0], s[1], s[2], s[3])
		for name, f := range map[string][]byte{
			"a JPEG, big-endian":    orientJPEG(t, m, binary.BigEndian, o),
			"a JPEG, little-endian": orientJPEG(t, m, binary.LittleEndian, o),
			"a PNG":                 orientPNG(t, m, o),
		} {
			t.Run(fmt.Sprintf("%d, %s", o, name), func(t *testing.T) {
				seen := orientSeen(t, f)
				for i := range seen {
					near(t, upright[i], seen[i])
				}
			})
		}
	}

	t.Run("no tag, or one that says nothing, is the image as stored", func(t *testing.T) {
		m := orientQuarters(32, g, y, r, b)
		want := [4]color.Color{g, y, r, b}
		for _, f := range [][]byte{
			encoded(t, m),
			orientJPEG(t, m, binary.BigEndian, 0),
			orientJPEG(t, m, binary.BigEndian, 9),
		} {
			seen := orientSeen(t, f)
			for i := range seen {
				near(t, want[i], seen[i])
			}
		}
	})
}

// TestTheBoundIsOnTheStoredPixels: an image is held to [portrait.MaxSide] on
// the sides its header says, before it is decoded and before it is turned, as
// before the tag was read. A quarter turn swaps the two sides and changes
// neither, so the tag cannot make an image larger than the bound smaller, nor
// a smaller one larger; and it is the renditions that are turned, at most 128
// pixels a side, so the turn costs nothing the bound has not paid for.
func TestTheBoundIsOnTheStoredPixels(t *testing.T) {
	x := require.New(t)
	m := paint(portrait.MaxSide, 600, func(x, y int) color.Color { return orientRed })

	p, err := portrait.Render(t.Context(), orientJPEG(t, m, binary.BigEndian, 6))
	x.NoError(err, "an image within the bound, to be turned upright, was refused")
	x.Equal([]uint32{32, 64, 128}, sizes(p))

	tall := paint(portrait.MaxSide+1, 16, func(x, y int) color.Color { return orientRed })
	_, err = portrait.Render(t.Context(), orientJPEG(t, tall, binary.BigEndian, 6))
	x.ErrorIs(err, portrait.ErrTooLarge, "a tag that turns it upright took an image over the bound")
}
