import { test } from '@playwright/test'
import { writeFileSync } from 'node:fs'

// TEMPORARY: on a failure, what every goroutine of `roster serve` was doing.
test.afterEach(async ({}, info) => {
	if (info.status === info.expectedStatus) return
	try {
		const dump = await (await fetch('http://127.0.0.1:18062/debug/pprof/goroutine?debug=2')).text()
		writeFileSync(`dist/hang-${info.title.replace(/\W+/g, '-')}.txt`, dump)
	} catch {}
})
