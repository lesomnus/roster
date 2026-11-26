import { expect, test } from '@playwright/test'

import { MAX_SIDE, smaller } from '#lib/portrait.js'

// How large a picture the page draws to send, which is arithmetic and needs
// neither a deployment nor a browser: this spec runs alone as well as with the
// others (`npx playwright test e2e/picture.spec.ts`).
//
// `Portray` takes an image of at most `portrait.MaxSide` pixels a side, and
// said a larger one was made smaller by the page that sends it; the page sent
// the file as it was picked, so a phone's photograph of 3024x1964 was answered
// `image: too large: 3024x1964 pixels, and the most is 2048 a side` and the
// person resized it by hand. What is drawn is no larger than the bound on its
// longer side and of the same proportions, and an image within the bound is
// not drawn at all: it is sent as it was picked.
//
// What only a browser shows is not here: that the canvas is drawn the way up a
// person sees the photograph (its orientation tag honoured), and what the JPEG
// drawn from it weighs.

test('a picture larger than roster takes is drawn as large as it takes, in the same proportions', () => {
	expect(MAX_SIDE).toBe(2048)

	expect(smaller(3024, 1964)).toEqual({ width: 2048, height: 1330 })
	expect(smaller(1964, 3024)).toEqual({ width: 1330, height: 2048 })
	expect(smaller(4032, 3024)).toEqual({ width: 2048, height: 1536 })
	expect(smaller(2049, 2049)).toEqual({ width: 2048, height: 2048 })
	expect(smaller(2049, 100)).toEqual({ width: 2048, height: 100 })

	// A sliver keeps a pixel of its narrow side rather than none.
	expect(smaller(100_000, 1)).toEqual({ width: 2048, height: 1 })
})

test('a picture within what roster takes is sent as it was picked', () => {
	expect(smaller(2048, 2048)).toBeUndefined()
	expect(smaller(2048, 1300)).toBeUndefined()
	expect(smaller(640, 480)).toBeUndefined()
	expect(smaller(1, 1)).toBeUndefined()
})
