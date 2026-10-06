<script setup>
import { ref, onMounted } from 'vue'
import {
  sessionDetail, listBerths, addReading, changeBerth, startOutage, endOutage,
  endSession, supplement, billPreview, confirmBill,
  fmtDT, tag, fmtMoney, fmtDur, nowLocalInput, toRFC,
} from '../api.js'

const props = defineProps({ id: Number })
const emit = defineEmits(['back', 'open-bill'])

const data = ref(null)
const berths = ref([])
const preview = ref(null)
const notice = ref('')
const error = ref('')

const readingForm = ref({ value: '', read_at: nowLocalInput(), operator: '值班员甲' })
const deviceForm = ref({ value: '', read_at: nowLocalInput(), device_id: 'METER-SIM-01', uid: '' })
const lastDevice = ref(null)
const berthForm = ref({ berth_id: '', value: '', at: nowLocalInput(), operator: '值班员甲' })
const outageForm = ref({ at: nowLocalInput(), reason: '' })
const endForm = ref({ value: '', at: nowLocalInput(), operator: '值班员甲' })
const suppForm = ref({ value: '', read_at: '', operator: '值班员甲', note: '' })

let seq = 0
function genUID() {
  seq += 1
  return `SIM-${Date.now().toString(36).toUpperCase()}-${seq}`
}

async function load() {
  notice.value = error.value = ''
  data.value = await sessionDetail(props.id)
  if (berthForm.value.berth_id === '' && berths.value.length) {
    berthForm.value.berth_id = berths.value[0].id
  }
  if (['ended', 'active'].includes(data.value.session.status)) {
    try { preview.value = await billPreview(props.id, data.value.session.status === 'active') }
    catch (e) { preview.value = null }
  } else {
    preview.value = null
  }
}
onMounted(async () => {
  berths.value = await listBerths()
  await load()
})

function fail(e) { error.value = e.message; notice.value = '' }

async function addManual() {
  try {
    const r = await addReading(props.id, {
      source: 'manual', value: Number(readingForm.value.value),
      read_at: toRFC(readingForm.value.read_at), operator: readingForm.value.operator,
    })
    notice.value = r.anomaly ? `⚠ 已触发异常（${tag(r.anomaly.kind)}），结算已暂停：${r.anomaly.detail}` : '手工读数已登记'
    readingForm.value.value = ''
    await load()
  } catch (e) { fail(e) }
}

async function sendDevice() {
  try {
    if (!deviceForm.value.uid) deviceForm.value.uid = genUID()
    const r = await addReading(props.id, {
      source: 'device', value: Number(deviceForm.value.value),
      read_at: toRFC(deviceForm.value.read_at),
      device_id: deviceForm.value.device_id, message_uid: deviceForm.value.uid,
    })
    lastDevice.value = r
    if (r.duplicate) notice.value = `检测到设备重复回传（报文 ${deviceForm.value.uid}），已幂等丢弃，未产生新读数。`
    else if (r.anomaly) notice.value = `⚠ 设备读数异常（${tag(r.anomaly.kind)}），已暂停结算：${r.anomaly.detail}`
    else notice.value = `设备报文 ${deviceForm.value.uid} 已入账。`
    deviceForm.value.value = ''
    deviceForm.value.uid = ''
    await load()
  } catch (e) { fail(e) }
}

async function resendLast() {
  if (!lastDevice.value) return
  try {
    const r = await addReading(props.id, {
      source: 'device', value: lastDevice.value.reading.value,
      read_at: lastDevice.value.reading.read_at,
      device_id: lastDevice.value.reading.device_id,
      message_uid: lastDevice.value.reading.message_uid,
    })
    notice.value = r.duplicate
      ? `模拟设备重发报文 ${lastDevice.value.reading.message_uid}：服务端识别为重复回传，原样返回且不新增读数。`
      : '重传结果异常（应被判重）'
    await load()
  } catch (e) { fail(e) }
}

async function doChangeBerth() {
  try {
    await changeBerth(props.id, {
      berth_id: Number(berthForm.value.berth_id), value: Number(berthForm.value.value),
      at: toRFC(berthForm.value.at), operator: berthForm.value.operator,
    })
    notice.value = '已换泊位：原计量段关闭并按原泊位费率落定，新计量段按新泊位费率计费。'
    berthForm.value.value = ''
    await load()
  } catch (e) { fail(e) }
}

async function doOutage(start) {
  try {
    if (start) {
      await startOutage(props.id, { at: toRFC(outageForm.value.at), reason: outageForm.value.reason, operator: '值班员甲' })
      notice.value = '停电开始，该时段不计入服务时长。'
    } else {
      await endOutage(props.id, { at: toRFC(outageForm.value.at), operator: '值班员甲' })
      notice.value = '停电结束，恢复供电计量。'
    }
    outageForm.value.at = nowLocalInput()
    await load()
  } catch (e) { fail(e) }
}

async function doEnd() {
  try {
    await endSession(props.id, {
      value: Number(endForm.value.value), at: toRFC(endForm.value.at), operator: endForm.value.operator,
    })
    notice.value = '接电已结束，最后计量段关闭，可在确认无误后生成账单。'
    await load()
  } catch (e) { fail(e) }
}

async function doSupplement() {
  try {
    await supplement(props.id, {
      operator: suppForm.value.operator, value: Number(suppForm.value.value),
      read_at: toRFC(suppForm.value.read_at), note: suppForm.value.note,
    })
    notice.value = '补录已提交，状态为「待复核」，必须由另一名值班员在复核台复核后方可生效。'
    suppForm.value = { value: '', read_at: '', operator: '值班员甲', note: '' }
    await load()
  } catch (e) { fail(e) }
}

async function doConfirmBill() {
  try {
    const b = await confirmBill(props.id)
    notice.value = `账单 ${b.bill_no} 已生成，等待船方确认。`
    await load()
    emit('open-bill', b.id)
  } catch (e) { fail(e) }
}

const openOutage = (segs) => segs.some(s => s.status === 'outage')
const segmentSeq = (id) => {
  const s = data.value?.segments.find(x => x.id === id)
  return s ? `第${s.seq}段` : id
}
</script>

<template>
  <div v-if="data">
    <button class="btn sm ghost" @click="emit('back')">← 返回会话列表</button>

    <div class="card" style="margin-top:12px">
      <div class="row-actions" style="justify-content:space-between">
        <h2 style="margin:0">会话 #{{ data.session.id }} · {{ data.session.vessel?.name }}</h2>
        <span class="tag" :class="data.session.status">{{ tag(data.session.status) }}</span>
      </div>
      <dl class="kv" style="margin-top:12px">
        <dt>起始泊位</dt><dd>{{ data.session.berth?.name }}</dd>
        <dt>值班员</dt><dd>{{ data.session.operator }}</dd>
        <dt>接电时间</dt><dd>{{ fmtDT(data.session.start_time) }}</dd>
        <dt>结束时间</dt><dd>{{ fmtDT(data.session.end_time) }}</dd>
      </dl>
      <div v-if="data.session.status === 'blocked'" class="banner" style="margin-top:12px">
        🚫 存在未处理的读数异常，本会话已暂停结算（不能结束/换泊位/出账）。请补录并由另一人复核。
      </div>
      <div v-if="notice" class="notice ok" style="margin-top:12px">{{ notice }}</div>
      <div v-if="error" class="notice err" style="margin-top:12px">{{ error }}</div>
    </div>

    <!-- 计量段时间线 -->
    <div class="card">
      <h2>② 真实计量区间（换泊位分段 / 停电挂起）</h2>
      <div class="seg-timeline">
        <div v-for="seg in data.segments" :key="seg.id"
             class="seg-node" :class="seg.status">
          <div class="row-actions" style="gap:10px">
            <strong>第 {{ seg.seq }} 段 · {{ seg.berth.name }}</strong>
            <span class="tag" :class="seg.status">{{ tag(seg.status) }}</span>
            <span v-if="seg.close_reason === 'berth_change'" class="muted">（换泊位关闭）</span>
            <span v-else-if="seg.close_reason === 'session_end'" class="muted">（结束接电关闭）</span>
          </div>
          <div class="muted" style="margin:4px 0">
            {{ fmtDT(seg.start_time) }} → {{ fmtDT(seg.end_time) }} ｜
            读数 {{ seg.start_reading }} → {{ seg.end_reading || '（进行中）' }} ｜
            费率 {{ seg.berth.rate_per_kwh }} 元/kWh ＋ {{ seg.berth.service_per_min }} 元/分钟
          </div>
        </div>
      </div>
    </div>

    <!-- 操作区 -->
    <div class="card" v-if="!data.session.end_time">
      <h2>③ 抄表 / 设备回传 / 换泊位 / 停电</h2>

      <h3>手工抄录读数</h3>
      <div class="grid g4">
        <div><label>读数 (kWh)</label><input type="number" step="0.01" v-model="readingForm.value" /></div>
        <div><label>抄表时间</label><input type="datetime-local" v-model="readingForm.read_at" /></div>
        <div><label>值班员</label><input v-model="readingForm.operator" /></div>
        <div style="align-self:end"><button class="btn" @click="addManual">提交读数</button></div>
      </div>

      <h3>电表设备模拟回传（同一报文ID重发用于验证去重）</h3>
      <div class="grid g4">
        <div><label>读数 (kWh)</label><input type="number" step="0.01" v-model="deviceForm.value" /></div>
        <div><label>回传时间</label><input type="datetime-local" v-model="deviceForm.read_at" /></div>
        <div><label>设备ID</label><input v-model="deviceForm.device_id" /></div>
        <div><label>报文ID（留空自动生成）</label><input v-model="deviceForm.uid" placeholder="自动生成" /></div>
      </div>
      <div style="margin-top:8px">
        <button class="btn teal" @click="sendDevice">设备上报</button>
        <button class="btn ghost" :disabled="!lastDevice" @click="resendLast">模拟重发上一条报文</button>
      </div>

      <h3>换泊位（结束原计量段，新段使用新泊位费率，电表读数连续）</h3>
      <div class="grid g4">
        <div>
          <label>新泊位</label>
          <select v-model="berthForm.berth_id">
            <option v-for="b in berths" :key="b.id" :value="b.id">
              {{ b.name }}（{{ b.rate_per_kwh }}元/kWh）
            </option>
          </select>
        </div>
        <div><label>换泊时电表读数</label><input type="number" step="0.01" v-model="berthForm.value" /></div>
        <div><label>换泊时间</label><input type="datetime-local" v-model="berthForm.at" /></div>
        <div><label>值班员</label><input v-model="berthForm.operator" /></div>
      </div>
      <div style="margin-top:8px"><button class="btn warn" @click="doChangeBerth">确认换泊位</button></div>

      <h3>停电登记（停电期间不算连续服务、不计服务费）</h3>
      <div class="grid g4">
        <div><label>时间</label><input type="datetime-local" v-model="outageForm.at" /></div>
        <div style="grid-column: span 2"><label>原因</label><input v-model="outageForm.reason" placeholder="如：港区线路检修" /></div>
      </div>
      <div style="margin-top:8px">
        <button class="btn warn" @click="doOutage(true)" :disabled="openOutage(data.segments)">停电开始</button>
        <button class="btn" @click="doOutage(false)" :disabled="!openOutage(data.segments)">停电结束 / 恢复供电</button>
      </div>

      <h3>结束接电</h3>
      <div class="grid g4">
        <div><label>电表止码 (kWh)</label><input type="number" step="0.01" v-model="endForm.value" /></div>
        <div><label>结束时间</label><input type="datetime-local" v-model="endForm.at" /></div>
        <div><label>值班员</label><input v-model="endForm.operator" /></div>
        <div style="align-self:end"><button class="btn danger" @click="doEnd">结束接电</button></div>
      </div>
    </div>

    <!-- 异常与补录 -->
    <div class="card">
      <h2>④ 异常读数与漏报补录（须另一人复核）</h2>
      <table v-if="data.anomalies.length">
        <thead><tr><th>类型</th><th>说明</th><th>状态</th><th>时间</th></tr></thead>
        <tbody>
          <tr v-for="a in data.anomalies" :key="a.id">
            <td><span class="tag" :class="a.status === 'open' ? 'open' : 'resolved'">{{ tag(a.kind) }}</span></td>
            <td>{{ a.detail }}</td>
            <td><span class="tag" :class="a.status">{{ a.status === 'open' ? '待处理' : '已处理' }}</span></td>
            <td class="nowrap">{{ fmtDT(a.created_at) }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="muted">暂无异常记录。</div>

      <h3>提交补录读数（提交后进入复核台）</h3>
      <div class="grid g4">
        <div><label>补录读数 (kWh)</label><input type="number" step="0.01" v-model="suppForm.value" /></div>
        <div><label>实际抄表时间</label><input type="datetime-local" v-model="suppForm.read_at" /></div>
        <div><label>录入值班员</label><input v-model="suppForm.operator" /></div>
        <div><label>情况说明</label><input v-model="suppForm.note" /></div>
      </div>
      <div style="margin-top:8px"><button class="btn" @click="doSupplement">提交补录（待另一人复核）</button></div>
    </div>

    <!-- 原始读数 -->
    <div class="card">
      <h2>⑤ 原始读数台账（账单核对依据）</h2>
      <table>
        <thead>
          <tr><th>时间</th><th>段</th><th>来源</th><th>读数</th><th>状态</th><th>录入/设备</th><th>复核人</th></tr>
        </thead>
        <tbody>
          <tr v-for="r in data.readings" :key="r.id">
            <td class="nowrap">{{ fmtDT(r.read_at) }}</td>
            <td>{{ segmentSeq(r.segment_id) }}</td>
            <td><span class="tag" :class="r.source">{{ tag(r.source) }}</span><span v-if="r.message_uid" class="muted"> #{{ r.message_uid }}</span></td>
            <td class="money">{{ r.value }}</td>
            <td><span class="tag" :class="r.status">{{ tag(r.status) }}</span></td>
            <td>{{ r.source === 'device' ? r.device_id : r.operator }}</td>
            <td>{{ r.reviewer || '—' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 账单预览 -->
    <div class="card" v-if="preview">
      <h2>⑥ 逐段费用预览</h2>
      <table>
        <thead>
          <tr>
            <th>段/泊位</th><th>区间</th><th>起止读数</th><th>用电量(kWh)</th><th>费率</th>
            <th>服务时长(扣停电)</th><th>电费</th><th>服务费</th><th>小计</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="it in preview.items" :key="it.segment.id">
            <td>第{{ it.segment.seq }}段 · {{ it.berth_name }}<span v-if="it.provisional" class="tag pending">预估</span></td>
            <td class="nowrap">{{ fmtDT(it.start_time) }}<br>{{ fmtDT(it.end_time) }}</td>
            <td>{{ it.start_reading }} → {{ it.end_reading }}</td>
            <td>{{ fmtMoney(it.kwh) }}</td>
            <td>{{ it.rate_per_kwh }}元/kWh<br><span class="muted">{{ it.service_per_min }}元/分</span></td>
            <td>{{ fmtDur(it.service_seconds) }}<br><span class="muted" v-if="it.outage_seconds">停电扣除 {{ fmtDur(it.outage_seconds) }}</span></td>
            <td>{{ fmtMoney(it.energy_amount) }}</td>
            <td>{{ fmtMoney(it.service_amount) }}</td>
            <td class="money">{{ fmtMoney(it.amount) }}</td>
          </tr>
          <tr class="total-row">
            <td colspan="3">合计</td>
            <td>{{ fmtMoney(preview.total_kwh) }}</td>
            <td></td><td></td>
            <td>{{ fmtMoney(preview.energy_amount) }}</td>
            <td>{{ fmtMoney(preview.service_amount) }}</td>
            <td class="money">¥{{ fmtMoney(preview.total_amount) }}</td>
          </tr>
        </tbody>
      </table>
      <div style="margin-top:12px" v-if="data.session.status === 'ended'">
        <button class="btn teal" @click="doConfirmBill">核对无误，生成账单（交船方确认）</button>
      </div>
    </div>
  </div>
</template>
