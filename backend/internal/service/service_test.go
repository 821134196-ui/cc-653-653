package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"shorepower/internal/models"
)

func testDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Berth{}, &models.Vessel{}, &models.Session{}, &models.MeterSegment{},
		&models.MeterReading{}, &models.PowerOutage{}, &models.AnomalyEvent{},
		&models.Bill{}, &models.BillItem{}, &models.Dispute{}, &models.PaymentReceipt{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_meter_reading_msg_uid
		ON meter_readings(message_uid) WHERE message_uid <> ''`).Error; err != nil {
		t.Fatalf("index: %v", err)
	}
	berths := []models.Berth{
		{Name: "A1", RatePerKWh: 1.20, ServicePerMin: 0.50},
		{Name: "A2", RatePerKWh: 1.35, ServicePerMin: 0.60},
		{Name: "B1", RatePerKWh: 1.50, ServicePerMin: 0.80},
	}
	db.Create(&berths)
	return db
}

func clock(day int, hour, min int) time.Time {
	return time.Date(2026, 10, day, hour, min, 0, 0, time.Local)
}

// 场景一：换泊位 —— 旧段按 A1 费率落定，新段按 B1 费率计费
func TestBerthChangeSplitsSegmentsAndRates(t *testing.T) {
	svc := New(testDB(t, "berthchange"))
	t0 := clock(1, 10, 0)

	sess, err := svc.RegisterSession("远洋号", "张代理", 1, "值班员甲", t0, 100)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// A1 段：11:00 读数 120
	if _, err := svc.AddReading(sess.ID, models.ReadingManual, 120, clock(1, 11, 0), "值班员甲", "", ""); err != nil {
		t.Fatalf("reading: %v", err)
	}
	// 12:00 换至 B1，读数 140
	pair, err := svc.ChangeBerth(sess.ID, 3, 140, clock(1, 12, 0), "值班员甲")
	if err != nil {
		t.Fatalf("change berth: %v", err)
	}
	if len(pair) != 2 {
		t.Fatalf("应产生旧段+新段 2 段，实际 %d", len(pair))
	}
	if pair[0].BerthID != 1 || pair[1].BerthID != 3 {
		t.Fatalf("分段泊位错误: %d -> %d", pair[0].BerthID, pair[1].BerthID)
	}
	if pair[0].Status != models.SegmentClosed || pair[0].CloseReason != "berth_change" {
		t.Fatalf("旧段应已关闭，实际 %s/%s", pair[0].Status, pair[0].CloseReason)
	}
	if pair[1].StartReading != 140 {
		t.Fatalf("新段应从 140 连续起算，实际 %.2f", pair[1].StartReading)
	}

	// B1 段：13:00 读数 150，14:00 结束读数 160
	if _, err := svc.AddReading(sess.ID, models.ReadingManual, 150, clock(1, 13, 0), "值班员甲", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EndSession(sess.ID, 160, clock(1, 14, 0), "值班员甲"); err != nil {
		t.Fatalf("end: %v", err)
	}

	bill, err := svc.ConfirmBill(sess.ID)
	if err != nil {
		t.Fatalf("confirm bill: %v", err)
	}
	items, err := svc.BillItems(bill.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("账单应逐段给出 2 条明细，实际 %d (err=%v)", len(items), err)
	}

	// 段1 A1：40kWh * 1.20 = 48；120 分钟 * 0.50 = 60
	it1 := items[0]
	if it1.BerthName != "A1" || it1.KWh != 40 || it1.ServiceSeconds != 7200 {
		t.Fatalf("段1电量/时长错误: %+v", it1)
	}
	if it1.RatePerKWh != 1.20 || it1.EnergyAmount != 48 || it1.ServiceAmount != 60 || it1.Amount != 108 {
		t.Fatalf("段1 A1 费率/金额错误: %+v", it1)
	}
	// 段2 B1：20kWh * 1.50 = 30；120 分钟 * 0.80 = 96
	it2 := items[1]
	if it2.BerthName != "B1" || it2.KWh != 20 || it2.RatePerKWh != 1.50 {
		t.Fatalf("段2费率错误: %+v", it2)
	}
	if it2.EnergyAmount != 30 || it2.ServiceAmount != 96 || it2.Amount != 126 {
		t.Fatalf("段2 B1 金额错误: %+v", it2)
	}
	if bill.TotalKWh != 60 || bill.TotalAmount != 234 {
		t.Fatalf("合计错误: kwh=%.2f amount=%.2f", bill.TotalKWh, bill.TotalAmount)
	}
}

// 场景二：停电区间不计服务时长，也不算连续服务
func TestOutageExcludedFromService(t *testing.T) {
	svc := New(testDB(t, "outage"))
	t0 := clock(2, 10, 0)
	sess, _ := svc.RegisterSession("乘风号", "", 1, "甲", t0, 0)

	if _, err := svc.StartOutage(sess.ID, clock(2, 11, 0), "港区线路检修", "甲"); err != nil {
		t.Fatalf("start outage: %v", err)
	}
	if _, err := svc.EndOutage(sess.ID, clock(2, 11, 30), "甲"); err != nil {
		t.Fatalf("end outage: %v", err)
	}
	if _, err := svc.EndSession(sess.ID, 10, clock(2, 12, 0), "甲"); err != nil {
		t.Fatalf("end: %v", err)
	}
	bill, _ := svc.ConfirmBill(sess.ID)
	items, _ := svc.BillItems(bill.ID)
	it := items[0]
	// 总跨度 120 分钟，停电 30 分钟 => 服务 90 分钟
	if it.OutageSeconds != 1800 {
		t.Fatalf("停电时长应为 1800s，实际 %d", it.OutageSeconds)
	}
	if it.ServiceSeconds != 5400 {
		t.Fatalf("服务时长应为 5400s（扣除停电），实际 %d", it.ServiceSeconds)
	}
	// 电费 10*1.2=12；服务费 90*0.5=45
	if it.Amount != 57 || bill.TotalAmount != 57 {
		t.Fatalf("停电期间计费错误: 段金额 %.2f 总金额 %.2f", it.Amount, bill.TotalAmount)
	}
}

// 场景三：读数倒退 / 异常跃升 → 冻结暂停结算；补录须另一人复核后恢复
func TestBackwardAndJumpFreezeAndReview(t *testing.T) {
	svc := New(testDB(t, "anomaly"))
	t0 := clock(3, 10, 0)
	sess, _ := svc.RegisterSession("激流号", "", 1, "值班员甲", t0, 100)

	// 倒退：11:00 读数 90
	res, err := svc.AddReading(sess.ID, models.ReadingManual, 90, clock(3, 11, 0), "甲", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Anomaly == nil || res.Anomaly.Kind != models.AnomalyBackward {
		t.Fatalf("倒退读数应产生 backward 异常")
	}
	if res.Reading.Status != models.ReadingAnomalous {
		t.Fatalf("倒退读数应标记 anomalous")
	}
	var blocked models.Session
	svc.DB.First(&blocked, sess.ID)
	if blocked.Status != models.SessionBlocked {
		t.Fatalf("会话应被冻结，实际 %s", blocked.Status)
	}
	// 暂停结算：不能出账、不能结束、不能换泊位
	if _, err := svc.PreviewBill(sess.ID, false); err != ErrAnomalyOpen {
		t.Fatalf("异常未处理时预览应被拒，实际 %v", err)
	}
	if _, err := svc.ConfirmBill(sess.ID); err != ErrAnomalyOpen {
		t.Fatalf("异常未处理时出账应被拒，实际 %v", err)
	}
	if _, err := svc.EndSession(sess.ID, 120, clock(3, 12, 0), "甲"); err != ErrAnomalyOpen {
		t.Fatalf("异常未处理时结束接电应被拒，实际 %v", err)
	}
	if _, err := svc.ChangeBerth(sess.ID, 2, 120, clock(3, 12, 0), "甲"); err != ErrAnomalyOpen {
		t.Fatalf("异常未处理时换泊位应被拒，实际 %v", err)
	}

	// 补录 11:00 真实读数 110，进入待复核
	supp, err := svc.SubmitSupplement(sess.ID, "值班员甲", 110, clock(3, 11, 0), "现场复核电表实际为110")
	if err != nil {
		t.Fatal(err)
	}
	if supp.Status != models.ReadingPending {
		t.Fatalf("补录应处于待复核状态")
	}
	// 同一人复核必须被拒绝
	if _, err := svc.ReviewSupplement(supp.ID, "值班员甲", true, ""); err != ErrReviewerSame {
		t.Fatalf("录入人自查应被拒绝，实际 %v", err)
	}
	// 另一人复核采纳
	if _, err := svc.ReviewSupplement(supp.ID, "值班员乙", true, "读数有效"); err != nil {
		t.Fatalf("另一人复核应通过: %v", err)
	}
	var unfrozen models.Session
	svc.DB.First(&unfrozen, sess.ID)
	if unfrozen.Status != models.SessionActive {
		t.Fatalf("异常处理后应恢复接电，实际 %s", unfrozen.Status)
	}
	// 倒退原始读数应已被驳回，不参与计量
	var bad models.MeterReading
	svc.DB.First(&bad, res.Reading.ID)
	if bad.Status != models.ReadingRejected {
		t.Fatalf("异常原始读数应被驳回，实际 %s", bad.Status)
	}

	// 异常跃升：12:00 读数 9999（功率远超阈值）
	jr, err := svc.AddReading(sess.ID, models.ReadingManual, 9999, clock(3, 12, 0), "甲", "", "")
	if err != nil || jr.Anomaly == nil || jr.Anomaly.Kind != models.AnomalyJump {
		t.Fatalf("应识别异常跃升，res=%v err=%v", jr, err)
	}
}

// 场景四：漏报（相邻有效读数间隔超阈值）→ 冻结，补录补齐后恢复
func TestMissingReadingSupplement(t *testing.T) {
	svc := New(testDB(t, "missing"))
	t0 := clock(4, 10, 0)
	sess, _ := svc.RegisterSession("远航号", "", 1, "甲", t0, 100)

	// 2 小时后才来读数，间隔 > 60 分钟，判漏报
	res, err := svc.AddReading(sess.ID, models.ReadingManual, 130, clock(4, 12, 0), "甲", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Anomaly == nil || res.Anomaly.Kind != models.AnomalyMissing {
		t.Fatalf("应产生 missing 异常")
	}
	// 补录 11:00 的读数 110，落在缺口区间内
	supp, _ := svc.SubmitSupplement(sess.ID, "甲", 110, clock(4, 11, 0), "补发漏抄")
	if _, err := svc.ReviewSupplement(supp.ID, "乙", true, ""); err != nil {
		t.Fatalf("补录复核失败: %v", err)
	}
	var open int64
	svc.DB.Model(&models.AnomalyEvent{}).Where("session_id = ? AND status = ?", sess.ID, models.AnomalyOpen).Count(&open)
	if open != 0 {
		t.Fatalf("补录补齐后未决异常应为 0")
	}
	if _, err := svc.EndSession(sess.ID, 140, clock(4, 13, 0), "甲"); err != nil {
		t.Fatalf("恢复后应能正常结束: %v", err)
	}
	// 110 已被采纳参与计量
	var n int64
	svc.DB.Model(&models.MeterReading{}).
		Where("session_id = ? AND status = ? AND value = ?", sess.ID, models.ReadingAccepted, 110.0).Count(&n)
	if n != 1 {
		t.Fatalf("补录读数应已采纳")
	}
}

// 补录值与相邻有效读数矛盾时，复核环节必须拦下
func TestSupplementContradictionRejected(t *testing.T) {
	svc := New(testDB(t, "suppbad"))
	t0 := clock(5, 10, 0)
	sess, _ := svc.RegisterSession("北斗号", "", 1, "甲", t0, 100)
	svc.AddReading(sess.ID, models.ReadingManual, 120, clock(5, 11, 0), "甲", "", "")
	svc.AddReading(sess.ID, models.ReadingManual, 140, clock(5, 12, 0), "甲", "", "")
	// 补 11:30 读数 130 合法；补 11:30 读数 150（大于后续 140）非法
	supp, _ := svc.SubmitSupplement(sess.ID, "甲", 150, clock(5, 11, 30), "")
	if _, err := svc.ReviewSupplement(supp.ID, "乙", true, ""); err == nil {
		t.Fatalf("与后续读数矛盾的补录应在复核时被拒绝")
	}
	var r models.MeterReading
	svc.DB.First(&r, supp.ID)
	if r.Status != models.ReadingPending {
		t.Fatalf("复核失败后应仍为待复核，实际 %s", r.Status)
	}
}

// 场景五：电表设备重复回传 —— 相同报文ID幂等丢弃，不产生重复读数/费用
func TestDeviceDuplicateIdempotent(t *testing.T) {
	svc := New(testDB(t, "dup"))
	t0 := clock(6, 10, 0)
	sess, _ := svc.RegisterSession("晨光号", "", 1, "甲", t0, 100)

	r1, err := svc.AddReading(sess.ID, models.ReadingDevice, 120, clock(6, 11, 0),
		"设备网关", "METER-A1-0001", "MSG-1")
	if err != nil {
		t.Fatal(err)
	}
	// 设备因网络重发，同一报文 ID 再次回传
	r2, err := svc.AddReading(sess.ID, models.ReadingDevice, 120, clock(6, 11, 0),
		"设备网关", "METER-A1-0001", "MSG-1")
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Duplicate || r2.Reading.ID != r1.Reading.ID {
		t.Fatalf("重复报文应返回幂等结果(Duplicate)，实际 %+v", r2)
	}
	var cnt int64
	svc.DB.Model(&models.MeterReading{}).
		Where("session_id = ? AND source = ?", sess.ID, models.ReadingDevice).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("重复回传不得新增读数，设备读数数量=%d", cnt)
	}
	// 同值不同报文ID（新时点）正常入账
	r3, err := svc.AddReading(sess.ID, models.ReadingDevice, 140, clock(6, 12, 0),
		"设备网关", "METER-A1-0001", "MSG-2")
	if err != nil || r3.Duplicate {
		t.Fatalf("新报文应正常入账，r3=%v err=%v", r3, err)
	}
	if _, err := svc.EndSession(sess.ID, 160, clock(6, 13, 0), "甲"); err != nil {
		t.Fatal(err)
	}
	bill, _ := svc.ConfirmBill(sess.ID)
	if bill.TotalKWh != 60 {
		t.Fatalf("重复回传不应虚增用电量，实际 %.2f", bill.TotalKWh)
	}
}

// 场景六：船方异议 —— 原账单、证据、结论全部保留，另出更正账单
func TestDisputeKeepsOriginalBill(t *testing.T) {
	svc := New(testDB(t, "dispute"))
	t0 := clock(7, 10, 0)
	sess, _ := svc.RegisterSession("和平号", "", 1, "甲", t0, 0)
	svc.AddReading(sess.ID, models.ReadingManual, 20, clock(7, 11, 0), "甲", "", "")
	if _, err := svc.EndSession(sess.ID, 20, clock(7, 12, 0), "甲"); err != nil {
		t.Fatal(err)
	}
	bill, err := svc.ConfirmBill(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ShipConfirm(bill.ID); err != nil {
		t.Fatalf("船方确认: %v", err)
	}

	// 异议：原账单不得被直接修改
	d, err := svc.RaiseDispute(bill.ID, "11:00-11:30 港区停电，服务费多计", "船方日志照片见附件IMG-7788")
	if err != nil {
		t.Fatal(err)
	}
	origSnapshot, err := svc.BillSnapshot(bill.ID)
	if err != nil {
		t.Fatalf("原账单证据快照必须保留可读: %v", err)
	}
	if len(origSnapshot.Readings) < 2 {
		t.Fatalf("快照应包含原始读数证据")
	}

	// 港口处理：更正（补登停电后重算）。先补登 11:00-11:30 停电
	if _, err := svc.StartOutage(sess.ID, clock(7, 11, 0), "补登：港区跳闸", "复核员乙"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EndOutage(sess.ID, clock(7, 11, 30), "复核员乙"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveDispute(d.ID, "值班长丙", "核实属实，停电30分钟，重出账单", "correct"); err != nil {
		t.Fatalf("异议更正处理失败: %v", err)
	}

	// 原账单仍在，状态 void 并指向更正账单；金额/明细快照原样未改
	var orig models.Bill
	svc.DB.First(&orig, bill.ID)
	if orig.Status != models.BillVoid || orig.SupersededByID == nil {
		t.Fatalf("原账单应置 void 且指向更正账单，实际 status=%s", orig.Status)
	}
	if orig.TotalAmount != bill.TotalAmount {
		t.Fatalf("原账单金额不得被改动: %.2f -> %.2f", bill.TotalAmount, orig.TotalAmount)
	}
	snap2, _ := svc.BillSnapshot(bill.ID)
	if len(snap2.Readings) != len(origSnapshot.Readings) {
		t.Fatalf("原账单快照证据必须保持不变")
	}
	var origItems []models.BillItem
	svc.DB.Where("bill_id = ?", bill.ID).Find(&origItems)
	if len(origItems) != 1 || origItems[0].ServiceSeconds != 7200 {
		t.Fatalf("原账单逐段明细不得被改动: %+v", origItems)
	}

	// 更正账单为新记录，停电时长已扣除：服务费 90 分钟 * 0.5 = 45
	var nb models.Bill
	svc.DB.First(&nb, *orig.SupersededByID)
	if nb.Status != models.BillConfirmed || nb.ID == bill.ID {
		t.Fatalf("应生成独立的更正账单")
	}
	newItems, _ := svc.BillItems(nb.ID)
	if newItems[0].OutageSeconds != 1800 || newItems[0].ServiceSeconds != 5400 {
		t.Fatalf("更正账单应体现停电扣除: %+v", newItems[0])
	}
	if newItems[0].ServiceAmount != 45 {
		t.Fatalf("更正后服务费应为 45，实际 %.2f", newItems[0].ServiceAmount)
	}

	// 异议记录保留了证据与处理结论
	var dd models.Dispute
	svc.DB.First(&dd, d.ID)
	if dd.Status != models.DisputeResolved || dd.Evidence == "" || dd.Conclusion == "" || dd.Handler != "值班长丙" {
		t.Fatalf("异议应保留证据、结论与处理人: %+v", dd)
	}

	// 更正账单可正常模拟支付并取得回执
	rcpt, err := svc.SimulatePay(nb.ID, "模拟银联渠道")
	if err != nil {
		t.Fatalf("模拟支付失败: %v", err)
	}
	if rcpt.Amount != nb.TotalAmount || rcpt.ReceiptNo == "" {
		t.Fatalf("付款回执错误: %+v", rcpt)
	}
	var paid models.Bill
	svc.DB.First(&paid, nb.ID)
	if paid.Status != models.BillPaid {
		t.Fatalf("支付后账单应为 paid")
	}
}

// 维持原判路径：异议不成立时原账单恢复确认状态
func TestDisputeUphold(t *testing.T) {
	svc := New(testDB(t, "uphold"))
	t0 := clock(8, 10, 0)
	sess, _ := svc.RegisterSession("白鹭号", "", 1, "甲", t0, 0)
	if _, err := svc.EndSession(sess.ID, 10, clock(8, 11, 0), "甲"); err != nil {
		t.Fatal(err)
	}
	bill, _ := svc.ConfirmBill(sess.ID)
	svc.ShipConfirm(bill.ID)
	d, _ := svc.RaiseDispute(bill.ID, "船方声称读数有误", "")
	if _, err := svc.ResolveDispute(d.ID, "值班长", "现场核验读数与设备记录一致，维持原账", "uphold"); err != nil {
		t.Fatal(err)
	}
	var b models.Bill
	svc.DB.First(&b, bill.ID)
	if b.Status != models.BillConfirmed {
		t.Fatalf("维持原判后账单应恢复 confirmed，实际 %s", b.Status)
	}
}
