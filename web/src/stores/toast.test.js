import { test } from 'node:test'
import assert from 'node:assert/strict'
import { bindToastApi } from './toast.js'

// Every call site in the console does `const { toast } = useToast()` and then `toast.success(...)` /
// `toast.error(...)` / etc — never the destructured `success`/`error`/... useToast() also returns. bindToastApi
// is the pure function useToast() wraps a live store's methods with (see toast.js); testing it directly here
// avoids having to render a component just to call a zustand hook.
function calls() {
  const log = []
  const fn = (...args) => log.push(args)
  return [fn, log]
}

test('toast.success/error/warn/info call straight through to the store methods useToast() was given', () => {
  const [success, successCalls] = calls()
  const [error, errorCalls] = calls()
  const [warn, warnCalls] = calls()
  const [info, infoCalls] = calls()
  const push = () => {}
  const { toast } = bindToastApi({ push, success, error, warn, info })

  assert.equal(typeof toast, 'function', 'toast must stay callable for its plain toast(msg, type, title) form')
  toast.success('节点已更新')
  toast.error('失败原因')
  toast.warn('将影响 3 台设备')
  toast.info('已复制')

  assert.deepEqual(successCalls, [['节点已更新']])
  assert.deepEqual(errorCalls, [['失败原因']])
  assert.deepEqual(warnCalls, [['将影响 3 台设备']])
  assert.deepEqual(infoCalls, [['已复制']])
})

test('the plain toast(msg, type, title) call form still routes through push with the right shape', () => {
  const pushCalls = []
  const push = (args) => pushCalls.push(args)
  const noop = () => {}
  const { toast } = bindToastApi({ push, success: noop, error: noop, warn: noop, info: noop })

  toast('已重置')
  toast('复制失败', 'error')
  toast('已切换分区：sites', 'info', '标题')

  assert.deepEqual(pushCalls, [
    { type: 'info', message: '已重置', title: '' },
    { type: 'error', message: '复制失败', title: '' },
    { type: 'info', message: '已切换分区：sites', title: '标题' },
  ])
})

test('bindToastApi also returns the bare success/error/warn/info methods for callers that destructure those', () => {
  const [success] = calls()
  const noop = () => {}
  const api = bindToastApi({ push: noop, success, error: noop, warn: noop, info: noop })

  assert.equal(api.success, success)
  assert.equal(api.toast.success, success, 'toast.success must be the exact same function as the bare one')
})
