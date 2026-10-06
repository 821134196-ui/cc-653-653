<script setup>
import { inject, ref, computed, watch } from 'vue'
import { api, fmtTime, fmtMoney, STATUS_LABELS, ANOMALY_LABELS } from '../api.js'

const { meta, role, roleMap, notify, refresh, tick } = inject('ctx')
const conns = ref([])
const selectedId = ref(null)
const detail = ref(null)

const form = ref({ vessel: '', meter: 'M-A1', reading: '' })
const ev = ref({ reading: '', time: '' })
const berthForm = ref({ to_meter: 'M-B1', from_reading: '', to_reading: '', time: '' })
const outageForm = ref({ reading: '', time: '', reason: '临时停电检修' })
const resumeForm = ref({ meter: '', reading: '', time: '' })
const closeForm = ref({ reading: '', time: '' })

const me = computed(() => roleMap.value[role.value].code)

async function load() {
  conns.value = await api.get('/connections')
  if (selectedId.value) await loadDetail()
}
async function loadDetail() {
  detail.value = await api.get(`/connections/${selectedId.value}`)
}
function select(c) {
  selectedId.value = c.id
  loadDetail()
}
watch(tick, load)
load()

function dtLocal(offsetMin = 0) {
  const d = new Date(Date.now() + offsetMin * 60000)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

async function createConn() {
  try {
    const c = await api.post('/connections', {
      vessel_name: form.value.vessel,
      meter_code: form.value.meter,
      reading: Number(form.value.reading || 0),
      report_by: me.value
    })
    notify(`接电单 ${c.code} 已登记，起始计量段已建立`, 'ok')
    form.value = { vessel: '', meter: form.value.meter, reading: '' }
    select(c)
    refresh()
  } catch (e) { notify(e.message, 'error') }
}

async function sendReading() {
  try {
    await api.post(`/connections/${selectedId.value}/readings`, {
      meter_code: detail.value.segments.find(s => !s.end_time)?.meter_code || detail.value.connection.start_meter_id,
      reading: Number(ev.value.reading),
      read_at: ev.value.time || undefined,
      reported_by: me.value
    })
    notify('手工读数已上报', 'ok')
    ev.value = { reading: '', time: '' }
    loadDetail()
  } catch (e) { notify(e.message, 'error') }
}

async function changeBerth() {
  try {
    await api.post(`/connections/${selectedId.value}/change-berth`, {
      time: berthForm.value.time || undefined,
      from_reading: Number(berthForm.value.from_reading),
      to_meter_code: berthForm.value.to_meter,
      to_reading: Number(berthForm.value.to_reading),
      report_by: me.value
    })
    notify('已换泊位：原计量段截止，新段按新泊位费率计费', 'ok')
    berthForm.value = { ...berthForm.value, from_reading: '', to_reading: '', time: '' }
    loadDetail()
  } catch (e) { notify(e.message, 'error') }
}

async function outageStart() {
  try {
    await api.post(`/connections/${selectedId.value}/outage`, {
      time: outageForm.value.time || undefined,
      reading: Number(outageForm.value.reading),
      reason: outageForm.value.reason,
      report_by: me.value
    })
    notify('停电已登记：当前段截止，停电期间不计服务时长', 'ok')
    outageForm.value = { reading: '', time: '', reason: outageForm.value.reason }
    loadDetail()
  } catch (e) { notify(e.message, 'error') }
}

async function resume() {
  try {
    await api.post(`/connections/${selectedId.value}/resume`, {
      time: resumeForm.value.time || undefined,
      meter_code: resumeForm.value.meter,
      reading: Number(resumeForm.value.reading),
      report_by: me.value
    })
    notify('供电已恢复，已另起新计量段', 'ok')
    resumeForm.value = { meter: '', reading: '', time: '' }
    loadDetail()
  } catch (e) { notify(e.message, 'error') }
}

async function closeConn() {
  try {
    await api.post(`/connections/${selectedId.value}/close`, {
      time: closeForm.value.time || undefined,
      reading: Number(closeForm.value.reading),
      report_by: me.value
    })
    notify('接电已结束', 'ok')
    closeForm.value = { reading: '', time: '' }
    loadDetail()
  } catch (e) { notify(e.message, 'error') }
}

async function genBill() {
  try {
    const b = await api.post(`/connections/${selectedId.value}/bill`, {})
    notify(`账单 ${b.code} 已生成，等待船方确认`, 'ok')
    loadDetail()
  } catch (e) { notify(e.message, 'error') }
}

const connStatus = { active: '接电中', closed: '已结束' }
const segStatus = { open: '计量中', closed: '已截止', suspended: '挂起' }
const reasonMap = { connect: '接电起始段', berth_change: '换泊位新段', power_restore: '停电恢复新段' }
const openSeg = computed(() => detail.value?.segments.find(s => !s.end_time) || null)
const inOutage = computed(() => detail.value?.outages.some(o => !o.end_time) || false)
const berthName = (code) => meta.value.berths.find(b => b.code === code)?.name || code
const meterName = (code) => meta.value.meters.find(m => m.code === code)?.name || code
</script>

<template>
  <div class="grid two">
    <div>
      <div class="panel" v-if="role === 'operator'">
        <h2>① 值班员登记接电</h2>
        <label>船名</label>
        <input v-model="form.vessel" placeholder="例：远洋号" />
        <label>泊位 / 电表</label>
        <select v-model="form.meter">
          <option v-for="m in meta.meters" :key="m.code" :value="m.code">
            {{ m.berth_code }} · {{ m.name }}（上限 {{ m.max_power_kw }}kW）
          </option>
        </select>
        <label>电表起始读数 (kWh)</label>
        <input v-model="form.reading" type="number" step="0.01" />
        <button class="btn" @click="createConn">登记并开始计量</button>
      </div>

      <div class="panel" style="margin-top:16px">
        <h2>接电单列表</h2>
        <div class="list-scroll">
          <table>
            <thead><tr><th>单号</th><th>船名</th><th>开始</th><th>状态</th></tr></thead>
            <tbody>
              <tr v-for="c in conns" :key="c.id" class="selectable"
                  :class="{ active: selectedId === c.id }" @click="select(c)">
                <td>{{ c.code }}</td>
                <td>{{ c.vessel_name }}</td>
                <td>{{ fmtTime(c.start_time) }}</td>
                <td>
                  <span class="tag" :class="c.status === 'active' ? 'info' : ''">{{ connStatus[c.status] }}</span>
                  <span v-if="c.billing_hold" class="tag hold">挂起</span>
                </td>
              </tr>
              <tr v-if="!conns.length"><td colspan="4" class="empty">暂无接电单</td></tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <div v-if="!detail" class="panel empty">请选择左侧接电单查看分段与操作</div>

    <div v-else>
      <div class="panel">
        <h2>{{ detail.connection.code }} · {{ detail.connection.vessel_name }}</h2>
        <div v-if="detail.connection.billing_hold" class="banner hold">
          ⛔ 结算已挂起，换泊位/停电/结束/出账均已暂停。待处置：{{ detail.connection.hold_reasons }}
        </div>
        <dl class="kv">
          <dt>起始表计</dt><dd>{{ meterName(detail.connection.start_meter_id) }}</dd>
          <dt>接电时间</dt><dd>{{ fmtTime(detail.connection.start_time) }} → {{ fmtTime(detail.connection.end_time) }}</dd>
          <dt>登记人</dt><dd>{{ detail.connection.report_by }}</dd>
          <dt>停电次数</dt><dd>{{ detail.outages.length }} 次</dd>
        </dl>

        <div v-if="detail.connection.status === 'active'" class="flow-actions">
          <button class="btn ghost sm" v-if="role==='operator' && openSeg && !inOutage"
                  @click="berthForm.time = dtLocal()">换泊位</button>
          <button class="btn warn sm" v-if="role==='operator' && openSeg && !inOutage"
                  @click="outageForm.time = dtLocal()">停电</button>
          <button class="btn ok sm" v-if="role==='operator' && inOutage"
                  @click="resumeForm.time = dtLocal()">恢复供电</button>
          <button class="btn danger sm" v-if="role==='operator'"
                  @click="closeForm.time = dtLocal()">结束接电</button>
        </div>
      </div>

      <!-- 操作表单 -->
      <div class="panel" v-if="detail.connection.status==='active' && role==='operator'">
        <div v-if="openSeg && !inOutage" style="margin-bottom:14px">
          <h3>设备/人工读数上报（当前段：{{ openSeg.meter_code }}）</h3>
          <div class="row">
            <div><label>表码 (kWh)</label><input type="number" step="0.01" v-model="ev.reading" /></div>
            <div><label>读数时间（留空=现在）</label><input type="datetime-local" v-model="ev.time" /></div>
          </div>
          <button class="btn sm" @click="sendReading">上报读数</button>
          <span class="muted" style="margin-left:10px;font-size:12px">自动回传请在“设备模拟”页操作</span>
        </div>

        <div v-if="openSeg && !inOutage" style="border-top:1px dashed var(--line);padding-top:10px">
          <h3>换泊位（原段截止 · 新泊位新费率）</h3>
          <div class="row">
            <div><label>原表 {{ openSeg.meter_code }} 止码</label>
              <input type="number" step="0.01" v-model="berthForm.from_reading" /></div>
            <div><label>时间</label><input type="datetime-local" v-model="berthForm.time" /></div>
          </div>
          <div class="row">
            <div><label>新泊位电表</label>
              <select v-model="berthForm.to_meter">
                <option v-for="m in meta.meters.filter(x => x.code !== openSeg.meter_code)"
                        :key="m.code" :value="m.code">{{ m.berth_code }} · {{ m.name }}</option>
              </select></div>
            <div><label>新表起码</label><input type="number" step="0.01" v-model="berthForm.to_reading" /></div>
          </div>
          <button class="btn warn sm" @click="changeBerth">确认换泊位</button>
        </div>

        <div v-if="openSeg && !inOutage" style="border-top:1px dashed var(--line);padding-top:10px">
          <h3>停电登记（停电期间不计服务时长）</h3>
          <div class="row">
            <div><label>停电时表码</label><input type="number" step="0.01" v-model="outageForm.reading" /></div>
            <div><label>时间</label><input type="datetime-local" v-model="outageForm.time" /></div>
            <div><label>原因</label><input v-model="outageForm.reason" /></div>
          </div>
          <button class="btn warn sm" @click="outageStart">开始停电</button>
        </div>

        <div v-if="inOutage" style="border-top:1px dashed var(--line);padding-top:10px">
          <h3>恢复供电（另起新计量段）</h3>
          <div class="row">
            <div><label>电表（留空=原表）</label>
              <select v-model="resumeForm.meter">
                <option value="">沿用停电前电表</option>
                <option v-for="m in meta.meters" :key="m.code" :value="m.code">{{ m.berth_code }} · {{ m.name }}</option>
              </select></div>
            <div><label>恢复时表码</label><input type="number" step="0.01" v-model="resumeForm.reading" /></div>
            <div><label>时间</label><input type="datetime-local" v-model="resumeForm.time" /></div>
          </div>
          <button class="btn ok sm" @click="resume">恢复并开新段</button>
        </div>

        <div style="border-top:1px dashed var(--line);padding-top:10px">
          <h3>结束接电</h3>
          <div class="row">
            <div><label>最终表码</label><input type="number" step="0.01" v-model="closeForm.reading" /></div>
            <div><label>时间</label><input type="datetime-local" v-model="closeForm.time" /></div>
          </div>
          <button class="btn danger sm" @click="closeConn">结束接电（截止最后一段）</button>
        </div>
      </div>

      <!-- 计量段 -->
      <div class="panel" style="margin-top:16px">
        <h2>真实接电区间 · 计量段（{{ detail.segments.length }}）</h2>
        <div v-for="s in detail.segments" :key="s.id" class="seg-card"
             :class="{ suspended: s.status === 'suspended' }">
          <div class="seg-head">
            <strong>第 {{ s.seq }} 段</strong>
            <span class="tag info">{{ s.berth_code }} · {{ berthName(s.berth_code) }}</span>
            <span class="tag">{{ meterName(s.meter_code) }}</span>
            <span class="tag">{{ reasonMap[s.reason] || s.reason }}</span>
            <span class="tag" :class="s.status === 'suspended' ? 'hold' : (s.end_time ? 'ok' : 'info')">
              {{ segStatus[s.status] }}
            </span>
          </div>
          <div class="kv">
            <dt>服务区间</dt><dd>{{ fmtTime(s.start_time) }} → {{ fmtTime(s.end_time) }}</dd>
            <dt>表码区间</dt><dd class="money">{{ s.start_reading }} → {{ s.end_reading ?? '…' }} kWh</dd>
            <dt>本段用电</dt><dd class="money">{{ s.end_reading != null ? fmtMoney(s.end_reading - s.start_reading) : '—' }} kWh</dd>
            <dt>适用费率</dt>
            <dd v-if="s.end_time">{{ s.rate_per_kwh || '—' }} 元/kWh ＋ {{ s.service_fee_per_hr || '—' }} 元/小时（出账时按泊位锁定）</dd>
            <dd v-else class="muted">出账时按 {{ s.berth_code }} 泊位费率锁定</dd>
          </div>
          <details class="raw">
            <summary>查看该段原始读数</summary>
            <table style="margin-top:6px">
              <thead><tr><th>时间</th><th>表码</th><th>来源</th><th>序号</th><th>状态</th><th>异常</th><th>上报/复核</th></tr></thead>
              <tbody>
                <tr v-for="r in detail.readings.filter(x => x.segment_id === s.id)" :key="r.id">
                  <td>{{ fmtTime(r.read_at) }}</td>
                  <td class="money">{{ r.reading }}</td>
                  <td>{{ r.source === 'device' ? '设备' : '人工' }}</td>
                  <td>{{ r.device_seq ?? '—' }}<span v-if="r.dup_count" class="tag warn">×{{ r.dup_count + 1 }}</span></td>
                  <td><span class="tag" :class="r.status==='accepted'?'ok':(r.status==='flagged'?'danger':(r.status==='excluded'?'hold':'warn'))">
                    {{ STATUS_LABELS[r.status] || r.status }}</span></td>
                  <td>{{ ANOMALY_LABELS[r.anomaly] || '' }}</td>
                  <td class="muted">{{ r.reported_by }}<template v-if="r.reviewed_by"> / {{ r.reviewed_by }}</template></td>
                </tr>
              </tbody>
            </table>
          </details>
        </div>

        <h3>停电记录（不计服务时长）</h3>
        <table v-if="detail.outages.length">
          <thead><tr><th>#</th><th>停电开始</th><th>恢复时间</th><th>停电时长</th><th>原因</th></tr></thead>
          <tbody>
            <tr v-for="(o, i) in detail.outages" :key="o.id">
              <td>{{ i + 1 }}</td>
              <td>{{ fmtTime(o.start_time) }}</td>
              <td>{{ fmtTime(o.end_time) }}</td>
              <td>{{ o.end_time ? ((new Date(o.end_time) - new Date(o.start_time)) / 3600000).toFixed(2) + ' h' : '进行中' }}</td>
              <td>{{ o.reason }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="muted">无停电</div>

        <div v-if="detail.connection.status === 'closed'" style="margin-top:14px">
          <button class="btn" @click="genBill" :disabled="detail.connection.billing_hold || detail.bill">
            {{ detail.bill ? '账单已生成：' + detail.bill.code : '生成账单（经船方确认后生效）' }}
          </button>
          <span v-if="detail.connection.billing_hold" class="muted" style="margin-left:8px">需先到“读数·异常复核”页处置挂起事项</span>
        </div>
      </div>

      <!-- 账单速览 -->
      <div class="panel" v-if="detail.bill" style="margin-top:16px">
        <h2>关联账单 {{ detail.bill.code }}（{{ {pending_confirmation:'待船方确认',confirmed:'已确认',paid:'已付款',disputed:'异议中'}[detail.bill.status] }}）</h2>
        <table>
          <thead><tr><th>段</th><th>泊位</th><th>服务区间</th><th>电量kWh</th><th>时长h</th><th>费率</th><th class="right">金额</th></tr></thead>
          <tbody>
            <tr v-for="it in detail.bill.items" :key="it.id">
              <td>{{ it.seq }}</td><td>{{ it.berth_code }}</td>
              <td>{{ fmtTime(it.start_time).slice(5) }} → {{ fmtTime(it.end_time).slice(5) }}</td>
              <td class="money">{{ it.kwh }}</td><td>{{ it.hours }}</td>
              <td>{{ it.rate_per_kwh }}元/kWh</td>
              <td class="right money">{{ fmtMoney(it.amount) }}</td>
            </tr>
          </tbody>
        </table>
        <div class="totals">
          <div class="t">合计电量 <b class="money">{{ fmtMoney(detail.bill.total_kwh) }}</b> kWh</div>
          <div class="t">总服务时长 <b>{{ detail.bill.total_hours }}</b> h</div>
          <div class="t">应收总额 <b class="money">¥{{ fmtMoney(detail.bill.total_amount) }}</b></div>
        </div>
      </div>
    </div>
  </div>
</template>
