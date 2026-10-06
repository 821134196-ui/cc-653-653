# 港口岸电接入计时与船舶结算系统

按船舶**真实接电区间**出账：换泊位自动分段并适用新泊位费率，停电时段不计服务时长，
读数倒退 / 异常跃升 / 漏报时暂停结算并需**另一人复核补录**，船方可逐段核对费用与原始读数，
异议时**原账单、证据、处理结论完整保留**，另出更正账单而非改旧账。

- 后端：Go + Gin + GORM + SQLite（纯 Go 驱动 `glebarez/sqlite`，无需 CGO）
- 前端：Vue 3 + Vite
- 电表设备与付款回执均为**本地模拟**（设备报文带唯一 ID，重复回传幂等丢弃）

## 一条命令启动

```bash
./run.sh          # 生产模式：构建前端并由 Go 在 :8080 统一托管
./run.sh dev      # 开发模式：Vite :5173 热更新，/api 代理到 :8080
```

打开 http://localhost:8080 （dev 模式为 http://localhost:5173）。

数据库文件：`backend/data/shorepower.db`（删除后重启会自动建表并写入示例泊位）。

## 业务流程

1. **登记接电**：值班员录入船名、泊位、电表起始读数、接电时间。
2. **计量**：手工抄录或「电表设备模拟回传」；换泊位、停电、结束均可操作。
   - 换泊位 → 关闭当前计量段（按当时费率落定），同一电表读数连续开新段、适用**新泊位费率**。
   - 停电开始/结束 → 停电区间从服务时长中扣除，**不算连续服务**。
3. **异常处理**：读数倒退、换算功率异常跃升、相邻有效读数间隔超 60 分钟（漏报）→
   会话冻结、暂停结算。值班员提交**补录**（状态：待复核），必须由**另一名值班员**在「复核台」采纳，
   系统还会校验补录值不得与相邻有效读数矛盾。
4. **出账**：结束接电后生成账单，**逐段**显示用电量、费率、服务时长（含停电扣除）、电费、服务费、小计；
   出账时固化包含全部**原始读数 / 停电 / 异常**的证据快照。
5. **船方确认 / 异议**：船方确认账单；如有异议提交理由与证据，原账单保持不变。
   港口处理时可「维持原判」或「另出更正账单」——原账单置为「已失效」但记录、明细、快照原样保留。
6. **模拟付款**：对已确认账单模拟支付渠道，生成付款回执。

## 三个重点验证场景（已自动化测试）

```bash
cd backend && go test ./... -v
```

| 测试 | 验证内容 |
|---|---|
| `TestBerthChangeSplitsSegmentsAndRates` | 换泊位分两段：A1(1.20元/kWh,0.50元/分) 与 B1(1.50,0.80) 各自计费，合计 108+126=234 |
| `TestBackwardAndJumpFreezeAndReview` | 倒退/跃升触发冻结，出账/结束/换泊全被拒；补录须另一人复核，自查被拒 |
| `TestMissingReadingSupplement` | 间隔 >60 分钟判漏报，补录复核采纳后恢复，补录值参与计量 |
| `TestSupplementContradictionRejected` | 补录读数大于后续有效读数时复核拦截 |
| `TestDeviceDuplicateIdempotent` | 相同报文 ID 重发返回 `duplicate`，不新增读数、不虚增电量 |
| `TestOutageExcludedFromService` | 30 分钟停电从 120 分钟跨度中扣除，按 90 分钟计服务费 |
| `TestDisputeKeepsOriginalBill` | 异议后原账单金额/明细/快照不变，另出更正账单（补登停电后重算）并可支付 |

### 快速手测（服务启动后）

```bash
# 登记
curl -s localhost:8080/api/sessions -H 'Content-Type: application/json' \
  -d '{"vessel_name":"测试轮","berth_id":1,"operator":"甲","start_reading":100}'
# 设备上报 + 同一报文重发（第二次 duplicate=true）
curl -s localhost:8080/api/sessions/1/readings -H 'Content-Type: application/json' \
  -d '{"source":"device","value":120,"device_id":"M1","message_uid":"U1"}'
curl -s localhost:8080/api/sessions/1/readings -H 'Content-Type: application/json' \
  -d '{"source":"device","value":120,"device_id":"M1","message_uid":"U1"}'
```

## 目录结构

```
backend/
  cmd/server/        程序入口（API + 前端静态托管）
  internal/models/   GORM 模型（会话/计量段/读数/停电/异常/账单/异议/回执）
  internal/store/    SQLite 初始化、迁移、部分唯一索引、种子泊位
  internal/service/  核心业务逻辑与全部场景测试
  internal/api/      Gin 路由与处理器
  web/dist/          前端构建产物（git 忽略外由构建生成）
frontend/            Vue 3 源码（登记/计量/复核台/账单/异议）
run.sh               一条命令启动
```

## 异常判定口径（可在 service 包调整）

- 倒退：新读数 < 上一条已采纳读数。
- 跃升：相邻读数换算功率 > `200 kWh/h`。
- 漏报：相邻已采纳读数间隔 > `60 分钟`。
