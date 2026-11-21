import * as THREE from 'three'

const SEGMENTS = 96
const FREE_STEPS = SEGMENTS * 6
const RETURN_STEPS = 48
const SECTIONS = FREE_STEPS + RETURN_STEPS + 1
const DENSE_FREE = SEGMENTS * 10
const DENSE_RETURN = 96
const DENSE_COUNT = DENSE_FREE + DENSE_RETURN + 1
const STEP = 1 / 120
const WIDTH = .27
const THICKNESS = .026
const LENGTH = 21.30
const RADIUS = Math.hypot(WIDTH / 2, THICKNESS / 2)
const SCALE = .84
const PHOTO_Y = 4.1 / 2 - 512 / 1448 * 4.1
const BACK_Z = -.30
const CENTRE_Z = BACK_Z - RADIUS / SCALE - .06
const CORNERS = [[1, 1], [-1, 1], [-1, -1], [1, -1]] as const

/** World-space particles: only the two pinned ends inherit the clasp's motion. */
export class WovenRibbon {
	readonly freeSegments = FREE_STEPS
	readonly geometry = new THREE.BufferGeometry()
	readonly mesh: THREE.Mesh
	readonly sway = new THREE.Vector3()
	private readonly points = Array.from({ length: SEGMENTS + 1 }, () => new THREE.Vector3())
	private readonly smoothPoints = this.points.map(() => new THREE.Vector3())
	private readonly smoothScratch = this.points.map(() => new THREE.Vector3())
	private readonly renderPoints = Array.from({ length: SECTIONS }, () => new THREE.Vector3())
	private readonly samples = Array.from({ length: DENSE_COUNT }, () => new THREE.Vector3())
	private readonly corrections = Array.from({ length: DENSE_COUNT }, () => new THREE.Vector3())
	private readonly arc = new Float64Array(DENSE_COUNT)
	private readonly tangents = this.renderPoints.map(() => new THREE.Vector3())
	private readonly frames = this.renderPoints.map(() => new THREE.Vector3())
	private readonly velocities = this.points.map(() => new THREE.Vector3())
	private readonly positions = new Float32Array(SECTIONS * 8 * 3)
	private readonly normals = new Float32Array(SECTIONS * 8 * 3)
	private readonly uvs = new Float32Array(SECTIONS * 8 * 2)
	private readonly twist = new Float64Array(SEGMENTS + 1)
	private readonly twistVelocity = new Float64Array(SEGMENTS + 1)
	private readonly flattenTorque = new Float64Array(SEGMENTS + 1)
	private readonly inertia = new THREE.Matrix3()
	private readonly spin = new THREE.Vector3()
	private readonly desiredSpin = new THREE.Vector3()
	private readonly cross = new THREE.Vector3()
	private readonly rootGrain = new THREE.Vector3()
	private readonly contactNormal = new THREE.Vector3()
	private readonly bendU = new THREE.Vector3()
	private readonly bendV = new THREE.Vector3()
	private readonly bendA = new THREE.Vector3()
	private readonly bendB = new THREE.Vector3()
	private readonly bendC = new THREE.Vector3()
	private readonly across = new THREE.Vector3()
	private readonly normal = new THREE.Vector3()
	private readonly delta = new THREE.Vector3()
	private readonly velocity = new THREE.Vector3()
	private readonly centre = new THREE.Vector3()
	private readonly local = new THREE.Vector3()
	private readonly wind = new THREE.Vector3()
	private readonly windTarget = new THREE.Vector3()
	private readonly lastA = new THREE.Vector3()
	private readonly lastB = new THREE.Vector3()
	private readonly pinA = new THREE.Vector3()
	private readonly pinB = new THREE.Vector3()
	private readonly neckA = new THREE.Vector3()
	private readonly neckB = new THREE.Vector3()
	private readonly exitDirection = new THREE.Vector3()
	private readonly join = new THREE.Vector3()
	private readonly toBadge = new THREE.Matrix4()
	private readonly toWorld = new THREE.Matrix4()
	private readonly rotation = new THREE.Quaternion()
	private readonly forward = new THREE.Vector3(0, 0, 1)
	private readonly curve: THREE.CatmullRomCurve3
	private segmentLength = 0
	private lastTime = 0
	private simulatedTime = 0
	private pending = 0
	private gustRemaining = 0
	private windAngle = 0
	private randomState: number
	private tipIndex = SEGMENTS / 2
	private rootReady = false
	private readonly trailing: boolean
	private readonly liftSeconds: number

	constructor(face: THREE.Material, edge: THREE.Material, seed = Math.floor(Math.random() * 0x100000000), initial: { trailing?: boolean; liftSeconds?: number } = {}) {
		this.trailing = initial.trailing ?? false
		this.liftSeconds = initial.liftSeconds ?? 0
		this.randomState = seed >>> 0
		this.windAngle = this.random() * Math.PI * 2
		this.curve = new THREE.CatmullRomCurve3([...this.smoothPoints, this.join], true, 'centripetal')
		const indices: number[] = []
		for (let side = 0; side < 4; side++) {
			const start = indices.length
			for (let i = 0; i < SECTIONS - 1; i++) {
				const a = i * 8 + side * 2
				const b = (i + 1) * 8 + side * 2
				indices.push(a, b, a + 1, a + 1, b, b + 1)
			}
			this.geometry.addGroup(start, indices.length - start, side % 2 === 0 ? 0 : 1)
		}
		this.geometry.setIndex(indices)
		this.geometry.setAttribute('position', new THREE.BufferAttribute(this.positions, 3).setUsage(THREE.DynamicDrawUsage))
		this.geometry.setAttribute('normal', new THREE.BufferAttribute(this.normals, 3).setUsage(THREE.DynamicDrawUsage))
		this.geometry.setAttribute('uv', new THREE.BufferAttribute(this.uvs, 2))
		this.mesh = new THREE.Mesh(this.geometry, [face, edge])
		this.mesh.name = 'Independent inertial woven lanyard'
		this.mesh.frustumCulled = false
	}

	private random() {
		this.randomState = (this.randomState + 0x6D2B79F5) >>> 0
		let n = this.randomState
		n = Math.imul(n ^ n >>> 15, n | 1)
		n ^= n + Math.imul(n ^ n >>> 7, n | 61)
		return ((n ^ n >>> 14) >>> 0) / 0x100000000
	}

	private initialise(a: THREE.Vector3, b: THREE.Vector3) {
		// This is only a starting pose. No particle is pulled back to it later.
		const side = this.random() < .5 ? -1 : 1
		const middle = a.clone().lerp(b, .5)
		const controls = this.trailing
			? [[.7, .5, -.28], [3, -1.7, -.50], [3.5, -4, -.65], [.5, -5.3, -.50], [-3.3, -3.5, -.42], [-2.2, -1.1, -.35], [-.7, .5, -.30]]
			: [[-.6, .65, -.28], [-.55, 1.75, -.50], [.75, 2, -.65], [2.2, 1.2, -.50], [2.1, -.25, -.42], [1.25, -1, -.35], [.25, -.2, -.30]]
		const curve = new THREE.CatmullRomCurve3([a.clone(), ...controls.map(([x, y, z]) => middle.clone().add(new THREE.Vector3(x! * side, y!, z!))), b.clone()], false, 'centripetal')
		const samples = curve.getPoints(512)
		const freeLength = LENGTH - b.distanceTo(this.join) - this.join.distanceTo(a)
		// Fit the smooth, cusp-free starting loop to the same material length
		// for every seed without scaling its two attachment points apart.
		for (let pass = 0; pass < 3; pass++) {
			let length = 0
			for (let i = 1; i < samples.length; i++) length += samples[i]!.distanceTo(samples[i - 1]!)
			for (let i = 0; i < samples.length; i++) {
				this.local.copy(a).lerp(b, i / 512)
				samples[i]!.sub(this.local).multiplyScalar(freeLength / length).add(this.local)
			}
		}
		const distances = [0]
		for (let i = 1; i < samples.length; i++) distances.push(distances[i - 1]! + samples[i]!.distanceTo(samples[i - 1]!))
		const sampleStep = distances[512]! / SEGMENTS
		this.segmentLength = freeLength / SEGMENTS
		let j = 1
		for (let i = 0; i <= SEGMENTS; i++) {
			const distance = i * sampleStep
			while (j < 512 && distances[j]! < distance) j++
			this.points[i]!.copy(samples[j - 1]!).lerp(samples[j]!, (distance - distances[j - 1]!) / (distances[j]! - distances[j - 1]!))
		}
		for (let i = 0; i < SECTIONS; i++) for (let face = 0; face < 4; face++) for (let k = 0; k < 2; k++) {
			const vertex = i * 8 + face * 2 + k
			this.uvs[vertex * 2] = (i <= FREE_STEPS ? i / FREE_STEPS * freeLength : freeLength + (i - FREE_STEPS) / RETURN_STEPS * (LENGTH - freeLength)) / 1.85
			this.uvs[vertex * 2 + 1] = face === 2 ? 1 - k : k
		}
		for (let i = 0; i < 16; i++) this.constrain(a, b)
		for (let i = 0; i <= SEGMENTS; i++) {
			this.velocities[i]!.set(0, 0, 0)
			if (this.points[i]!.y < this.points[this.tipIndex]!.y) this.tipIndex = i
		}
		this.lastA.copy(a); this.lastB.copy(b)
		this.geometry.getAttribute('uv').needsUpdate = true
	}

	private project(a: number, b: number, length: number, stiffness = 1) {
		const wa = a < 2 || a >= SEGMENTS - 1 ? 0 : 1
		const wb = b < 2 || b >= SEGMENTS - 1 ? 0 : 1
		this.delta.subVectors(this.points[b]!, this.points[a]!)
		const distance = this.delta.length()
		if (distance < 1e-9 || wa + wb === 0) return
		const correction = (distance - length) / distance * stiffness / (wa + wb)
		this.points[a]!.addScaledVector(this.delta, wa * correction)
		this.points[b]!.addScaledVector(this.delta, -wb * correction)
	}

	private collide(point: THREE.Vector3) {
		this.local.copy(point).applyMatrix4(this.toBadge)
		// Contact only near the card. An infinite rotating plane would sweep
		// distant, freely floating sections around with the badge again.
		const outside = Math.max(Math.abs(this.local.x) - 1.45, -2.05 - PHOTO_Y - this.local.y, this.local.y - (2.05 - PHOTO_Y))
		if (outside < .6) {
			// Round the contact margin. A rectangular on/off margin creates a
			// depth cliff where adjacent links can be pushed to opposite sides.
			const edge = Math.max(0, outside - RADIUS / SCALE - .02)
			const limit = CENTRE_Z + edge * edge * 8
			if (this.local.z > limit) {
				this.local.z = limit
				point.copy(this.local).applyMatrix4(this.toWorld)
			}
		}
	}

	private place(index: number, from: number) {
		this.delta.subVectors(this.points[index]!, this.points[from]!)
		const distance = this.delta.length()
		if (distance > 1e-9) this.points[index]!.copy(this.points[from]!).addScaledVector(this.delta, this.segmentLength / distance)
	}

	private stretchError() {
		let error = 0
		for (let i = 0; i < SEGMENTS; i++) error = Math.max(error, Math.abs(this.points[i]!.distanceTo(this.points[i + 1]!) / this.segmentLength - 1))
		return error
	}

	private constrain(a: THREE.Vector3, b: THREE.Vector3, bend = true) {
		this.points[0]!.copy(a); this.points[SEGMENTS]!.copy(b)
		this.neckA.copy(a).addScaledVector(this.exitDirection, this.segmentLength)
		this.neckB.copy(b).addScaledVector(this.exitDirection, this.segmentLength)
		this.points[1]!.copy(this.neckA); this.points[SEGMENTS - 1]!.copy(this.neckB)
		for (let iteration = 0; iteration < 16; iteration++) {
			// Uniform flexural stiffness: no remembered crease at a material index.
			if (bend && iteration < 2) for (let i = 0; i < SEGMENTS - 1; i++) this.project(i, i + 2, this.segmentLength * 1.996, .015)
			// Long-span flexural stiffness suppresses small wrinkles uniformly,
			// leaving a few broad bends without installing fixed crease/hinge sites.
			if (bend && iteration === 0) for (let i = 2; i < SEGMENTS - 10; i++) this.project(i, i + 8, this.segmentLength * 7.91, .006)
			if (bend) this.limitCurvature()
			for (let i = 2; i < SEGMENTS - 1; i++) this.collide(this.points[i]!)
			for (let j = 0; j < SEGMENTS; j++) {
				const i = iteration % 2 === 0 ? j : SEGMENTS - 1 - j
				this.project(i, i + 1, this.segmentLength)
			}
		}
		// Large kinematic jumps need a global length projection; merely adding
		// local relaxation passes lets transient stretch diffuse too slowly.
		for (let pass = 0; pass < 128 && (this.stretchError() > .0001 || bend && this.curvatureError() > .002); pass++) {
			if (bend) this.limitCurvature()
			for (let i = 2; i < SEGMENTS - 1; i++) this.place(i, i - 1)
			for (let i = SEGMENTS - 2; i > 1; i--) this.place(i, i + 1)
			for (let i = 2; i < SEGMENTS - 1; i++) this.collide(this.points[i]!)
		}
	}

	private limitCurvature() {
		const minimumCosine = Math.cos(.40)
		for (let i = 1; i < SEGMENTS; i++) {
			this.bendU.subVectors(this.points[i]!, this.points[i - 1]!)
			this.bendV.subVectors(this.points[i + 1]!, this.points[i]!)
			const lengthU = this.bendU.length(), lengthV = this.bendV.length()
			if (lengthU < 1e-8 || lengthV < 1e-8) continue
			this.bendU.multiplyScalar(1 / lengthU); this.bendV.multiplyScalar(1 / lengthV)
			const cosine = THREE.MathUtils.clamp(this.bendU.dot(this.bendV), -1, 1)
			if (cosine >= minimumCosine) continue
			this.bendA.copy(this.bendU).multiplyScalar(cosine).sub(this.bendV).multiplyScalar(1 / lengthU)
			this.bendC.copy(this.bendV).multiplyScalar(-cosine).add(this.bendU).multiplyScalar(1 / lengthV)
			this.bendB.copy(this.bendA).add(this.bendC).negate()
			const wa = i - 1 < 2 || i - 1 >= SEGMENTS - 1 ? 0 : 1
			const wb = i < 2 || i >= SEGMENTS - 1 ? 0 : 1
			const wc = i + 1 < 2 || i + 1 >= SEGMENTS - 1 ? 0 : 1
			const denominator = wa * this.bendA.lengthSq() + wb * this.bendB.lengthSq() + wc * this.bendC.lengthSq()
			if (denominator < 1e-10) continue
			let correction = (minimumCosine - cosine) / denominator
			const largest = correction * Math.max(wa * this.bendA.length(), wb * this.bendB.length(), wc * this.bendC.length())
			if (largest > .03) correction *= .03 / largest
			this.points[i - 1]!.addScaledVector(this.bendA, correction * wa)
			this.points[i]!.addScaledVector(this.bendB, correction * wb)
			this.points[i + 1]!.addScaledVector(this.bendC, correction * wc)
		}
	}

	private curvatureError() {
		let error = 0
		for (let i = 1; i < SEGMENTS; i++) {
			this.bendU.subVectors(this.points[i]!, this.points[i - 1]!).normalize()
			this.bendV.subVectors(this.points[i + 1]!, this.points[i]!).normalize()
			error = Math.max(error, Math.cos(.40) - this.bendU.dot(this.bendV))
		}
		return error
	}

	private step(a: THREE.Vector3, b: THREE.Vector3) {
		this.simulatedTime += STEP
		const clampLift = ((a.y - this.points[0]!.y) + (b.y - this.points[SEGMENTS]!.y)) / (2 * STEP)
		this.gustRemaining -= STEP
		if (this.gustRemaining <= 0) {
			this.gustRemaining = 14 + this.random() * 10
			this.windAngle += Math.PI * (.65 + this.random() * .7)
			const strength = .20 + this.random() * .25
			this.windTarget.set(Math.cos(this.windAngle) * strength, (this.random() - .5) * .18, Math.sin(this.windAngle) * strength * .45)
		}
		this.wind.lerp(this.windTarget, 1 - Math.exp(-STEP / 4))
		this.centre.set(0, 0, 0)
		for (let i = 2; i < SEGMENTS - 1; i++) this.centre.add(this.points[i]!)
		this.centre.multiplyScalar(1 / (SEGMENTS - 3))
		// Remove only coherent rigid rotation, leaving translation and local
		// bending inertia intact. Linear air drag alone lets the loop keep spinning.
		let xx = 0, yy = 0, zz = 0, xy = 0, xz = 0, yz = 0
		this.spin.set(0, 0, 0)
		for (let i = 2; i < SEGMENTS - 1; i++) {
			this.delta.copy(this.points[i]!).sub(this.centre)
			this.velocity.copy(this.velocities[i]!)
			this.spin.add(this.cross.crossVectors(this.delta, this.velocity))
			const { x, y, z } = this.delta
			xx += y * y + z * z; yy += x * x + z * z; zz += x * x + y * y
			xy -= x * y; xz -= x * z; yz -= y * z
		}
		const regularisation = (xx + yy + zz) * .00001 + .000001
		this.inertia.set(xx + regularisation, xy, xz, xy, yy + regularisation, yz, xz, yz, zz + regularisation).invert()
		this.spin.applyMatrix3(this.inertia)
		this.desiredSpin.copy(this.spin).multiplyScalar(Math.exp(-3 * STEP)).clampLength(0, .08)
		this.spin.sub(this.desiredSpin)
		const middleX = (a.x + b.x) / 2, middleY = (a.y + b.y) / 2, middleZ = (a.z + b.z) / 2
		for (let i = 2; i < SEGMENTS - 1; i++) {
			const p = this.points[i]!
			const s = i / SEGMENTS
			const freedom = Math.sin(s * Math.PI)
			this.delta.copy(p).sub(this.centre)
			this.velocity.copy(this.velocities[i]!).sub(this.cross.crossVectors(this.spin, this.delta)).multiplyScalar(Math.exp(-.75 * STEP))
			// Transmit the rising clamp's momentum gradually along the loose
			// loop. Once the card stops there is no launch force: it coasts.
			if (this.simulatedTime <= this.liftSeconds && clampLift > this.velocity.y) {
				this.velocity.y += (clampLift - this.velocity.y) * (1 - Math.exp(-STEP * 1.6)) * freedom
			}
			// Only the centre of mass has a weak buoyant confinement. There are
			// no per-particle rest positions or rotation inherited from the badge.
			this.velocity.x += (this.wind.x * freedom - (this.centre.x - middleX) * .07) * STEP
			this.velocity.y += (this.wind.y * freedom + (middleY + 1.2 - this.centre.y) * .11) * STEP
			// A weak world-depth envelope prevents the whole loop rolling edge-on
			// while still allowing out-of-plane bends and delayed clasp following.
			this.velocity.z += (this.wind.z * freedom + (middleZ - .45 - p.z) * .30) * STEP
			this.velocities[i]!.copy(this.velocity)
			p.addScaledVector(this.velocity, STEP)
			// A weak elastic face torque gradually relaxes edge-on folds. It
			// permits either broad face and never snaps a section to a plane.
			const torque = (this.twist[i - 1]! + this.twist[i + 1]! - this.twist[i]! * 2) * 12 - this.twist[i]! * .45
				+ this.flattenTorque[i]! * .65 + this.wind.z * .25 * freedom
			this.twistVelocity[i] = this.twistVelocity[i]! * Math.exp(-STEP * 2) + torque * STEP
			this.twist[i] = THREE.MathUtils.clamp(this.twist[i]! + this.twistVelocity[i]! * STEP, -.45, .45)
		}
		this.velocities[0]!.copy(a).sub(this.points[0]!).multiplyScalar(1 / STEP)
		this.velocities[SEGMENTS]!.copy(b).sub(this.points[SEGMENTS]!).multiplyScalar(1 / STEP)
		this.velocities[1]!.copy(a).addScaledVector(this.exitDirection, this.segmentLength).sub(this.points[1]!).multiplyScalar(1 / STEP)
		this.velocities[SEGMENTS - 1]!.copy(b).addScaledVector(this.exitDirection, this.segmentLength).sub(this.points[SEGMENTS - 1]!).multiplyScalar(1 / STEP)
		this.constrain(a, b)
		// Project velocities separately from positional corrections. Feeding the
		// entire length/bend correction back into Verlet velocity pumps spin into
		// the loop even when the external air current is gentle.
		for (let pass = 0; pass < 12; pass++) for (let i = 0; i < SEGMENTS; i++) {
			const wa = i < 2 || i >= SEGMENTS - 1 ? 0 : 1, wb = i + 1 < 2 || i + 1 >= SEGMENTS - 1 ? 0 : 1
			if (wa + wb === 0) continue
			this.delta.subVectors(this.points[i + 1]!, this.points[i]!).normalize()
			const speed = this.cross.subVectors(this.velocities[i + 1]!, this.velocities[i]!).dot(this.delta) / (wa + wb)
			this.velocities[i]!.addScaledVector(this.delta, speed * wa)
			this.velocities[i + 1]!.addScaledVector(this.delta, -speed * wb)
		}
		this.contactNormal.setFromMatrixColumn(this.toWorld, 2).normalize()
		for (let i = 2; i < SEGMENTS - 1; i++) {
			this.local.copy(this.points[i]!).applyMatrix4(this.toBadge)
			const outside = Math.max(Math.abs(this.local.x) - 1.45, -2.05 - PHOTO_Y - this.local.y, this.local.y - (2.05 - PHOTO_Y))
			const edge = Math.max(0, outside - RADIUS / SCALE - .02)
			if (outside < .6 && this.local.z > CENTRE_Z + edge * edge * 8 - .0001) {
				const towardCard = this.velocities[i]!.dot(this.contactNormal)
				if (towardCard > 0) this.velocities[i]!.addScaledVector(this.contactNormal, -towardCard)
			}
		}
	}

	/** Length correction with fixed ends and zero endpoint derivative. */
	private fitLength(nodes: THREE.Vector3[], start: number, end: number, target: number) {
		this.pinA.copy(nodes[start]!); this.pinB.copy(nodes[end]!)
		for (let i = start; i <= end; i++) {
			const s = (i - start) / (end - start)
			this.local.copy(this.pinA).lerp(this.pinB, s)
			this.corrections[i]!.copy(nodes[i]!).sub(this.local).multiplyScalar(Math.pow(Math.sin(s * Math.PI), 2))
		}
		let scale = 1
		for (let pass = 0; pass < 6; pass++) {
			let length = 0, derivative = 0
			for (let i = start + 1; i <= end; i++) {
				this.delta.subVectors(nodes[i]!, nodes[i - 1]!)
				this.cross.subVectors(this.corrections[i]!, this.corrections[i - 1]!)
				this.delta.addScaledVector(this.cross, scale - 1)
				const edge = this.delta.length()
				length += edge
				if (edge > 1e-9) derivative += this.delta.dot(this.cross) / edge
			}
			if (Math.abs(length - target) < 1e-8 || Math.abs(derivative) < 1e-8) break
			scale = THREE.MathUtils.clamp(scale - (length - target) / derivative, .05, 1.35)
		}
		for (let i = start; i <= end; i++) nodes[i]!.addScaledVector(this.corrections[i]!, scale - 1)
	}

	private resample(start: number, end: number, outStart: number, outEnd: number) {
		this.arc[start] = 0
		for (let i = start + 1; i <= end; i++) this.arc[i] = this.arc[i - 1]! + this.samples[i]!.distanceTo(this.samples[i - 1]!)
		let j = start + 1
		for (let i = outStart; i <= outEnd; i++) {
			const distance = (i - outStart) / (outEnd - outStart) * this.arc[end]!
			while (j < end && this.arc[j]! < distance) j++
			const span = this.arc[j]! - this.arc[j - 1]!
			this.renderPoints[i]!.copy(this.samples[j - 1]!).lerp(this.samples[j]!, span > 1e-9 ? (distance - this.arc[j - 1]!) / span : 0)
		}
		this.renderPoints[outStart]!.copy(this.samples[start]!)
		this.renderPoints[outEnd]!.copy(this.samples[end]!)
	}

	private surface(badgeMatrix: THREE.Matrix4, elapsed: number) {
		for (let i = 0; i <= SEGMENTS; i++) this.smoothPoints[i]!.copy(this.points[i]!)
		// Fair the display centerline before tessellation. The physical particles
		// keep their constraints/inertia; the visible strip does not inherit a
		// one-link corner or a contact solver's high-frequency zigzag.
		for (let pass = 0; pass < 3; pass++) {
			for (let i = 2; i < SEGMENTS - 1; i++) this.smoothScratch[i]!.copy(this.smoothPoints[i]!).multiplyScalar(6)
				.addScaledVector(this.smoothPoints[i - 1]!, 4).addScaledVector(this.smoothPoints[i + 1]!, 4)
				.add(this.smoothPoints[Math.max(0, i - 2)]!).add(this.smoothPoints[Math.min(SEGMENTS, i + 2)]!).multiplyScalar(1 / 16)
			for (let i = 2; i < SEGMENTS - 1; i++) {
				const fromClamp = Math.min(i - 1, SEGMENTS - 1 - i) * this.segmentLength
				this.smoothPoints[i]!.lerp(this.smoothScratch[i]!, THREE.MathUtils.smoothstep(fromClamp, 0, .5))
			}
		}
		const split = SEGMENTS / (SEGMENTS + 2)
		for (let i = 0; i <= DENSE_FREE; i++) this.curve.getPoint(i / DENSE_FREE * split, this.samples[i]!)
		for (let i = 1; i <= DENSE_RETURN; i++) this.curve.getPoint(split + i / DENSE_RETURN * (1 - split), this.samples[DENSE_FREE + i]!)
		const freeLength = this.segmentLength * SEGMENTS
		this.fitLength(this.samples, 0, DENSE_FREE, freeLength)
		this.fitLength(this.samples, DENSE_FREE, DENSE_COUNT - 1, LENGTH - freeLength)
		this.resample(0, DENSE_FREE, 0, FREE_STEPS)
		this.resample(DENSE_FREE, DENSE_COUNT - 1, FREE_STEPS, SECTIONS - 1)
		// Restore the exact material lengths after tessellation too. The correction
		// vanishes with zero slope at the held ends, preserving smooth joins.
		this.fitLength(this.renderPoints, 0, FREE_STEPS, freeLength)
		this.fitLength(this.renderPoints, FREE_STEPS, SECTIONS - 1, LENGTH - freeLength)
		this.renderPoints[SECTIONS - 1]!.copy(this.renderPoints[0]!)
		for (let i = 0; i < SECTIONS - 1; i++) {
			const before = this.renderPoints[(i + SECTIONS - 2) % (SECTIONS - 1)]!
			const after = this.renderPoints[(i + 1) % (SECTIONS - 1)]!
			this.tangents[i]!.subVectors(after, before).normalize()
		}
		this.tangents[SECTIONS - 1]!.copy(this.tangents[0]!)
		this.local.setFromMatrixColumn(badgeMatrix, 0).normalize()
		this.local.addScaledVector(this.tangents[0]!, -this.local.dot(this.tangents[0]!)).normalize()
		if (this.local.lengthSq() < .5) this.local.crossVectors(this.tangents[0]!, this.forward).normalize()
		if (!this.rootReady) { this.rootGrain.copy(this.local); this.rootReady = true }
		else {
			this.rootGrain.addScaledVector(this.tangents[0]!, -this.rootGrain.dot(this.tangents[0]!)).normalize()
			if (this.rootGrain.dot(this.local) < 0) this.local.negate()
			this.rootGrain.lerp(this.local, 1 - Math.exp(-elapsed * 3)).normalize()
		}
		this.frames[0]!.copy(this.rootGrain)
		for (let i = 1; i <= FREE_STEPS; i++) {
			this.rotation.setFromUnitVectors(this.tangents[i - 1]!, this.tangents[i]!)
			this.frames[i]!.copy(this.frames[i - 1]!).applyQuaternion(this.rotation).normalize()
		}
		// Keep the visible band rotation-minimising. Unwrapping an end-alignment
		// angle through successive poses stores whole turns in the material;
		// the small elastic twist field cannot unwind that separate global roll.
		// Let the grain swivel inside the crimp instead, retaining the damped
		// local face torque and closing the frame only on the concealed return.
		for (let i = 1; i <= FREE_STEPS; i++) {
			const s = i / FREE_STEPS, at = s * SEGMENTS, index = Math.min(SEGMENTS - 1, Math.floor(at)), f = at - index
			const p0 = this.twist[Math.max(0, index - 1)]!, p1 = this.twist[index]!, p2 = this.twist[index + 1]!, p3 = this.twist[Math.min(SEGMENTS, index + 2)]!
			const v0 = (p2 - p0) / 2, v1 = (p3 - p1) / 2, f2 = f * f, f3 = f2 * f
			const twist = (2 * f3 - 3 * f2 + 1) * p1 + (f3 - 2 * f2 + f) * v0 + (-2 * f3 + 3 * f2) * p2 + (f3 - f2) * v1
			this.frames[i]!.applyAxisAngle(this.tangents[i]!, twist)
		}
		for (let i = 2; i < SEGMENTS - 1; i++) {
			const at = i * (FREE_STEPS / SEGMENTS)
			this.normal.crossVectors(this.frames[at]!, this.tangents[at]!).normalize()
			// The elastic energy 1 - (normal · forward)^2 treats front and back
			// equally, so untwisting does not introduce 180-degree face flips.
			this.flattenTorque[i] = this.normal.dot(this.forward)
				* this.tangents[at]!.dot(this.cross.crossVectors(this.normal, this.forward))
		}
		for (let i = FREE_STEPS + 1; i < SECTIONS; i++) {
			this.rotation.setFromUnitVectors(this.tangents[i - 1]!, this.tangents[i]!)
			this.frames[i]!.copy(this.frames[i - 1]!).applyQuaternion(this.rotation).normalize()
			// Settle onto the crimp's flat grain just inside its mouth, above
			// the plate. Spreading this closure across the entire short return
			// would turn its broad edge forward through the back of the card.
			this.local.copy(this.rootGrain).addScaledVector(this.tangents[i]!, -this.rootGrain.dot(this.tangents[i]!)).normalize()
			const roll = Math.atan2(this.tangents[i]!.dot(this.cross.crossVectors(this.frames[i]!, this.local)), this.frames[i]!.dot(this.local))
			this.frames[i]!.applyAxisAngle(this.tangents[i]!, roll * THREE.MathUtils.smoothstep(i - FREE_STEPS, 0, 6))
		}
		let length = 0, frontZ = -Infinity
		for (let i = 0; i < SECTIONS - 1; i++) {
			const point = this.renderPoints[i]!
			length += point.distanceTo(this.renderPoints[i + 1]!)
			// The overlapped ends are compressed inside the crimp. Blend back to
			// full strap width smoothly, without a sudden shoulder at its mouth.
			const heldDistance = i <= FREE_STEPS ? Math.min(i, FREE_STEPS - i) / FREE_STEPS * freeLength : 0
			const width = THREE.MathUtils.lerp(.14, WIDTH, THREE.MathUtils.smoothstep(heldDistance, 0, .22))
			this.across.copy(this.frames[i]!)
			this.normal.crossVectors(this.across, this.tangents[i]!).normalize()
			for (let face = 0; face < 4; face++) for (let k = 0; k < 2; k++) {
				const [w, d] = CORNERS[(face + k) % 4]!
				const vertex = i * 8 + face * 2 + k
				this.local.copy(point).addScaledVector(this.across, w * width / 2).addScaledVector(this.normal, d * THICKNESS / 2)
				this.positions[vertex * 3] = this.local.x; this.positions[vertex * 3 + 1] = this.local.y; this.positions[vertex * 3 + 2] = this.local.z
				this.local.applyMatrix4(this.toBadge)
				if (Math.abs(this.local.x) < 1.45 && this.local.y > -2.05 - PHOTO_Y && this.local.y < 2.05 - PHOTO_Y) frontZ = Math.max(frontZ, this.local.z)
				const n = face % 2 === 0 ? this.normal : this.across, sign = face === 0 || face === 3 ? 1 : -1
				this.normals[vertex * 3] = n.x * sign; this.normals[vertex * 3 + 1] = n.y * sign; this.normals[vertex * 3 + 2] = n.z * sign
			}
		}
		this.positions.set(this.positions.subarray(0, 24), (SECTIONS - 1) * 24)
		this.normals.set(this.normals.subarray(0, 24), (SECTIONS - 1) * 24)
		this.geometry.getAttribute('position').needsUpdate = true
		this.geometry.getAttribute('normal').needsUpdate = true
		return { length, frontZ: frontZ === -Infinity ? BACK_Z - .01 : frontZ }
	}

	update(time: number, a: THREE.Vector3, b: THREE.Vector3, badgeMatrix: THREE.Matrix4, still = false, join?: THREE.Vector3) {
		const elapsed = still ? 0 : Math.min(.08, Math.max(0, time - this.lastTime))
		this.toWorld.copy(badgeMatrix); this.toBadge.copy(badgeMatrix).invert()
		this.exitDirection.set(0, 1, -.35).transformDirection(badgeMatrix)
		if (join !== undefined) this.join.copy(join)
		else {
			this.join.copy(a).lerp(b, .5)
			this.delta.set(0, -.07, -.03).transformDirection(badgeMatrix).multiplyScalar(Math.hypot(.07, .03))
			this.join.add(this.delta)
		}
		if (this.segmentLength === 0) this.initialise(a, b)
		this.pending += elapsed
		this.lastTime = time
		const steps = Math.floor(this.pending / STEP)
		for (let i = 0; i < steps; i++) {
			this.pinA.copy(this.lastA).lerp(a, (i + 1) / steps)
			this.pinB.copy(this.lastB).lerp(b, (i + 1) / steps)
			this.step(this.pinA, this.pinB)
			this.pending -= STEP
		}
		if (this.points[0]!.distanceToSquared(a) > 1e-12 || this.points[SEGMENTS]!.distanceToSquared(b) > 1e-12) this.constrain(a, b, false)
		this.lastA.copy(a); this.lastB.copy(b)
		let maxStretch = 0
		this.centre.set(0, 0, 0)
		for (let i = 0; i <= SEGMENTS; i++) {
			this.centre.add(this.points[i]!)
			if (i > 0) maxStretch = Math.max(maxStretch, Math.abs(this.points[i]!.distanceTo(this.points[i - 1]!) / this.segmentLength - 1))
		}
		const surface = this.surface(badgeMatrix, elapsed)
		this.centre.multiplyScalar(1 / (SEGMENTS + 1))
		this.sway.copy(this.centre).applyMatrix4(this.toBadge)
		this.sway.x *= .15; this.sway.y = 0; this.sway.z = (this.sway.z + .8) * .15
		return {
			tip: this.renderPoints[this.tipIndex * (FREE_STEPS / SEGMENTS)]!, length: surface.length, restLength: LENGTH,
			maxStretch, frontZ: surface.frontZ,
			anchorError: Math.max(this.points[0]!.distanceTo(a), this.points[SEGMENTS]!.distanceTo(b)),
			wind: this.wind,
		}
	}
}
