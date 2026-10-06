package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&User{}, &Berth{}, &Meter{}, &Connection{}, &MeterSegment{},
		&MeterReading{}, &OutageRecord{}, &Bill{}, &BillItem{},
		&PaymentReceipt{}, &Dispute{}, &CreditNote{},
	); err != nil {
		t.Fatal(err)
	}
	seed(db)
	return db
}

func at(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02T15:04:05", s, time.Local)
	return t
}

func intPtr(v int) *int       { return &v }
func fPtr(v float64) *float64 { return &v }

func splitIDs(s string) []uint {
	var ids []uint
	cur := 0
	has := false
	for _, ch := range s {
		if ch == ',' {
			if has {
				ids = append(ids, uint(cur))
			}
			cur, has = 0, false
			continue
		}
		cur = cur*10 + int(ch-'0')
		has = true
	}
	if has {
		ids = append(ids, uint(cur))
	}
	return ids
}

func devRead(t *testing.T, db *gorm.DB, meter string, val float64, when time.Time, seq int) (MeterReading, bool) {
	t.Helper()
	r, dup, err := ingestReading(db, readingInput{
		MeterCode: meter, Reading: val, ReadAt: when, Source: SourceDevice,
		DeviceSeq: intPtr(seq), ReportedBy: "simulator",
	})
	if err != nil {
		t.Fatalf("device reading seq=%d val=%v: %v", seq, val, err)
	}
	return r, dup
}

func segCount(t *testing.T, db *gorm.DB, connID uint) int64 {
	var n int64
	db.Model(&MeterSegment{}).Where("connection_id = ?", connID).Count(&n)
	return n
}

// 场景1：换泊位——原段按 A1 费率截止，新段按 B1 费率，各自独立
func TestChangeBerth_SeparateSegmentsAndRates(t *testing.T) {
	db := testDB(t)
	conn, err := createConnection(db, createConnInput{
		VesselName: "远洋号", MeterCode: "M-A1", Reading: 100,
		StartTime: at("2026-10-06T08:00:00"), ReportBy: "op01",
	})
	if err != nil {
		t.Fatal(err)
	}
	devRead(t, db, "M-A1", 110, at("2026-10-06T09:00:00"), 1)

	err = changeBerth(db, conn.ID, changeBerthInput{
		Time: at("2026-10-06T10:00:00"), FromReading: fPtr(120),
		ToMeterCode: "M-B1", ToReading: 500, ReportBy: "op01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := segCount(t, db, conn.ID); got != 2 {
		t.Fatalf("期望2个计量段, 实际 %d", got)
	}
	devRead(t, db, "M-B1", 520, at("2026-10-06T11:00:00"), 1)
	if err := closeConnection(db, conn.ID, boundaryInput{
		Time: at("2026-10-06T12:00:00"), Reading: fPtr(540), ReportBy: "op01",
	}); err != nil {
		t.Fatal(err)
	}
	bill, err := generateBill(db, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bill.Items) != 2 {
		t.Fatalf("账单应有2段, 实际 %d", len(bill.Items))
	}
	i1, i2 := bill.Items[0], bill.Items[1]
	if i1.BerthCode != "A1" || i2.BerthCode != "B1" {
		t.Fatalf("泊位错误: %s / %s", i1.BerthCode, i2.BerthCode)
	}
	if i1.KWh != 20 || i1.Hours != 2 {
		t.Fatalf("段1 电量/时长错误: %v / %v", i1.KWh, i1.Hours)
	}
	if i2.KWh != 40 || i2.Hours != 2 {
		t.Fatalf("段2 电量/时长错误: %v / %v", i2.KWh, i2.Hours)
	}
	if i1.RatePerKWh != 1.20 || i2.RatePerKWh != 1.55 {
		t.Fatalf("费率未按泊位区分: %v / %v", i1.RatePerKWh, i2.RatePerKWh)
	}
	if i1.Amount != 40 || i2.Amount != 86 {
		t.Fatalf("分段金额错误: 段1=%v(期望40) 段2=%v(期望86)", i1.Amount, i2.Amount)
	}
	if bill.TotalAmount != 126 || bill.TotalKWh != 60 || bill.TotalHours != 4 {
		t.Fatalf("账单合计错误: amount=%v kwh=%v hours=%v", bill.TotalAmount, bill.TotalKWh, bill.TotalHours)
	}
}

// 场景2：停电——停电窗口不计服务时长，恢复后另起新段；窗口内回传不采信
func TestOutage_ExcludedFromService(t *testing.T) {
	db := testDB(t)
	conn, _ := createConnection(db, createConnInput{
		VesselName: "长风号", MeterCode: "M-A2", Reading: 100,
		StartTime: at("2026-10-06T08:00:00"), ReportBy: "op01",
	})
	devRead(t, db, "M-A2", 110, at("2026-10-06T09:00:00"), 1)
	if err := reportOutage(db, conn.ID, boundaryInput{
		Time: at("2026-10-06T10:00:00"), Reading: fPtr(120), ReportBy: "op01",
	}, "码头线路检修"); err != nil {
		t.Fatal(err)
	}
	// 停电期间设备仍回传：必须被留存但标记 excluded
	r, _, err := ingestReading(db, readingInput{
		MeterCode: "M-A2", Reading: 999, ReadAt: at("2026-10-06T10:30:00"),
		Source: SourceDevice, DeviceSeq: intPtr(2), ReportedBy: "simulator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != ReadExcluded || r.Anomaly != AnomalyDuringOutage {
		t.Fatalf("停电窗口读数应 excluded/during_outage, 实际 %s/%s", r.Status, r.Anomaly)
	}
	if err := resumePower(db, conn.ID, resumeInput{
		Time: at("2026-10-06T11:00:00"), Reading: 120, ReportBy: "op01",
	}); err != nil {
		t.Fatal(err)
	}
	if err := closeConnection(db, conn.ID, boundaryInput{
		Time: at("2026-10-06T12:00:00"), Reading: fPtr(130), ReportBy: "op01",
	}); err != nil {
		t.Fatal(err)
	}
	bill, err := generateBill(db, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bill.Items) != 2 {
		t.Fatalf("停电恢复后应另起新段, 段数=%d", len(bill.Items))
	}
	// 08-10 两段共3小时；10:00-11:00 停电不计
	if bill.TotalHours != 3 {
		t.Fatalf("总服务时长应为3h(剔除停电1h), 实际 %v", bill.TotalHours)
	}
	if bill.TotalKWh != 30 {
		t.Fatalf("总电量应为30, 实际 %v", bill.TotalKWh)
	}
	for _, it := range bill.Items {
		if it.StartTime.Equal(at("2026-10-06T10:00:00")) ||
			(it.StartTime.Before(at("2026-10-06T11:00:00")) && it.EndTime.After(at("2026-10-06T10:00:00"))) {
			// 段2 起点恰为11:00，不包含停电窗口
		}
	}
	if !bill.Items[1].StartTime.Equal(at("2026-10-06T11:00:00")) {
		t.Fatalf("恢复段应从11:00开始, 实际 %s", bill.Items[1].StartTime)
	}
}

// 场景3：读数倒退——挂起结算，复核员处置后才能出账；异常记录保留
func TestRegression_HoldThenResolve(t *testing.T) {
	db := testDB(t)
	conn, _ := createConnection(db, createConnInput{
		VesselName: "远征号", MeterCode: "M-A1", Reading: 100,
		StartTime: at("2026-10-06T08:00:00"), ReportBy: "op01",
	})
	devRead(t, db, "M-A1", 110, at("2026-10-06T09:00:00"), 1)
	bad, _, err := ingestReading(db, readingInput{
		MeterCode: "M-A1", Reading: 90, ReadAt: at("2026-10-06T10:00:00"),
		Source: SourceDevice, DeviceSeq: intPtr(2), ReportedBy: "simulator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Status != ReadFlagged || bad.Anomaly != AnomalyRegression {
		t.Fatalf("倒退读数应 flagged/regression, 实际 %s/%s", bad.Status, bad.Anomaly)
	}
	var c Connection
	db.First(&c, conn.ID)
	if !c.BillingHold {
		t.Fatal("出现异常后接电单应进入结算挂起")
	}
	if err := closeConnection(db, conn.ID, boundaryInput{
		Time: at("2026-10-06T11:00:00"), Reading: fPtr(120), ReportBy: "op01",
	}); err == nil {
		t.Fatal("挂起期间不应允许结束接电")
	}
	// 值班员无权处置异常
	if _, err := resolveAnomaly(db, bad.ID, resolveInput{Reviewer: "op01", Resolution: "discarded"}); err == nil {
		t.Fatal("值班员不应能自行处置异常")
	}
	// 复核员剔除错误读数（记录仍保留为 rejected）
	if _, err := resolveAnomaly(db, bad.ID, resolveInput{
		Reviewer: "rv01", Resolution: "discarded", Note: "表计故障误传，剔除",
	}); err != nil {
		t.Fatal(err)
	}
	db.First(&c, conn.ID)
	if c.BillingHold {
		t.Fatal("异常处置后应解除挂起")
	}
	if err := closeConnection(db, conn.ID, boundaryInput{
		Time: at("2026-10-06T11:00:00"), Reading: fPtr(120), ReportBy: "op01",
	}); err != nil {
		t.Fatal(err)
	}
	bill, err := generateBill(db, conn.ID)
	if err != nil {
		t.Fatalf("解除挂起后应能出账: %v", err)
	}
	if bill.TotalKWh != 20 {
		t.Fatalf("剔除倒退读数后电量应=20, 实际 %v", bill.TotalKWh)
	}
	var kept MeterReading
	if err := db.First(&kept, bad.ID).Error; err != nil || kept.Status != ReadRejected {
		t.Fatalf("异常读数必须作为证据保留为 rejected, err=%v status=%s", err, kept.Status)
	}
}

// 场景3b：异常跃升（折算功率超电表上限）
func TestJumpDetected(t *testing.T) {
	db := testDB(t)
	conn, _ := createConnection(db, createConnInput{
		VesselName: "跃进号", MeterCode: "M-A1", Reading: 100,
		StartTime: at("2026-10-06T08:00:00"), ReportBy: "op01",
	})
	// 1小时暴增 100000 kWh => 100000kW，远超 200kW 上限
	bad, _, _ := ingestReading(db, readingInput{
		MeterCode: "M-A1", Reading: 100100, ReadAt: at("2026-10-06T09:00:00"),
		Source: SourceDevice, DeviceSeq: intPtr(1), ReportedBy: "simulator",
	})
	if bad.Status != ReadFlagged || bad.Anomaly != AnomalyJump {
		t.Fatalf("应判异常跃升, 实际 %s/%s", bad.Status, bad.Anomaly)
	}
	if err := closeConnection(db, conn.ID, boundaryInput{
		Time: at("2026-10-06T10:00:00"), Reading: fPtr(100200), ReportBy: "op01",
	}); err == nil {
		t.Fatal("挂起期间不应能结束接电")
	}
}

// 场景4：漏报补录——必须另一人复核，自录自审被拒
func TestMissingReport_SupplementNeedsSecondReviewer(t *testing.T) {
	db := testDB(t)
	conn, _ := createConnection(db, createConnInput{
		VesselName: "海航号", MeterCode: "M-A1", Reading: 100,
		StartTime: at("2026-10-06T08:00:00"), ReportBy: "op01",
	})
	devRead(t, db, "M-A1", 110, at("2026-10-06T09:00:00"), 1)
	// 序列号从1跳到3 => 漏报
	r3, _, _ := ingestReading(db, readingInput{
		MeterCode: "M-A1", Reading: 121, ReadAt: at("2026-10-06T10:00:00"),
		Source: SourceDevice, DeviceSeq: intPtr(3), ReportedBy: "simulator",
	})
	if r3.Anomaly != AnomalyMissing {
		t.Fatalf("序列号跳号应判漏报, 实际 %s", r3.Anomaly)
	}
	// 值班员补录缺失的 seq2（无设备序列号的手工读数）
	sup, err := submitSupplement(db, supplementInput{
		ConnectionID: conn.ID, MeterCode: "M-A1", Reading: 115,
		ReadAt: at("2026-10-06T09:30:00"), ReportedBy: "op01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sup.Status != ReadPendingReview {
		t.Fatalf("补录应待复核, 实际 %s", sup.Status)
	}
	// 自录自审必须被拒
	if _, err := reviewSupplement(db, sup.ID, reviewInput{Reviewer: "op01", Approve: true}); err == nil {
		t.Fatal("补录人不能自行复核")
	}
	// 另一人复核通过
	if _, err := reviewSupplement(db, sup.ID, reviewInput{
		Reviewer: "rv01", Approve: true, Note: "与船舶日志一致",
	}); err != nil {
		t.Fatalf("另一人复核应通过: %v", err)
	}
	// 漏报的 seq3 经复核确认采信，挂起解除
	if _, err := resolveAnomaly(db, r3.ID, resolveInput{
		Reviewer: "rv01", Resolution: "accepted", Note: "设备延迟补传，数值合理",
	}); err != nil {
		t.Fatal(err)
	}
	var c Connection
	db.First(&c, conn.ID)
	if c.BillingHold {
		t.Fatal("补录复核+异常处置后应解除挂起")
	}
	if err := closeConnection(db, conn.ID, boundaryInput{
		Time: at("2026-10-06T11:00:00"), Reading: fPtr(130), ReportBy: "op01",
	}); err != nil {
		t.Fatal(err)
	}
	bill, err := generateBill(db, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 补录读数仅作为证据，电量仍按段边界 100→130
	if bill.TotalKWh != 30 {
		t.Fatalf("电量应为30, 实际 %v", bill.TotalKWh)
	}
	var item BillItem
	if err := db.Where("bill_id = ? AND seq = 1", bill.ID).First(&item).Error; err != nil {
		t.Fatal(err)
	}
	// 采信读数链应包含补录记录
	var n int64
	db.Model(&MeterReading{}).Where("id IN ? AND status = ?",
		splitIDs(item.ReadingIDs), ReadAccepted).Count(&n)
	if n < 4 { // 起始 + seq1 + 补录 + seq3 + 结束
		t.Fatalf("段证据链应包含补录读数, accepted数=%d ids=%s", n, item.ReadingIDs)
	}
}

// 场景5：设备重复回传——同一序列号只采信一次
func TestDuplicateDeviceUpload(t *testing.T) {
	db := testDB(t)
	conn, _ := createConnection(db, createConnInput{
		VesselName: "重复号", MeterCode: "M-A1", Reading: 100,
		StartTime: at("2026-10-06T08:00:00"), ReportBy: "op01",
	})
	r1, dup1, _ := ingestReading(db, readingInput{
		MeterCode: "M-A1", Reading: 110, ReadAt: at("2026-10-06T09:00:00"),
		Source: SourceDevice, DeviceSeq: intPtr(7), ReportedBy: "simulator",
	})
	if dup1 {
		t.Fatal("首次回传不应是重复")
	}
	// 设备重发同样的序列号（值甚至略有差异也必须忽略）
	r2, dup2, err := ingestReading(db, readingInput{
		MeterCode: "M-A1", Reading: 111, ReadAt: at("2026-10-06T09:00:05"),
		Source: SourceDevice, DeviceSeq: intPtr(7), ReportedBy: "simulator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dup2 || r2.ID != r1.ID {
		t.Fatalf("重复回传必须命中原记录 id: dup=%v id2=%d id1=%d", dup2, r2.ID, r1.ID)
	}
	// 第三次重放
	_, dup3, _ := ingestReading(db, readingInput{
		MeterCode: "M-A1", Reading: 110, ReadAt: at("2026-10-06T09:00:10"),
		Source: SourceDevice, DeviceSeq: intPtr(7), ReportedBy: "simulator",
	})
	if !dup3 {
		t.Fatal("第三次同序列号仍应判定重复")
	}
	var n int64
	db.Model(&MeterReading{}).Where("meter_code = ? AND status = ?", "M-A1", ReadAccepted).Count(&n)
	if n != 2 { // 起始读数 + seq7
		t.Fatalf("重复回传不得产生新计费数据, accepted读数=%d (期望2)", n)
	}
	var fresh MeterReading
	db.First(&fresh, r1.ID)
	if fresh.DupCount != 2 {
		t.Fatalf("原记录重复计数应为2, 实际 %d", fresh.DupCount)
	}
	if err := closeConnection(db, conn.ID, boundaryInput{
		Time: at("2026-10-06T10:00:00"), Reading: fPtr(120), ReportBy: "op01",
	}); err != nil {
		t.Fatal(err)
	}
	bill, err := generateBill(db, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bill.TotalKWh != 20 {
		t.Fatalf("重复回传不得影响电量, kwh=%v", bill.TotalKWh)
	}
}

// 场景6：异议——原账单冻结不改，退款走调整单，付款按净额
func TestDispute_PreservesOriginalBill(t *testing.T) {
	db := testDB(t)
	conn, _ := createConnection(db, createConnInput{
		VesselName: "异议号", MeterCode: "M-A1", Reading: 100,
		StartTime: at("2026-10-06T08:00:00"), ReportBy: "op01",
	})
	if err := closeConnection(db, conn.ID, boundaryInput{
		Time: at("2026-10-06T10:00:00"), Reading: fPtr(140), ReportBy: "op01",
	}); err != nil {
		t.Fatal(err)
	}
	bill, err := generateBill(db, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalAmount := bill.TotalAmount
	originalSnapshot := bill.SnapshotJSON
	if err := confirmBill(db, bill.ID, "ship"); err != nil {
		t.Fatal(err)
	}
	d, err := raiseDispute(db, bill.ID, disputeInput{
		Reason: "第2小时未实际用电", RaisedBy: "ship",
	})
	if err != nil {
		t.Fatal(err)
	}
	var b Bill
	db.First(&b, bill.ID)
	if b.Status != BillDisputed {
		t.Fatal("异议期间账单应冻结为 disputed")
	}
	// 异议处理中不能付款
	if _, err := payBill(db, bill.ID, "ship"); err == nil {
		t.Fatal("异议处理中不应允许付款")
	}
	_, note, err := resolveDispute(db, d.ID, resolveDisputeInput{
		Reviewer: "rv01", Conclusion: "核实为设备误计，退20元",
		Action: "refund", Amount: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if note == nil || note.Kind != "refund" || note.Amount != 20 {
		t.Fatalf("应生成退款调整单: %+v", note)
	}
	db.First(&b, bill.ID)
	if b.TotalAmount != originalAmount || b.SnapshotJSON != originalSnapshot {
		t.Fatal("原账单金额与快照必须保持不变")
	}
	rcpt, err := payBill(db, bill.ID, "ship")
	if err != nil {
		t.Fatal(err)
	}
	if rcpt.Amount != originalAmount-20 {
		t.Fatalf("付款回执应按净额 %v, 实际 %v", originalAmount-20, rcpt.Amount)
	}
}

// 场景7：重复出账被禁止
func TestBillCannotRegenerate(t *testing.T) {
	db := testDB(t)
	conn, _ := createConnection(db, createConnInput{
		VesselName: "单次号", MeterCode: "M-A1", Reading: 100,
		StartTime: at("2026-10-06T08:00:00"), ReportBy: "op01",
	})
	_ = closeConnection(db, conn.ID, boundaryInput{
		Time: at("2026-10-06T09:00:00"), Reading: fPtr(108), ReportBy: "op01",
	})
	if _, err := generateBill(db, conn.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := generateBill(db, conn.ID); err == nil {
		t.Fatal("同一接电单不应能重复出账")
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
