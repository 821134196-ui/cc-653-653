package service

import (
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"

	"shorepower/internal/models"
)

// 异常判定阈值（本地模拟口径）
const (
	// JumpKWhPerHour 相邻读数换算功率超过该值视为异常跃升
	JumpKWhPerHour = 200.0
	// MissingGap 相邻已采纳读数间隔超过该时长视为漏报
	MissingGap = 60 * time.Minute
)

var (
	ErrNotFound     = errors.New("记录不存在")
	ErrInvalidState = errors.New("当前状态不允许该操作")
	ErrReviewerSame = errors.New("复核人不能与录入人为同一人")
	ErrAnomalyOpen  = errors.New("存在未处理的读数异常，已暂停结算")
	ErrValueInvalid = errors.New("补录读数与相邻有效读数矛盾")
)

// Service 业务逻辑层
type Service struct{ DB *gorm.DB }

func New(db *gorm.DB) *Service { return &Service{DB: db} }

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// CurrentSegment 返回会话当前计量段
func (s *Service) CurrentSegment(tx *gorm.DB, sessionID uint) (*models.MeterSegment, error) {
	var seg models.MeterSegment
	if err := tx.Where("session_id = ?", sessionID).Order("seq DESC").First(&seg).Error; err != nil {
		return nil, ErrNotFound
	}
	return &seg, nil
}

// lastAcceptedReading 取段内时间序最新一条已采纳读数
func (s *Service) lastAcceptedReading(tx *gorm.DB, segmentID uint, before time.Time) (*models.MeterReading, error) {
	var r models.MeterReading
	err := tx.Where("segment_id = ? AND status IN ? AND read_at <= ?",
		segmentID, []string{models.ReadingAccepted}, before).
		Order("read_at DESC, id DESC").First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &r, err
}

func (s *Service) anchorReading(tx *gorm.DB, seg *models.MeterSegment, operator, source string) error {
	r := models.MeterReading{
		SegmentID:  seg.ID,
		SessionID:  seg.SessionID,
		Source:     source,
		Value:      seg.StartReading,
		ReadAt:     seg.StartTime,
		RecordedAt: time.Now(),
		Status:     models.ReadingAccepted,
		Operator:   operator,
	}
	return tx.Create(&r).Error
}

// RegisterSession 值班员登记接电：船名、泊位、电表起始读数、接电时间
func (s *Service) RegisterSession(vesselName, agent string, berthID uint, operator string, start time.Time, startReading float64) (*models.Session, error) {
	var berth models.Berth
	if err := s.DB.First(&berth, berthID).Error; err != nil {
		return nil, fmt.Errorf("泊位不存在: %w", err)
	}
	if start.IsZero() {
		start = time.Now()
	}
	var vessel models.Vessel
	err := s.DB.Where("name = ?", vesselName).First(&vessel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		vessel = models.Vessel{Name: vesselName, Agent: agent}
		if err := s.DB.Create(&vessel).Error; err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	var out *models.Session
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		sess := models.Session{
			VesselID:  vessel.ID,
			BerthID:   berth.ID,
			Operator:  operator,
			StartTime: start,
			Status:    models.SessionActive,
		}
		if err := tx.Create(&sess).Error; err != nil {
			return err
		}
		var count int64
		tx.Model(&models.MeterSegment{}).Where("session_id = ?", sess.ID).Count(&count)
		seg := models.MeterSegment{
			SessionID:    sess.ID,
			BerthID:      berth.ID,
			Seq:          int(count) + 1,
			StartReading: startReading,
			StartTime:    start,
			Status:       models.SegmentServing,
		}
		if err := tx.Create(&seg).Error; err != nil {
			return err
		}
		if err := s.anchorReading(tx, &seg, operator, models.ReadingManual); err != nil {
			return err
		}
		out = &sess
		return nil
	})
	return out, err
}

// ReadingResult 上报结果：Duplicate 表示设备重复回传被幂等丢弃
type ReadingResult struct {
	Reading   models.MeterReading  `json:"reading"`
	Duplicate bool                 `json:"duplicate"`
	Anomaly   *models.AnomalyEvent `json:"anomaly"`
}

// AddReading 登记一条手工抄录或设备回传读数。
// source=device 时必须带 messageUID，相同 UID 重复回传原样返回并标记 Duplicate。
func (s *Service) AddReading(sessionID uint, source string, value float64, readAt time.Time, operator, deviceID, messageUID string) (*ReadingResult, error) {
	if readAt.IsZero() {
		readAt = time.Now()
	}
	if source == models.ReadingDevice {
		if messageUID == "" {
			return nil, errors.New("设备回传必须提供报文ID")
		}
		// 幂等：重复回传直接返回原记录
		var exist models.MeterReading
		if err := s.DB.Where("message_uid = ?", messageUID).First(&exist).Error; err == nil {
			return &ReadingResult{Reading: exist, Duplicate: true}, nil
		}
	}

	var sess models.Session
	if err := s.DB.First(&sess, sessionID).Error; err != nil {
		return nil, ErrNotFound
	}
	if sess.Status == models.SessionBilled || sess.Status == models.BillPaid {
		return nil, fmt.Errorf("%w: 会话已结算", ErrInvalidState)
	}

	res := &ReadingResult{}
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		seg, err := s.CurrentSegment(tx, sessionID)
		if err != nil {
			return err
		}
		last, err := s.lastAcceptedReading(tx, seg.ID, readAt)
		if err != nil {
			return err
		}

		r := models.MeterReading{
			SegmentID:  seg.ID,
			SessionID:  sessionID,
			Source:     source,
			Value:      value,
			ReadAt:     readAt,
			RecordedAt: time.Now(),
			DeviceID:   deviceID,
			MessageUID: messageUID,
			Operator:   operator,
			Status:     models.ReadingAccepted,
		}

		kind, detail := classify(last, value, readAt)
		if kind != "" {
			r.Status = models.ReadingAnomalous
			if err := tx.Create(&r).Error; err != nil {
				return err
			}
			ev := models.AnomalyEvent{
				SessionID: sessionID,
				SegmentID: seg.ID,
				ReadingID: &r.ID,
				Kind:      kind,
				Detail:    detail,
				Status:    models.AnomalyOpen,
			}
			if err := tx.Create(&ev).Error; err != nil {
				return err
			}
			r.AnomalyID = &ev.ID
			if err := tx.Model(&r).Update("anomaly_id", ev.ID).Error; err != nil {
				return err
			}
			if err := s.freeze(tx, sessionID, seg.ID); err != nil {
				return err
			}
			res.Reading = r
			res.Anomaly = &ev
			return nil
		}

		if err := tx.Create(&r).Error; err != nil {
			return err
		}

		// 漏报检测：与上一条有效读数间隔过大
		if last != nil && readAt.Sub(last.ReadAt) > MissingGap {
			ev := models.AnomalyEvent{
				SessionID: sessionID,
				SegmentID: seg.ID,
				ReadingID: &r.ID,
				Kind:      models.AnomalyMissing,
				Detail: fmt.Sprintf("与上一条有效读数间隔 %s（阈值 %s），期间疑似漏报",
					readAt.Sub(last.ReadAt).Round(time.Second), MissingGap),
				Status: models.AnomalyOpen,
			}
			if err := tx.Create(&ev).Error; err != nil {
				return err
			}
			if err := s.freeze(tx, sessionID, seg.ID); err != nil {
				return err
			}
			res.Anomaly = &ev
		}
		res.Reading = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// classify 对照上一条有效读数判断倒退/跃升
func classify(last *models.MeterReading, value float64, at time.Time) (kind, detail string) {
	if last == nil {
		return "", ""
	}
	if value < last.Value {
		return models.AnomalyBackward, fmt.Sprintf(
			"读数倒退：新读数 %.2f 小于上一有效读数 %.2f（%s）", value, last.Value, last.ReadAt.Format(time.RFC3339))
	}
	elapsed := at.Sub(last.ReadAt).Hours()
	if elapsed > 0 {
		rate := (value - last.Value) / elapsed
		if rate > JumpKWhPerHour {
			return models.AnomalyJump, fmt.Sprintf(
				"异常跃升：%.2fkWh / %.2fh = %.1f kWh/h，超过阈值 %.0f kWh/h",
				value-last.Value, elapsed, rate, JumpKWhPerHour)
		}
	}
	return "", ""
}

// freeze 冻结：计量段挂起、会话暂停结算
func (s *Service) freeze(tx *gorm.DB, sessionID, segmentID uint) error {
	if err := tx.Model(&models.MeterSegment{}).Where("id = ? AND status = ?",
		segmentID, models.SegmentServing).Update("status", models.SegmentFlagged).Error; err != nil {
		return err
	}
	return tx.Model(&models.Session{}).Where("id = ?", sessionID).
		Update("status", models.SessionBlocked).Error
}

// SubmitSupplement 漏报/异常补录，进入待复核状态，必须由另一人复核
func (s *Service) SubmitSupplement(sessionID uint, operator string, value float64, readAt time.Time, note string) (*models.MeterReading, error) {
	if readAt.IsZero() {
		readAt = time.Now()
	}
	var seg models.MeterSegment
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		cur, err := s.CurrentSegment(tx, sessionID)
		if err != nil {
			return err
		}
		seg = *cur
		r := models.MeterReading{
			SegmentID:  seg.ID,
			SessionID:  sessionID,
			Source:     models.ReadingSupplement,
			Value:      value,
			ReadAt:     readAt,
			RecordedAt: time.Now(),
			Status:     models.ReadingPending,
			Operator:   operator,
			ReviewNote: note,
		}
		return tx.Create(&r).Error
	})
	if err != nil {
		return nil, err
	}
	var r models.MeterReading
	if err := s.DB.Where("session_id = ? AND status = ?", sessionID, models.ReadingPending).
		Order("id DESC").First(&r).Error; err != nil {
		return nil, err
	}
	return &r, err
}

// ReviewSupplement 另一人复核补录读数
func (s *Service) ReviewSupplement(readingID uint, reviewer string, approve bool, note string) (*models.MeterReading, error) {
	var r models.MeterReading
	if err := s.DB.First(&r, readingID).Error; err != nil {
		return nil, ErrNotFound
	}
	if r.Status != models.ReadingPending {
		return nil, fmt.Errorf("%w: 该读数不是待复核状态", ErrInvalidState)
	}
	if reviewer == r.Operator {
		return nil, ErrReviewerSame
	}

	now := time.Now()
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		if approve {
			// 与相邻有效读数核对，防止补录本身造成倒退/跃升
			var prev, next models.MeterReading
			tx.Where("segment_id = ? AND status = ? AND read_at < ?", r.SegmentID, models.ReadingAccepted, r.ReadAt).
				Order("read_at DESC, id DESC").First(&prev)
			tx.Where("segment_id = ? AND status = ? AND read_at > ?", r.SegmentID, models.ReadingAccepted, r.ReadAt).
				Order("read_at ASC, id ASC").First(&next)
			if prev.ID != 0 && r.Value < prev.Value {
				return fmt.Errorf("%w: 小于之前读数 %.2f", ErrValueInvalid, prev.Value)
			}
			if next.ID != 0 {
				if r.Value > next.Value {
					return fmt.Errorf("%w: 大于之后读数 %.2f", ErrValueInvalid, next.Value)
				}
				if h := next.ReadAt.Sub(r.ReadAt).Hours(); h > 0 && (next.Value-r.Value)/h > JumpKWhPerHour {
					return fmt.Errorf("%w: 与后续读数构成跃升", ErrValueInvalid)
				}
			}
			r.Status = models.ReadingAccepted
		} else {
			r.Status = models.ReadingRejected
		}
		r.Reviewer = reviewer
		r.ReviewedAt = &now
		if note != "" {
			r.ReviewNote = note
		}
		if err := tx.Save(&r).Error; err != nil {
			return err
		}

		if approve {
			// 驳回引发异常的原始读数（倒退/跃升）
			if err := tx.Model(&models.MeterReading{}).
				Where("segment_id = ? AND status = ?", r.SegmentID, models.ReadingAnomalous).
				Updates(map[string]any{"status": models.ReadingRejected, "reviewer": reviewer, "reviewed_at": now}).
				Error; err != nil {
				return err
			}
			if err := s.resolveAnomaliesFor(tx, r); err != nil {
				return err
			}
			return s.unfreezeIfClear(tx, r.SessionID, r.SegmentID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// resolveAnomaliesFor 补录采纳后，关闭漏报区间被覆盖/对应段的未决异常
func (s *Service) resolveAnomaliesFor(tx *gorm.DB, r models.MeterReading) error {
	var events []models.AnomalyEvent
	if err := tx.Where("segment_id = ? AND status = ?", r.SegmentID, models.AnomalyOpen).
		Find(&events).Error; err != nil {
		return err
	}
	now := time.Now()
	for _, ev := range events {
		close := false
		switch ev.Kind {
		case models.AnomalyBackward, models.AnomalyJump:
			// 有补录读数替代，即关闭
			close = true
		case models.AnomalyMissing:
			// 补录点落在异常区间内（上一读数 -> 触发读数之间）即视为补齐
			if ev.ReadingID != nil {
				var trigger models.MeterReading
				if err := tx.First(&trigger, *ev.ReadingID).Error; err == nil {
					var prev models.MeterReading
					tx.Where("segment_id = ? AND status = ? AND read_at < ?", r.SegmentID, models.ReadingAccepted, trigger.ReadAt).
						Order("read_at DESC, id DESC").First(&prev)
					if (prev.ID == 0 || !r.ReadAt.Before(prev.ReadAt)) && !r.ReadAt.After(trigger.ReadAt) {
						close = true
					}
				}
			}
		}
		if close {
			if err := tx.Model(&ev).Updates(map[string]any{
				"status":      models.AnomalyResolved,
				"resolution":  "supplement",
				"resolver":    r.Reviewer,
				"resolved_at": now,
			}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// unfreezeIfClear 该会话不再有未决异常时恢复计量
func (s *Service) unfreezeIfClear(tx *gorm.DB, sessionID, segmentID uint) error {
	var n int64
	if err := tx.Model(&models.AnomalyEvent{}).
		Where("session_id = ? AND status = ?", sessionID, models.AnomalyOpen).
		Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if err := tx.Model(&models.MeterSegment{}).
		Where("id = ? AND status = ?", segmentID, models.SegmentFlagged).
		Update("status", models.SegmentAnswered).Error; err != nil {
		return err
	}
	var sess models.Session
	if err := tx.First(&sess, sessionID).Error; err != nil {
		return err
	}
	target := models.SessionActive
	if sess.EndTime != nil {
		target = models.SessionEnded
	}
	// answered 的段若已有关闭时间则置 closed
	tx.Model(&models.MeterSegment{}).
		Where("id = ? AND status = ? AND end_time IS NOT NULL", segmentID, models.SegmentAnswered).
		Update("status", models.SegmentClosed)
	tx.Model(&models.MeterSegment{}).
		Where("id = ? AND status = ? AND end_time IS NULL", segmentID, models.SegmentAnswered).
		Update("status", models.SegmentServing)
	return tx.Model(&models.Session{}).Where("id = ? AND status = ?", sessionID, models.SessionBlocked).
		Update("status", target).Error
}

// ChangeBerth 换泊位：关闭当前段（按当时费率落定），以同一电表读数开新段适用新泊位费率
func (s *Service) ChangeBerth(sessionID uint, newBerthID uint, value float64, at time.Time, operator string) ([]models.MeterSegment, error) {
	if at.IsZero() {
		at = time.Now()
	}
	var newBerth models.Berth
	if err := s.DB.First(&newBerth, newBerthID).Error; err != nil {
		return nil, fmt.Errorf("泊位不存在: %w", err)
	}
	out := []models.MeterSegment{}
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var sess models.Session
		if err := tx.First(&sess, sessionID).Error; err != nil {
			return ErrNotFound
		}
		if sess.Status == models.SessionBlocked {
			return ErrAnomalyOpen
		}
		if sess.EndTime != nil {
			return fmt.Errorf("%w: 会话已结束", ErrInvalidState)
		}
		seg, err := s.CurrentSegment(tx, sessionID)
		if err != nil {
			return err
		}
		if seg.BerthID == newBerthID {
			return errors.New("新泊位与当前泊位相同，无需换泊")
		}
		last, err := s.lastAcceptedReading(tx, seg.ID, at)
		if err != nil {
			return err
		}
		if kind, detail := classify(last, value, at); kind != "" {
			ev := models.AnomalyEvent{SessionID: sessionID, SegmentID: seg.ID, Kind: kind, Detail: detail, Status: models.AnomalyOpen}
			tx.Create(&ev)
			return s.freeze(tx, sessionID, seg.ID)
		}

		// 关闭旧段
		seg.EndReading = value
		seg.EndTime = &at
		seg.Status = models.SegmentClosed
		seg.CloseReason = "berth_change"
		seg.ServiceSeconds = s.serviceSeconds(tx, *seg, at)
		if err := tx.Save(seg).Error; err != nil {
			return err
		}
		closeReading := models.MeterReading{
			SegmentID: seg.ID, SessionID: sessionID, Source: models.ReadingManual,
			Value: value, ReadAt: at, RecordedAt: time.Now(),
			Status: models.ReadingAccepted, Operator: operator,
		}
		if err := tx.Create(&closeReading).Error; err != nil {
			return err
		}

		// 开新段：电表读数连续，费率取新泊位
		var count int64
		tx.Model(&models.MeterSegment{}).Where("session_id = ?", sessionID).Count(&count)
		newSeg := models.MeterSegment{
			SessionID: sessionID, BerthID: newBerthID, Seq: int(count) + 1,
			StartReading: value, StartTime: at, Status: models.SegmentServing,
		}
		if err := tx.Create(&newSeg).Error; err != nil {
			return err
		}
		if err := s.anchorReading(tx, &newSeg, operator, models.ReadingManual); err != nil {
			return err
		}
		out = append(out, *seg, newSeg)
		return nil
	})
	return out, err
}

// StartOutage 停电开始：计量段转入停电挂起
func (s *Service) StartOutage(sessionID uint, at time.Time, reason, operator string) (*models.PowerOutage, error) {
	if at.IsZero() {
		at = time.Now()
	}
	var out *models.PowerOutage
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		seg, err := s.CurrentSegment(tx, sessionID)
		if err != nil {
			return err
		}
		var open int64
		tx.Model(&models.PowerOutage{}).Where("segment_id = ? AND end IS NULL", seg.ID).Count(&open)
		if open > 0 {
			return errors.New("该段已有未结束的停电区间")
		}
		o := models.PowerOutage{SegmentID: seg.ID, SessionID: sessionID, Start: at, Reason: reason, Operator: operator}
		if err := tx.Create(&o).Error; err != nil {
			return err
		}
		if seg.Status == models.SegmentServing {
			if err := tx.Model(seg).Update("status", models.SegmentOutage).Error; err != nil {
				return err
			}
		}
		out = &o
		return nil
	})
	return out, err
}

// EndOutage 停电结束：恢复供电
func (s *Service) EndOutage(sessionID uint, at time.Time, operator string) (*models.PowerOutage, error) {
	if at.IsZero() {
		at = time.Now()
	}
	var out *models.PowerOutage
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		seg, err := s.CurrentSegment(tx, sessionID)
		if err != nil {
			return err
		}
		var o models.PowerOutage
		if err := tx.Where("segment_id = ? AND end IS NULL", seg.ID).Order("id DESC").First(&o).Error; err != nil {
			return errors.New("没有进行中的停电区间")
		}
		if at.Before(o.Start) {
			return errors.New("停电结束时间早于开始时间")
		}
		o.End = &at
		if err := tx.Save(&o).Error; err != nil {
			return err
		}
		if seg.Status == models.SegmentOutage {
			if err := tx.Model(seg).Update("status", models.SegmentServing).Error; err != nil {
				return err
			}
		}
		out = &o
		return nil
	})
	return out, err
}

// EndSession 结束接电，关闭最后一个计量段
func (s *Service) EndSession(sessionID uint, value float64, at time.Time, operator string) (*models.Session, error) {
	if at.IsZero() {
		at = time.Now()
	}
	var out *models.Session
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var sess models.Session
		if err := tx.First(&sess, sessionID).Error; err != nil {
			return ErrNotFound
		}
		if sess.EndTime != nil {
			return fmt.Errorf("%w: 会话已结束", ErrInvalidState)
		}
		if sess.Status == models.SessionBlocked {
			return ErrAnomalyOpen
		}
		seg, err := s.CurrentSegment(tx, sessionID)
		if err != nil {
			return err
		}
		last, err := s.lastAcceptedReading(tx, seg.ID, at)
		if err != nil {
			return err
		}
		if kind, detail := classify(last, value, at); kind != "" {
			ev := models.AnomalyEvent{SessionID: sessionID, SegmentID: seg.ID, Kind: kind, Detail: detail, Status: models.AnomalyOpen}
			tx.Create(&ev)
			return s.freeze(tx, sessionID, seg.ID)
		}
		seg.EndReading = value
		seg.EndTime = &at
		seg.Status = models.SegmentClosed
		seg.CloseReason = "session_end"
		seg.ServiceSeconds = s.serviceSeconds(tx, *seg, at)
		if err := tx.Save(seg).Error; err != nil {
			return err
		}
		cr := models.MeterReading{
			SegmentID: seg.ID, SessionID: sessionID, Source: models.ReadingManual,
			Value: value, ReadAt: at, RecordedAt: time.Now(),
			Status: models.ReadingAccepted, Operator: operator,
		}
		if err := tx.Create(&cr).Error; err != nil {
			return err
		}
		sess.EndTime = &at
		sess.Status = models.SessionEnded
		if err := tx.Save(&sess).Error; err != nil {
			return err
		}
		out = &sess
		return nil
	})
	return out, err
}

// outageSeconds 统计 [start,end] 与停电区间的交集总秒数；未关闭的停电区间按 end 截断并自动关闭
func (s *Service) outageSeconds(tx *gorm.DB, segmentID uint, start, end time.Time) int64 {
	var oos []models.PowerOutage
	tx.Where("segment_id = ?", segmentID).Find(&oos)
	var total int64
	now := time.Now()
	for _, o := range oos {
		oe := now
		if o.End != nil {
			oe = *o.End
		}
		lo, hi := o.Start, oe
		if lo.Before(start) {
			lo = start
		}
		if hi.After(end) {
			hi = end
		}
		if hi.After(lo) {
			total += int64(hi.Sub(lo).Seconds())
		}
	}
	return total
}

// serviceSeconds 段实际服务秒数 = 跨度 - 停电交集
func (s *Service) serviceSeconds(tx *gorm.DB, seg models.MeterSegment, end time.Time) int64 {
	span := int64(end.Sub(seg.StartTime).Seconds())
	outage := s.outageSeconds(tx, seg.ID, seg.StartTime, end)
	if span < outage {
		return 0
	}
	return span - outage
}
