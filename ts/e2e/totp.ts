import { createHmac } from 'node:crypto'

// The authenticator app, in twenty lines: RFC 6238 over the `otpauth://` URI
// the account page shows. Written here rather than pulled in so that the test
// checks roster's arithmetic against an independent one.
export function totp(uri: string, at: Date = new Date(), skew = 0): string {
	const u = new URL(uri)
	const secret = base32(u.searchParams.get('secret') ?? '')
	const digits = Number(u.searchParams.get('digits') ?? 6)
	const period = Number(u.searchParams.get('period') ?? 30)
	const algorithm = (u.searchParams.get('algorithm') ?? 'SHA1').toLowerCase()

	const step = Math.floor(at.getTime() / 1000 / period) + skew
	const counter = Buffer.alloc(8)
	counter.writeBigUInt64BE(BigInt(step))
	const mac = createHmac(algorithm, secret).update(counter).digest()
	const offset = (mac[mac.length - 1] ?? 0) & 0xf
	const code = (mac.readUInt32BE(offset) & 0x7fffffff) % 10 ** digits
	return String(code).padStart(digits, '0')
}

function base32(s: string): Buffer {
	const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
	let bits = 0
	let value = 0
	const out: number[] = []
	for (const c of s.toUpperCase().replace(/=+$/, '')) {
		const v = alphabet.indexOf(c)
		if (v < 0) continue
		value = (value << 5) | v
		bits += 5
		if (bits >= 8) {
			out.push((value >>> (bits - 8)) & 0xff)
			bits -= 8
		}
	}
	return Buffer.from(out)
}
