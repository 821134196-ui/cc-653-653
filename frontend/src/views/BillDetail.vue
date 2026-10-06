<script setup>
import { ref, onMounted } from 'vue'
import {
  billDetail, billSnapshot, shipConfirm, raiseDispute, payBill,
  fmtDT, fmtMoney, fmtDur, tag,
} from '../api.js'

const props = defineProps({ id: Number })
const emit = defineEmits(['back', 'open-session', 'open'])

const d = ref(null)
const snap = ref(null)
const showSnap = ref(false)
const showDispute = ref(false)
const dispute = ref({ reason: '', evidence: '' })
const channel = ref('模拟支付网关')
const msg = ref('')
const err = ref('')

async function load() {
  msg.value = err.value = ''
  d.value = await billDetail(props.id)
  try { snap.value = await billSnapshot(props.id) } catch { snap.value = null }
}
onMounted(load)

function fail(e) { err.value = e.message; msg.value = '' }

async function confirm() {
  try { await shipConfirm(props.id); msg.value = '船方已确认账单。'; await load() } catch (e) { fail(e) }
}
async function submitDispute() {
  if (!dispute.value.reason.trim()) { err.value = '请填写异议理由'; return }
  try {
    await raiseDispute(props.id, dispute.value);
    msg.value = '异议已登记：原账单、原始读数证据全部原样保留。'
    showDispute.value = false
    dispute.value = { reason: '', evidence: '' }
    await load()
  } catch (e) { fail(e) }
}
async function pay() {
  try {
    const r = await payBill(props.id, { channel: channel.value })
    msg.value = `模拟付款成功，回执号 ${r.receipt_no}，金额 ¥${fmtMoney(r.amount)}（${r.channel}）。`
    await load()
  } catch (e) { fail(e) }
}
async function toggleSnap() {
  showSnap.value = !showSnap.value
  if (showSnap.value && !snap.value) snap.value = await billSnapshot(props.id)
}
</script>

<template>
  <div v-if="d">
    <button class="btn sm ghost" @click="emit('back')">← 返回账单列表</button>

    <div class="card" style="margin-top:12px">
      <div class="row-actions" style="justify-content:space-between">
        <h2 style="margin:0">账单 {{ d.bill.bill_no }}</h2>
        <span class="tag" :class="d.bill.status">{{ tag(d.bill.status) }}</span>
      </div>
      <div v-if="msg" class="notice ok" style="margin-top:10px">{{ msg }}</div>
      <div v-if="err" class="notice err" style="margin-top:10px">{{ err }}</div>
      <div v-if="d.bill.status === 'void'" class="banner" style="margin-top:10px">
        本账单已被更正账单取代（见下方异议处理），但账单本身与原始证据完整保留、未被修改。
      </div>
      <dl class="kv" style="margin-top:12px">
        <dt>船名</dt><dd>{{ d.bill.vessel_name }}</dd>
        <dt>登记值班员</dt><dd>{{ d.bill.operator }}</dd>
        <dt>会话</dt><dd><a href="#" @click.prevent="emit('open-session', d.bill.session_id)">#{{ d.bill.session_id }}</a></dd>
        <dt>生成时间</dt><dd>{{ fmtDT(d.bill.created_at) }}</dd>
        <dt>船方确认</dt><dd>{{ fmtDT(d.bill.confirmed_at) }}</dd>
      </dl>
      <div class="row-actions" style="margin-top:12px">
        <button v-if="d.bill.status === 'pending'" class="btn teal" @click="confirm">船方确认账单</button>
        <button v-if="d.bill.status === 'confirmed'" class="btn ghost" @click="showDispute = !showDispute">船方提出异议</button>
        <template v-if="d.bill.status === 'confirmed'">
          <input v-model="channel" style="max-width:180px" placeholder="模拟支付渠道" />
          <button class="btn" @click="pay">模拟付款并取回执</button>
        </template>
      </div>
      <div v-if="showDispute" class="card" style="margin:12px 0 0; background:#fafcfe">
        <label>异议理由 *</label>
        <input v-model="dispute.reason" placeholder="如：某时段港区停电，服务费多计" />
        <label style="margin-top:8px">证据说明</label>
        <textarea v-model="dispute.evidence" rows="3" placeholder="船方日志 / 照片编号等"></textarea>
        <div style="margin-top:8px">
          <button class="btn warn" @click="submitDispute">提交异议（不改动原账单）</button>
        </div>
      </div>
    </div>

    <div class="card">
      <h2>逐段费用明细（船方可逐段核对）</h2>
      <table>
        <thead>
          <tr>
            <th>段/泊位</th><th>服务区间</th><th>电表起→止</th><th>用电量(kWh)</th>
            <th>电费费率</th><th>服务时长</th><th>停电扣除</th><th>电费</th><th>服务费</th><th>小计</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="it in d.items" :key="it.id">
            <td>第{{ it.seq }}段<br><strong>{{ it.berth_name }}</strong></td>
            <td class="nowrap">{{ fmtDT(it.start_time) }}<br>→ {{ fmtDT(it.end_time) }}</td>
            <td>{{ it.start_reading }} → {{ it.end_reading }}</td>
            <td>{{ fmtMoney(it.kwh) }}</td>
            <td>{{ it.rate_per_kwh }} 元/kWh</td>
            <td>{{ fmtDur(it.service_seconds) }}<br><span class="muted">{{ it.service_per_min }}元/分</span></td>
            <td>{{ it.outage_seconds ? fmtDur(it.outage_seconds) : '无' }}</td>
            <td>{{ fmtMoney(it.energy_amount) }}</td>
            <td>{{ fmtMoney(it.service_amount) }}</td>
            <td class="money">¥{{ fmtMoney(it.amount) }}</td>
          </tr>
          <tr class="total-row">
            <td colspan="3">合计</td>
            <td>{{ fmtMoney(d.bill.total_kwh) }}</td>
            <td colspan="3"></td>
            <td>{{ fmtMoney(d.bill.energy_amount) }}</td>
            <td>{{ fmtMoney(d.bill.service_amount) }}</td>
            <td class="money">¥{{ fmtMoney(d.bill.total_amount) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="card">
      <h2>原始读数 / 证据快照（出账时固化）</h2>
      <details class="snap" :open="showSnap">
        <summary @click.prevent="toggleSnap">{{ showSnap ? '收起' : '查看出账时的全部原始读数、停电区间与异常记录' }}</summary>
        <pre class="raw" v-if="snap">{{ JSON.stringify(snap, null, 2) }}</pre>
        <div v-else class="muted">无快照</div>
      </details>
    </div>

    <div class="card" v-if="d.dispute">
      <h2>异议记录</h2>
      <dl class="kv">
        <dt>理由</dt><dd>{{ d.dispute.reason }}</dd>
        <dt>船方证据</dt><dd>{{ d.dispute.evidence || '—' }}</dd>
        <dt>状态</dt><dd><span class="tag" :class="d.dispute.status">{{ d.dispute.status === 'open' ? '待处理' : '已处理' }}</span></dd>
        <template v-if="d.dispute.status === 'resolved'">
          <dt>处理结论</dt><dd>{{ d.dispute.conclusion }}</dd>
          <dt>处理人</dt><dd>{{ d.dispute.handler }}</dd>
          <dt>更正账单</dt>
          <dd v-if="d.dispute.new_bill_id">
            <button class="btn sm ghost" @click="emit('open', d.dispute.new_bill_id)">
              查看更正账单 #{{ d.dispute.new_bill_id }}
            </button>
          </dd>
          <dd v-else>维持原账单</dd>
        </template>
      </dl>
    </div>

    <div class="card" v-if="d.receipt">
      <h2>付款回执（本地模拟）</h2>
      <dl class="kv">
        <dt>回执号</dt><dd>{{ d.receipt.receipt_no }}</dd>
        <dt>金额</dt><dd class="money">¥{{ fmtMoney(d.receipt.amount) }}</dd>
        <dt>渠道</dt><dd>{{ d.receipt.channel }}</dd>
        <dt>到账时间</dt><dd>{{ fmtDT(d.receipt.paid_at) }}</dd>
      </dl>
    </div>
  </div>
</template>
