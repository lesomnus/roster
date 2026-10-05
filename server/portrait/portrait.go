// Package portrait is a picture of somebody as roster keeps one: an image
// handed over once, rendered at the sizes a screen draws a person, and kept on
// their row as `data:` URIs.
//
// # Why roster renders rather than keeping what it was handed
//
// Because then what is on the row is bounded by this file and not by whoever
// called. A caller handing over a portrait could hand over a megabyte of one,
// or three sizes that are three different people, and every `Get` and every
// page of people would carry what they chose. Rendered here, a portrait is the
// same few kilobytes whoever wrote it -- which is what lets a row hold one at
// all -- and a caller sends the one thing it has, which is an image.
//
// # Why the image is decoded at all
//
// Measuring is not enough: a JPEG says how large it is in its header, and a
// header is something anybody can write. Decoding is the check that it is a
// picture, the crop and the scaling need the pixels anyway, and re-encoding
// drops whatever else the file carried -- a camera's metadata, which can be
// where a photograph was taken.
package portrait

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/url"

	// The formats an image may arrive in, registered for `image.Decode`.
	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	rstr "github.com/lesomnus/roster/rstr"
)

// Sizes are the renditions a portrait holds, in pixels a side, smallest first.
// `holder.ext.proto` says why these.
var Sizes = []int{32, 64, 128}

// PageSize is the one rendition a page of people carries; see [Page].
const PageSize = 64

const (
	// MaxBytes is the most an image may weigh. Below gRPC's default of four
	// mebibytes for a whole message, with room for the rest of the request.
	MaxBytes = 2 << 20

	// MaxSide is the most pixels an image may have on a side. Read from the
	// header before anything is decoded, because a small file can claim a vast
	// image and decoding one costs the memory the claim says.
	MaxSide = 4096

	// MaxURL is the longest `profile.picture` roster keeps.
	MaxURL = 2048
)

// quality is what each rendition is encoded at. High, because at 128 pixels
// the difference between this and the encoder's default is a few hundred
// bytes and the difference on a face is visible.
const quality = 85

var (
	// ErrNotAnImage is bytes that do not decode as one of the formats above.
	ErrNotAnImage = errors.New("not an image this reads: JPEG, PNG, GIF or WebP")

	// ErrTooLarge is an image over [MaxBytes] or [MaxSide].
	ErrTooLarge = errors.New("too large")

	// ErrURL is a picture URL that is not one roster hands a page.
	ErrURL = errors.New("an https URL, which a page fetches as it is")
)

// Render is the portrait an image makes: the square in its middle, at each of
// [Sizes] no larger than that square, as JPEG.
//
// An image smaller than the largest size is not enlarged. Its own size is the
// largest rendition instead, which is a smaller portrait and a truthful one.
func Render(b []byte) (_ *rstr.Portrait, err error) {
	if len(b) > MaxBytes {
		return nil, fmt.Errorf("%w: %d bytes, and the most is %d", ErrTooLarge, len(b), MaxBytes)
	}

	c, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || c.Width < 1 || c.Height < 1 {
		return nil, ErrNotAnImage
	}
	if c.Width > MaxSide || c.Height > MaxSide {
		return nil, fmt.Errorf("%w: %dx%d pixels, and the most is %d a side", ErrTooLarge, c.Width, c.Height, MaxSide)
	}

	// A decoder that panics on what it was handed has still been handed
	// something that is not a picture, and the answer to that is a refusal
	// rather than a process gone.
	defer func() {
		if r := recover(); r != nil {
			err = ErrNotAnImage
		}
	}()

	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, ErrNotAnImage
	}

	r := src.Bounds()
	side := min(r.Dx(), r.Dy())
	from := image.Rect(0, 0, side, side).Add(r.Min).Add(image.Pt((r.Dx()-side)/2, (r.Dy()-side)/2))

	vs := []*rstr.Rendition{}
	for _, size := range sizes(side) {
		at := image.NewRGBA(image.Rect(0, 0, size, size))

		// White under it, because JPEG has no transparency and a transparent
		// pixel left as it is encodes as black.
		draw.Draw(at, at.Bounds(), image.White, image.Point{}, draw.Src)
		draw.CatmullRom.Scale(at, at.Bounds(), src, from, draw.Over, nil)

		var w bytes.Buffer
		if err := jpeg.Encode(&w, at, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}

		vs = append(vs, rstr.Rendition_builder{
			Size: uint32(size),
			Uri:  "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(w.Bytes()),
		}.Build())
	}

	return rstr.Portrait_builder{Renditions: vs}.Build(), nil
}

// sizes is which renditions a square this many pixels a side makes.
func sizes(side int) []int {
	vs := []int{}
	for _, s := range Sizes {
		if s >= side {
			return append(vs, side)
		}
		vs = append(vs, s)
	}

	return vs
}

// Page cuts a portrait down to what a page of people carries: one rendition,
// the largest no larger than [PageSize].
//
// # Why a page carries one
//
// A page is up to a hundred people, and every size of every one of them is a
// megabyte that the screen drawing the page has no use for -- it draws each
// person once, small. One rendition is a few kilobytes a row, and a screen
// that wants somebody larger asks for them with `Get`, which carries every
// size.
//
// It is safe to cut because nothing writes a portrait back. `Portray` and
// `Fill` take an image, and a profile write cannot reach this field
// (`holder.ext.proto`), so a page read and saved whole loses nothing.
func Page(p *rstr.Portrait) {
	vs := p.GetRenditions()
	if len(vs) < 2 {
		return
	}

	keep := vs[0]
	for _, v := range vs[1:] {
		if v.GetSize() > PageSize {
			break
		}
		keep = v
	}
	p.SetRenditions([]*rstr.Rendition{keep})
}

// CheckURL refuses a `profile.picture` roster will not hand a page. Empty is no
// picture, and is fine.
//
// https and nothing else. A page puts this in `src`, or in `href` for a link
// to the full picture, and a scheme that is not a fetch -- `javascript:`, a
// `data:` document -- is one a page can be made to run. Plain http is refused
// too: a page served over https will not load it, so it is a picture nobody
// can see.
func CheckURL(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > MaxURL {
		return fmt.Errorf("%w: %d bytes, and the most is %d", ErrURL, len(v), MaxURL)
	}

	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ErrURL
	}

	return nil
}
