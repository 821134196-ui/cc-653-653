#!/usr/bin/env node
// 端到端验证：通过真实 HTTP 接口验证
//   1) 换泊位分段、各自适用泊位费率、账单逐段金额
//   2) 异常读数（倒退）挂起结算，复核处置前禁止结束/出账，处置后放行且证据保留
//   3) 设备重复回传（同序列号）幂等，只累加 dup_count，不产生计费数据
//   4) 停电窗口内回传不采信、停电不计服务时长（附验）
const BASE = process.env.SP_BASE || 'http://localhost:18080'

let pass = 0, fail = 0
function ok(cond, msg) {
  if (cond) { pass++; console.log(`  ✅ ${msg}`) }
  else { fail++; console.error(`  ❌ ${msg}`) }
}
async function call(method, path, body) {
  const res = await fetch(BASE + '/api' + path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined
  })
  const data = await res.json().catch(() => ({}))
  return { status: res.status, data }
}
async function need(method, path, body) {
  const r = await call(method, path, body)
  if (r.status >= 400) throw new Error(`${method} ${path} -> ${r.status}: ${r.data.error}`)
  return r.data
}
const sleep = (ms) => new Promise(r => setTimeout(r, ms))
// 显式读数时间：默认每步间隔1小时，避免瞬间大增量触发功率跃升校验
const t0 = Date.now()
function iso(offsetMin) { return new Date(t0 + offsetMin * 60000).toISOString() }

async function waitReady() {
  for (let i = 0; i < 40; i++) {
    try {
      const r = await fetch(BASE + '/api/meta')
      if (r.ok) return
    } catch {}
    await sleep(500)
  }
  throw new Error('服务未就绪')
}

async function scenarioBerthChange() {
  console.log('\n=== 场景1：换泊位分段 + 各自泊位费率 ===')
  const c = await need('POST', '/connections', { vessel_name: 'E2E换泊号', meter_code: 'M-A1', reading: 100, start_time: iso(0), report_by: 'op01' })
  await need('POST', '/devices/readings', { meter_code: 'M-A1', reading: 110, read_at: iso(60), report_by: 'sim' })
  const chg = await call('POST', `/connections/${c.id}/change-berth`, {
    time: iso(120), from_reading: 120, to_meter_code: 'M-B1', to_reading: 500, report_by: 'op01'
  })
  ok(chg.status === 200, '换泊位操作成功（原段截止、新段开启）')
  await need('POST', '/devices/readings', { meter_code: 'M-B1', reading: 520, read_at: iso(180), report_by: 'sim' })
  await need('POST', `/connections/${c.id}/close`, { time: iso(240), reading: 540, report_by: 'op01' })
  const bill = await need('POST', `/connections/${c.id}/bill`, {})
  ok(bill.items.length === 2, `账单逐段显示 2 段（实际 ${bill.items.length}）`)
  const [i1, i2] = bill.items
  ok(i1.berth_code === 'A1' && i1.rate_per_kwh === 1.2, `段1 泊位A1 费率1.20（实际 ${i1.berth_code}/${i1.rate_per_kwh}）`)
  ok(i2.berth_code === 'B1' && i2.rate_per_kwh === 1.55, `段2 泊位B1 费率1.55（实际 ${i2.berth_code}/${i2.rate_per_kwh}）`)
  ok(i1.kwh === 20 && Math.abs(i1.hours - 2) < 0.01, `段1 用电20kWh/服务2h（实际 ${i1.kwh}kWh/${i1.hours}h）`)
  ok(i2.kwh === 40 && Math.abs(i2.hours - 2) < 0.01, `段2 用电40kWh/服务2h（实际 ${i2.kwh}kWh/${i2.hours}h）`)
  // 电费确定：20*1.2=24、40*1.55=62；服务费：2h*8=16、2h*12=24
  ok(Math.abs(i1.energy_fee - 24) < 0.01, `段1 电费=24元（实际 ${i1.energy_fee}）`)
  ok(Math.abs(i2.energy_fee - 62) < 0.01, `段2 电费=62元（实际 ${i2.energy_fee}）`)
  ok(Math.abs(i1.service_fee - 16) < 0.01, `段1 服务费=16元（实际 ${i1.service_fee}）`)
  ok(Math.abs(i2.service_fee - 24) < 0.01, `段2 服务费=24元（实际 ${i2.service_fee}）`)
  ok(Math.abs(i1.amount - 40) < 0.01, `段1 合计=40元（实际 ${i1.amount}）`)
  ok(Math.abs(i2.amount - 86) < 0.01, `段2 合计=86元（实际 ${i2.amount}）`)
  ok(Math.abs(bill.total_amount - (i1.amount + i2.amount)) < 0.001, '账单总额=各段之和')
  // 原始读数可查
  const detail = await need('GET', `/bills/${bill.id}`)
  ok(JSON.stringify(detail.snapshot).includes('M-A1') && JSON.stringify(detail.snapshot.readings).length > 50,
    '账单快照保留全部原始读数可供船方核对')
  return bill
}

async function scenarioAnomaly() {
  console.log('\n=== 场景2：读数倒退 → 挂起结算 → 双人处置后放行 ===')
  const c = await need('POST', '/connections', { vessel_name: 'E2E异常号', meter_code: 'M-A2', reading: 100, start_time: iso(0), report_by: 'op01' })
  const bad = await need('POST', '/devices/readings', { meter_code: 'M-A2', reading: 90, read_at: iso(60), report_by: 'sim' })
  ok(bad.reading.status === 'flagged' && bad.reading.anomaly === 'regression', '倒退读数被标记 flagged/regression')
  const blockedClose = await call('POST', `/connections/${c.id}/close`, { time: iso(120), reading: 120, report_by: 'op01' })
  ok(blockedClose.status === 409, `挂起期间禁止结束接电（HTTP ${blockedClose.status}：${blockedClose.data.error}）`)
  const blockedBill = await call('POST', `/connections/${c.id}/bill`, {})
  ok(blockedBill.status >= 400, '挂起期间禁止出账')
  // 值班员自行处置应被拒
  const selfResolve = await call('POST', `/readings/${bad.reading.id}/resolve`,
    { reviewer: 'op01', resolution: 'discarded', note: '自审' })
  ok(selfResolve.status === 403, '值班员无权自行处置异常')
  // 复核员剔除
  const r = await need('POST', `/readings/${bad.reading.id}/resolve`,
    { reviewer: 'rv01', resolution: 'discarded', note: '表计故障误传，剔除' })
  ok(r.status === 'rejected', '复核员处置后异常记录状态为 rejected（记录仍保留）')
  const closed = await call('POST', `/connections/${c.id}/close`, { time: iso(120), reading: 120, report_by: 'op01' })
  ok(closed.status === 200, '异常解除后可以结束接电')
  const bill = await need('POST', `/connections/${c.id}/bill`, {})
  ok(bill.total_kwh === 20, `剔除倒退值后电量=20（实际 ${bill.total_kwh}）`)
  const readings = await need('GET', `/readings?connection_id=${c.id}`)
  const kept = readings.find(x => x.id === bad.reading.id)
  ok(kept && kept.status === 'rejected' && kept.reviewed_by === 'rv01', '原始异常读数与复核结论留痕，未被删除')
}

async function scenarioDuplicate() {
  console.log('\n=== 场景3：设备重复回传幂等 ===')
  const c = await need('POST', '/connections', { vessel_name: 'E2E重复号', meter_code: 'M-B2', reading: 1000, start_time: iso(0), report_by: 'op01' })
  const first = await need('POST', '/devices/readings',
    { meter_code: 'M-B2', reading: 1010, read_at: iso(60), device_seq: 42, report_by: 'sim' })
  ok(first.duplicated === false, '序列号42首次回传正常采信')
  // 设备重发：同序列号、不同读数、更晚时间
  const dup1 = await need('POST', '/devices/readings',
    { meter_code: 'M-B2', reading: 9999, read_at: iso(61), device_seq: 42, report_by: 'sim-retry' })
  ok(dup1.duplicated === true && dup1.reading.id === first.reading.id && dup1.reading.reading === 1010,
    '同序列号重发被判重，返回原记录且错误读数9999未入库')
  const dup2 = await need('POST', `/readings/${first.reading.id}/replay`, {})
  ok(dup2.duplicated === true && dup2.reading.dup_count === 2,
    `重放接口累计 dup_count=2（实际 ${dup2.reading.dup_count}）`)
  await need('POST', `/connections/${c.id}/close`, { time: iso(120), reading: 1020, report_by: 'op01' })
  const bill = await need('POST', `/connections/${c.id}/bill`, {})
  ok(bill.total_kwh === 20, `重复回传不影响计费，电量=20（实际 ${bill.total_kwh}）`)
  const readings = await need('GET', `/readings?connection_id=${c.id}`)
  const accepted = readings.filter(r => r.status === 'accepted')
  ok(accepted.length === 3, `采信读数仅3条（起始+seq42+终止，实际 ${accepted.length}），无重复计费数据`)
}

async function scenarioOutage() {
  console.log('\n=== 场景4（附验）：停电窗口不连续计费 ===')
  const c = await need('POST', '/connections', { vessel_name: 'E2E停电号', meter_code: 'M-A1', reading: 100, start_time: iso(0), report_by: 'op01' })
  await need('POST', '/devices/readings', { meter_code: 'M-A1', reading: 110, read_at: iso(60), report_by: 'sim' })
  await need('POST', `/connections/${c.id}/outage`, { time: iso(120), reading: 120, reason: '线路检修', report_by: 'op01' })
  const during = await need('POST', '/devices/readings', { meter_code: 'M-A1', reading: 888, read_at: iso(150), report_by: 'sim' })
  ok(during.reading.status === 'excluded' && during.reading.anomaly === 'during_outage',
    '停电窗口内设备回传被留存但标记 excluded，不采信')
  await need('POST', `/connections/${c.id}/resume`, { time: iso(180), reading: 120, report_by: 'op01' })
  await need('POST', '/devices/readings', { meter_code: 'M-A1', reading: 130, read_at: iso(240), report_by: 'sim' })
  await need('POST', `/connections/${c.id}/close`, { time: iso(300), reading: 140, report_by: 'op01' })
  const bill = await need('POST', `/connections/${c.id}/bill`, {})
  ok(bill.items.length === 2, '停电恢复后产生第2计量段')
  ok(bill.total_kwh === 40, `电量按两段边界=40（实际 ${bill.total_kwh}），停电窗口读数不计`)
  ok(Math.abs(bill.total_hours - 4) < 0.01, `总服务时长=4h（0-2h + 3-5h，剔除停电1h，实际 ${bill.total_hours}h）`)
  const gap = bill.items[1].start_time
  ok(new Date(gap) >= new Date(bill.items[0].end_time), '新段开始时间不早于原段截止，停电区间不属于任何段')
}

async function scenarioDisputeFlow(bill) {
  console.log('\n=== 场景5（附验）：账单确认→异议冻结→调整单→净额付款 ===')
  const wrongRole = await call('POST', `/bills/${bill.id}/confirm`, { by: 'op01' })
  ok(wrongRole.status === 403, '非船方不能确认账单')
  await need('POST', `/bills/${bill.id}/confirm`, { by: 'ship' })
  await need('POST', `/bills/${bill.id}/dispute`, { reason: 'E2E：第二段费率异议', by: 'ship' })
  const payBlocked = await call('POST', `/bills/${bill.id}/pay`, { by: 'ship' })
  ok(payBlocked.status === 409, '异议处理中禁止付款')
  const disputes = await need('GET', '/disputes')
  const d = disputes.find(x => x.bill_id === bill.id && x.status === 'open')
  const before = (await need('GET', `/bills/${bill.id}`)).bill.total_amount
  await need('POST', `/disputes/${d.id}/resolve`,
    { reviewer: 'rv01', conclusion: 'E2E：核实退10元', action: 'refund', amount: 10 })
  const after = await need('GET', `/bills/${bill.id}`)
  ok(after.bill.total_amount === before, '调整后原账单金额不变（走调整单）')
  ok(after.credit_notes.length === 1 && after.credit_notes[0].kind === 'refund', '生成退款调整单并随账可查')
  const rcpt = await need('POST', `/bills/${bill.id}/pay`, { by: 'ship' })
  ok(Math.abs(rcpt.amount - (before - 10)) < 0.01, `模拟付款回执按净额 ¥${before - 10}（实际 ¥${rcpt.amount}）`)
}

;(async () => {
  await waitReady()
  console.log(`目标服务：${BASE}`)
  const bill = await scenarioBerthChange()
  await scenarioDisputeFlow(bill)
  await scenarioAnomaly()
  await scenarioDuplicate()
  await scenarioOutage()
  console.log(`\n结果：${pass} 通过 / ${fail} 失败`)
  process.exit(fail ? 1 : 0)
})().catch(e => { console.error('E2E 异常中断:', e.message); process.exit(1) })
