import { test } from 'node:test'
import assert from 'node:assert/strict'
import { DEFAULT_BUSY_KEY, busyKeyMatches, canStartBusyAction, normalizeBusyKey, restoreFocusIfStranded } from './busyAction.js'

// useBusyAction itself calls real React hooks, so it can only be exercised by mounting a component; these
// tests instead cover the pure reentrancy rule it is built on (see busyAction.js's doc comment for why that
// split exists) — the same rule that decides whether a second click while a request is in flight actually
// reaches the server, or is dropped.

test('normalizeBusyKey maps "no key" (a single busy boolean) to the shared default key', () => {
  assert.equal(normalizeBusyKey(undefined), DEFAULT_BUSY_KEY)
  assert.equal(normalizeBusyKey('node-1:delete'), 'node-1:delete')
  assert.equal(normalizeBusyKey(''), '', 'an explicit falsy key is still a real key, not "no key"')
})

test('canStartBusyAction only allows a call to start when nothing is already running', () => {
  assert.equal(canStartBusyAction(null), true)
  assert.equal(canStartBusyAction(DEFAULT_BUSY_KEY), false)
  assert.equal(canStartBusyAction('node-1:delete'), false, 'a different key still blocks a new call — one action runs at a time per hook instance')
})

test('busyKeyMatches drives isBusy(key): true only for the exact key currently running', () => {
  assert.equal(busyKeyMatches(null, undefined), false, 'nothing running')
  assert.equal(busyKeyMatches(DEFAULT_BUSY_KEY, undefined), true, 'default key running, queried with no key')
  assert.equal(busyKeyMatches('node-1:delete', 'node-1:delete'), true)
  assert.equal(busyKeyMatches('node-1:delete', 'node-2:delete'), false, 'a different row must not show as busy')
  assert.equal(busyKeyMatches('node-1:delete', undefined), false, 'a keyed action running must not satisfy the default-key query')
})

// restoreFocusIfStranded's actual browser behavior (only refocus when focus fell to <body>, and only if the
// element is still attached) needs a real DOM and is covered by the headless smoke test instead; this suite
// runs under plain node:test with no `document` global, so all it can check here is that the function stays a
// safe no-op rather than throwing when there is nothing (or no browser) to act on.
test('restoreFocusIfStranded is a no-op without a DOM or without an element to restore', () => {
  assert.doesNotThrow(() => restoreFocusIfStranded(null))
  assert.doesNotThrow(() => restoreFocusIfStranded(undefined))
  assert.doesNotThrow(() => restoreFocusIfStranded({ focus: () => { throw new Error('must not be called: with no document there is no way to know focus fell to body') } }))
})
