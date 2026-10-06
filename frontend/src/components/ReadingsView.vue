<script setup>
import { inject, ref, watch, onMounted } from 'vue'
import { api, fmtTime, STATUS_LABELS, ANOMALY_LABELS } from '../api.js'

const { meta, role, roleMap, notify, refresh, tick } = inject('ctx')
const readings = ref([])
const conns = ref([])
const filter = ref({ meter: '', status: '', connection_id: '' })

const devForm = ref({ meter: 'M-A1', reading: '', time: '', seq: '' })
const supForm = ref({ conn: '', meter: 'M-A1', reading: '', time: '' })
const sim = ref({ meter: 'M-A1', on: false, step: 0.2, interval: 5 })
const simStates = ref({})

async function load() {
  const q = new URLSearchParams()
  if (filter.value.meter) q.set('meter', filter.value.meter)
  if (filter.value.status) q.set('status', filter.value.status)
  if (filter.value.connection_id) q.set('connection_id', filter.value.connection_id)
  readings.value = await api.get('/readings?' + q.toString())
  conns.value = await api.get('/connections')
  simStates.value = await api.get('/sim/status')
}
watch(tick, load)
onMounted(load)

const me = () => roleMap.value[role.value].code

async function deviceSend() {
  try {
    const body = { meter_code: devForm.value.meter, reported_by: 'simulator-manual' }
    if (devForm.value.reading !== '') body.reading = Number(devForm.value.reading)
    if (devForm.value.time) body.read_at = devForm.value.time
    if (devForm.value.seq !== '') body.device_seq = Number(devForm.value.seq)
    const r = await api.post('/devices/readings', body)
    notify(r.duplicated ? `⚠️ 设备序列号重复，已忽略（原记录 #${r.reading.id} 重复计数+1）` : '设备读数已回传',
      r.duplicated ? 'hold' : 'ok')
    devForm.value.reading = ''; devForm.value.seq = ''; devForm.value.time = ''
    load()
  } catch (e) { notify(e.message, 'error') }
}

async function replay(r) {
  try {
    const res = await api.post(`/readings/${r.id}/replay`, {})
    notify(res.duplicated
      ? `重复回传验证成功：序列号未产生新记录，仅累加到 #${res.reading.id}（dup_count=${res.reading.dup_count}）`
      : '未判重，请检查', 'hold')
    load()
  } catch (e) { notify(e.message, 'error') }
}

async function submitSup() {
  if (!supForm.value.conn) return notify('请选择接电单', 'error')
  try {
    const r = await api.post(`/connections/${supForm.value.conn}/supplement`, {
      meter_code: supForm.value.meter,
      reading: Number(supForm.value.reading),
      read_at: supForm.value.time,
      reported_by: me()
    })
    notify(`补录 #${r.id} 已提交，等待另一人复核（不能自录自审）`, 'ok')
    supForm.value.reading = ''; supForm.value.time = ''
    load()
  } catch (e) { notify(e.message, 'error') }
}

async function review(r, approve) {
  const note = prompt(approve ? '复核意见（通过）：' : '复核意见（驳回）：', approve ? '与船舶日志一致' : '数值存疑')
  if (note === null) return
  try {
    await api.post(`/readings/${r.id}/review`, { reviewer: me(), approve, note })
    notify(approve ? '补录已复核通过并归入计量段' : '补录已驳回', approve ? 'ok' : 'hold')
    load(); refresh()
  } catch (e) { notify(e.message, 'error') }
}

async function resolveAnomaly(r, decision) {
  const note = prompt(decision === 'accepted' ? '采信依据：' : '剔除依据：',
    decision === 'accepted' ? '人工核实数值真实' : '设备故障误传，剔除')
  if (note === null) return
  try {
    await api.post(`/readings/${r.id}/resolve`, { reviewer: me(), resolution: decision, note })
    notify(`异常 #${r.id} 已处置（原始记录保留为证据）`, 'ok')
    load(); refresh()
  } catch (e) { notify(e.message, 'error') }
}

async function setAuto() {
  try {
    await api.post('/sim/auto', {
      meter_code: sim.value.meter, on: sim.value.on,
      step_kwh: Number(sim.value.step), interval_sec: Number(sim.value.interval)
    })
    notify(`设备 ${sim.value.meter} 自动回传已${sim.value.on ? '开启（每 ' + sim.value.interval + 's +' + sim.value.step + 'kWh）' : '关闭'}`, 'info')
    load()
  } catch (e) { notify(e.message, 'error') }
}

const pendingCount = () => readings.value.filter(r => r.status === 'pending_review').length
const flaggedCount = () => readings.value.filter(r => r.status === 'flagged').length
</script>

<template>
  <div class="grid two">
    <div>
      <div class="panel">
        <h2>📡 电表设备模拟（本地）</h2>
        <div class="row">
          <div><label>电表</label>
            <select v-model="devForm.meter">
              <option v-for="m in meta.meters" :key="m.code" :value="m.code">
                {{ m.berth_code }} · {{ m.code }}（{{ m.max_power_kw }}kW）
              </option>
            </select></div>
          <div><label>表码（留空=自动+增量）</label>
            <input type="number" step="0.01" v-model="devForm.reading" placeholder="自动递增" /></div>
        </div>
        <div class="row">
          <div><label>读数时间</label><input type="datetime-local" v-model="devForm.time" /></div>
          <div><label>设备序列号（留空=自动分配）</label>
            <input type="number" v-model="devForm.seq" placeholder="自动" /></div>
        </div>
        <div class="flow-actions">
          <button class="btn sm" @click="deviceSend">回传一次读数</button>
          <button class="btn ghost sm" @click="devForm.reading = -9999; deviceSend()">模拟读数倒退</button>
          <button class="btn ghost sm" @click="devForm.reading = 999999; deviceSend()">模拟异常跃升</button>
        </div>
        <p class="muted" style="font-size:12px;margin:8px 0 0">
          倒退/跃升快捷按钮会以明显异常值上报；同一序列号再次回传自动判重。
        </p>

        <h3>自动回传</h3>
        <div class="row">
          <div><label>电表</label><select v-model="sim.meter">
            <option v-for="m in meta.meters" :key="m.code" :value="m.code">{{ m.code }}</option>
          </select></div>
          <div><label>每 tick 增量 kWh</label><input type="number" v-model="sim.step" /></div>
          <div><label>间隔(秒)</label><input type="number" v-model="sim.interval" /></div>
        </div>
        <div class="flow-actions">
          <button class="btn ok sm" @click="sim.on = true; setAuto()">开启自动回传</button>
          <button class="btn danger sm" @click="sim.on = false; setAuto()">关闭</button>
        </div>
        <table style="margin-top:8px">
          <tbody>
            <tr v-for="(st, code) in simStates" :key="code">
              <td><strong>{{ code }}</strong></td>
              <td>当前表码 <b class="money">{{ st.value?.toFixed(2) }}</b></td>
              <td>下一序号 {{ st.next_seq }}</td>
              <td><span class="tag" :class="st.auto ? 'ok' : ''">{{ st.auto ? '自动中' : '手动' }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="panel" v-if="role === 'operator'" style="margin-top:16px">
        <h2>✍️ 漏报补录（提交后待另一人复核）</h2>
        <div class="row">
          <div><label>接电单</label>
            <select v-model="supForm.conn">
              <option value="">请选择</option>
              <option v-for="c in conns" :key="c.id" :value="c.id">{{ c.code }} · {{ c.vessel_name }}</option>
            </select></div>
          <div><label>电表</label><select v-model="supForm.meter">
            <option v-for="m in meta.meters" :key="m.code" :value="m.code">{{ m.code }}</option>
          </select></div>
        </div>
        <div class="row">
          <div><label>补录表码</label><input type="number" step="0.01" v-model="supForm.reading" /></div>
          <div><label>实际读数时间</label><input type="datetime-local" v-model="supForm.time" /></div>
        </div>
        <button class="btn sm" @click="submitSup">提交补录（pending_review）</button>
      </div>
    </div>

    <div class="panel">
      <h2>原始读数台账（证据链，永不物理删除）
        <span v-if="flaggedCount()" class="tag danger">异常 {{ flaggedCount() }}</span>
        <span v-if="pendingCount()" class="tag warn">待复核 {{ pendingCount() }}</span>
      </h2>
      <div class="row" style="margin-bottom:8px">
        <select v-model="filter.meter" @change="load">
          <option value="">全部电表</option>
          <option v-for="m in meta.meters" :key="m.code" :value="m.code">{{ m.code }}</option>
        </select>
        <select v-model="filter.connection_id" @change="load">
          <option value="">全部接电单</option>
          <option v-for="c in conns" :key="c.id" :value="c.id">{{ c.code }}</option>
        </select>
        <select v-model="filter.status" @change="load">
          <option value="">全部状态</option>
          <option value="accepted">采信</option>
          <option value="flagged">异常待处置</option>
          <option value="pending_review">补录待复核</option>
          <option value="rejected">剔除/驳回</option>
          <option value="excluded">停电窗口不采信</option>
        </select>
      </div>
      <div class="list-scroll">
        <table>
          <thead><tr>
            <th>#</th><th>接电单</th><th>电表</th><th>时间</th><th>表码</th><th>来源/序号</th>
            <th>状态</th><th>异常</th><th>处置</th>
          </tr></thead>
          <tbody>
            <tr v-for="r in readings" :key="r.id">
              <td>{{ r.id }}</td>
              <td>{{ r.connection_id ?? '—' }}</td>
              <td>{{ r.meter_code }}</td>
              <td>{{ fmtTime(r.read_at) }}</td>
              <td class="money">{{ r.reading }}</td>
              <td>
                {{ r.source === 'device' ? '设备' : '人工' }}
                <span class="muted">#{{ r.device_seq ?? '—' }}</span>
                <span v-if="r.dup_count" class="tag warn">重复×{{ r.dup_count + 1 }}</span>
              </td>
              <td>
                <span class="tag"
                  :class="r.status==='accepted'?'ok':(r.status==='flagged'?'danger':(r.status==='excluded'?'hold':'warn'))">
                  {{ STATUS_LABELS[r.status] || r.status }}
                </span>
              </td>
              <td>{{ ANOMALY_LABELS[r.anomaly] || (r.anomaly || '') }}</td>
              <td>
                <div v-if="r.status === 'pending_review'">
                  <template v-if="role === 'reviewer' || (role === 'operator' && r.reported_by !== me())">
                    <button class="btn ok sm" @click="review(r, true)">复核通过</button>
                    <button class="btn danger sm" @click="review(r, false)">驳回</button>
                  </template>
                  <span v-else class="muted">需另一人复核</span>
                </div>
                <div v-else-if="r.status === 'flagged'">
                  <button class="btn ok sm" @click="resolveAnomaly(r, 'accepted')"
                          :disabled="role !== 'reviewer'">采信</button>
                  <button class="btn danger sm" @click="resolveAnomaly(r, 'discarded')"
                          :disabled="role !== 'reviewer'">剔除</button>
                  <span v-if="role !== 'reviewer'" class="muted">复核员处置</span>
                </div>
                <div v-else-if="r.device_seq && r.source === 'device'">
                  <button class="btn ghost sm" @click="replay(r)">重放回传</button>
                </div>
                <div v-else-if="r.status === 'rejected'" class="muted">
                  {{ r.resolution }}<template v-if="r.reviewed_by"> · {{ r.reviewed_by }}</template>
                </div>
                <span v-else class="muted">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
