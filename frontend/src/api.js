const BASE = '/api'

async function request(method, path, body) {
  const opts = { method, headers: { 'Content-Type': 'application/json' } }
  if (body !== undefined) opts.body = JSON.stringify(body)
  const res = await fetch(BASE + path, opts)
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    throw new Error(data.error || `HTTP ${res.status}`)
  }
  return data
}

export const api = {
  get: (p) => request('GET', p),
  post: (p, b) => request('POST', p, b ?? {})
}

export const STATUS_LABELS = {
  accepted: '采信',
  flagged: '异常待处置',
  pending_review: '补录待复核',
  duplicate: '重复回传',
  rejected: '剔除/驳回',
  held: '挂起暂存',
  excluded: '停电窗口不采信'
}

export const ANOMALY_LABELS = {
  regression: '读数倒退',
  jump: '异常跃升',
  missing: '漏报(跳号)',
  out_of_order: '时间倒序',
  during_outage: '停电窗口内回传'
}

export const BILL_STATUS = {
  pending_confirmation: '待船方确认',
  confirmed: '已确认',
  paid: '已付款',
  disputed: '异议处理中'
}

export function fmtTime(s) {
  if (!s) return '—'
  const d = new Date(s)
  if (isNaN(d)) return s
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function fmtMoney(v) {
  return (v ?? 0).toFixed(2)
}
