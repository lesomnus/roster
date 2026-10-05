package portrait_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
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

	p, err := portrait.Render(encoded(t, paint(300, 300, func(int, int) color.Color { return color.NRGBA{200, 40, 40, 255} })))
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
		p, err := portrait.Render(encoded(t, paint(c.side, c.side, func(int, int) color.Color { return color.White })))
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
	p, err := portrait.Render(encoded(t, m))
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

	p, err := portrait.Render(encoded(t, paint(64, 64, func(int, int) color.Color { return color.NRGBA{} })))
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
			p, err := portrait.Render(b)
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
			_, err := portrait.Render(b)
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

		_, err := portrait.Render(b)
		x.ErrorIs(err, portrait.ErrTooLarge)
	})
	t.Run("in bytes", func(t *testing.T) {
		_, err := portrait.Render(make([]byte, portrait.MaxBytes+1))
		require.ErrorIs(t, err, portrait.ErrTooLarge)
	})
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
	for _, v := range []string{"", "https://lh3.googleusercontent.com/a/x=s96-c", "https://s3.hday.io/pictures/ab.jpg"} {
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
