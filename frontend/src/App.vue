<script setup>
import { reactive, ref, onMounted, provide } from 'vue'
import { api } from './api.js'
import ConnectionsView from './components/ConnectionsView.vue'
import ReadingsView from './components/ReadingsView.vue'
import BillsView from './components/BillsView.vue'
import DisputesView from './components/DisputesView.vue'

const meta = ref({ users: [], berths: [], meters: [] })
const tab = ref('ops')
const message = reactive({ text: '', type: '' })
const tick = ref(0)

const role = ref(localStorage.getItem('sp_role') || 'operator')
const roleMap = {
  operator: { label: '值班员', code: 'op01' },
  reviewer: { label: '复核员', code: 'rv01' },
  vessel: { label: '船方', code: 'ship' }
}
function setRole(r) {
  role.value = r
  localStorage.setItem('sp_role', r)
}

function notify(text, type = 'info') {
  message.text = text
  message.type = type
  setTimeout(() => (message.text = ''), 4000)
}
function refresh() { tick.value++ }

provide('ctx', { meta, role, roleMap, notify, refresh, tick })

onMounted(async () => {
  meta.value = await api.get('/meta')
  setInterval(refresh, 5000)
})
</script>

<template>
  <header class="topbar">
    <h1>⚓ 港口岸电接入计时与船舶结算系统</h1>
    <div class="role-switch">
      <span style="font-size:12px;opacity:.85">当前身份：</span>
      <button v-for="(v, k) in roleMap" :key="k" :class="{ active: role === k }" @click="setRole(k)">
        {{ v.label }}
      </button>
    </div>
  </header>
  <nav class="tabs">
    <button :class="{ active: tab === 'ops' }" @click="tab = 'ops'">接电作业</button>
    <button :class="{ active: tab === 'readings' }" @click="tab = 'readings'">读数 · 异常复核 · 设备模拟</button>
    <button :class="{ active: tab === 'bills' }" @click="tab = 'bills'">账单与付款</button>
    <button :class="{ active: tab === 'disputes' }" @click="tab = 'disputes'">异议处理</button>
  </nav>
  <main>
    <div v-if="message.text" class="banner" :class="message.type">{{ message.text }}</div>
    <ConnectionsView v-if="tab === 'ops'" />
    <ReadingsView v-else-if="tab === 'readings'" />
    <BillsView v-else-if="tab === 'bills'" />
    <DisputesView v-else-if="tab === 'disputes'" />
  </main>
</template>
