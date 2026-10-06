<script setup>
import { ref } from 'vue'
import SessionsView from './views/SessionsView.vue'
import SessionDetail from './views/SessionDetail.vue'
import ReviewView from './views/ReviewView.vue'
import BillsView from './views/BillsView.vue'
import BillDetail from './views/BillDetail.vue'
import DisputesView from './views/DisputesView.vue'

const tab = ref('sessions')
const selectedSession = ref(null)
const selectedBill = ref(null)

const nav = [
  { key: 'sessions', label: '接电与计量' },
  { key: 'review', label: '异常 / 复核台' },
  { key: 'bills', label: '账单' },
  { key: 'disputes', label: '异议处理' },
]

function openSession(id) {
  selectedSession.value = id
  tab.value = 'session-detail'
}
function openBill(id) {
  selectedBill.value = id
  tab.value = 'bill-detail'
}
function go(t) { tab.value = t }
</script>

<template>
  <header class="topbar">
    <span class="logo">⚓ 港口岸电结算系统</span>
    <span class="sub">真实接电区间出账 · 换泊位分段计费 · 停电不计费 · 船方可逐段核对</span>
  </header>
  <div class="layout">
    <nav class="side">
      <button v-for="n in nav" :key="n.key"
              :class="{ active: tab === n.key || (n.key === 'sessions' && tab === 'session-detail') || (n.key === 'bills' && tab === 'bill-detail') }"
              @click="go(n.key)">{{ n.label }}</button>
    </nav>
    <main>
      <SessionsView v-if="tab === 'sessions'" @open="openSession" />
      <SessionDetail v-else-if="tab === 'session-detail' && selectedSession"
                     :id="selectedSession" @open-bill="openBill" @back="go('sessions')" />
      <ReviewView v-else-if="tab === 'review'" @open-session="openSession" />
      <BillsView v-else-if="tab === 'bills'" @open="openBill" />
      <BillDetail v-else-if="tab === 'bill-detail' && selectedBill"
                  :id="selectedBill" @back="go('bills')" @open-session="openSession" @open="openBill" />
      <DisputesView v-else-if="tab === 'disputes'" @open-bill="openBill" />
    </main>
  </div>
</template>
