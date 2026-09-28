import { useCallback, useRef, useState } from 'react'

// busyAction.js guards an async action (save/delete/create/start/stop/...) against being fired twice by the
// same click: a real click and its accidental double (or a slow network making the button look unresponsive,
// so the operator clicks again) must not both reach the server. React state alone cannot do this — setState
// only takes effect on the next render, which is too late to stop a second call landing in the same tick (or
// even the same microtask, before the button's `disabled` attribute has actually re-rendered). So the actual
// gate is a plain ref checked synchronously; `busy` state exists only to drive the visible loading indicator
// (spinner / "处理中…" / aria-busy), never to decide whether a call is allowed to start.
//
// The reentrancy rule itself is a pure function of "what key (if any) is already running", kept separate from
// the hook below so it can be unit-tested with plain values (see busyAction.test.js) instead of having to
// mount a component — the same split toast.js uses for bindToastApi/useToast.

// DEFAULT_BUSY_KEY is used when a caller has only one action to guard (e.g. a single save button) and does not
// need to distinguish which of several rows/keys is busy.
export const DEFAULT_BUSY_KEY = '__busy__'

export function normalizeBusyKey(key) {
  return key === undefined ? DEFAULT_BUSY_KEY : key
}

// canStartBusyAction says whether a new call may start, given the key currently running (or null for none).
export function canStartBusyAction(runningKey) {
  return runningKey === null
}

// busyKeyMatches is what isBusy(key) below reduces to: is `key` (or the default key, if omitted) the one
// currently running?
export function busyKeyMatches(busy, key) {
  return busy !== null && busy === normalizeBusyKey(key)
}

// useBusyAction returns:
//   - busy: the key currently running, or null
//   - isBusy(key?): whether that particular key (default key if omitted) is the one running
//   - run(key?, action): runs `action` (an async function) guarded against reentrancy; a call that arrives
//     while another is still running is dropped (the in-flight one wins) rather than queued, since every
//     caller in this app represents "the user clicked the same button again", not "do this later too". Errors
//     from `action` propagate to the caller (after busy is cleared) so each call site keeps its own
//     try/catch — this hook only owns the reentrancy guard and the busy flag, not error presentation.
//
// Call it either as run(action) for a single busy boolean, or run(key, action) for per-row actions (e.g.
// `${node.id}:delete`) so unrelated rows stay interactive while one of them is busy.
export function useBusyAction() {
  const runningRef = useRef(null)
  const [busy, setBusy] = useState(null)

  const run = useCallback(async (keyOrAction, maybeAction) => {
    const hasExplicitKey = typeof keyOrAction !== 'function'
    const key = normalizeBusyKey(hasExplicitKey ? keyOrAction : undefined)
    const action = hasExplicitKey ? maybeAction : keyOrAction
    if (!canStartBusyAction(runningRef.current)) return undefined
    runningRef.current = key
    setBusy(key)
    try {
      return await action()
    } finally {
      runningRef.current = null
      setBusy(null)
    }
  }, [])

  const isBusy = useCallback((key) => busyKeyMatches(busy, key), [busy])

  return { busy, isBusy, run }
}
