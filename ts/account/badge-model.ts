import * as THREE from 'three'
import { RoomEnvironment } from 'three/addons/environments/RoomEnvironment.js'
import { WovenRibbon } from '#lib/badge-ribbon.js'
import { badgeArrival, BADGE_ARRIVAL_SECONDS } from '#lib/badge-arrival.js'

export interface BadgeIdentity {
	name: string
	alias: string
	brand: string
	department: string
	employeeNo: string
	portrait: string | undefined
}

export interface BadgeModel {
	update(identity: BadgeIdentity): void
	hold(active: boolean): void
	dispose(): void
}

const W = 2.9
const H = 4.1
// Rotate around the printed portrait, so the gaze never pulls it off centre.
const PHOTO_Y = H / 2 - (228 + 568 / 2) / 1448 * H
const FRONT_Z = .132
const BADGE_SCALE = .84
const DEG = Math.PI / 180

function roundPath(path: THREE.Path, x: number, y: number, w: number, h: number, r: number) {
	path.moveTo(x + r, y)
	path.lineTo(x + w - r, y)
	path.quadraticCurveTo(x + w, y, x + w, y + r)
	path.lineTo(x + w, y + h - r)
	path.quadraticCurveTo(x + w, y + h, x + w - r, y + h)
	path.lineTo(x + r, y + h)
	path.quadraticCurveTo(x, y + h, x, y + h - r)
	path.lineTo(x, y + r)
	path.quadraticCurveTo(x, y, x + r, y)
	path.closePath()
}

function plateShape(w = W, h = H) {
	const shape = new THREE.Shape()
	roundPath(shape, -w / 2, -h / 2, w, h, .17)
	const slot = new THREE.Path()
	roundPath(slot, -.34, h / 2 - .22, .68, .085, .04)
	shape.holes.push(slot)
	return shape
}

function brandMark(ctx: CanvasRenderingContext2D, x: number, y: number, size: number) {
	const square = size * .4
	for (const dx of [0, size - square]) for (const dy of [0, size - square]) ctx.fillRect(x + dx, y + dy, square, square)
}

function makeCanvas(width: number, height: number) {
	const canvas = document.createElement('canvas')
	canvas.width = width
	canvas.height = height
	const ctx = canvas.getContext('2d')
	if (ctx === null) throw new Error('Canvas textures are unavailable')
	const texture = new THREE.CanvasTexture(canvas)
	texture.colorSpace = THREE.SRGBColorSpace
	return { canvas, ctx, texture }
}

function fitText(ctx: CanvasRenderingContext2D, text: string, x: number, y: number, width: number, size: number, weight = 600) {
	let px = size
	do { ctx.font = `${weight} ${px}px system-ui, sans-serif`; px -= 2 } while (ctx.measureText(text).width > width && px > 20)
	ctx.fillText(text, x, y, width)
}

function drawStrap(ctx: CanvasRenderingContext2D, brand: string) {
	ctx.fillStyle = '#d9f642'
	ctx.fillRect(0, 0, 1024, 128)
	// Fine warp and weft stay in the material, including the unprinted edges.
	for (let x = 0; x < 1024; x += 4) {
		ctx.fillStyle = x % 8 === 0 ? '#26350920' : '#ffffff30'
		ctx.fillRect(x, 0, 1, 128)
	}
	ctx.fillStyle = '#2b3d0d'
	brandMark(ctx, 70, 35, 58)
	fitText(ctx, brand, 164, 93, 790, 84, 750)
	ctx.fillStyle = '#556c233a'
	ctx.fillRect(0, 5, 1024, 3)
	ctx.fillRect(0, 120, 1024, 3)
}

/** WebGL supplies the body and cloth; its front plane projects live HTML. */
export function mountBadge(stage: HTMLElement, viewport: HTMLElement, canvas: HTMLCanvasElement, bounds: HTMLElement, initial: BadgeIdentity): BadgeModel {
	const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true, powerPreference: 'low-power' })
	renderer.setPixelRatio(Math.min(devicePixelRatio, 1.5))
	renderer.outputColorSpace = THREE.SRGBColorSpace
	renderer.toneMapping = THREE.ACESFilmicToneMapping
	renderer.toneMappingExposure = 1
	const world = new THREE.Scene()
	const camera = new THREE.PerspectiveCamera(30, 1, .1, 80)
	camera.position.set(0, 0, 17)
	const room = new RoomEnvironment()
	const pmrem = new THREE.PMREMGenerator(renderer)
	const environment = pmrem.fromScene(room, .04)
	world.environment = environment.texture
	world.environmentIntensity = .4
	room.dispose()
	pmrem.dispose()
	world.add(new THREE.HemisphereLight(0xffffff, 0x303927, .65))
	const key = new THREE.DirectionalLight(0xfff9e8, 1.6)
	key.position.set(-4, 6, 8)
	world.add(key)
	const rim = new THREE.DirectionalLight(0xe3ffb4, 1.2)
	rim.position.set(5, 1, -2)
	world.add(rim)
	const fill = new THREE.DirectionalLight(0xe7edff, .6)
	fill.position.set(5, -2, 7)
	world.add(fill)

	const back = makeCanvas(512, 724)
	const strap = makeCanvas(1024, 128)
	strap.texture.wrapS = THREE.RepeatWrapping
	strap.texture.wrapT = THREE.ClampToEdgeWrapping
	for (const texture of [back.texture, strap.texture]) texture.anisotropy = Math.min(8, renderer.capabilities.getMaxAnisotropy())
	const badge = new THREE.Group()
	badge.scale.setScalar(BADGE_SCALE)
	world.add(badge)
	const plate = new THREE.Group()
	plate.position.set(0, -PHOTO_Y, -FRONT_Z)
	badge.add(plate)
	const shape = plateShape()
	const bodyGeometry = new THREE.ExtrudeGeometry(shape, { depth: .19, bevelEnabled: true, bevelSegments: 3, steps: 1, bevelSize: .035, bevelThickness: .035, curveSegments: 12 })
	bodyGeometry.translate(0, 0, -.095)
	const shell = new THREE.MeshPhysicalMaterial({ color: 0xf2f2e9, roughness: .29, clearcoat: 1, clearcoatRoughness: .16 })
	const edge = new THREE.MeshStandardMaterial({ color: 0x8e9784, metalness: .32, roughness: .3 })
	const body = new THREE.Mesh(bodyGeometry, [shell, edge])
	body.name = 'Bevelled badge with real thickness and a cut-out slot'
	plate.add(body)
	const faceGeometry = new THREE.ShapeGeometry(shape, 16)
	const facePositions = faceGeometry.getAttribute('position')
	const uv = faceGeometry.getAttribute('uv')
	for (let i = 0; i < facePositions.count; i++) uv.setXY(i, (facePositions.getX(i) + W / 2) / W, (facePositions.getY(i) + H / 2) / H)
	const print = new THREE.Mesh(faceGeometry, new THREE.MeshPhysicalMaterial({ color: 0xf2f2e9, roughness: .85, clearcoat: .08, clearcoatRoughness: .5, specularIntensity: .1 }))
	print.position.z = FRONT_Z
	plate.add(print)
	const reverse = new THREE.Mesh(faceGeometry, new THREE.MeshStandardMaterial({ map: back.texture, roughness: .52 }))
	reverse.rotation.y = Math.PI
	reverse.position.z = -FRONT_Z
	plate.add(reverse)

	const chrome = new THREE.MeshStandardMaterial({ color: 0xcbd1c5, metalness: .96, roughness: .19, envMapIntensity: 1.8 })
	const hardware = new THREE.Group()
	hardware.position.set(0, H / 2 - .1, .11)
	plate.add(hardware)
	// Two fabric ends overlap behind a flat crimp; no hook or split ring.
	const crimpShape = new THREE.Shape()
	roundPath(crimpShape, -.18, -.12, .36, .42, .04)
	const crimpGeometry = new THREE.ExtrudeGeometry(crimpShape, { depth: .055, bevelEnabled: true, bevelThickness: .009, bevelSize: .009, bevelSegments: 2, steps: 1 })
	const frontCrimp = new THREE.Mesh(crimpGeometry, chrome)
	frontCrimp.position.z = .045
	hardware.add(frontCrimp)
	const backCrimp = new THREE.Mesh(crimpGeometry, chrome)
	backCrimp.position.z = -.405
	hardware.add(backCrimp)
	const crown = new THREE.Mesh(new THREE.BoxGeometry(.36, .065, .51), chrome)
	crown.position.set(0, .26, -.155)
	hardware.add(crown)
	const seam = new THREE.Mesh(new THREE.BoxGeometry(.23, .018, .008), new THREE.MeshStandardMaterial({ color: 0x626b59, metalness: .7, roughness: .45 }))
	seam.position.set(0, .04, .112)
	hardware.add(seam)
	const weave = makeCanvas(64, 64)
	weave.texture.colorSpace = THREE.NoColorSpace
	weave.texture.wrapS = weave.texture.wrapT = THREE.RepeatWrapping
	weave.texture.repeat.set(18, 3)
	weave.ctx.fillStyle = '#777'; weave.ctx.fillRect(0, 0, 64, 64)
	for (let i = 0; i < 64; i += 4) {
		weave.ctx.fillStyle = '#bbb'; weave.ctx.fillRect(i, 0, 1, 64)
		weave.ctx.fillStyle = '#444'; weave.ctx.fillRect(0, i, 64, 1)
	}
	weave.texture.needsUpdate = true
	const cloth = new THREE.MeshPhysicalMaterial({ map: strap.texture, bumpMap: weave.texture, bumpScale: .012, roughness: .85, sheen: .35, sheenRoughness: .8, sheenColor: 0x8fa02a, specularIntensity: .25, side: THREE.DoubleSide })
	const selvedge = new THREE.MeshStandardMaterial({ color: 0x6d841d, roughness: .85, side: THREE.DoubleSide })
	const reduced = matchMedia('(prefers-reduced-motion: reduce)')
	const ribbon = new WovenRibbon(cloth, selvedge, undefined, { trailing: !reduced.matches, liftSeconds: BADGE_ARRIVAL_SECONDS })
	world.add(ribbon.mesh)

	let disposed = false
	let width = 1
	let height = 1
	let viewportTop = 0
	let frame = 0
	let measureFrame = 0
	let previous = 0
	let lastDraw = 0
	let time = 0
	let arrivalDistance = 0
	let worldHeight = 1
	let scrollTarget = 0
	let scrollLift = 0
	let visible = true
	let available = true
	let hovered = false
	let held = false
	let focus = 0
	let hoverBounds: { left: number; top: number; right: number; bottom: number } | null = null
	const target = new THREE.Vector2()
	const look = new THREE.Vector2()
	const targetTranslation = new THREE.Vector2()
	const translation = new THREE.Vector2()
	const pointer = new THREE.Vector2()
	const ray = new THREE.Raycaster()
	const leftPin = new THREE.Vector3()
	const rightPin = new THREE.Vector3()
	const join = new THREE.Vector3()
	const corners = [new THREE.Vector3(-W / 2, -H / 2 - PHOTO_Y, 0), new THREE.Vector3(W / 2, -H / 2 - PHOTO_Y, 0), new THREE.Vector3(W / 2, H / 2 - PHOTO_Y, 0), new THREE.Vector3(-W / 2, H / 2 - PHOTO_Y, 0)]
	const projected = new THREE.Vector3()
	const cssLocal = new THREE.Matrix4().set(W / 1024, 0, 0, -W / 2, 0, -H / 1448, 0, H / 2 - PHOTO_Y, 0, 0, 1, .001, 0, 0, 0, 1)
	const cssViewport = new THREE.Matrix4()
	const cssTransform = new THREE.Matrix4()
	const projectBounds = (frontFacing = false) => {
		let left = Infinity, top = Infinity, right = -Infinity, bottom = -Infinity
		for (const corner of corners) {
			projected.copy(corner)
			if (frontFacing) { projected.multiplyScalar(BADGE_SCALE); projected.y += scrollLift }
			else badge.localToWorld(projected)
			projected.project(camera)
			const x = (projected.x + 1) * width / 2
			const y = (1 - projected.y) * height / 2
			left = Math.min(left, x); right = Math.max(right, x)
			top = Math.min(top, y); bottom = Math.max(bottom, y)
		}
		return { left, top, right, bottom }
	}
	const render = (still: boolean) => {
		const free = 1 - focus
		const arrival = still ? 0 : badgeArrival(time, arrivalDistance)
		const rock = still ? 0 : Math.sin(time * .52) * 1.7 + Math.sin(time * .31) * .6
		badge.rotation.set((look.y + rock) * DEG * free, look.x * DEG * free, 0)
		badge.position.set(translation.x * free, translation.y * free + arrival + scrollLift, 0)
		const sway = ribbon.sway
		hardware.rotation.set(still ? 0 : THREE.MathUtils.clamp(sway.z * .10, -.015, .015), still ? 0 : THREE.MathUtils.clamp(sway.x * .05, -.015, .015), still ? 0 : THREE.MathUtils.clamp(-sway.x * .08, -.020, .020))
		badge.updateWorldMatrix(true, true)
		leftPin.set(0, .18, -.34); hardware.localToWorld(leftPin)
		rightPin.set(0, .18, -.375); hardware.localToWorld(rightPin)
		join.set(0, -.01, -.36); hardware.localToWorld(join)
		const clothState = ribbon.update(time, leftPin, rightPin, badge.matrixWorld, still, join)
		renderer.render(world, camera)
		const box = projectBounds()
		// Project CSS pixels through the exact camera and badge matrices. A
		// bounding rectangle would lose perspective and drift away at the edges.
		cssTransform.copy(cssViewport).multiply(camera.projectionMatrix).multiply(camera.matrixWorldInverse).multiply(badge.matrixWorld).multiply(cssLocal)
		// WebGL's depth row uses a different convention from CSS. Only the
		// homogeneous x/y/w projection belongs on this flat HTML surface.
		cssTransform.elements[2] = cssTransform.elements[6] = cssTransform.elements[14] = 0
		cssTransform.elements[10] = 1
		bounds.style.transform = `matrix3d(${cssTransform.elements.join(',')})`
		bounds.inert = box.bottom < 0 || box.top > height || box.right < 0 || box.left > width
		// Geometry diagnostics are also useful when checking hover/motion without
		// treating a canvas's pixels as an inaccessible replacement for the record.
		stage.dataset.badgePose = JSON.stringify([badge.rotation.x / DEG, badge.rotation.y / DEG, badge.rotation.z / DEG, badge.position.x])
		stage.dataset.badgePosition = JSON.stringify(badge.position.toArray())
		stage.dataset.badgeArrival = arrival.toFixed(8)
		stage.dataset.badgeArrived = String(still || time >= BADGE_ARRIVAL_SECONDS)
		stage.dataset.badgeScroll = scrollLift.toFixed(8)
		projected.set(0, 0, 0); badge.localToWorld(projected); projected.project(camera)
		stage.dataset.badgePortraitCenter = JSON.stringify([(projected.x + 1) * width / 2, viewportTop + (1 - projected.y) * height / 2])
		stage.dataset.ribbonLength = clothState.length.toFixed(6)
		stage.dataset.ribbonRestLength = clothState.restLength.toFixed(6)
		stage.dataset.ribbonMaxStretch = clothState.maxStretch.toFixed(6)
		stage.dataset.ribbonFrontZ = clothState.frontZ.toFixed(6)
		stage.dataset.claspPose = JSON.stringify(hardware.rotation.toArray().slice(0, 3))
		stage.dataset.ribbonAnchorError = clothState.anchorError.toFixed(8)
		stage.dataset.ribbonWorldTip = JSON.stringify(clothState.tip.toArray())
		stage.dataset.ribbonWind = JSON.stringify(clothState.wind.toArray())
		stage.dataset.badgeMatrix = JSON.stringify(badge.matrixWorld.elements)
		projected.copy(clothState.tip).project(camera)
		stage.dataset.ribbonTip = JSON.stringify([(projected.x + 1) * width / 2, viewportTop + (1 - projected.y) * height / 2])
	}
	const update = (identity: BadgeIdentity) => {
		drawStrap(strap.ctx, identity.brand)
		strap.texture.needsUpdate = true
		back.ctx.fillStyle = '#d9f642'; back.ctx.fillRect(0, 0, 512, 724)
		back.ctx.fillStyle = '#2b3d0d'; brandMark(back.ctx, 208, 210, 96)
		fitText(back.ctx, identity.brand, 55, 400, 402, 74)
		back.ctx.font = '17px monospace'; back.ctx.fillText('ONE OF US.', 195, 468)
		back.texture.needsUpdate = true
		if (available) render(reduced.matches)
	}
	const resize = () => {
		if (disposed) return
		stage.style.setProperty('--badge-scene-top', `${stage.getBoundingClientRect().top + scrollY}px`)
		const top = viewport.getBoundingClientRect().top
		width = viewport.clientWidth; height = viewport.clientHeight
		cssViewport.set(width / 2, 0, 0, width / 2, 0, -height / 2, 0, height / 2, 0, 0, 1, 0, 0, 0, 0, 1)
		renderer.setPixelRatio(Math.min(devicePixelRatio, 1.5))
		renderer.setSize(width, height, false)
		camera.aspect = width / height
		// The portrait, not the card's outline or the scene below the toolbar,
		// belongs at the viewport centre. The ribbon's material length and rest
		// shape are independent of the viewport dimensions.
		camera.fov = width < 600 ? 33 : 30
		viewportTop = top
		const viewHeight = 2 * camera.position.z * Math.tan(camera.fov * DEG / 2)
		worldHeight = viewHeight
		scrollTarget = Math.min(Math.max(scrollY, 0) / height, 2) * worldHeight
		if (reduced.matches) scrollLift = scrollTarget
		camera.position.y = (innerHeight / 2 - top - height / 2) * viewHeight / height
		if (arrivalDistance === 0) arrivalDistance = viewHeight / 2 - camera.position.y + 2.5
		camera.lookAt(0, camera.position.y, 0)
		camera.updateProjectionMatrix()
		camera.updateMatrixWorld()
		hovered = false; hoverBounds = null
		if (available) render(reduced.matches)
	}
	const tick = (at: number) => {
		const delta = previous === 0 ? 16 : Math.max(0, at - previous)
		const elapsed = Math.min(250, delta)
		previous = at
		// Keep the kinematic ascent and fixed-step cloth on the same clock
		// when a slow renderer cannot consume a full wall-clock frame.
		time += Math.min(delta, 80) / 1000
		const motionDelta = Math.min(delta, 80) / 1000
		const travel = (scrollTarget - scrollLift) * (1 - Math.exp(-motionDelta / .14))
		scrollLift += THREE.MathUtils.clamp(travel, -worldHeight * motionDelta, worldHeight * motionDelta)
		if (Math.abs(scrollTarget - scrollLift) < .00001) scrollLift = scrollTarget
		look.lerp(target, 1 - Math.exp(-elapsed / 280))
		if (look.distanceToSquared(target) < .0001) look.copy(target)
		translation.lerp(targetTranslation, 1 - Math.exp(-elapsed / 280))
		if (translation.distanceToSquared(targetTranslation) < .00000001) translation.copy(targetTranslation)
		const goal = hovered || held ? 1 : 0
		focus += (goal - focus) * (1 - Math.exp(-elapsed / 120))
		if (Math.abs(focus - goal) < .01) focus = goal
		// Slow cloth and a deliberately eased gaze do not need a 60Hz GPU
		// workload. Keep input responsive on integrated/software renderers too.
		// Tolerate rAF timestamp rounding, otherwise a 60Hz display can skip
		// another refresh and accidentally render at 20fps instead of 30.
		if (at - lastDraw >= 1000 / 30 - 1) { render(false); lastDraw = at }
		// At two viewport heights even the longest possible hanging half-loop
		// is above the view. Resume on scroll-back instead of drawing empty frames.
		if (time < BADGE_ARRIVAL_SECONDS || scrollLift < worldHeight * 2 - .00001) frame = requestAnimationFrame(tick)
		else { frame = 0; previous = 0 }
	}
	const reconcile = () => {
		cancelAnimationFrame(frame)
		frame = 0
		previous = 0
		lastDraw = 0
		if (!available || disposed) return
		if (reduced.matches) { look.set(0, 0); translation.set(0, 0); focus = 0; hovered = false; render(true) }
		else if (visible && !document.hidden && (time < BADGE_ARRIVAL_SECONDS || scrollTarget < worldHeight * 2 || scrollLift < worldHeight * 2 - .00001)) frame = requestAnimationFrame(tick)
	}
	const move = (event: PointerEvent) => {
		if (event.pointerType !== 'mouse' || reduced.matches || !available || !visible || time < BADGE_ARRIVAL_SECONDS) return
		if (held) return
		if (event.target instanceof Element && event.target.closest('.account-settings') !== null) { leave(); return }
		const rect = canvas.getBoundingClientRect()
		const x = event.clientX - rect.left
		const y = event.clientY - rect.top
		pointer.set(x / width * 2 - 1, 1 - y / height * 2)
		if (hovered && hoverBounds !== null && (x < hoverBounds.left || x > hoverBounds.right || y < hoverBounds.top || y > hoverBounds.bottom)) { hovered = false; hoverBounds = null }
		if (!hovered) {
			ray.setFromCamera(pointer, camera)
			if (ray.intersectObject(body).length > 0) {
				hovered = true
				const actual = projectBounds()
				const flat = projectBounds(true)
				hoverBounds = { left: Math.min(actual.left, flat.left) - 8, right: Math.max(actual.right, flat.right) + 8, top: Math.min(actual.top, flat.top) - 8, bottom: Math.max(actual.bottom, flat.bottom) + 8 }
			}
		}
		if (!hovered) {
			target.set(THREE.MathUtils.clamp(event.clientX / innerWidth * 2 - 1, -1, 1) * 25, THREE.MathUtils.clamp(1 - event.clientY / innerHeight * 2, -1, 1) * 11)
			const dx = event.clientX - innerWidth / 2, dy = event.clientY - innerHeight / 2
			const distance = Math.hypot(dx, dy)
			const fraction = Math.min(distance / (Math.min(innerWidth, innerHeight) * .45), 1)
			// Radial ease-out: its slope decreases to zero at the cutoff, then
			// remains flat. Moving the pointer farther cannot increase translation.
			const travel = .48 * (1 - Math.pow(1 - fraction, 3))
			targetTranslation.set(distance === 0 ? 0 : dx / distance * travel, distance === 0 ? 0 : -dy / distance * travel)
		} else targetTranslation.set(0, 0)
	}
	const leave = () => { target.set(0, 0); targetTranslation.set(0, 0); hovered = false; hoverBounds = null }
	const scroll = () => {
		scrollTarget = Math.min(Math.max(scrollY, 0) / height, 2) * worldHeight
		leave()
		if (reduced.matches) { scrollLift = scrollTarget; if (available) render(true) }
		else if (frame === 0) reconcile()
	}
	const out = (event: PointerEvent) => { if (event.relatedTarget === null) leave() }
	const lost = (event: Event) => { event.preventDefault(); available = false; bounds.inert = false; stage.dataset.renderer = 'fallback'; cancelAnimationFrame(frame) }
	const restored = () => { available = true; stage.dataset.renderer = 'webgl'; resize(); reconcile() }
	const dimensions = new ResizeObserver(() => { cancelAnimationFrame(measureFrame); measureFrame = requestAnimationFrame(resize) })
	const visibility = new IntersectionObserver(([entry]) => { visible = entry?.isIntersecting ?? false; reconcile() })
	dimensions.observe(stage)
	visibility.observe(viewport)
	window.addEventListener('pointermove', move)
	window.addEventListener('pointerout', out)
	window.addEventListener('blur', leave)
	window.addEventListener('scroll', scroll, { passive: true })
	document.addEventListener('visibilitychange', reconcile)
	window.addEventListener('resize', resize)
	reduced.addEventListener('change', reconcile)
	canvas.addEventListener('webglcontextlost', lost)
	canvas.addEventListener('webglcontextrestored', restored)
	resize()
	update(initial)
	stage.dataset.renderer = 'webgl'
	reconcile()
	return {
		update,
		hold(active) { held = active; if (active) { target.set(0, 0); targetTranslation.set(0, 0) } },
		dispose() {
			disposed = true
			cancelAnimationFrame(frame); cancelAnimationFrame(measureFrame)
			dimensions.disconnect(); visibility.disconnect()
			window.removeEventListener('pointermove', move); window.removeEventListener('pointerout', out); window.removeEventListener('blur', leave)
			window.removeEventListener('scroll', scroll)
			document.removeEventListener('visibilitychange', reconcile); reduced.removeEventListener('change', reconcile)
			window.removeEventListener('resize', resize)
			canvas.removeEventListener('webglcontextlost', lost); canvas.removeEventListener('webglcontextrestored', restored)
			const geometries = new Set<THREE.BufferGeometry>()
			const materials = new Set<THREE.Material>()
			world.traverse((object) => {
				if (object instanceof THREE.Mesh) {
					geometries.add(object.geometry)
					for (const material of Array.isArray(object.material) ? object.material : [object.material]) materials.add(material)
				}
			})
			for (const geometry of geometries) geometry.dispose()
			for (const material of materials) material.dispose()
			for (const texture of [back.texture, strap.texture, weave.texture]) texture.dispose()
			environment.dispose(); renderer.dispose(); renderer.forceContextLoss()
		},
	}
}
