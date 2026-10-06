<script setup>
import { ref, onMounted } from 'vue'
import { pendingReadings, listAnomalies, reviewReading, fmtDT, tag } from '../api.js'

const emit = defineEmits(['open-session'])
const pending = ref([])
const anomalies = ref([])
const reviewers = ref({})
const notes = ref({})
const msg = ref('')

async function load() {
  pending.value = await pendingReadings()
  anomalies.value = await listAnomalies('')
}
onMounted(load)

async function review(id, approve) {
  msg.value = ''
  const reviewer = reviewers.value[id]?.trim()
  if (!reviewer) { msg.value = '请填写复核人（必须与录入人不同）'; return }
  try {
    await reviewReading(id, { reviewer, approve, note: notes.value[id] || '' })
    msg.value = approve ? '补录已复核采纳，关联异常已关闭、会话恢复结算。' : '补录已驳回。'
    reviewers.value[id] = ''
    notes.value[id] = ''
    await load()
  } catch (e) { msg.value = e.message }
}

const kindCn = { backward: '读数倒退', jump: '异常跃升', missing: '漏报' }
</script>

<template>
  <div class="card">
    <h2>补录复核台（双人控制：复核人必须不同于录入人）</h2>
    <div v-if="msg" class="notice info">{{ msg }}</div>
    <table v-if="pending.length">
      <thead>
        <tr><th>#</th><th>会话</th><th>补录时间</th><th>读数</th><th>录入人</th><th>说明</th><th>复核人</th><th>意见</th><th>操作</th></tr>
      </thead>
      <tbody>
        <tr v-for="r in pending" :key="r.id">
          <td>{{ r.id }}</td>
          <td><a href="#" @click.prevent="emit('open-session', r.session_id)">#{{ r.session_id }}</a></td>
          <td class="nowrap">{{ fmtDT(r.read_at) }}</td>
          <td class="money">{{ r.value }}</td>
          <td>{{ r.operator }}</td>
          <td>{{ r.review_note || '—' }}</td>
          <td><input v-model="reviewers[r.id]" placeholder="另一名值班员" style="min-width:110px" /></td>
          <td><input v-model="notes[r.id]" placeholder="复核意见（可选）" style="min-width:140px" /></td>
          <td class="nowrap">
            <button class="btn sm teal" @click="review(r.id, true)">采纳</button>
            <button class="btn sm danger" @click="review(r.id, false)">驳回</button>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-else class="empty">没有待复核的补录读数。</div>
  </div>

  <div class="card">
    <h2>异常事件（倒退 / 跃升 / 漏报）</h2>
    <table v-if="anomalies.length">
      <thead><tr><th>#</th><th>会话</th><th>类型</th><th>说明</th><th>状态</th><th>处理方式</th><th>处理人</th><th>时间</th></tr></thead>
      <tbody>
        <tr v-for="a in anomalies" :key="a.id">
          <td>{{ a.id }}</td>
          <td><a href="#" @click.prevent="emit('open-session', a.session_id)">#{{ a.session_id }}</a></td>
          <td><span class="tag open">{{ kindCn[a.kind] || a.kind }}</span></td>
          <td>{{ a.detail }}</td>
          <td><span class="tag" :class="a.status">{{ a.status === 'open' ? '待处理（结算暂停）' : '已处理' }}</span></td>
          <td>{{ a.resolution === 'supplement' ? '补录采纳' : (a.resolution || '—') }}</td>
          <td>{{ a.resolver || '—' }}</td>
          <td class="nowrap">{{ fmtDT(a.created_at) }}</td>
        </tr>
      </tbody>
    </table>
    <div v-else class="empty">暂无异常事件。</div>
  </div>
</template>
