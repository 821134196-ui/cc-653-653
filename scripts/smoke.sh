#!/usr/bin/env bash
set -euo pipefail
B=http://localhost:8080/api
T0="2026-10-10T10:00:00+08:00"
jget() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const o=JSON.parse(s);console.log(eval("o."+process.argv[1]))})' "$1"; }
check() { if [ "$1" = "$2" ]; then echo "  ✅ $3 = $1"; else echo "  ❌ $3: 期望 $2 实际 $1"; FAIL=1; fi; }
FAIL=0
U1="U-1-$(date +%s%N)-$RANDOM"
U2="U-2-$(date +%s%N)-$RANDOM"

echo "== 场景A：换泊位 + 停电 + 设备重复回传 + 异议更正 =="
SID=$(curl -s $B/sessions -H 'Content-Type: application/json' \
  -d "{\"vessel_name\":\"冒烟轮\",\"agent\":\"代理王\",\"berth_id\":1,\"operator\":\"值班员甲\",\"start_time\":\"$T0\",\"start_reading\":100}" | jget id)
echo "会话 #$SID"

# 设备上报 + 重复回传
curl -s $B/sessions/$SID/readings -H 'Content-Type: application/json' \
  -d "{\"source\":\"device\",\"value\":120,\"read_at\":\"2026-10-10T11:00:00+08:00\",\"device_id\":\"M-01\",\"message_uid\":\"$U1\"}" > /dev/null
DUP=$(curl -s $B/sessions/$SID/readings -H 'Content-Type: application/json' \
  -d "{\"source\":\"device\",\"value\":120,\"read_at\":\"2026-10-10T11:00:00+08:00\",\"device_id\":\"M-01\",\"message_uid\":\"$U1\"}" | jget duplicate)
check "$DUP" "true" "重复报文幂等丢弃"

# 换泊位 A1 -> B1
curl -s $B/sessions/$SID/change-berth -H 'Content-Type: application/json' \
  -d "{\"berth_id\":3,\"value\":140,\"at\":\"2026-10-10T12:00:00+08:00\",\"operator\":\"值班员甲\"}" > /dev/null
SEGS=$(curl -s $B/sessions/$SID | jget "segments.length")
check "$SEGS" "2" "换泊位产生两个计量段"

# B1 段内停电 13:30-13:45，14:00 结束
curl -s $B/sessions/$SID/readings -H 'Content-Type: application/json' \
  -d "{\"source\":\"device\",\"value\":150,\"read_at\":\"2026-10-10T13:00:00+08:00\",\"device_id\":\"M-01\",\"message_uid\":\"$U2\"}" > /dev/null
curl -s $B/sessions/$SID/outages/start -H 'Content-Type: application/json' \
  -d '{"at":"2026-10-10T13:30:00+08:00","reason":"检修","operator":"甲"}' > /dev/null
curl -s $B/sessions/$SID/outages/end -H 'Content-Type: application/json' \
  -d '{"at":"2026-10-10T13:45:00+08:00","operator":"甲"}' > /dev/null
curl -s $B/sessions/$SID/end -H 'Content-Type: application/json' \
  -d '{"value":160,"at":"2026-10-10T14:00:00+08:00","operator":"值班员甲"}' > /dev/null

BILL=$(curl -s -X POST $B/sessions/$SID/bills)
BID=$(echo "$BILL" | jget id)
ITEMS=$(curl -s $B/bills/$BID | jget "items.length")
check "$ITEMS" "2" "账单逐段 2 条明细"
TOTAL=$(echo "$BILL" | jget total_amount)
check "$TOTAL" "222" "原账单总额(含15分钟停电): 段1=108, 段2=30+84=114, 合计222"
IT2_SVC=$(curl -s $B/bills/$BID | jget "items[1].service_seconds")
check "$IT2_SVC" "6300" "B1段服务105分钟(120-15停电)"

# 快照可查、含原始读数
SNAPN=$(curl -s $B/bills/$BID/snapshot | jget "readings.length")
[ "$SNAPN" -ge 5 ] && echo "  ✅ 出账快照含原始读数 $SNAPN 条" || { echo "  ❌ 快照异常 $SNAPN"; FAIL=1; }

# 船方确认 -> 异议（原账单保留）
curl -s -X POST $B/bills/$BID/confirm > /dev/null
DID=$(curl -s -X POST $B/bills/$BID/disputes -H 'Content-Type: application/json' \
  -d '{"reason":"11:00-11:30也曾停电","evidence":"船方日志LOG-9"}' | jget id)
# 异议后无法直接支付
PAYERR=$(curl -s -X POST $B/bills/$BID/pay -H 'Content-Type: application/json' -d '{}' | jget error || true)
echo "  ℹ️ 异议账单支付被拦: $PAYERR"
# 维持原账单
curl -s -X POST $B/disputes/$DID/resolve -H 'Content-Type: application/json' \
  -d '{"handler":"值班长","conclusion":"经核验记录一致，维持原判","action":"uphold"}' > /dev/null
BSTATUS=$(curl -s $B/bills/$BID | jget bill.status)
check "$BSTATUS" "confirmed" "维持原判后账单恢复已确认"
RCPT=$(curl -s -X POST $B/bills/$BID/pay -H 'Content-Type: application/json' -d '{"channel":"模拟银联"}')
RNO=$(echo "$RCPT" | jget receipt_no)
[ -n "$RNO" ] && echo "  ✅ 模拟付款回执: $RNO" || { echo "  ❌ 无回执"; FAIL=1; }

echo
echo "== 场景B：读数倒退冻结 + 补录双人复核 =="
S2=$(curl -s $B/sessions -H 'Content-Type: application/json' \
  -d "{\"vessel_name\":\"异常轮\",\"berth_id\":2,\"operator\":\"值班员甲\",\"start_time\":\"$T0\",\"start_reading\":100}" | jget id)
R=$(curl -s $B/sessions/$S2/readings -H 'Content-Type: application/json' \
  -d '{"source":"manual","value":90,"read_at":"2026-10-10T11:00:00+08:00","operator":"值班员甲"}')
AK=$(echo "$R" | jget anomaly.kind)
check "$AK" "backward" "倒退读数触发异常"
BLK=$(curl -s $B/sessions/$S2 | jget session.status)
check "$BLK" "blocked" "会话冻结暂停结算"
PVERR=$(curl -s "$B/sessions/$S2/bill-preview" | jget error)
echo "  ℹ️ 冻结时预览拦截: $PVERR"
# 补录
SUPP=$(curl -s -X POST $B/sessions/$S2/supplements -H 'Content-Type: application/json' \
  -d '{"operator":"值班员甲","value":110,"read_at":"2026-10-10T11:00:00+08:00","note":"现场复核"}')
RID=$(echo "$SUPP" | jget id)
# 同一人复核应被拒
SAMEERR=$(curl -s -X POST $B/readings/$RID/review -H 'Content-Type: application/json' \
  -d '{"reviewer":"值班员甲","approve":true}' | jget error)
echo "  ℹ️ 自查拦截: $SAMEERR"
# 另一人复核
curl -s -X POST $B/readings/$RID/review -H 'Content-Type: application/json' \
  -d '{"reviewer":"值班员乙","approve":true,"note":"有效"}' > /dev/null
ST=$(curl -s $B/sessions/$S2 | jget session.status)
check "$ST" "active" "另一人复核后恢复结算"

echo
echo "== 场景C：漏报冻结 + 补录恢复 =="
S3=$(curl -s $B/sessions -H 'Content-Type: application/json' \
  -d "{\"vessel_name\":\"漏报轮\",\"berth_id\":1,\"operator\":\"甲\",\"start_time\":\"$T0\",\"start_reading\":100}" | jget id)
MK=$(curl -s $B/sessions/$S3/readings -H 'Content-Type: application/json' \
  -d '{"source":"manual","value":130,"read_at":"2026-10-10T12:00:00+08:00","operator":"甲"}' | jget anomaly.kind)
check "$MK" "missing" "间隔2小时判漏报"
RID3=$(curl -s -X POST $B/sessions/$S3/supplements -H 'Content-Type: application/json' \
  -d '{"operator":"甲","value":112,"read_at":"2026-10-10T11:00:00+08:00","note":"补发"}' | jget id)
curl -s -X POST $B/readings/$RID3/review -H 'Content-Type: application/json' \
  -d '{"reviewer":"乙","approve":true}' > /dev/null
ST3=$(curl -s $B/sessions/$S3 | jget session.status)
check "$ST3" "active" "补录复核后漏报异常解除"

echo
echo "== 前端托管 =="
HTML=$(curl -s localhost:8080/ | head -c 20)
[[ "$HTML" == *"<!DOCTYPE html"* ]] && echo "  ✅ / 返回前端页面" || { echo "  ❌ 前端未托管: $HTML"; FAIL=1; }

echo
[ "$FAIL" = 0 ] && echo "🎉 全部端到端冒烟检查通过" || { echo "🚫 存在失败项"; exit 1; }
