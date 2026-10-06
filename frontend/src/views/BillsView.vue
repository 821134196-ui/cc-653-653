<script setup>
import { ref, onMounted } from 'vue'
import { listBills, fmtDT, fmtMoney, tag } from '../api.js'

const emit = defineEmits(['open'])
const bills = ref([])
const filter = ref('')
const filters = [
  ['', '全部'], ['pending', '待船方确认'], ['confirmed', '已确认'],
  ['disputed', '异议中'], ['paid', '已付款'], ['void', '已失效'],
]
async function load() { bills.value = await listBills(filter.value) }
onMounted(load)
</script>

<template>
  <div class="card">
    <h2>账单列表</h2>
    <div class="pill-row">
      <button v-for="f in filters" :key="f[0]"
              :class="{ on: filter === f[0] }" @click="filter = f[0]; load()">{{ f[1] }}</button>
    </div>
    <table>
      <thead>
        <tr><th>账单号</th><th>会话</th><th>船名</th><th>总电量(kWh)</th><th>电费</th><th>服务费</th><th>总额</th><th>状态</th><th>生成时间</th><th></th></tr>
      </thead>
      <tbody>
        <tr v-for="b in bills" :key="b.id">
          <td class="nowrap">{{ b.bill_no }}</td>
          <td>#{{ b.session_id }}</td>
          <td><strong>{{ b.vessel_name }}</strong></td>
          <td>{{ fmtMoney(b.total_kwh) }}</td>
          <td>{{ fmtMoney(b.energy_amount) }}</td>
          <td>{{ fmtMoney(b.service_amount) }}</td>
          <td class="money">¥{{ fmtMoney(b.total_amount) }}</td>
          <td><span class="tag" :class="b.status">{{ tag(b.status) }}</span></td>
          <td class="nowrap">{{ fmtDT(b.created_at) }}</td>
          <td><button class="btn sm ghost" @click="emit('open', b.id)">查看</button></td>
        </tr>
        <tr v-if="!bills.length"><td colspan="10"><div class="empty">暂无账单</div></td></tr>
      </tbody>
    </table>
  </div>
</template>
