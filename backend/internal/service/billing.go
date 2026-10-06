package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"shorepower/internal/models"
)

// ComputedItem 逐段计算结果
type ComputedItem struct {
	Segment        models.MeterSegment `json:"segment"`
	BerthName      string              `json:"berth_name"`
	RatePerKWh     float64             `json:"rate_per_kwh"`
	ServicePerMin  float64             `json:"service_per_min"`
	StartReading   float64             `json:"start_reading"`
	EndReading     float64             `json:"end_reading"`
	StartTime      time.Time           `json:"start_time"`
	EndTime        time.Time           `json:"end_time"`
	KWh            float64             `json:"kwh"`
	ServiceSeconds int64               `json:"service_seconds"`
	OutageSeconds  int64               `json:"outage_seconds"`
	EnergyAmount   float64             `json:"energy_amount"`
	ServiceAmount  float64             `json:"service_amount"`
	Amount         float64             `json:"amount"`
	Provisional    bool                `json:"provisional"` // 段尚未关闭，预估值
}

// BillPreview 账单预览/明细
type BillPreview struct {
	SessionID     uint           `json:"session_id"`
	VesselName    string         `json:"vessel_name"`
	Items         []ComputedItem `json:"items"`
	TotalKWh      float64        `json:"total_kwh"`
	EnergyAmount  float64        `json:"energy_amount"`
	ServiceAmount float64        `json:"service_amount"`
	TotalAmount   float64        `json:"total_amount"`
}

// Snapshot 出账时留存的不可变快照（含原始读数证据）
type Snapshot struct {
	Bill        BillPreview           `json:"bill"`
	Readings    []models.MeterReading `json:"readings"`
	Outages     []models.PowerOutage  `json:"outages"`
	Anomalies   []models.AnomalyEvent `json:"anomalies"`
	GeneratedAt time.Time             `json:"generated_at"`
}

// ComputeItems 汇总会话全部计量段。allowOpen=true 时未关闭段按最新有效读数预估。
func (s *Service) ComputeItems(tx *gorm.DB, sessionID uint, allowOpen bool) ([]ComputedItem, error) {
	var n int64
	if err := tx.Model(&models.AnomalyEvent{}).
		Where("session_id = ? AND status = ?", sessionID, models.AnomalyOpen).Count(&n).Error; err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrAnomalyOpen
	}
	var segs []models.MeterSegment
	if err := tx.Where("session_id = ?", sessionID).Order("seq ASC").Find(&segs).Error; err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return nil, errors.New("该会话没有计量段")
	}
	items := make([]ComputedItem, 0, len(segs))
	for _, seg := range segs {
		var berth models.Berth
		if err := tx.First(&berth, seg.BerthID).Error; err != nil {
			return nil, err
		}
		end := seg.StartTime
		endReading := seg.StartReading
		provisional := false
		serviceSec := seg.ServiceSeconds

		if seg.EndTime != nil {
			end = *seg.EndTime
			endReading = seg.EndReading
			// 始终按当前停电记录重算，保证补登停电后异议更正能反映真实服务时长
			serviceSec = s.serviceSeconds(tx, seg, end)
		} else {
			if !allowOpen {
				return nil, fmt.Errorf("%w: 第 %d 计量段尚未关闭（请先结束接电或换泊位）", ErrInvalidState, seg.Seq)
			}
			last, err := s.lastAcceptedReading(tx, seg.ID, time.Now())
			if err != nil {
				return nil, err
			}
			provisional = true
			if last != nil {
				endReading = last.Value
				end = last.ReadAt
			}
			serviceSec = s.serviceSeconds(tx, seg, end)
		}

		kwh := endReading - seg.StartReading
		if kwh < 0 {
			kwh = 0
		}
		outageSec := s.outageSeconds(tx, seg.ID, seg.StartTime, end)
		energy := round2(kwh * berth.RatePerKWh)
		service := round2(float64(serviceSec) / 60.0 * berth.ServicePerMin)
		items = append(items, ComputedItem{
			Segment:        seg,
			BerthName:      berth.Name,
			RatePerKWh:     berth.RatePerKWh,
			ServicePerMin:  berth.ServicePerMin,
			StartReading:   seg.StartReading,
			EndReading:     endReading,
			StartTime:      seg.StartTime,
			EndTime:        end,
			KWh:            round2(kwh),
			ServiceSeconds: serviceSec,
			OutageSeconds:  outageSec,
			EnergyAmount:   energy,
			ServiceAmount:  service,
			Amount:         round2(energy + service),
			Provisional:    provisional,
		})
	}
	return items, nil
}

func (s *Service) vesselName(tx *gorm.DB, sessionID uint) string {
	var sess models.Session
	tx.Preload("Vessel").First(&sess, sessionID)
	return sess.Vessel.Name
}

// PreviewBill 预览账单（不落库）
func (s *Service) PreviewBill(sessionID uint, allowOpen bool) (*BillPreview, error) {
	p := &BillPreview{SessionID: sessionID, Items: []ComputedItem{}}
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		items, err := s.ComputeItems(tx, sessionID, allowOpen)
		if err != nil {
			return err
		}
		p.VesselName = s.vesselName(tx, sessionID)
		p.Items = items
		for _, it := range items {
			p.TotalKWh += it.KWh
			p.EnergyAmount += it.EnergyAmount
			p.ServiceAmount += it.ServiceAmount
			p.TotalAmount += it.Amount
		}
		p.TotalKWh = round2(p.TotalKWh)
		p.EnergyAmount = round2(p.EnergyAmount)
		p.ServiceAmount = round2(p.ServiceAmount)
		p.TotalAmount = round2(p.TotalAmount)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ConfirmBill 结束后经船方确认生成账单，逐段落库并保存含原始读数的快照
func (s *Service) ConfirmBill(sessionID uint) (*models.Bill, error) {
	var out *models.Bill
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var sess models.Session
		if err := tx.First(&sess, sessionID).Error; err != nil {
			return ErrNotFound
		}
		if sess.Status == models.SessionBlocked {
			return ErrAnomalyOpen
		}
		if sess.EndTime == nil {
			return fmt.Errorf("%w: 接电尚未结束，不能生成正式账单", ErrInvalidState)
		}
		items, err := s.ComputeItems(tx, sessionID, false)
		if err != nil {
			return err
		}
		preview := BillPreview{SessionID: sessionID, VesselName: s.vesselName(tx, sessionID), Items: items}
		for _, it := range items {
			preview.TotalKWh += it.KWh
			preview.EnergyAmount += it.EnergyAmount
			preview.ServiceAmount += it.ServiceAmount
			preview.TotalAmount += it.Amount
		}
		preview.TotalKWh = round2(preview.TotalKWh)
		preview.EnergyAmount = round2(preview.EnergyAmount)
		preview.ServiceAmount = round2(preview.ServiceAmount)
		preview.TotalAmount = round2(preview.TotalAmount)

		var readings []models.MeterReading
		tx.Where("session_id = ?", sessionID).Order("segment_id ASC, read_at ASC, id ASC").Find(&readings)
		var outages []models.PowerOutage
		tx.Where("session_id = ?", sessionID).Order("start ASC").Find(&outages)
		var anomalies []models.AnomalyEvent
		tx.Where("session_id = ?", sessionID).Order("id ASC").Find(&anomalies)
		snap := Snapshot{Bill: preview, Readings: readings, Outages: outages, Anomalies: anomalies, GeneratedAt: time.Now()}
		raw, err := json.Marshal(snap)
		if err != nil {
			return err
		}

		bill := models.Bill{
			SessionID:     sessionID,
			BillNo:        fmt.Sprintf("BILL%s-%d", time.Now().Format("20060102-150405"), sessionID),
			VesselName:    preview.VesselName,
			Operator:      sess.Operator,
			Status:        models.BillPending,
			TotalKWh:      preview.TotalKWh,
			EnergyAmount:  preview.EnergyAmount,
			ServiceAmount: preview.ServiceAmount,
			TotalAmount:   preview.TotalAmount,
			SnapshotJSON:  string(raw),
		}
		if err := tx.Create(&bill).Error; err != nil {
			return err
		}
		for _, it := range items {
			bi := models.BillItem{
				BillID:         bill.ID,
				SegmentID:      it.Segment.ID,
				Seq:            it.Segment.Seq,
				BerthName:      it.BerthName,
				StartTime:      it.StartTime,
				EndTime:        it.EndTime,
				StartReading:   it.StartReading,
				EndReading:     it.EndReading,
				KWh:            it.KWh,
				RatePerKWh:     it.RatePerKWh,
				ServiceSeconds: it.ServiceSeconds,
				ServicePerMin:  it.ServicePerMin,
				OutageSeconds:  it.OutageSeconds,
				EnergyAmount:   it.EnergyAmount,
				ServiceAmount:  it.ServiceAmount,
				Amount:         it.Amount,
			}
			if err := tx.Create(&bi).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&sess).Update("status", models.SessionBilled).Error; err != nil {
			return err
		}
		out = &bill
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetBill(out.ID)
}

// ShipConfirm 船方确认账单
func (s *Service) ShipConfirm(billID uint) (*models.Bill, error) {
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var b models.Bill
		if err := tx.First(&b, billID).Error; err != nil {
			return ErrNotFound
		}
		if b.Status != models.BillPending {
			return fmt.Errorf("%w: 仅待确认账单可由船方确认（当前 %s）", ErrInvalidState, b.Status)
		}
		now := time.Now()
		b.Status = models.BillConfirmed
		b.ConfirmedAt = &now
		return tx.Save(&b).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetBill(billID)
}

// GetBill 返回账单及逐段明细
func (s *Service) GetBill(billID uint) (*models.Bill, error) {
	var b models.Bill
	if err := s.DB.First(&b, billID).Error; err != nil {
		return nil, ErrNotFound
	}
	return &b, nil
}

func (s *Service) BillItems(billID uint) ([]models.BillItem, error) {
	var items []models.BillItem
	if err := s.DB.Where("bill_id = ?", billID).Order("seq ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// BillSnapshot 读取出账时留存的原始读数/停电/异常证据快照
func (s *Service) BillSnapshot(billID uint) (*Snapshot, error) {
	var b models.Bill
	if err := s.DB.First(&b, billID).Error; err != nil {
		return nil, ErrNotFound
	}
	var snap Snapshot
	if b.SnapshotJSON == "" {
		return nil, errors.New("该账单无快照")
	}
	if err := json.Unmarshal([]byte(b.SnapshotJSON), &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// RaiseDispute 船方提出异议：原账单及其证据原样保留
func (s *Service) RaiseDispute(billID uint, reason, evidence string) (*models.Dispute, error) {
	var out *models.Dispute
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var b models.Bill
		if err := tx.First(&b, billID).Error; err != nil {
			return ErrNotFound
		}
		if b.Status != models.BillConfirmed && b.Status != models.BillPaid {
			return fmt.Errorf("%w: 仅已确认账单可提出异议（当前 %s）", ErrInvalidState, b.Status)
		}
		var open int64
		tx.Model(&models.Dispute{}).Where("bill_id = ? AND status = ?", billID, models.DisputeOpen).Count(&open)
		if open > 0 {
			return errors.New("该账单已有未处理异议")
		}
		d := models.Dispute{BillID: billID, SessionID: b.SessionID, Reason: reason, Evidence: evidence, Status: models.DisputeOpen}
		if err := tx.Create(&d).Error; err != nil {
			return err
		}
		if err := tx.Model(&b).Update("status", models.BillDisputed).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Session{}).Where("id = ?", b.SessionID).
			Update("status", models.SessionDisputed).Error; err != nil {
			return err
		}
		out = &d
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveDispute 处理异议。
// action="uphold"：维持原账单，恢复为已确认；
// action="correct"：依据当前计量数据生成一张全新的更正账单，原账单置 void 但记录原样保留。
func (s *Service) ResolveDispute(disputeID uint, handler, conclusion, action string) (*models.Dispute, error) {
	var out *models.Dispute
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var d models.Dispute
		if err := tx.First(&d, disputeID).Error; err != nil {
			return ErrNotFound
		}
		if d.Status != models.DisputeOpen {
			return fmt.Errorf("%w: 异议已处理", ErrInvalidState)
		}
		var b models.Bill
		if err := tx.First(&b, d.BillID).Error; err != nil {
			return err
		}
		now := time.Now()
		d.Handler = handler
		d.Conclusion = conclusion
		d.Status = models.DisputeResolved
		d.ResolvedAt = &now

		switch action {
		case "uphold":
			b.Status = models.BillConfirmed
			if err := tx.Save(&b).Error; err != nil {
				return err
			}
		case "correct":
			// 重新逐段计算（任何修正都应先在计量/读数层完成，原账单绝不在原地修改）
			items, err := s.ComputeItems(tx, d.SessionID, false)
			if err != nil {
				return err
			}
			preview := BillPreview{SessionID: d.SessionID, VesselName: b.VesselName, Items: items}
			for _, it := range items {
				preview.TotalKWh += it.KWh
				preview.EnergyAmount += it.EnergyAmount
				preview.ServiceAmount += it.ServiceAmount
				preview.TotalAmount += it.Amount
			}
			preview.TotalKWh = round2(preview.TotalKWh)
			preview.EnergyAmount = round2(preview.EnergyAmount)
			preview.ServiceAmount = round2(preview.ServiceAmount)
			preview.TotalAmount = round2(preview.TotalAmount)

			var readings []models.MeterReading
			tx.Where("session_id = ?", d.SessionID).Order("segment_id ASC, read_at ASC, id ASC").Find(&readings)
			var outages []models.PowerOutage
			tx.Where("session_id = ?", d.SessionID).Order("start ASC").Find(&outages)
			var anomalies []models.AnomalyEvent
			tx.Where("session_id = ?", d.SessionID).Order("id ASC").Find(&anomalies)
			raw, _ := json.Marshal(Snapshot{Bill: preview, Readings: readings, Outages: outages, Anomalies: anomalies, GeneratedAt: now})

			nb := models.Bill{
				SessionID:     d.SessionID,
				BillNo:        fmt.Sprintf("BILL%s-R%d", now.Format("20060102-150405"), d.ID),
				VesselName:    b.VesselName,
				Operator:      b.Operator,
				Status:        models.BillConfirmed,
				ConfirmedAt:   &now,
				TotalKWh:      preview.TotalKWh,
				EnergyAmount:  preview.EnergyAmount,
				ServiceAmount: preview.ServiceAmount,
				TotalAmount:   preview.TotalAmount,
				SnapshotJSON:  string(raw),
			}
			if err := tx.Create(&nb).Error; err != nil {
				return err
			}
			for _, it := range items {
				bi := models.BillItem{
					BillID: nb.ID, SegmentID: it.Segment.ID, Seq: it.Segment.Seq,
					BerthName: it.BerthName, StartTime: it.StartTime, EndTime: it.EndTime,
					StartReading: it.StartReading, EndReading: it.EndReading, KWh: it.KWh,
					RatePerKWh: it.RatePerKWh, ServiceSeconds: it.ServiceSeconds, ServicePerMin: it.ServicePerMin,
					OutageSeconds: it.OutageSeconds, EnergyAmount: it.EnergyAmount, ServiceAmount: it.ServiceAmount, Amount: it.Amount,
				}
				if err := tx.Create(&bi).Error; err != nil {
					return err
				}
			}
			// 原账单保留，仅标记失效并指向更正账单
			b.Status = models.BillVoid
			b.SupersededByID = &nb.ID
			if err := tx.Save(&b).Error; err != nil {
				return err
			}
			d.NewBillID = &nb.ID
		default:
			return errors.New("action 必须是 uphold 或 correct")
		}
		if err := tx.Save(&d).Error; err != nil {
			return err
		}
		out = &d
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SimulatePay 本地模拟付款渠道回执
func (s *Service) SimulatePay(billID uint, channel string) (*models.PaymentReceipt, error) {
	if channel == "" {
		channel = "模拟支付网关"
	}
	var out *models.PaymentReceipt
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var b models.Bill
		if err := tx.First(&b, billID).Error; err != nil {
			return ErrNotFound
		}
		if b.Status != models.BillConfirmed {
			return fmt.Errorf("%w: 仅已确认账单可支付（当前 %s）", ErrInvalidState, b.Status)
		}
		var existed models.PaymentReceipt
		if err := tx.Where("bill_id = ?", billID).First(&existed).Error; err == nil {
			return errors.New("该账单已有付款回执")
		}
		now := time.Now()
		rcpt := models.PaymentReceipt{
			BillID:    billID,
			ReceiptNo: fmt.Sprintf("PAY%s-%d", now.Format("20060102-150405"), billID),
			Amount:    b.TotalAmount,
			Channel:   channel,
			PaidAt:    now,
		}
		if err := tx.Create(&rcpt).Error; err != nil {
			return err
		}
		if err := tx.Model(&b).Update("status", models.BillPaid).Error; err != nil {
			return err
		}
		out = &rcpt
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
