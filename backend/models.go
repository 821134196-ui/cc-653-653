package main

import "time"

// 角色
const (
	RoleOperator = "operator" // 值班员：登记、换泊位、停电、结束接电、补录
	RoleReviewer = "reviewer" // 复核员：补录复核（须与补录人不同）
	RoleVessel   = "vessel"   // 船方：确认账单、付款、提出异议
)

// 接电单状态
const (
	ConnActive = "active" // 接电中（可能含多个计量段）
	ConnClosed = "closed" // 已结束并出账
)

// 计量段状态
const (
	SegOpen      = "open"
	SegClosed    = "closed"
	SegSuspended = "suspended" // 因异常读数挂起，暂停结算
)

// 读数状态
const (
	ReadAccepted      = "accepted"       // 采信
	ReadFlagged       = "flagged"        // 异常：倒退/跃升/漏报/时序错乱
	ReadPendingReview = "pending_review" // 补录，待第二人复核
	ReadDuplicate     = "duplicate"      // 设备重复回传，不采信
	ReadRejected      = "rejected"       // 复核驳回
	ReadHeld          = "held"           // 挂起期间收到，暂存待异常解除后重算
	ReadExcluded      = "excluded"       // 停电窗口回传，不采信且不计费
)

// 异常类型
const (
	AnomalyRegression   = "regression"    // 读数倒退
	AnomalyJump         = "jump"          // 异常跃升（功率超电表上限）
	AnomalyMissing      = "missing"       // 漏报（设备序列号跳号）
	AnomalyOutOfOrder   = "out_of_order"  // 时间倒序
	AnomalyDuringOutage = "during_outage" // 停电窗口内的回传
)

// 账单状态（账单内容一经生成不可变，状态只流转）
const (
	BillPending   = "pending_confirmation" // 待船方确认
	BillConfirmed = "confirmed"
	BillPaid      = "paid"
	BillDisputed  = "disputed" // 异议处理中，原账单冻结保留
)

// 异议处理
const (
	DisputeOpen   = "open"
	DisputeClosed = "closed"
)

// User 本地用户（三种角色，演示双人复核用）
type User struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	Code      string    `gorm:"uniqueIndex" json:"code"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// Berth 泊位及其费率
type Berth struct {
	ID              uint    `gorm:"primarykey" json:"id"`
	Code            string  `gorm:"uniqueIndex" json:"code"`
	Name            string  `json:"name"`
	RatePerKWh      float64 `json:"rate_per_kwh"`       // 电费 元/kWh
	ServiceFeePerHr float64 `json:"service_fee_per_hr"` // 服务费 元/小时
}

// Meter 虚拟电表（每泊位一只，本地模拟）
type Meter struct {
	ID         uint    `gorm:"primarykey" json:"id"`
	Code       string  `gorm:"uniqueIndex" json:"code"`
	Name       string  `json:"name"`
	BerthCode  string  `json:"berth_code"`
	MaxPowerKW float64 `json:"max_power_kw"` // 合理最大功率，超过判异常跃升
}

// Connection 一次接电（一艘船一次停靠，跨多个计量段）
type Connection struct {
	ID           uint       `gorm:"primarykey" json:"id"`
	Code         string     `gorm:"uniqueIndex" json:"code"`
	VesselName   string     `json:"vessel_name"`
	StartMeterID string     `json:"start_meter_id"`
	StartTime    time.Time  `json:"start_time"`
	EndTime      *time.Time `json:"end_time"`
	Status       string     `json:"status"`    // active | closed
	ReportBy     string     `json:"report_by"` // 登记值班员
	// 结算挂起：存在未处理异常时禁止出账
	BillingHold bool      `gorm:"default:false" json:"billing_hold"`
	HoldReasons string    `json:"hold_reasons"` // 逗号分隔的未决异常
	BillID      *uint     `json:"bill_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// MeterSegment 计量段：同一泊位连续供电的区间；换泊位/停电都另起新段
type MeterSegment struct {
	ID           uint   `gorm:"primarykey" json:"id"`
	ConnectionID uint   `gorm:"uniqueIndex:idx_seg_conn_seq" json:"connection_id"`
	Seq          int    `gorm:"uniqueIndex:idx_seg_conn_seq" json:"seq"`
	MeterCode    string `json:"meter_code"`
	BerthCode    string `json:"berth_code"`
	// 起始终端读数（换泊位/停电恢复时为新段起点）
	StartTime      time.Time  `json:"start_time"`
	StartReading   float64    `json:"start_reading"`
	StartReadingID *uint      `json:"start_reading_id"`
	EndTime        *time.Time `json:"end_time"`
	EndReading     *float64   `json:"end_reading"`
	EndReadingID   *uint      `json:"end_reading_id"`
	// 段产生原因：接电 / 换泊位 / 停电恢复
	Reason string `gorm:"default:'connect'" json:"reason"`
	Status string `json:"status"` // open | closed | suspended
	Note   string `json:"note"`
	// 关段时按泊位锁定的费率快照，换泊位后的新段自然适用新费率
	RatePerKWh      float64 `json:"rate_per_kwh"`
	ServiceFeePerHr float64 `json:"service_fee_per_hr"`
}

// MeterReading 原始读数（证据，永不物理删除或就地改写）
type MeterReading struct {
	ID           uint      `gorm:"primarykey" json:"id"`
	ConnectionID *uint     `gorm:"index" json:"connection_id"`
	SegmentID    *uint     `gorm:"index" json:"segment_id"`
	MeterCode    string    `gorm:"index;uniqueIndex:idx_meter_seq,priority:1" json:"meter_code"`
	Reading      float64   `json:"reading"`
	ReadAt       time.Time `gorm:"read_at" json:"read_at"`
	Source       string    `json:"source"` // device | manual
	// 设备单调序号：(meter_code, device_seq) 唯一，用于识别重复回传与漏报
	DeviceSeq *int   `gorm:"uniqueIndex:idx_meter_seq,priority:2" json:"device_seq"`
	Status    string `json:"status"`
	Anomaly   string `json:"anomaly"` // flagged 时的异常类型
	// 异常处置（记录保留，只标记解决，不就地抹除）
	ResolvedAt *time.Time `json:"resolved_at"`
	Resolution string     `json:"resolution"`
	// 重复回传指向被采信的原始记录
	DupOfID *uint `json:"dup_of_id"`
	// 同一设备序列号被重复回传的次数（首次正常入库，后续只累加计数）
	DupCount int `gorm:"default:0" json:"dup_count"`
	// 上报/补录人与复核人（双人复核）
	ReportedBy string     `json:"reported_by"`
	ReviewedBy string     `json:"reviewed_by"`
	ReviewedAt *time.Time `json:"reviewed_at"`
	ReviewNote string     `json:"review_note"`
	CreatedAt  time.Time  `json:"created_at"`
}

// OutageRecord 停电登记：停电窗口不属于任何计量段，不计服务时长
type OutageRecord struct {
	ID            uint       `gorm:"primarykey" json:"id"`
	ConnectionID  uint       `gorm:"index" json:"connection_id"`
	SegmentBefore uint       `json:"segment_before"` // 停电前结束的段
	SegmentAfter  *uint      `json:"segment_after"`  // 恢复后新建的段
	StartTime     time.Time  `json:"start_time"`
	EndTime       *time.Time `json:"end_time"`
	Reason        string     `json:"reason"`
	ReportBy      string     `json:"report_by"`
}

// Bill 账单（内容快照不可变；异议不就地改账，走调整单）
type Bill struct {
	ID           uint       `gorm:"primarykey" json:"id"`
	Code         string     `gorm:"uniqueIndex" json:"code"`
	ConnectionID uint       `gorm:"uniqueIndex" json:"connection_id"`
	VesselName   string     `json:"vessel_name"`
	TotalKWh     float64    `json:"total_kwh"`
	TotalHours   float64    `json:"total_hours"`
	EnergyFee    float64    `json:"energy_fee"`
	ServiceFee   float64    `json:"service_fee"`
	TotalAmount  float64    `json:"total_amount"`
	Status       string     `json:"status"`
	SnapshotJSON string     `gorm:"type:text" json:"-"` // 出账时原始读数/段快照
	ConfirmedBy  string     `json:"confirmed_by"`
	ConfirmedAt  *time.Time `json:"confirmed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	Items        []BillItem `gorm:"foreignKey:BillID" json:"items"`
}

// BillItem 逐段账项
type BillItem struct {
	ID              uint      `gorm:"primarykey" json:"id"`
	BillID          uint      `gorm:"index" json:"bill_id"`
	SegmentID       uint      `json:"segment_id"`
	Seq             int       `json:"seq"`
	BerthCode       string    `json:"berth_code"`
	MeterCode       string    `json:"meter_code"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	StartReading    float64   `json:"start_reading"`
	EndReading      float64   `json:"end_reading"`
	KWh             float64   `json:"kwh"`
	Hours           float64   `json:"hours"`
	RatePerKWh      float64   `json:"rate_per_kwh"`
	ServiceFeePerHr float64   `json:"service_fee_per_hr"`
	EnergyFee       float64   `json:"energy_fee"`
	ServiceFee      float64   `json:"service_fee"`
	Amount          float64   `json:"amount"`
	Reason          string    `json:"reason"`
	// 该段采信的原始读数 id（供“查看原始读数”）
	ReadingIDs string `json:"reading_ids"`
}

// PaymentReceipt 本地模拟付款回执
type PaymentReceipt struct {
	ID     uint      `gorm:"primarykey" json:"id"`
	Code   string    `gorm:"uniqueIndex" json:"code"`
	BillID uint      `gorm:"uniqueIndex" json:"bill_id"`
	Amount float64   `json:"amount"`
	Method string    `json:"method"` // mock
	PaidAt time.Time `json:"paid_at"`
	PaidBy string    `json:"paid_by"`
}

// Dispute 船方异议：原账单、证据、处理结论全部保留
type Dispute struct {
	ID           uint       `gorm:"primarykey" json:"id"`
	BillID       uint       `gorm:"index" json:"bill_id"`
	Reason       string     `gorm:"reason"`
	Evidence     string     `gorm:"evidence"` // 证据描述（读数 id/快照等）
	RaisedBy     string     `gorm:"raised_by"`
	RaisedAt     time.Time  `json:"raised_at"`
	Status       string     `json:"status"`     // open | closed
	Conclusion   string     `json:"conclusion"` // 处理结论
	ResolvedBy   string     `json:"resolved_by"`
	ResolvedAt   *time.Time `json:"resolved_at"`
	CreditNoteID *uint      `json:"credit_note_id"`
}

// CreditNote 调整单：异议成立时冲账/补收，原始账单不动
type CreditNote struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	Code      string    `gorm:"uniqueIndex" json:"code"`
	BillID    uint      `gorm:"index" json:"bill_id"`
	DisputeID uint      `gorm:"index" json:"dispute_id"`
	Kind      string    `gorm:"kind" json:"kind"` // refund 退款冲减 | additional 补收
	Amount    float64   `json:"amount"`
	Reason    string    `json:"reason"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}
