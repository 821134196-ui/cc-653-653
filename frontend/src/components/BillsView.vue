<script setup>
import { inject, ref, watch, onMounted, computed } from 'vue'
import { api, fmtTime, fmtMoney, BILL_STATUS } from '../api.js'

const { role, roleMap, notify, refresh, tick } = inject('ctx')
const bills = ref([])
const selectedId = ref(null)
const detail = ref(null)
const showSnapshot = ref(false)

async function load() {
  bills.value = await api.get('/bills')
  if (selectedId.value) {
    detail.value = await api.get(`/bills/${selectedId.value}`)
  }
}
watch(tick, load)
onMounted(load)

const me = () => roleMap.value[role.value].code

function select(b) { selectedId.value = b.id; showSnapshot.value = false; load() }

async function confirm() {
  try {
    await api.post(`/bills/${selectedId.value}/confirm`, { by: me() })
    notify('船方已确认账单', 'ok'); load()
  } catch (e) { notify(e.message, 'error') }
}
async function pay() {
  try {
    const r = await api.post(`/bills/${selectedId.value}/pay`, { by: me() })
    notify(`付款成功，回执 ${r.code}，实付 ¥${fmtMoney(r.amount)}（${r.method}）`, 'ok'); load()
  } catch (e) { notify(e.message, 'error') }
}

const disputeReason = ref('')
async function dispute() {
  if (!disputeReason.value) return notify('请填写异议理由', 'error')
  try {
    await api.post(`/bills/${selectedId.value}/dispute`, { reason: disputeReason.value, by: me() })
    notify('异议已提出：原账单、证据与快照已冻结保留', 'hold')
    disputeReason.value = ''
    load(); refresh()
  } catch (e) { notify(e.message, 'error') }
}

const netPayable = computed(() => {
  if (!detail.value) return 0
  let v = detail.value.bill.total_amount
  for (const n of detail.value.credit_notes) v += n.kind === 'refund' ? -n.amount : n.amount
  return fmtMoney(v)
})
</script>

<template>
  <div class="grid two">
    <div class="panel">
      <h2>账单列表</h2>
      <div class="list-scroll">
        <table>
          <thead><tr><th>账单号</th><th>船名</th><th>金额</th><th>状态</th></tr></thead>
          <tbody>
            <tr v-for="b in bills" :key="b.id" class="selectable" :class="{ active: selectedId === b.id }" @click="select(b)">
              <td>{{ b.code }}</td>
              <td>{{ b.vessel_name }}</td>
              <td class="money">¥{{ fmtMoney(b.total_amount) }}</td>
              <td><span class="tag"
                :class="b.status==='paid'?'ok':(b.status==='disputed'?'hold':(b.status==='confirmed'?'info':'warn'))">
                {{ BILL_STATUS[b.status] }}</span></td>
            </tr>
            <tr v-if="!bills.length"><td colspan="4" class="empty">暂无账单（接电结束后由值班员生成）</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-if="!detail" class="panel empty">请选择账单查看逐段费用与原始读数</div>

    <div v-else class="panel">
      <h2>账单 {{ detail.bill.code }} · {{ detail.bill.vessel_name }}</h2>
      <dl class="kv">
        <dt>账单状态</dt><dd>{{ BILL_STATUS[detail.bill.status] }}</dd>
        <dt>生成时间</dt><dd>{{ fmtTime(detail.bill.created_at) }}</dd>
        <dt>确认信息</dt><dd>{{ detail.bill.confirmed_by || '—' }} {{ detail.bill.confirmed_by ? '· ' + fmtTime(detail.bill.confirmed_at) : '' }}</dd>
      </dl>

      <h3>逐段费用（船方可逐段核对）</h3>
      <table>
        <thead><tr>
          <th>段</th><th>泊位/电表</th><th>服务区间</th><th>起止表码</th>
          <th>电量(kWh)</th><th>时长(h)</th><th>电费费率</th><th>服务费</th><th class="right">金额</th>
        </tr></thead>
        <tbody>
          <tr v-for="it in detail.bill.items" :key="it.id">
            <td>{{ it.seq }}<div class="muted" style="font-size:11px">{{ it.reason }}</div></td>
            <td>{{ it.berth_code }}<div class="muted">{{ it.meter_code }}</div></td>
            <td>{{ fmtTime(it.start_time) }}<br><span class="muted">至 {{ fmtTime(it.end_time) }}</span></td>
            <td class="money">{{ it.start_reading }} → {{ it.end_reading }}</td>
            <td class="money">{{ it.kwh }}</td>
            <td>{{ it.hours }}</td>
            <td>{{ it.rate_per_kwh }} 元/kWh<br><span class="money muted">{{ it.service_fee_per_hr }}元/h</span></td>
            <td class="money">电{{ fmtMoney(it.energy_fee) }}<br>服{{ fmtMoney(it.service_fee) }}</td>
            <td class="right money"><b>¥{{ fmtMoney(it.amount) }}</b></td>
          </tr>
        </tbody>
      </table>

      <div class="totals">
        <div class="t">总电量 <b class="money">{{ fmtMoney(detail.bill.total_kwh) }}</b> kWh</div>
        <div class="t">总服务时长 <b>{{ detail.bill.total_hours }}</b> h（已剔除停电）</div>
        <div class="t">电费 <b class="money">¥{{ fmtMoney(detail.bill.energy_fee) }}</b></div>
        <div class="t">服务费 <b class="money">¥{{ fmtMoney(detail.bill.service_fee) }}</b></div>
        <div class="t">原账单金额 <b class="money">¥{{ fmtMoney(detail.bill.total_amount) }}</b></div>
      </div>

      <div v-if="detail.credit_notes.length" style="margin-top:10px">
        <h3>调整单（异议处理结果，不改动原账单）</h3>
        <table>
          <thead><tr><th>调整单号</th><th>类型</th><th class="right">金额</th><th>原因</th></tr></thead>
          <tbody>
            <tr v-for="n in detail.credit_notes" :key="n.id">
              <td>{{ n.code }}</td>
              <td><span class="tag" :class="n.kind === 'refund' ? 'ok' : 'warn'">
                {{ n.kind === 'refund' ? '退款冲减' : '补收' }}</span></td>
              <td class="right money">{{ n.kind === 'refund' ? '-' : '+' }}¥{{ fmtMoney(n.amount) }}</td>
              <td>{{ n.reason }}</td>
            </tr>
          </tbody>
        </table>
        <div class="totals"><div class="t">实际应付 <b class="money">¥{{ netPayable }}</b></div></div>
      </div>

      <details class="raw" style="margin-top:12px" :open="showSnapshot">
        <summary>🔍 查看原始读数与出账快照（不可篡改的证据）</summary>
        <div class="snapshot-box" style="margin-top:8px">{{ JSON.stringify(detail.snapshot, null, 2) }}</div>
      </details>

      <div v-if="detail.receipt" class="banner ok" style="margin-top:12px">
        付款回执 {{ detail.receipt.code }}：¥{{ fmtMoney(detail.receipt.amount) }} 已于 {{ fmtTime(detail.receipt.paid_at) }} 由 {{ detail.receipt.paid_by }} 支付（{{ detail.receipt.method }}）
      </div>

      <div class="flow-actions" v-if="role === 'vessel'">
        <button class="btn ok sm" v-if="detail.bill.status === 'pending_confirmation'" @click="confirm">船方确认账单</button>
        <button class="btn sm" v-if="detail.bill.status === 'confirmed'" @click="pay">模拟付款（生成回执）</button>
      </div>
      <div class="panel" style="margin-top:12px;background:#fafbfc" v-if="role==='vessel' && detail.bill.status !== 'paid'">
        <h3>对账单有异议？（提出后原账单保留冻结，不会被直接修改）</h3>
        <div class="row">
          <input v-model="disputeReason" placeholder="例：第2段服务费时段与实际不符" />
          <button class="btn warn sm" style="margin-top:0" @click="dispute">提出异议</button>
        </div>
      </div>
    </div>
  </div>
</template>
