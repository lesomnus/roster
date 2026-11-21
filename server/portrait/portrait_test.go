package portrait_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/portrait"
)

// paint is an image w by h, each pixel what at says.
func paint(w, h int, at func(x, y int) color.Color) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			m.Set(x, y, at(x, y))
		}
	}

	return m
}

func encoded(t *testing.T, m image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, m))

	return b.Bytes()
}

// decoded is a rendition read back the way a browser reads it: the URI, then
// the JPEG inside.
func decoded(t *testing.T, v *rstr.Rendition) image.Image {
	t.Helper()
	x := require.New(t)

	data, ok := strings.CutPrefix(v.GetUri(), "data:image/jpeg;base64,")
	x.True(ok, "not a JPEG data URI: %.40s", v.GetUri())
	b, err := base64.StdEncoding.DecodeString(data)
	x.NoError(err)
	m, err := jpeg.Decode(bytes.NewReader(b))
	x.NoError(err)

	return m
}

func near(t *testing.T, want, got color.Color) {
	t.Helper()
	wr, wg, wb, _ := want.RGBA()
	gr, gg, gb, _ := got.RGBA()
	for _, d := range []int{int(wr>>8) - int(gr>>8), int(wg>>8) - int(gg>>8), int(wb>>8) - int(gb>>8)} {
		require.LessOrEqual(t, max(d, -d), 24, "want %v, got %v", want, got)
	}
}

func sizes(p *rstr.Portrait) []uint32 {
	vs := []uint32{}
	for _, v := range p.GetRenditions() {
		vs = append(vs, v.GetSize())
	}

	return vs
}

// An image is kept at three sizes, smallest first, each one a JPEG of exactly
// the size it says -- which is what a page puts in `src` without looking.
func TestAnImageIsKeptAtEverySize(t *testing.T) {
	x := require.New(t)

	p, err := portrait.Render(t.Context(), encoded(t, paint(300, 300, func(int, int) color.Color { return color.NRGBA{200, 40, 40, 255} })))
	x.NoError(err)
	x.Equal([]uint32{32, 64, 128}, sizes(p))

	for _, v := range p.GetRenditions() {
		m := decoded(t, v)
		x.Equal(image.Rect(0, 0, int(v.GetSize()), int(v.GetSize())), m.Bounds())
		near(t, color.NRGBA{200, 40, 40, 255}, m.At(int(v.GetSize())/2, int(v.GetSize())/2))
	}
}

// Smaller than the largest size is not enlarged: its own size is the largest
// rendition, and nothing is made that would only be the same pixels bigger.
func TestASmallImageIsNotEnlarged(t *testing.T) {
	for _, c := range []struct {
		side int
		want []uint32
	}{
		{20, []uint32{20}},
		{32, []uint32{32}},
		{40, []uint32{32, 40}},
		{64, []uint32{32, 64}},
		{96, []uint32{32, 64, 96}},
		{128, []uint32{32, 64, 128}},
		{129, []uint32{32, 64, 128}},
	} {
		x := require.New(t)
		p, err := portrait.Render(t.Context(), encoded(t, paint(c.side, c.side, func(int, int) color.Color { return color.White })))
		x.NoError(err)
		x.Equal(c.want, sizes(p), "%d pixels a side", c.side)
	}
}

// The square kept is the one in the middle, which is where a face is in a
// picture somebody chose of themselves.
func TestTheMiddleIsKept(t *testing.T) {
	x := require.New(t)

	// Red, green, blue, side by side; the middle third is the square.
	m := paint(300, 100, func(x, _ int) color.Color {
		switch {
		case x < 100:
			return color.NRGBA{255, 0, 0, 255}
		case x < 200:
			return color.NRGBA{0, 255, 0, 255}
		default:
			return color.NRGBA{0, 0, 255, 255}
		}
	})
	p, err := portrait.Render(t.Context(), encoded(t, m))
	x.NoError(err)
	x.Equal([]uint32{32, 64, 100}, sizes(p))

	got := decoded(t, p.GetRenditions()[1])
	for _, at := range []image.Point{{4, 4}, {32, 32}, {59, 59}} {
		near(t, color.NRGBA{0, 255, 0, 255}, got.At(at.X, at.Y))
	}
}

// JPEG has no transparency, and a transparent pixel encoded as it is comes out
// black -- so what is under a picture with a clear background is white.
func TestWhatIsTransparentIsWhite(t *testing.T) {
	x := require.New(t)

	p, err := portrait.Render(t.Context(), encoded(t, paint(64, 64, func(int, int) color.Color { return color.NRGBA{} })))
	x.NoError(err)
	near(t, color.White, decoded(t, p.GetRenditions()[0]).At(16, 16))
}

// Every format a directory or a person hands over is read, and what is kept is
// JPEG whichever it was.
func TestEachFormatIsRead(t *testing.T) {
	m := paint(80, 80, func(int, int) color.Color { return color.NRGBA{30, 90, 200, 255} })

	var j, g bytes.Buffer
	require.NoError(t, jpeg.Encode(&j, m, nil))
	require.NoError(t, gif.Encode(&g, m, nil))

	// One pixel of lossy WebP, the probe browsers have been sent for years;
	// nothing in Go writes WebP, which is why roster does not keep it.
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	require.NoError(t, err)

	for name, b := range map[string][]byte{"jpeg": j.Bytes(), "png": encoded(t, m), "gif": g.Bytes(), "webp": webp} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			p, err := portrait.Render(t.Context(), b)
			x.NoError(err)
			x.NotEmpty(p.GetRenditions())
			decoded(t, p.GetRenditions()[0])
		})
	}
}

// What is not a picture is refused as one, whatever it claims to be.
func TestWhatIsNotAnImageIsRefused(t *testing.T) {
	var j bytes.Buffer
	require.NoError(t, jpeg.Encode(&j, paint(64, 64, func(int, int) color.Color { return color.Black }), nil))

	for name, b := range map[string][]byte{
		"nothing":   nil,
		"text":      []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>"),
		"truncated": j.Bytes()[:j.Len()/2],
	} {
		t.Run(name, func(t *testing.T) {
			_, err := portrait.Render(t.Context(), b)
			require.ErrorIs(t, err, portrait.ErrNotAnImage)
		})
	}
}

// Too large is refused before it is decoded: a few hundred bytes of PNG can say
// it is five thousand pixels wide, and believing it is the memory it costs.
func TestATooLargeImageIsRefusedBeforeItIsDecoded(t *testing.T) {
	t.Run("on a side", func(t *testing.T) {
		x := require.New(t)
		b := encoded(t, image.NewGray(image.Rect(0, 0, portrait.MaxSide+1, 1)))
		x.Less(len(b), 64<<10, "the point is a small file")

		_, err := portrait.Render(t.Context(), b)
		x.ErrorIs(err, portrait.ErrTooLarge)
	})
	t.Run("in bytes", func(t *testing.T) {
		_, err := portrait.Render(t.Context(), make([]byte, portrait.MaxBytes+1))
		require.ErrorIs(t, err, portrait.ErrTooLarge)
	})

	// What the wire takes is 2048 a side, and 4096 was: one image of that,
	// which a caller needs no role to send, cost a process up to 269 MiB to
	// render (#279). Held by a header that claims the old largest image and
	// has no such pixels behind it: read to find out, it is no image; refused
	// from its header, it is too large, and that is the answer.
	t.Run("and the most is 2048 a side, from the header alone", func(t *testing.T) {
		x := require.New(t)
		x.Equal(2048, portrait.MaxSide, "what a picture may be is said in `holder_svc.ext.proto` and in the manual, by this number")

		_, err := portrait.Render(t.Context(), claiming(t, 4096, 4096))
		x.ErrorIs(err, portrait.ErrTooLarge, "an image of the size that cost the most was decoded to find out what it is")
		x.ErrorContains(err, "2048", "the refusal does not say what the most is")

		_, err = portrait.Render(t.Context(), claiming(t, portrait.MaxSide, portrait.MaxSide))
		x.ErrorIs(err, portrait.ErrNotAnImage, "a header alone was taken for a picture")
	})
}

// claiming is a PNG whose header says it is w by h and that holds no such
// pixels: one pixel's worth, under a header rewritten and summed again.
func claiming(t *testing.T, w, h int) []byte {
	t.Helper()
	b := encoded(t, image.NewGray(image.Rect(0, 0, 1, 1)))

	// The signature, then the first chunk: its length, `IHDR`, thirteen bytes
	// that begin with the width and the height, and their sum.
	require.Equal(t, "IHDR", string(b[12:16]))
	binary.BigEndian.PutUint32(b[16:], uint32(w))
	binary.BigEndian.PutUint32(b[20:], uint32(h))
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))

	return b
}

// progressive is a progressive JPEG of w by h, its three components at full
// resolution and every coefficient zero: about three bits a block, and the
// costliest thing a decoder can be handed for its size, since a progressive
// image is every coefficient of every block kept until its last scan.
func progressive(w, h int) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xff, 0xd8})

	// One quantization table, all ones.
	b.Write([]byte{0xff, 0xdb, 0, 67, 0})
	b.Write(bytes.Repeat([]byte{1}, 64))

	// The frame: progressive, eight bits, three components each sampled once.
	b.Write([]byte{0xff, 0xc2, 0, 17, 8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 3, 1, 0x11, 0, 2, 0x11, 0, 3, 0x11, 0})

	// One table for the first coefficient, with one code, one bit long, that
	// says *the same as the block before*.
	b.Write([]byte{0xff, 0xc4, 0, 20, 0x00, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x00})

	// One scan of that coefficient for all three, which is that code once a
	// block.
	b.Write([]byte{0xff, 0xda, 0, 12, 3, 1, 0x00, 2, 0x00, 3, 0x00, 0, 0, 0})
	bits := 3 * ((w + 7) / 8) * ((h + 7) / 8)
	b.Write(make([]byte, (bits+7)/8+2))

	b.Write([]byte{0xff, 0xd9})

	return b.Bytes()
}

// TestTheLargestPictureIsRenderedWithinABound: what rendering an image costs
// a process is the image decoded whole and the scaler's rows beside it, and
// nothing a file weighs says how much that is. The largest the wire took, 4096
// pixels a side, allocated 93 MiB as 57 KiB of PNG, 157 MiB at sixteen bits a
// channel and 269 MiB as 98 KiB of progressive JPEG, and `Portray` is
// everybody's to call about their own row.
//
// The largest it takes now is held to under 96 MiB, whichever format it comes
// in: measured, 46 and 74 for the two costliest. A change to how an image is
// decoded or scaled that costs more than that for one image fails here, before
// it is a process's memory.
func TestTheLargestPictureIsRenderedWithinABound(t *testing.T) {
	const most = 96 << 20

	deep := image.NewNRGBA64(image.Rect(0, 0, portrait.MaxSide, portrait.MaxSide))
	for i := range deep.Pix {
		deep.Pix[i] = 0x7f
	}

	for _, c := range []struct {
		name string
		b    []byte
	}{
		{"a PNG at sixteen bits a channel", encoded(t, deep)},
		{"a progressive JPEG", progressive(portrait.MaxSide, portrait.MaxSide)},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := require.New(t)
			x.LessOrEqual(len(c.b), portrait.MaxBytes, "the wire would not take this, so it proves nothing")

			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			p, err := portrait.Render(t.Context(), c.b)
			runtime.ReadMemStats(&after)

			x.NoError(err)
			x.Equal([]uint32{32, 64, 128}, sizes(p))
			x.Less(after.TotalAlloc-before.TotalAlloc, uint64(most),
				"%d MiB to render one image of %d bytes", (after.TotalAlloc-before.TotalAlloc)>>20, len(c.b))
		})
	}
}

// A page carries one rendition, the largest no larger than the page's size.
func TestAPageCarriesOne(t *testing.T) {
	of := func(sizes ...uint32) *rstr.Portrait {
		vs := []*rstr.Rendition{}
		for _, s := range sizes {
			vs = append(vs, rstr.Rendition_builder{Size: s, Uri: "data:"}.Build())
		}

		return rstr.Portrait_builder{Renditions: vs}.Build()
	}

	for _, c := range []struct{ have, want []uint32 }{
		{[]uint32{32, 64, 128}, []uint32{64}},
		{[]uint32{32, 64, 96}, []uint32{64}},
		{[]uint32{32, 40}, []uint32{40}},
		{[]uint32{20}, []uint32{20}},
		{[]uint32{}, []uint32{}},
	} {
		p := of(c.have...)
		portrait.Page(p)
		require.Equal(t, c.want, sizes(p), "from %v", c.have)
	}

	portrait.Page(nil)
}

// A picture URL is https, or nothing.
func TestAPictureURLIsOneAPageFetches(t *testing.T) {
	for _, v := range []string{"", "https://lh3.googleusercontent.com/a/x=s96-c", "https://s3.example.com/pictures/ab.jpg"} {
		require.NoError(t, portrait.CheckURL(v), v)
	}
	for _, v := range []string{
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"data:image/jpeg;base64,AAAA",
		"http://example.com/a.jpg",
		"https://user:pass@example.com/a.jpg",
		"https:///a.jpg",
		"//example.com/a.jpg",
		"a picture",
		"https://example.com/" + strings.Repeat("a", portrait.MaxURL),
	} {
		require.ErrorIs(t, portrait.CheckURL(v), portrait.ErrURL, v)
	}
}
