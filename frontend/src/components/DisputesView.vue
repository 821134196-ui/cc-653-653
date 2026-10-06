<script setup>
import { inject, ref, watch, onMounted } from 'vue'
import { api, fmtTime, fmtMoney } from '../api.js'

const { role, roleMap, notify, tick } = inject('ctx')
const disputes = ref([])
const form = ref({ conclusion: '', action: 'reject', amount: 0 })

async function load() { disputes.value = await api.get('/disputes') }
watch(tick, load)
onMounted(load)

const billCache = ref({})
async function ensureBill(id) {
  if (!billCache.value[id]) billCache.value[id] = (await api.get(`/bills/${id}`)).bill
  return billCache.value[id]
}
async function billCode(d) {
  const b = await ensureBill(d.bill_id)
  return `${b.code} · ${b.vessel_name}`
}
const codes = ref({})
watch(disputes, async (list) => {
  for (const d of list) {
    if (!codes.value[d.id]) codes.value[d.id] = await billCode(d)
  }
}, { immediate: true })

async function resolve(d) {
  if (!form.value.conclusion) return notify('处理结论不能为空', 'error')
  try {
    const r = await api.post(`/disputes/${d.id}/resolve`, {
      reviewer: roleMap.value[role.value].code,
      conclusion: form.value.conclusion,
      action: form.value.action,
      amount: Number(form.value.amount)
    })
    if (r.credit_note) notify(`已处理并生成${r.credit_note.kind === 'refund' ? '退款' : '补收'}调整单 ${r.credit_note.code}`, 'ok')
    else notify('异议已驳回，维持原账单', 'hold')
    form.value = { conclusion: '', action: 'reject', amount: 0 }
    load()
  } catch (e) { notify(e.message, 'error') }
}
</script>

<template>
  <div class="panel">
    <h2>船方异议处理（原账单 · 证据 · 结论全程留痕）</h2>
    <table>
      <thead><tr>
        <th>#</th><th>账单</th><th>异议理由</th><th>证据</th><th>提出人/时间</th>
        <th>状态</th><th>处理结论 / 调整</th>
      </tr></thead>
      <tbody>
        <tr v-for="d in disputes" :key="d.id">
          <td>{{ d.id }}</td>
          <td>{{ codes[d.id] || ('账单#' + d.bill_id) }}</td>
          <td style="min-width:180px">{{ d.reason }}</td>
          <td class="muted" style="font-size:12px;max-width:220px">{{ d.evidence }}</td>
          <td>{{ d.raised_by }}<div class="muted">{{ fmtTime(d.raised_at) }}</div></td>
          <td><span class="tag" :class="d.status === 'open' ? 'hold' : 'ok'">
            {{ d.status === 'open' ? '待处理' : '已处理' }}</span></td>
          <td style="min-width:300px">
            <template v-if="d.status === 'open'">
              <div v-if="role === 'reviewer'">
                <textarea v-model="form.conclusion" placeholder="处理结论（复核岗填写）"></textarea>
                <div class="row">
                  <select v-model="form.action">
                    <option value="reject">异议不成立，维持原账单</option>
                    <option value="refund">成立：退款冲减</option>
                    <option value="additional">成立：补收差额</option>
                  </select>
                  <input v-if="form.action !== 'reject'" type="number" step="0.01" v-model="form.amount" placeholder="金额" />
                </div>
                <button class="btn sm" @click="resolve(d)">提交处理结论</button>
              </div>
              <span v-else class="muted">等待复核岗处理</span>
            </template>
            <template v-else>
              <div>{{ d.conclusion }}</div>
              <div class="muted" style="font-size:12px">
                {{ d.resolved_by }} · {{ fmtTime(d.resolved_at) }}
                <span v-if="d.credit_note_id"> · 调整单 #{{ d.credit_note_id }}</span>
              </div>
            </template>
          </td>
        </tr>
        <tr v-if="!disputes.length"><td colspan="7" class="empty">暂无异议</td></tr>
      </tbody>
    </table>
  </div>
</template>
