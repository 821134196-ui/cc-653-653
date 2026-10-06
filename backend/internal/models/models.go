package models

import "time"

// 状态常量
const (
	// Session
	SessionActive   = "active"   // 接电中
	SessionEnded    = "ended"    // 已结束
	SessionBlocked  = "blocked"  // 异常冻结，暂停结算
	SessionBilled   = "billed"   // 已出账
	SessionDisputed = "disputed" // 有异议待处理

	// Segment
	SegmentServing  = "serving"  // 正常供电服务中
	SegmentOutage   = "outage"   // 停电中（区间挂起，不计服务时长）
	SegmentClosed   = "closed"   // 已随换泊位/结束而关闭
	SegmentFlagged  = "flagged"  // 读数异常挂起
	SegmentAnswered = "answered" // 异常已处理，恢复有效计量

	// Reading
	ReadingManual     = "manual"     // 值班员手工抄录
	ReadingDevice     = "device"     // 电表设备自动回传
	ReadingPending    = "pending"    // 待复核
	ReadingAccepted   = "accepted"   // 已复核采纳
	ReadingRejected   = "rejected"   // 复核驳回（不参与计量）
	ReadingDuplicate  = "duplicate"  // 设备重复回传，幂等丢弃
	ReadingAnomalous  = "anomalous"  // 异常（倒退/跃升），触发冻结
	ReadingSupplement = "supplement" // 漏报补录（须另一人复核）

	// Anomaly
	AnomalyOpen     = "open"
	AnomalyResolved = "resolved"

	AnomalyBackward = "backward" // 读数倒退
	AnomalyJump     = "jump"     // 异常跃升
	AnomalyMissing  = "missing"  // 漏报（长时间无读数）

	// Bill
	BillPending   = "pending"   // 待船方确认
	BillConfirmed = "confirmed" // 船方已确认
	BillDisputed  = "disputed"  // 船方提出异议
	BillPaid      = "paid"      // 已付款
	BillVoid      = "void"      // 被更正账单取代（原账单保留，仅置失效）

	// Dispute
	DisputeOpen     = "open"
	DisputeResolved = "resolved"

	// Payment
	PaymentPaid = "paid"
)

// Berth 泊位：每个泊位有自己的电价费率与服务费
type Berth struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	Name          string    `gorm:"uniqueIndex;not null" json:"name"`
	RatePerKWh    float64   `gorm:"not null" json:"rate_per_kwh"`    // 电费 元/kWh
	ServicePerMin float64   `gorm:"not null" json:"service_per_min"` // 服务费 元/分钟
	CreatedAt     time.Time `json:"created_at"`
}

// Vessel 船舶
type Vessel struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	Name      string    `gorm:"uniqueIndex;not null" json:"name"`
	Agent     string    `json:"agent"` // 船方联系人
	CreatedAt time.Time `json:"created_at"`
}

// Session 一次接电业务：船名、起始泊位、值班员
type Session struct {
	ID        uint       `gorm:"primarykey" json:"id"`
	VesselID  uint       `gorm:"index;not null" json:"vessel_id"`
	Vessel    Vessel     `gorm:"foreignKey:VesselID" json:"vessel"`
	BerthID   uint       `gorm:"not null" json:"berth_id"` // 起始泊位（仅登记用）
	Berth     Berth      `gorm:"foreignKey:BerthID" json:"berth"`
	Operator  string     `gorm:"not null" json:"operator"` // 登记值班员
	StartTime time.Time  `gorm:"not null" json:"start_time"`
	EndTime   *time.Time `json:"end_time,omitempty"`
	Status    string     `gorm:"index;not null;default:active" json:"status"`
	Note      string     `json:"note"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// MeterSegment 计量段。换泊位即关闭当前段、另开新段；新段适用新泊位费率。
// 停电时段以 Start/End 标记并不计入 ServiceDuration。
type MeterSegment struct {
	ID           uint       `gorm:"primarykey" json:"id"`
	SessionID    uint       `gorm:"index;not null" json:"session_id"`
	BerthID      uint       `gorm:"not null" json:"berth_id"`
	Berth        Berth      `gorm:"foreignKey:BerthID" json:"berth"`
	Seq          int        `gorm:"not null" json:"seq"` // 段序号，从1开始
	StartReading float64    `gorm:"not null" json:"start_reading"`
	EndReading   float64    `json:"end_reading"` // 关闭时落定
	StartTime    time.Time  `gorm:"not null" json:"start_time"`
	EndTime      *time.Time `json:"end_time,omitempty"`
	Status       string     `gorm:"index;not null;default:serving" json:"status"`
	CloseReason  string     `json:"close_reason"` // berth_change / session_end / outage_split
	// 实际服务时长（秒），出账时落定；停电时间已扣除
	ServiceSeconds int64     `gorm:"default:0" json:"service_seconds"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// MeterReading 每一条抄表读数（手工/设备/补录）。账单可回溯到原始读数。
type MeterReading struct {
	ID         uint       `gorm:"primarykey" json:"id"`
	SegmentID  uint       `gorm:"index;not null" json:"segment_id"`
	SessionID  uint       `gorm:"index;not null" json:"session_id"`
	Source     string     `gorm:"index;not null" json:"source"` // manual/device/supplement
	Value      float64    `gorm:"not null" json:"value"`
	ReadAt     time.Time  `gorm:"index;not null" json:"read_at"`
	RecordedAt time.Time  `gorm:"not null" json:"recorded_at"`
	DeviceID   string     `gorm:"index" json:"device_id"`
	MessageUID string     `json:"message_uid"` // 设备报文唯一ID（部分唯一索引，见 store.Migrate），用于去重
	Status     string     `gorm:"index;not null" json:"status"`
	Operator   string     `json:"operator"` // 录入人
	Reviewer   string     `json:"reviewer"` // 复核人（必须与录入人不同）
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
	ReviewNote string     `json:"review_note"`
	AnomalyID  *uint      `json:"anomaly_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// PowerOutage 停电区间：区间内不算连续服务
type PowerOutage struct {
	ID        uint       `gorm:"primarykey" json:"id"`
	SegmentID uint       `gorm:"index;not null" json:"segment_id"`
	SessionID uint       `gorm:"index;not null" json:"session_id"`
	Start     time.Time  `gorm:"not null" json:"start"`
	End       *time.Time `json:"end,omitempty"`
	Reason    string     `json:"reason"`
	Operator  string     `gorm:"not null" json:"operator"`
	CreatedAt time.Time  `json:"created_at"`
}

// AnomalyEvent 异常事件：倒退 / 跃升 / 漏报
type AnomalyEvent struct {
	ID         uint       `gorm:"primarykey" json:"id"`
	SessionID  uint       `gorm:"index;not null" json:"session_id"`
	SegmentID  uint       `gorm:"index;not null" json:"segment_id"`
	ReadingID  *uint      `json:"reading_id,omitempty"`
	Kind       string     `gorm:"index;not null" json:"kind"`
	Detail     string     `gorm:"not null" json:"detail"`
	Status     string     `gorm:"index;not null;default:open" json:"status"`
	Resolution string     `json:"resolution"` // reject / supplement / accept
	Resolver   string     `json:"resolver"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Bill 账单。确认前逐段预演，确认后快照不可变；异议时原账单保留。
type Bill struct {
	ID             uint       `gorm:"primarykey" json:"id"`
	SessionID      uint       `gorm:"index;not null" json:"session_id"`
	BillNo         string     `gorm:"uniqueIndex;not null" json:"bill_no"`
	VesselName     string     `gorm:"not null" json:"vessel_name"`
	Operator       string     `gorm:"not null" json:"operator"`
	Status         string     `gorm:"index;not null;default:pending" json:"status"`
	TotalKWh       float64    `gorm:"not null" json:"total_kwh"`
	EnergyAmount   float64    `gorm:"not null" json:"energy_amount"`
	ServiceAmount  float64    `gorm:"not null" json:"service_amount"`
	TotalAmount    float64    `gorm:"not null" json:"total_amount"`
	ConfirmedAt    *time.Time `json:"confirmed_at,omitempty"`
	SupersededByID *uint      `json:"superseded_by_id,omitempty"` // 更正账单
	SnapshotJSON   string     `gorm:"type:text" json:"-"`         // 出账时完整快照（原始读数证据）
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// BillItem 账单逐段明细：用电量、费率、服务时长
type BillItem struct {
	ID             uint      `gorm:"primarykey" json:"id"`
	BillID         uint      `gorm:"index;not null" json:"bill_id"`
	SegmentID      uint      `gorm:"not null" json:"segment_id"`
	Seq            int       `gorm:"not null" json:"seq"`
	BerthName      string    `gorm:"not null" json:"berth_name"`
	StartTime      time.Time `gorm:"not null" json:"start_time"`
	EndTime        time.Time `gorm:"not null" json:"end_time"`
	StartReading   float64   `gorm:"not null" json:"start_reading"`
	EndReading     float64   `gorm:"not null" json:"end_reading"`
	KWh            float64   `gorm:"not null" json:"kwh"`
	RatePerKWh     float64   `gorm:"not null" json:"rate_per_kwh"`
	ServiceSeconds int64     `gorm:"not null" json:"service_seconds"`
	ServicePerMin  float64   `gorm:"not null" json:"service_per_min"`
	OutageSeconds  int64     `gorm:"not null" json:"outage_seconds"`
	EnergyAmount   float64   `gorm:"not null" json:"energy_amount"`
	ServiceAmount  float64   `gorm:"not null" json:"service_amount"`
	Amount         float64   `gorm:"not null" json:"amount"`
}

// Dispute 船方异议：保留原账单、证据与处理结论
type Dispute struct {
	ID         uint       `gorm:"primarykey" json:"id"`
	BillID     uint       `gorm:"index;not null" json:"bill_id"`
	SessionID  uint       `gorm:"index;not null" json:"session_id"`
	Reason     string     `gorm:"not null" json:"reason"`
	Evidence   string     `gorm:"type:text" json:"evidence"` // 船方提交的证据说明
	Status     string     `gorm:"index;not null;default:open" json:"status"`
	Conclusion string     `gorm:"type:text" json:"conclusion"` // 港口处理结论
	Handler    string     `json:"handler"`
	NewBillID  *uint      `json:"new_bill_id,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// PaymentReceipt 本地模拟付款回执
type PaymentReceipt struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	BillID    uint      `gorm:"uniqueIndex;not null" json:"bill_id"`
	ReceiptNo string    `gorm:"uniqueIndex;not null" json:"receipt_no"`
	Amount    float64   `gorm:"not null" json:"amount"`
	Channel   string    `gorm:"not null" json:"channel"` // 模拟支付渠道
	PaidAt    time.Time `gorm:"not null" json:"paid_at"`
	CreatedAt time.Time `json:"created_at"`
}
