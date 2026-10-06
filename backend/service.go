package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ============================ 接电与分段 ============================

type createConnInput struct {
	VesselName string
	MeterCode  string
	Reading    float64
	StartTime  time.Time
	ReportBy   string
}

func createConnection(db *gorm.DB, in createConnInput) (*Connection, error) {
	var meter Meter
	if err := db.Where("code = ?", in.MeterCode).First(&meter).Error; err != nil {
		return nil, fail(400, "电表不存在: "+in.MeterCode)
	}
	if in.StartTime.IsZero() {
		in.StartTime = time.Now()
	}
	if strings.TrimSpace(in.VesselName) == "" {
		return nil, fail(400, "船名不能为空")
	}
	// 同一只表不能有两段进行中的接电
	var busy int64
	db.Model(&MeterSegment{}).Where("meter_code = ? AND status = ?", meter.Code, SegOpen).Count(&busy)
	if busy > 0 {
		return nil, fail(409, "该电表已有进行中的计量段，请先结束或停电/换泊位")
	}

	var out Connection
	code := ""
	err := db.Transaction(func(tx *gorm.DB) error {
		code = genCode(tx, &Connection{}, "C")
		conn := Connection{
			Code: code, VesselName: in.VesselName, StartMeterID: meter.Code,
			StartTime: in.StartTime, Status: ConnActive, ReportBy: in.ReportBy,
		}
		if err := tx.Create(&conn).Error; err != nil {
			return err
		}
		seg := MeterSegment{
			ConnectionID: conn.ID, Seq: 1, MeterCode: meter.Code, BerthCode: meter.BerthCode,
			StartTime: in.StartTime, StartReading: in.Reading, Reason: "connect", Status: SegOpen,
		}
		if err := tx.Create(&seg).Error; err != nil {
			return err
		}
		// 起始读数作为原始读数留存
		r := MeterReading{
			ConnectionID: &conn.ID, SegmentID: &seg.ID, MeterCode: meter.Code,
			Reading: in.Reading, ReadAt: in.StartTime, Source: SourceManual,
			Status: ReadAccepted, ReportedBy: in.ReportBy,
		}
		if err := tx.Create(&r).Error; err != nil {
			return err
		}
		seg.StartReadingID = &r.ID
		return tx.Save(&seg).Error
	})
	if err != nil {
		return nil, mapErr(err)
	}
	db.First(&out, "code = ?", code)
	return &out, nil
}

const (
	SourceManual = "manual"
	SourceDevice = "device"
)

// terminalReading 关段事件可携带的终止/起始读数
type boundaryInput struct {
	Time     time.Time
	Reading  *float64 // 可选；缺省用最近一条采信读数
	ReportBy string
}

// persistBoundary 校验并留存关段/新段边界读数；异常则拒绝整笔操作（异常须走读数/补录流程）
func persistBoundary(tx *gorm.DB, connID, segID *uint, meter Meter, t time.Time, val float64, by string) (MeterReading, error) {
	if err := checkAnomaly(tx, meter, val, t, nil, deref(connID)); err != nil {
		return MeterReading{}, err
	}
	r := MeterReading{
		ConnectionID: connID, SegmentID: segID, MeterCode: meter.Code,
		Reading: val, ReadAt: t, Source: SourceManual,
		Status: ReadAccepted, ReportedBy: by,
	}
	err := tx.Create(&r).Error
	return r, err
}

// closeOpenSegment 结束某接电单当前打开的计量段
func closeOpenSegment(tx *gorm.DB, conn *Connection, b boundaryInput, meter Meter) (*MeterSegment, error) {
	var seg MeterSegment
	if err := tx.Where("connection_id = ? AND status = ?", conn.ID, SegOpen).Order("seq desc").First(&seg).Error; err != nil {
		return nil, fail(400, "没有进行中的计量段")
	}
	endVal := 0.0
	var endR *MeterReading
	if b.Reading != nil {
		r, err := persistBoundary(tx, &conn.ID, &seg.ID, meter, b.Time, *b.Reading, b.ReportBy)
		if err != nil {
			return nil, err
		}
		endR = &r
		endVal = r.Reading
	} else {
		last, err := lastAccepted(tx, meter.Code, &conn.ID)
		if err != nil {
			return nil, fail(400, "缺少终止读数：该表还没有任何采信读数")
		}
		endVal = last.Reading
		endR = last
		if b.Time.Before(last.ReadAt) {
			return nil, fail(400, "结束时间早于最后一条采信读数")
		}
	}
	if b.Time.Before(seg.StartTime) {
		return nil, fail(400, "结束时间早于本段开始时间")
	}
	seg.EndTime = &b.Time
	seg.EndReading = &endVal
	seg.EndReadingID = &endR.ID
	if conn.BillingHold {
		seg.Status = SegSuspended
	} else {
		seg.Status = SegClosed
	}
	if err := tx.Save(&seg).Error; err != nil {
		return nil, err
	}
	return &seg, nil
}

func openNewSegment(tx *gorm.DB, conn *Connection, meter Meter, reason string, t time.Time, startVal float64, r MeterReading) error {
	var n int64
	tx.Model(&MeterSegment{}).Where("connection_id = ?", conn.ID).Count(&n)
	seg := MeterSegment{
		ConnectionID: conn.ID, Seq: int(n) + 1, MeterCode: meter.Code, BerthCode: meter.BerthCode,
		StartTime: t, StartReading: startVal, Reason: reason, Status: SegOpen,
	}
	if err := tx.Create(&seg).Error; err != nil {
		return err
	}
	r.SegmentID = &seg.ID
	seg.StartReadingID = &r.ID
	return tx.Save(&seg).Error
}

type changeBerthInput struct {
	Time        time.Time
	FromReading *float64
	ToMeterCode string
	ToReading   float64
	ReportBy    string
}

// changeBerth 换泊位：结束原计量段，按新泊位费率另起新段
func changeBerth(db *gorm.DB, connID uint, in changeBerthInput) error {
	return db.Transaction(func(tx *gorm.DB) error {
		conn, err := loadActiveConn(tx, connID)
		if err != nil {
			return err
		}
		if err := ensureNotHeld(conn); err != nil {
			return err
		}
		var oldSeg MeterSegment
		if err := tx.Where("connection_id = ? AND status = ?", connID, SegOpen).First(&oldSeg).Error; err != nil {
			return fail(400, "当前没有进行中的计量段（可能正在停电）")
		}
		var oldMeter, newMeter Meter
		if err := tx.Where("code = ?", oldSeg.MeterCode).First(&oldMeter).Error; err != nil {
			return err
		}
		if err := tx.Where("code = ?", in.ToMeterCode).First(&newMeter).Error; err != nil {
			return fail(400, "新泊位电表不存在: "+in.ToMeterCode)
		}
		if newMeter.Code == oldMeter.Code {
			return fail(400, "新泊位与当前泊位相同，无需换泊位")
		}
		var busy int64
		tx.Model(&MeterSegment{}).Where("meter_code = ? AND status = ? AND connection_id <> ?", newMeter.Code, SegOpen, connID).Count(&busy)
		if busy > 0 {
			return fail(409, "目标电表正被其他接电单占用")
		}
		if in.Time.IsZero() {
			in.Time = time.Now()
		}
		// 1) 原段在换泊位时刻截止
		if _, err := closeOpenSegment(tx, conn, boundaryInput{Time: in.Time, Reading: in.FromReading, ReportBy: in.ReportBy}, oldMeter); err != nil {
			return err
		}
		// 2) 新表起始读数留存并开新段（费率在关段出账时按泊位快照锁定）
		r, err := persistBoundary(tx, &conn.ID, nil, newMeter, in.Time, in.ToReading, in.ReportBy)
		if err != nil {
			return err
		}
		return openNewSegment(tx, conn, newMeter, "berth_change", in.Time, in.ToReading, r)
	})
}

// reportOutage 停电开始：截止当前计量段；停电窗口不存在任何计量段
func reportOutage(db *gorm.DB, connID uint, b boundaryInput, reason string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		conn, err := loadActiveConn(tx, connID)
		if err != nil {
			return err
		}
		var open OutageRecord
		err = tx.Where("connection_id = ? AND end_time IS NULL", connID).First(&open).Error
		if err == nil {
			return fail(409, "该接电单已在停电中")
		}
		if err := ensureNotHeld(conn); err != nil {
			return err
		}
		var oldSeg MeterSegment
		if err := tx.Where("connection_id = ? AND status = ?", connID, SegOpen).First(&oldSeg).Error; err != nil {
			return fail(400, "没有进行中的计量段")
		}
		var meter Meter
		if err := tx.Where("code = ?", oldSeg.MeterCode).First(&meter).Error; err != nil {
			return err
		}
		if b.Time.IsZero() {
			b.Time = time.Now()
		}
		seg, err := closeOpenSegment(tx, conn, b, meter)
		if err != nil {
			return err
		}
		outage := OutageRecord{
			ConnectionID: connID, SegmentBefore: uint(seg.Seq),
			StartTime: b.Time, Reason: reason, ReportBy: b.ReportBy,
		}
		return tx.Create(&outage).Error
	})
}

type resumeInput struct {
	Time      time.Time
	MeterCode string // 可空：沿用停电前电表
	Reading   float64
	ReportBy  string
}

// resumePower 停电恢复：另起新计量段，停电期间不计服务时长
func resumePower(db *gorm.DB, connID uint, in resumeInput) error {
	return db.Transaction(func(tx *gorm.DB) error {
		conn, err := loadActiveConn(tx, connID)
		if err != nil {
			return err
		}
		var outage OutageRecord
		if err := tx.Where("connection_id = ? AND end_time IS NULL", connID).Order("id desc").First(&outage).Error; err != nil {
			return fail(400, "没有进行中的停电登记")
		}
		meterCode := in.MeterCode
		if meterCode == "" {
			var prev MeterSegment
			if err := tx.Where("connection_id = ? AND seq = ?", connID, outage.SegmentBefore).First(&prev).Error; err != nil {
				return err
			}
			meterCode = prev.MeterCode
		}
		var meter Meter
		if err := tx.Where("code = ?", meterCode).First(&meter).Error; err != nil {
			return fail(400, "电表不存在: "+meterCode)
		}
		if in.Time.IsZero() {
			in.Time = time.Now()
		}
		if in.Time.Before(outage.StartTime) {
			return fail(400, "恢复时间早于停电开始时间")
		}
		r, err := persistBoundary(tx, &conn.ID, nil, meter, in.Time, in.Reading, in.ReportBy)
		if err != nil {
			return err
		}
		if err := openNewSegment(tx, conn, meter, "power_restore", in.Time, in.Reading, r); err != nil {
			return err
		}
		end := in.Time
		outage.EndTime = &end
		outage.SegmentAfter = lastSeqPtr(tx, connID)
		return tx.Save(&outage).Error
	})
}

// closeConnection 结束接电：截止最后一段
func closeConnection(db *gorm.DB, connID uint, b boundaryInput) error {
	return db.Transaction(func(tx *gorm.DB) error {
		conn, err := loadActiveConn(tx, connID)
		if err != nil {
			return err
		}
		if err := ensureNotHeld(conn); err != nil {
			return err
		}
		var openOutage int64
		tx.Model(&OutageRecord{}).Where("connection_id = ? AND end_time IS NULL", connID).Count(&openOutage)
		if openOutage > 0 {
			return fail(409, "尚在停电中，请先恢复供电再结束接电")
		}
		var seg MeterSegment
		if err := tx.Where("connection_id = ? AND status = ?", connID, SegOpen).First(&seg).Error; err != nil {
			return fail(400, "没有进行中的计量段")
		}
		var meter Meter
		if err := tx.Where("code = ?", seg.MeterCode).First(&meter).Error; err != nil {
			return err
		}
		if b.Time.IsZero() {
			b.Time = time.Now()
		}
		if _, err := closeOpenSegment(tx, conn, b, meter); err != nil {
			return err
		}
		conn.Status = ConnClosed
		conn.EndTime = &b.Time
		return tx.Save(conn).Error
	})
}

func loadActiveConn(tx *gorm.DB, id uint) (*Connection, error) {
	var c Connection
	if err := tx.First(&c, id).Error; err != nil {
		return nil, fail(404, "接电单不存在")
	}
	if c.Status != ConnActive {
		return nil, fail(409, "接电单已结束")
	}
	return &c, nil
}

func ensureNotHeld(c *Connection) error {
	if c.BillingHold {
		return fail(409, "结算已挂起，暂停换泊位/停电/结束等操作，请先处置："+c.HoldReasons)
	}
	return nil
}

func lastSeqPtr(tx *gorm.DB, connID uint) *uint {
	var s MeterSegment
	if err := tx.Where("connection_id = ?", connID).Order("seq desc").First(&s).Error; err != nil {
		return nil
	}
	v := uint(s.Seq)
	return &v
}

// ============================ 读数接入与异常校验 ============================

type readingInput struct {
	MeterCode    string
	Reading      float64
	ReadAt       time.Time
	Source       string
	DeviceSeq    *int
	ReportedBy   string
	ConnectionID *uint // 手工上报可指定
}

// ingestReading 设备回传/手工录入统一入口。返回读数、是否重复回传。
func ingestReading(db *gorm.DB, in readingInput) (reading MeterReading, duplicated bool, err error) {
	if in.Source == "" {
		in.Source = SourceDevice
	}
	if in.ReadAt.IsZero() {
		in.ReadAt = time.Now()
	}
	var meter Meter
	if e := db.Where("code = ?", in.MeterCode).First(&meter).Error; e != nil {
		return MeterReading{}, false, fail(400, "电表不存在: "+in.MeterCode)
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		// 1) 设备序列号幂等：重复回传只累加次数，绝不产生第二条计费数据
		if in.DeviceSeq != nil {
			var existed MeterReading
			if e := tx.Where("meter_code = ? AND device_seq = ?", meter.Code, *in.DeviceSeq).First(&existed).Error; e == nil {
				if e := tx.Model(&MeterReading{}).Where("id = ?", existed.ID).
					UpdateColumn("dup_count", gorm.Expr("dup_count + 1")).Error; e != nil {
					return e
				}
				reading = existed
				reading.DupCount++
				duplicated = true
				return nil
			}
		}

		// 2) 找到当前的计量段（挂起段也允许设备读数进入，供后续研判）；停电窗口内的回传单独留存、不采信
		var seg *MeterSegment
		var conn *Connection
		q := tx.Where("meter_code = ? AND status IN ?", meter.Code, []string{SegOpen, SegSuspended})
		if in.ConnectionID != nil {
			q = tx.Where("connection_id = ? AND meter_code = ? AND status IN ?",
				*in.ConnectionID, meter.Code, []string{SegOpen, SegSuspended})
		}
		var openSeg MeterSegment
		if e := q.Order("connection_id, seq").First(&openSeg).Error; e == nil {
			seg = &openSeg
			var c Connection
			if e2 := tx.First(&c, seg.ConnectionID).Error; e2 == nil {
				conn = &c
			}
		} else {
			// 停电窗口？查该表所属接电单是否存在未结束停电
			var outage OutageRecord
			e := tx.Where("end_time IS NULL AND connection_id IN (?)",
				tx.Model(&MeterSegment{}).Select("connection_id").Where("meter_code = ?", meter.Code)).
				Order("id desc").First(&outage).Error
			if e == nil {
				r := MeterReading{
					ConnectionID: &outage.ConnectionID, MeterCode: meter.Code, Reading: in.Reading,
					ReadAt: in.ReadAt, Source: in.Source, DeviceSeq: in.DeviceSeq,
					Status: ReadExcluded, Anomaly: AnomalyDuringOutage, ReportedBy: in.ReportedBy,
				}
				if e := tx.Create(&r).Error; e != nil {
					return e
				}
				reading = r
				return nil
			}
			return fail(409, "该电表当前没有进行中的计量段，读数被拒收")
		}

		// 3) 四项校验：倒退 / 跃升 / 漏报 / 时间倒序
		anomaly := detectAnomaly(tx, meter, in.Reading, in.ReadAt, in.DeviceSeq, conn.ID)
		status := ReadAccepted
		if anomaly != "" {
			status = ReadFlagged
		}
		r := MeterReading{
			ConnectionID: &conn.ID, SegmentID: &seg.ID, MeterCode: meter.Code,
			Reading: in.Reading, ReadAt: in.ReadAt, Source: in.Source, DeviceSeq: in.DeviceSeq,
			Status: status, Anomaly: anomaly, ReportedBy: in.ReportedBy,
		}
		if e := tx.Create(&r).Error; e != nil {
			return e
		}
		reading = r
		if anomaly != "" {
			return refreshHold(tx, conn.ID)
		}
		return nil
	})
	return reading, duplicated, mapErr(err)
}

func meterBelongsConn(tx *gorm.DB, meterCode string, connID uint) bool {
	var n int64
	tx.Model(&MeterSegment{}).Where("connection_id = ? AND meter_code = ?", connID, meterCode).Count(&n)
	return n > 0
}

// detectAnomaly 返回异常类型；空串表示正常
func detectAnomaly(tx *gorm.DB, meter Meter, val float64, at time.Time, seq *int, connID uint) string {
	last, err := lastAccepted(tx, meter.Code, &connID)
	if err != nil {
		return "" // 首条读数无参照
	}
	if at.Before(last.ReadAt) {
		return AnomalyOutOfOrder
	}
	if val < last.Reading {
		return AnomalyRegression
	}
	hours := at.Sub(last.ReadAt).Hours()
	if val > last.Reading && hours > 0 {
		avgKW := (val - last.Reading) / hours
		if avgKW > meter.MaxPowerKW {
			return AnomalyJump
		}
	}
	// 设备序列号跳号 => 漏报
	if seq != nil && last.DeviceSeq != nil && *seq > *last.DeviceSeq+1 {
		return AnomalyMissing
	}
	return ""
}

// checkAnomaly 用于边界录入：异常直接拒绝（仅在当前接电单范围内比较，跨单表码归零不算倒退）
func checkAnomaly(tx *gorm.DB, meter Meter, val float64, at time.Time, seq *int, connID uint) error {
	a := detectAnomaly(tx, meter, val, at, seq, connID)
	switch a {
	case "":
		return nil
	case AnomalyRegression:
		return fail(400, "读数低于上一条采信读数，疑似表计倒走，已拒绝该操作")
	case AnomalyJump:
		return fail(400, "读数折算功率超过电表上限，疑似异常跃升，已拒绝该操作")
	case AnomalyOutOfOrder:
		return fail(400, "读数时间早于上一条采信读数")
	default:
		return fail(400, "异常读数: "+a)
	}
}

func lastAccepted(tx *gorm.DB, meterCode string, connID *uint) (*MeterReading, error) {
	q := tx.Where("meter_code = ? AND status = ?", meterCode, ReadAccepted)
	if connID != nil && *connID != 0 {
		q = q.Where("connection_id = ?", *connID)
	}
	var r MeterReading
	if err := q.Order("read_at desc, id desc").First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// refreshHold 依据未决异常/待复核补录刷新挂起状态与计量段状态
func refreshHold(tx *gorm.DB, connID uint) error {
	var flagged []MeterReading
	tx.Where("connection_id = ? AND status = ? AND resolved_at IS NULL", connID, ReadFlagged).Find(&flagged)
	var pending []MeterReading
	tx.Where("connection_id = ? AND status = ?", connID, ReadPendingReview).Find(&pending)

	reasons := map[string]bool{}
	for _, r := range flagged {
		reasons["异常读数#"+itoa(r.ID)+":"+r.Anomaly] = true
	}
	for _, r := range pending {
		reasons["待复核补录#"+itoa(r.ID)] = true
	}
	var keys []string
	for k := range reasons {
		keys = append(keys, k)
	}
	var conn Connection
	if err := tx.First(&conn, connID).Error; err != nil {
		return err
	}
	conn.BillingHold = len(keys) > 0
	conn.HoldReasons = strings.Join(keys, "；")
	if err := tx.Save(&conn).Error; err != nil {
		return err
	}
	if conn.BillingHold {
		// 进行中的计量段挂起，停电/换泊位/结束/出账全部暂停
		tx.Model(&MeterSegment{}).Where("connection_id = ? AND status = ?", connID, SegOpen).
			Update("status", SegSuspended)
	} else {
		// 解除：未截止的段恢复 open，已截止的段转为 closed
		tx.Model(&MeterSegment{}).Where("connection_id = ? AND status = ? AND end_time IS NULL", connID, SegSuspended).
			Update("status", SegOpen)
		tx.Model(&MeterSegment{}).Where("connection_id = ? AND status = ? AND end_time IS NOT NULL", connID, SegSuspended).
			Update("status", SegClosed)
	}
	return nil
}

// ============================ 补录与双人复核 ============================

type supplementInput struct {
	ConnectionID uint
	MeterCode    string
	Reading      float64
	ReadAt       time.Time
	ReportedBy   string // 值班员
}

// submitSupplement 值班员补录漏报读数：先挂起待另一人复核
func submitSupplement(db *gorm.DB, in supplementInput) (*MeterReading, error) {
	var meter Meter
	if err := db.Where("code = ?", in.MeterCode).First(&meter).Error; err != nil {
		return nil, fail(400, "电表不存在")
	}
	var conn Connection
	if err := db.First(&conn, in.ConnectionID).Error; err != nil {
		return nil, fail(404, "接电单不存在")
	}
	if in.ReadAt.IsZero() {
		return nil, fail(400, "补录必须带读数时间")
	}
	r := MeterReading{
		ConnectionID: &conn.ID, MeterCode: meter.Code, Reading: in.Reading,
		ReadAt: in.ReadAt, Source: SourceManual, Status: ReadPendingReview,
		ReportedBy: in.ReportedBy,
	}
	if err := db.Create(&r).Error; err != nil {
		return nil, err
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return refreshHold(tx, conn.ID) }); err != nil {
		return nil, err
	}
	return &r, nil
}

type reviewInput struct {
	Reviewer string
	Approve  bool
	Note     string
}

// reviewSupplement 另一人复核补录（复核人必须与补录人不同）
func reviewSupplement(db *gorm.DB, id uint, in reviewInput) (*MeterReading, error) {
	var r MeterReading
	if err := db.First(&r, id).Error; err != nil {
		return nil, fail(404, "读数不存在")
	}
	if r.Status != ReadPendingReview {
		return nil, fail(409, "该读数不在待复核状态")
	}
	if in.Reviewer == "" {
		return nil, fail(400, "缺少复核人")
	}
	if in.Reviewer == r.ReportedBy {
		return nil, fail(403, "补录须由另一人复核，不能自录自审")
	}
	if !userHasRole(db, in.Reviewer, RoleReviewer) && !userHasRole(db, in.Reviewer, RoleOperator) {
		return nil, fail(403, "复核人必须是值班/复核岗位")
	}
	now := time.Now()
	r.ReviewedBy = in.Reviewer
	r.ReviewedAt = &now
	r.ReviewNote = in.Note
	if in.Approve {
		// 归入对应计量段；落在停电窗口的不予采信
		if inWindow, err := inOutageWindow(db, r.ConnectionID, r.ReadAt); err != nil {
			return nil, err
		} else if inWindow {
			return nil, fail(400, "补录时间落在停电窗口，不能计为用电读数")
		}
		seg, err := segmentAtTime(db, *r.ConnectionID, r.MeterCode, r.ReadAt)
		if err != nil {
			return nil, fail(400, "补录时间不属于该表任何计量段: "+err.Error())
		}
		// 数值合理性：不得低于段起点、不得高于段内下一条采信读数
		if r.Reading < seg.StartReading {
			return nil, fail(400, "补录读数低于该段起始读数")
		}
		r.SegmentID = &seg.ID
		r.Status = ReadAccepted
		r.Resolution = "supplement_approved"
	} else {
		r.Status = ReadRejected
		r.Resolution = "supplement_rejected"
	}
	if err := db.Save(&r).Error; err != nil {
		return nil, err
	}
	db.Transaction(func(tx *gorm.DB) error { return refreshHold(tx, *r.ConnectionID) })
	return &r, nil
}

type resolveInput struct {
	Reviewer   string
	Resolution string // accepted 采信 | discarded 剔除
	Note       string
}

// resolveAnomaly 复核员对倒退/跃升等异常读数作结论；记录保留不删除
func resolveAnomaly(db *gorm.DB, id uint, in resolveInput) (*MeterReading, error) {
	var r MeterReading
	if err := db.First(&r, id).Error; err != nil {
		return nil, fail(404, "读数不存在")
	}
	if r.Status != ReadFlagged || r.ResolvedAt != nil {
		return nil, fail(409, "该读数不是待处理异常")
	}
	if !userHasRole(db, in.Reviewer, RoleReviewer) {
		return nil, fail(403, "须由复核员岗处理异常")
	}
	now := time.Now()
	r.ResolvedAt = &now
	r.ReviewedBy = in.Reviewer
	r.ReviewNote = in.Note
	switch in.Resolution {
	case "accepted":
		// 采信即人工确认该值：保留异常标记作为证据，状态转采信
		r.Status = ReadAccepted
		r.Resolution = "anomaly_accepted:" + in.Note
	case "discarded":
		r.Status = ReadRejected
		r.Resolution = "anomaly_discarded:" + in.Note
	default:
		return nil, fail(400, "结论须为 accepted 或 discarded")
	}
	if err := db.Save(&r).Error; err != nil {
		return nil, err
	}
	db.Transaction(func(tx *gorm.DB) error { return refreshHold(tx, *r.ConnectionID) })
	return &r, nil
}

func userHasRole(db *gorm.DB, code, role string) bool {
	var n int64
	db.Model(&User{}).Where("code = ? AND role = ?", code, role).Count(&n)
	return n > 0
}

func inOutageWindow(db *gorm.DB, connID *uint, t time.Time) (bool, error) {
	if connID == nil {
		return false, nil
	}
	var n int64
	db.Model(&OutageRecord{}).
		Where("connection_id = ? AND start_time <= ? AND (end_time IS NULL OR end_time >= ?)", *connID, t, t).
		Count(&n)
	return n > 0, nil
}

func segmentAtTime(db *gorm.DB, connID uint, meterCode string, t time.Time) (*MeterSegment, error) {
	var s MeterSegment
	err := db.Where("connection_id = ? AND meter_code = ? AND start_time <= ? AND (end_time IS NULL OR end_time >= ?)",
		connID, meterCode, t, t).Order("seq").First(&s).Error
	if err != nil {
		return nil, errors.New("无匹配计量段")
	}
	return &s, nil
}

// ============================ 出账 ============================

type billSnapshot struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Connection  Connection     `json:"connection"`
	Segments    []MeterSegment `json:"segments"`
	Readings    []MeterReading `json:"readings"`
	Outages     []OutageRecord `json:"outages"`
}

func generateBill(db *gorm.DB, connID uint) (*Bill, error) {
	var bill Bill
	err := db.Transaction(func(tx *gorm.DB) error {
		var conn Connection
		if err := tx.First(&conn, connID).Error; err != nil {
			return fail(404, "接电单不存在")
		}
		if conn.Status != ConnClosed {
			return fail(409, "接电尚未结束，不能出账")
		}
		if conn.BillingHold {
			return fail(409, "结算挂起中，存在未处理异常或待复核补录："+conn.HoldReasons)
		}
		var existed Bill
		if err := tx.Where("connection_id = ?", connID).First(&existed).Error; err == nil {
			return fail(409, "该接电单已生成账单，账单不可重新生成")
		}
		var segs []MeterSegment
		tx.Where("connection_id = ?", connID).Order("seq").Find(&segs)
		if len(segs) == 0 {
			return fail(400, "没有计量段")
		}
		berthCache := map[string]Berth{}
		bill = Bill{Code: genCode(tx, &Bill{}, "B"), ConnectionID: connID, VesselName: conn.VesselName, Status: BillPending}
		var items []BillItem
		var snapReadings []MeterReading
		tx.Where("connection_id = ?", connID).Order("read_at, id").Find(&snapReadings)
		var outages []OutageRecord
		tx.Where("connection_id = ?", connID).Order("id").Find(&outages)

		for i := range segs {
			s := &segs[i]
			if s.Status == SegSuspended {
				return fail(409, "计量段#"+itoa(s.Seq)+"处于挂起状态")
			}
			if s.EndTime == nil || s.EndReading == nil {
				return fail(409, "计量段#"+itoa(s.Seq)+"未结束")
			}
			berth, ok := berthCache[s.BerthCode]
			if !ok {
				if err := tx.Where("code = ?", s.BerthCode).First(&berth).Error; err != nil {
					return err
				}
				berthCache[s.BerthCode] = berth
			}
			// 该段全部采信读数（含边界）作为原始证据
			var rs []MeterReading
			tx.Where("status = ? AND ((segment_id = ?) OR id = ? OR id = ?)",
				ReadAccepted, s.ID, deref(s.StartReadingID), deref(s.EndReadingID)).
				Order("read_at, id").Find(&rs)
			var ids []string
			for _, rr := range rs {
				ids = append(ids, itoa(int(rr.ID)))
			}
			kwh := round2(*s.EndReading - s.StartReading)
			if kwh < 0 {
				return fail(500, "计量段#"+itoa(s.Seq)+"用电量为负，数据异常")
			}
			hours := hoursBetween(s.StartTime, *s.EndTime)
			energy := round2(kwh * berth.RatePerKWh)
			svc := round2(hours * berth.ServiceFeePerHr)
			// 锁定段费率快照
			s.RatePerKWh = berth.RatePerKWh
			s.ServiceFeePerHr = berth.ServiceFeePerHr
			tx.Save(s)
			item := BillItem{
				SegmentID: s.ID, Seq: s.Seq, BerthCode: s.BerthCode, MeterCode: s.MeterCode,
				StartTime: s.StartTime, EndTime: *s.EndTime,
				StartReading: s.StartReading, EndReading: *s.EndReading,
				KWh: kwh, Hours: round4(hours), RatePerKWh: berth.RatePerKWh, ServiceFeePerHr: berth.ServiceFeePerHr,
				EnergyFee: energy, ServiceFee: svc, Amount: round2(energy + svc),
				Reason: reasonText(s.Reason), ReadingIDs: strings.Join(ids, ","),
			}
			bill.TotalKWh += kwh
			bill.TotalHours += hours
			bill.EnergyFee += energy
			bill.ServiceFee += svc
			items = append(items, item)
		}
		bill.TotalKWh = round2(bill.TotalKWh)
		bill.TotalHours = round4(bill.TotalHours)
		bill.EnergyFee = round2(bill.EnergyFee)
		bill.ServiceFee = round2(bill.ServiceFee)
		bill.TotalAmount = round2(bill.EnergyFee + bill.ServiceFee)
		snap := billSnapshot{GeneratedAt: time.Now(), Connection: conn, Segments: segs, Readings: snapReadings, Outages: outages}
		raw, _ := json.Marshal(snap)
		bill.SnapshotJSON = string(raw)
		if err := tx.Create(&bill).Error; err != nil {
			return err
		}
		for i := range items {
			items[i].BillID = bill.ID
			if err := tx.Create(&items[i]).Error; err != nil {
				return err
			}
		}
		bill.Items = items
		return tx.Model(&Connection{}).Where("id = ?", connID).Update("bill_id", bill.ID).Error
	})
	if err != nil {
		return nil, mapErr(err)
	}
	db.Preload("Items").First(&bill, bill.ID)
	return &bill, nil
}

func reasonText(r string) string {
	switch r {
	case "connect":
		return "接电起始段"
	case "berth_change":
		return "换泊位新段"
	case "power_restore":
		return "停电恢复新段"
	default:
		return r
	}
}

func deref(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}

func round4(x float64) float64 { return float64(int64(x*10000+0.5)) / 10000 }

// ============================ 确认 / 付款（模拟回执） / 异议 ============================

func confirmBill(db *gorm.DB, billID uint, by string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var b Bill
		if err := tx.First(&b, billID).Error; err != nil {
			return fail(404, "账单不存在")
		}
		if b.Status != BillPending {
			return fail(409, "账单当前状态不可确认: "+b.Status)
		}
		if !userHasRole(tx, by, RoleVessel) {
			return fail(403, "须由船方确认")
		}
		now := time.Now()
		b.Status = BillConfirmed
		b.ConfirmedBy = by
		b.ConfirmedAt = &now
		return tx.Save(&b).Error
	})
}

type disputeInput struct {
	Reason   string
	Evidence string
	RaisedBy string
}

func raiseDispute(db *gorm.DB, billID uint, in disputeInput) (*Dispute, error) {
	if strings.TrimSpace(in.Reason) == "" {
		return nil, fail(400, "异议理由不能为空")
	}
	var d *Dispute
	err := db.Transaction(func(tx *gorm.DB) error {
		var b Bill
		if err := tx.First(&b, billID).Error; err != nil {
			return fail(404, "账单不存在")
		}
		if !userHasRole(tx, in.RaisedBy, RoleVessel) {
			return fail(403, "须由船方提出异议")
		}
		if b.Status == BillDisputed {
			return fail(409, "账单已有进行中的异议")
		}
		// 原始账单与快照原样冻结，仅状态流转
		b.Status = BillDisputed
		if err := tx.Save(&b).Error; err != nil {
			return err
		}
		// 证据：固定为该单原始读数/分段快照，不可篡改
		d = &Dispute{
			BillID: billID, Reason: in.Reason,
			Evidence: firstNonEmpty(in.Evidence, "出账快照#"+b.Code+"（含全部原始读数，系统留存）"),
			RaisedBy: in.RaisedBy, RaisedAt: time.Now(), Status: DisputeOpen,
		}
		return tx.Create(d).Error
	})
	return d, mapErr(err)
}

type resolveDisputeInput struct {
	Reviewer   string
	Conclusion string
	Action     string // reject | refund | additional
	Amount     float64
}

// resolveDispute 处理异议：原账单不动，成立时开调整单冲账/补收
func resolveDispute(db *gorm.DB, disputeID uint, in resolveDisputeInput) (*Dispute, *CreditNote, error) {
	var d Dispute
	var cn *CreditNote
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&d, disputeID).Error; err != nil {
			return fail(404, "异议不存在")
		}
		if d.Status != DisputeOpen {
			return fail(409, "异议已处理")
		}
		if !userHasRole(tx, in.Reviewer, RoleReviewer) {
			return fail(403, "须由复核岗处理异议")
		}
		if strings.TrimSpace(in.Conclusion) == "" {
			return fail(400, "处理结论不能为空")
		}
		var b Bill
		if err := tx.First(&b, d.BillID).Error; err != nil {
			return err
		}
		switch in.Action {
		case "refund", "additional":
			if in.Amount <= 0 {
				return fail(400, "调整金额必须大于0")
			}
			note := CreditNote{
				Code: genCode(tx, &CreditNote{}, "CN"), BillID: b.ID, DisputeID: d.ID,
				Kind: in.Action, Amount: in.Amount,
				Reason: firstNonEmpty(in.Conclusion, "异议调整"), CreatedBy: in.Reviewer,
			}
			if err := tx.Create(&note).Error; err != nil {
				return err
			}
			cn = &note
			d.CreditNoteID = &note.ID
		case "reject":
		default:
			return fail(400, "action 须为 reject/refund/additional")
		}
		now := time.Now()
		d.Status = DisputeClosed
		d.Conclusion = in.Conclusion
		d.ResolvedBy = in.Reviewer
		d.ResolvedAt = &now
		if err := tx.Save(&d).Error; err != nil {
			return err
		}
		// 账单恢复到付款前/已付款状态；金额本身永不就地修改
		var paid int64
		tx.Model(&PaymentReceipt{}).Where("bill_id = ?", b.ID).Count(&paid)
		newStatus := BillConfirmed
		if paid > 0 {
			newStatus = BillPaid
		}
		return tx.Model(&Bill{}).Where("id = ?", b.ID).Update("status", newStatus).Error
	})
	return &d, cn, mapErr(err)
}

// payBill 本地模拟付款：生成回执
func payBill(db *gorm.DB, billID uint, by string) (*PaymentReceipt, error) {
	var rcpt *PaymentReceipt
	err := db.Transaction(func(tx *gorm.DB) error {
		var b Bill
		if err := tx.First(&b, billID).Error; err != nil {
			return fail(404, "账单不存在")
		}
		if b.Status == BillDisputed {
			return fail(409, "账单异议处理中，暂不能付款")
		}
		if b.Status != BillConfirmed {
			return fail(409, "账单须经船方确认后方可付款: "+b.Status)
		}
		var net float64
		var notes []CreditNote
		tx.Where("bill_id = ?", b.ID).Find(&notes)
		net = b.TotalAmount
		for _, n := range notes {
			if n.Kind == "refund" {
				net -= n.Amount
			} else {
				net += n.Amount
			}
		}
		net = round2(net)
		rcpt = &PaymentReceipt{
			Code: genCode(tx, &PaymentReceipt{}, "R"), BillID: b.ID,
			Amount: net, Method: "MOCK-本地模拟支付", PaidAt: time.Now(), PaidBy: by,
		}
		if err := tx.Create(rcpt).Error; err != nil {
			return err
		}
		return tx.Model(&Bill{}).Where("id = ?", b.ID).Update("status", BillPaid).Error
	})
	return rcpt, mapErr(err)
}

// ============================ 杂项 ============================

func genCode(tx *gorm.DB, model any, prefix string) string {
	var n int64
	tx.Model(model).Count(&n)
	return fmt.Sprintf("%s%04d", prefix, n+1)
}

func itoa[T ~int | ~uint](v T) string { return fmt.Sprintf("%d", v) }

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func mapErr(err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		return err
	}
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE") {
		return fail(409, "数据重复冲突: "+err.Error())
	}
	return err
}
