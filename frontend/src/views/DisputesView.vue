<script setup>
import { ref, onMounted } from 'vue'
import { listDisputes, resolveDispute, fmtDT } from '../api.js'

const emit = defineEmits(['open-bill'])
const disputes = ref([])
const filter = ref('')
const forms = ref({})
const msg = ref('')

async function load() { disputes.value = await listDisputes(filter.value) }
onMounted(load)

function openForm(id) {
  forms.value[id] = forms.value[id] || { handler: '值班长', conclusion: '', action: 'uphold' }
}

async function resolve(id) {
  const f = forms.value[id]
  msg.value = ''
  if (!f.handler.trim() || !f.conclusion.trim()) { msg.value = '请填写处理人与处理结论'; return }
  try {
    const r = await resolveDispute(id, f)
    msg.value = f.action === 'correct'
      ? `异议成立，已生成更正账单 #${r.new_bill_id}；原账单保留为「已失效」。`
      : '已维持原账单。'
    delete forms.value[id]
    await load()
  } catch (e) { msg.value = e.message }
}
</script>

<template>
  <div class="card">
    <h2>船方异议处理</h2>
    <div v-if="msg" class="notice info">{{ msg }}</div>
    <div class="pill-row">
      <button :class="{ on: filter === '' }" @click="filter = ''; load()">全部</button>
      <button :class="{ on: filter === 'open' }" @click="filter = 'open'; load()">待处理</button>
      <button :class="{ on: filter === 'resolved' }" @click="filter = 'resolved'; load()">已处理</button>
    </div>
    <table>
      <thead>
        <tr><th>#</th><th>账单</th><th>理由</th><th>船方证据</th><th>状态</th><th>处理结论 / 更正账单</th><th></th></tr>
      </thead>
      <tbody>
        <template v-for="d in disputes" :key="d.id">
          <tr>
            <td>{{ d.id }}</td>
            <td><button class="btn sm ghost" @click="emit('open-bill', d.bill_id)">#{{ d.bill_id }}</button></td>
            <td style="max-width:220px">{{ d.reason }}</td>
            <td style="max-width:220px">{{ d.evidence || '—' }}</td>
            <td><span class="tag" :class="d.status">{{ d.status === 'open' ? '待处理' : '已处理' }}</span></td>
            <td>
              <template v-if="d.status === 'resolved'">
                <div>{{ d.conclusion }}</div>
                <div class="muted">处理人：{{ d.handler }} ｜
                  <span v-if="d.new_bill_id">更正账单 #{{ d.new_bill_id }}</span>
                  <span v-else>维持原账单</span>
                </div>
              </template>
              <button v-else class="btn sm warn" @click="openForm(d.id)">处理</button>
            </td>
            <td class="nowrap">{{ fmtDT(d.created_at) }}</td>
          </tr>
          <tr v-if="forms[d.id]">
            <td></td>
            <td colspan="6">
              <div class="card" style="margin:0;background:#fafcfe">
                <div class="grid g3">
                  <div><label>处理人</label><input v-model="forms[d.id].handler" /></div>
                  <div style="grid-column:span 2"><label>处理结论（写入记录，与原账单证据一并长期保留）</label>
                    <input v-model="forms[d.id].conclusion" placeholder="如：核实属实，已补登停电并重算" /></div>
                </div>
                <div style="margin-top:10px">
                  <label>处理方式</label>
                  <label style="display:inline;margin-right:18px">
                    <input type="radio" style="width:auto" value="uphold" v-model="forms[d.id].action" /> 维持原账单
                  </label>
                  <label style="display:inline">
                    <input type="radio" style="width:auto" value="correct" v-model="forms[d.id].action" /> 异议成立 → 另出更正账单（原账单保留不改）
                  </label>
                </div>
                <div style="margin-top:10px">
                  <button class="btn teal" @click="resolve(d.id)">提交处理结论</button>
                </div>
              </div>
            </td>
          </tr>
        </template>
        <tr v-if="!disputes.length"><td colspan="7"><div class="empty">暂无异议</div></td></tr>
      </tbody>
    </table>
  </div>
</template>
