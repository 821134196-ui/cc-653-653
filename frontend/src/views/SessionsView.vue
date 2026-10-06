<script setup>
import { ref, onMounted } from 'vue'
import { listBerths, listSessions, registerSession, fmtDT, tag, nowLocalInput, toRFC } from '../api.js'

const emit = defineEmits(['open'])
const berths = ref([])
const sessions = ref([])
const filter = ref('')
const error = ref('')
const ok = ref('')

const form = ref({
  vessel_name: '', agent: '', berth_id: '', operator: '',
  start_time: nowLocalInput(), start_reading: 0,
})

async function load() {
  sessions.value = await listSessions(filter.value)
}
onMounted(async () => {
  berths.value = await listBerths()
  form.value.berth_id = berths.value[0]?.id || ''
  await load()
})

async function submit() {
  error.value = ok.value = ''
  try {
    const body = {
      vessel_name: form.value.vessel_name.trim(),
      agent: form.value.agent.trim(),
      berth_id: Number(form.value.berth_id),
      operator: form.value.operator.trim(),
      start_time: toRFC(form.value.start_time),
      start_reading: Number(form.value.start_reading),
    }
    const s = await registerSession(body)
    ok.value = `已登记接电：会话 #${s.id}`
    form.value.vessel_name = ''
    form.value.agent = ''
    await load()
  } catch (e) {
    error.value = e.message
  }
}
</script>

<template>
  <div class="card">
    <h2>① 值班员登记接电</h2>
    <div v-if="error" class="notice err">{{ error }}</div>
    <div v-if="ok" class="notice ok">{{ ok }}</div>
    <div class="grid g4">
      <div><label>船名 *</label><input v-model="form.vessel_name" placeholder="如：远洋号" /></div>
      <div><label>船方联系人</label><input v-model="form.agent" placeholder="可选" /></div>
      <div>
        <label>泊位（费率）*</label>
        <select v-model="form.berth_id">
          <option v-for="b in berths" :key="b.id" :value="b.id">
            {{ b.name }}（{{ b.rate_per_kwh }}元/kWh，{{ b.service_per_min }}元/分）
          </option>
        </select>
      </div>
      <div><label>值班员 *</label><input v-model="form.operator" placeholder="姓名/工号" /></div>
      <div><label>接电时间 *</label><input type="datetime-local" v-model="form.start_time" /></div>
      <div><label>电表起始读数 (kWh) *</label><input type="number" step="0.01" v-model.number="form.start_reading" /></div>
      <div style="align-self:end"><button class="btn teal" @click="submit">登记接电</button></div>
    </div>
  </div>

  <div class="card">
    <h2>接电会话</h2>
    <div class="pill-row">
      <button :class="{ on: filter === '' }" @click="filter = ''; load()">全部</button>
      <button :class="{ on: filter === 'active' }" @click="filter = 'active'; load()">接电中</button>
      <button :class="{ on: filter === 'blocked' }" @click="filter = 'blocked'; load()">异常冻结</button>
      <button :class="{ on: filter === 'ended' }" @click="filter = 'ended'; load()">已结束</button>
      <button :class="{ on: filter === 'billed' }" @click="filter = 'billed'; load()">已出账</button>
      <button :class="{ on: filter === 'disputed' }" @click="filter = 'disputed'; load()">异议中</button>
    </div>
    <table>
      <thead>
        <tr><th>#</th><th>船名</th><th>起始泊位</th><th>值班员</th><th>接电时间</th><th>结束时间</th><th>状态</th><th></th></tr>
      </thead>
      <tbody>
        <tr v-for="s in sessions" :key="s.id">
          <td>{{ s.id }}</td>
          <td><strong>{{ s.vessel?.name }}</strong></td>
          <td>{{ s.berth?.name }}</td>
          <td>{{ s.operator }}</td>
          <td class="nowrap">{{ fmtDT(s.start_time) }}</td>
          <td class="nowrap">{{ fmtDT(s.end_time) }}</td>
          <td><span class="tag" :class="s.status">{{ tag(s.status) }}</span></td>
          <td><button class="btn sm ghost" @click="emit('open', s.id)">进入计量</button></td>
        </tr>
        <tr v-if="!sessions.length"><td colspan="8"><div class="empty">暂无会话，请先在上方登记接电</div></td></tr>
      </tbody>
    </table>
  </div>
</template>
