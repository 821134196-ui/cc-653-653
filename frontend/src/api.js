const BASE = '/api'

async function request(method, path, body) {
  const opts = { method, headers: { 'Content-Type': 'application/json' } }
  if (body !== undefined) opts.body = JSON.stringify(body)
  const res = await fetch(BASE + path, opts)
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    throw new Error(data.error || `请求失败 (${res.status})`)
  }
  return data
}

export const api = {
  get: (p) => request('GET', p),
  post: (p, b) => request('POST', p, b ?? {}),
}

// ---- 字典 ----
export const listBerths = () => api.get('/berths')
export const listSessions = (status = '') => api.get('/sessions' + (status ? `?status=${status}` : ''))
export const registerSession = (b) => api.post('/sessions', b)
export const sessionDetail = (id) => api.get(`/sessions/${id}`)
export const addReading = (id, b) => api.post(`/sessions/${id}/readings`, b)
export const changeBerth = (id, b) => api.post(`/sessions/${id}/change-berth`, b)
export const startOutage = (id, b) => api.post(`/sessions/${id}/outages/start`, b)
export const endOutage = (id, b) => api.post(`/sessions/${id}/outages/end`, b)
export const endSession = (id, b) => api.post(`/sessions/${id}/end`, b)
export const supplement = (id, b) => api.post(`/sessions/${id}/supplements`, b)
export const billPreview = (id, open = false) => api.get(`/sessions/${id}/bill-preview${open ? '?open=1' : ''}`)
export const confirmBill = (id) => api.post(`/sessions/${id}/bills`)

export const pendingReadings = () => api.get('/readings/pending')
export const reviewReading = (id, b) => api.post(`/readings/${id}/review`, b)
export const listAnomalies = (status) => api.get('/anomalies' + (status ? `?status=${status}` : ''))

export const listBills = (status = '') => api.get('/bills' + (status ? `?status=${status}` : ''))
export const billDetail = (id) => api.get(`/bills/${id}`)
export const billSnapshot = (id) => api.get(`/bills/${id}/snapshot`)
export const shipConfirm = (id) => api.post(`/bills/${id}/confirm`)
export const raiseDispute = (id, b) => api.post(`/bills/${id}/disputes`, b)
export const payBill = (id, b) => api.post(`/bills/${id}/pay`, b)

export const listDisputes = (status = '') => api.get('/disputes' + (status ? `?status=${status}` : ''))
export const resolveDispute = (id, b) => api.post(`/disputes/${id}/resolve`, b)

// ---- 工具函数 ----
export const fmtDT = (s) => {
  if (!s) return '—'
  const d = new Date(s)
  if (isNaN(d)) return s
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}
export const fmtMoney = (v) => Number(v || 0).toFixed(2)
export const fmtDur = (sec) => {
  sec = Number(sec || 0)
  const h = Math.floor(sec / 3600)
  const m = Math.round((sec % 3600) / 60)
  if (h && m) return `${h}小时${m}分`
  if (h) return `${h}小时`
  return `${m}分钟`
}
// 本地 datetime-local 默认值
export const nowLocalInput = () => {
  const d = new Date()
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}
export const toRFC = (v) => (v ? new Date(v).toISOString() : '')

export const STATUS_CN = {
  active: '接电中', ended: '已结束', blocked: '异常冻结', billed: '已出账', disputed: '异议处理中',
  serving: '供电中', outage: '停电中', closed: '已关闭', flagged: '异常挂起', answered: '已恢复',
  pending: '待处理/待复核', confirmed: '已确认', paid: '已付款', void: '已更正失效',
  open: '待处理', resolved: '已处理',
  manual: '手工', device: '设备', supplement: '补录',
  accepted: '已采纳', rejected: '已驳回', anomalous: '异常', duplicate: '重复丢弃',
}
export const tag = (s) => STATUS_CN[s] || s
