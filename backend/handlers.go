package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Server struct {
	DB *gorm.DB
	// 模拟器内存状态：每只表的下一个设备序号、当前读数、上次时间
	sim map[string]*simState
}

type simState struct {
	mu       sync.Mutex
	NextSeq  int
	Value    float64
	LastAt   time.Time
	AutoOn   bool
	Interval time.Duration
	StepKWh  float64 // 每个 tick 增量
}

func bind(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return false
	}
	return true
}

func errJSON(c *gin.Context, err error) {
	if ae, ok := err.(*apiError); ok {
		c.JSON(ae.Code, gin.H{"error": ae.Msg})
		return
	}
	c.JSON(500, gin.H{"error": err.Error()})
}

func uid(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "id 不是数字"})
		return 0, false
	}
	return uint(id), true
}

func (s *Server) meta(c *gin.Context) {
	var users []User
	var berths []Berth
	var meters []Meter
	s.DB.Order("id").Find(&users)
	s.DB.Order("id").Find(&berths)
	s.DB.Order("id").Find(&meters)
	c.JSON(200, gin.H{"users": users, "berths": berths, "meters": meters})
}

// ---------- 接电单 ----------

func (s *Server) createConn(c *gin.Context) {
	var in struct {
		VesselName string  `json:"vessel_name"`
		MeterCode  string  `json:"meter_code"`
		Reading    float64 `json:"reading"`
		StartTime  string  `json:"start_time"`
		ReportBy   string  `json:"report_by"`
	}
	if !bind(c, &in) {
		return
	}
	t := time.Now()
	if in.StartTime != "" {
		if pt, err := time.ParseInLocation(time.RFC3339, in.StartTime, time.Local); err == nil {
			t = pt
		}
	}
	conn, err := createConnection(s.DB, createConnInput{
		VesselName: in.VesselName, MeterCode: in.MeterCode, Reading: in.Reading,
		StartTime: t, ReportBy: in.ReportBy,
	})
	if err != nil {
		errJSON(c, err)
		return
	}
	// 登记起始读数后同步模拟器表码，避免设备序列从旧值起报
	s.syncSim(conn.StartMeterID, in.Reading, t)
	c.JSON(201, conn)
}

func (s *Server) listConns(c *gin.Context) {
	var conns []Connection
	s.DB.Order("id desc").Find(&conns)
	c.JSON(200, conns)
}

func (s *Server) connDetail(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var conn Connection
	if err := s.DB.First(&conn, id).Error; err != nil {
		errJSON(c, fail(404, "接电单不存在"))
		return
	}
	var segs []MeterSegment
	s.DB.Where("connection_id = ?", id).Order("seq").Find(&segs)
	var readings []MeterReading
	s.DB.Where("connection_id = ?", id).Order("read_at, id").Find(&readings)
	var outages []OutageRecord
	s.DB.Where("connection_id = ?", id).Order("id").Find(&outages)
	var bill *Bill
	if conn.BillID != nil {
		var b Bill
		s.DB.Preload("Items").First(&b, *conn.BillID)
		bill = &b
	}
	c.JSON(200, gin.H{"connection": conn, "segments": segs, "readings": readings, "outages": outages, "bill": bill})
}

func parseTime(s string, def time.Time) time.Time {
	if s == "" {
		return def
	}
	if t, err := time.ParseInLocation(time.RFC3339, s, time.Local); err == nil {
		return t
	}
	return def
}

func (s *Server) changeBerth(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		Time        string   `json:"time"`
		FromReading *float64 `json:"from_reading"`
		ToMeterCode string   `json:"to_meter_code"`
		ToReading   float64  `json:"to_reading"`
		ReportBy    string   `json:"report_by"`
	}
	if !bind(c, &in) {
		return
	}
	t := parseTime(in.Time, time.Now())
	if err := changeBerth(s.DB, id, changeBerthInput{
		Time: t, FromReading: in.FromReading, ToMeterCode: in.ToMeterCode,
		ToReading: in.ToReading, ReportBy: in.ReportBy,
	}); err != nil {
		errJSON(c, err)
		return
	}
	s.syncSim(in.ToMeterCode, in.ToReading, t)
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) outageStart(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		Time     string  `json:"time"`
		Reading  float64 `json:"reading"`
		Reason   string  `json:"reason"`
		ReportBy string  `json:"report_by"`
	}
	if !bind(c, &in) {
		return
	}
	r := in.Reading
	if err := reportOutage(s.DB, id, boundaryInput{
		Time: parseTime(in.Time, time.Now()), Reading: &r, ReportBy: in.ReportBy,
	}, in.Reason); err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) outageResume(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		Time      string  `json:"time"`
		MeterCode string  `json:"meter_code"`
		Reading   float64 `json:"reading"`
		ReportBy  string  `json:"report_by"`
	}
	if !bind(c, &in) {
		return
	}
	t := parseTime(in.Time, time.Now())
	if err := resumePower(s.DB, id, resumeInput{
		Time: t, MeterCode: in.MeterCode, Reading: in.Reading, ReportBy: in.ReportBy,
	}); err != nil {
		errJSON(c, err)
		return
	}
	mc := in.MeterCode
	if mc == "" {
		var outage OutageRecord
		if s.DB.Where("connection_id = ? AND end_time IS NOT NULL", id).Order("id desc").First(&outage).Error == nil {
			var seg MeterSegment
			if s.DB.Where("connection_id = ? AND seq = ?", id, outage.SegmentBefore).First(&seg).Error == nil {
				mc = seg.MeterCode
			}
		}
	}
	s.syncSim(mc, in.Reading, t)
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) closeConn(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		Time     string  `json:"time"`
		Reading  float64 `json:"reading"`
		ReportBy string  `json:"report_by"`
	}
	if !bind(c, &in) {
		return
	}
	t := parseTime(in.Time, time.Now())
	if err := closeConnection(s.DB, id, boundaryInput{
		Time: t, Reading: &in.Reading, ReportBy: in.ReportBy,
	}); err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// ---------- 读数 ----------

func (s *Server) deviceReading(c *gin.Context) {
	var in struct {
		MeterCode  string   `json:"meter_code"`
		Reading    *float64 `json:"reading"` // 可空：模拟器按增量推进
		ReadAt     string   `json:"read_at"`
		DeviceSeq  *int     `json:"device_seq"` // 可空：模拟器自动序号
		Source     string   `json:"source"`
		ReportedBy string   `json:"reported_by"`
	}
	if !bind(c, &in) {
		return
	}
	t := parseTime(in.ReadAt, time.Now())
	val := 0.0
	seq := in.DeviceSeq
	if in.Reading != nil {
		val = *in.Reading
	} else {
		val = s.advanceSim(in.MeterCode, t)
	}
	if seq == nil {
		n := s.nextSeq(in.MeterCode)
		seq = &n
	}
	r, dup, err := ingestReading(s.DB, readingInput{
		MeterCode: in.MeterCode, Reading: val, ReadAt: t,
		Source: firstNonEmpty(in.Source, SourceDevice), DeviceSeq: seq, ReportedBy: in.ReportedBy,
	})
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(201, gin.H{"reading": r, "duplicated": dup})
}

func (s *Server) manualReading(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		MeterCode  string  `json:"meter_code"`
		Reading    float64 `json:"reading"`
		ReadAt     string  `json:"read_at"`
		ReportedBy string  `json:"reported_by"`
	}
	if !bind(c, &in) {
		return
	}
	cid := id
	r, dup, err := ingestReading(s.DB, readingInput{
		ConnectionID: &cid, MeterCode: in.MeterCode, Reading: in.Reading,
		ReadAt: parseTime(in.ReadAt, time.Now()), Source: SourceManual, ReportedBy: in.ReportedBy,
	})
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(201, gin.H{"reading": r, "duplicated": dup})
}

func (s *Server) listReadings(c *gin.Context) {
	q := s.DB.Model(&MeterReading{}).Order("id desc").Limit(500)
	if m := c.Query("meter"); m != "" {
		q = q.Where("meter_code = ?", m)
	}
	if conn := c.Query("connection_id"); conn != "" {
		q = q.Where("connection_id = ?", conn)
	}
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	var rs []MeterReading
	q.Find(&rs)
	c.JSON(200, rs)
}

// ---------- 补录 / 复核 / 异常处置 ----------

func (s *Server) submitSupplement(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		MeterCode  string  `json:"meter_code"`
		Reading    float64 `json:"reading"`
		ReadAt     string  `json:"read_at"`
		ReportedBy string  `json:"reported_by"`
	}
	if !bind(c, &in) {
		return
	}
	r, err := submitSupplement(s.DB, supplementInput{
		ConnectionID: id, MeterCode: in.MeterCode, Reading: in.Reading,
		ReadAt: parseTime(in.ReadAt, time.Now()), ReportedBy: in.ReportedBy,
	})
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(201, r)
}

func (s *Server) reviewReading(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		Reviewer string `json:"reviewer"`
		Approve  bool   `json:"approve"`
		Note     string `json:"note"`
	}
	if !bind(c, &in) {
		return
	}
	r, err := reviewSupplement(s.DB, id, reviewInput{Reviewer: in.Reviewer, Approve: in.Approve, Note: in.Note})
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(200, r)
}

func (s *Server) resolveAnomaly(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		Reviewer   string `json:"reviewer"`
		Resolution string `json:"resolution"`
		Note       string `json:"note"`
	}
	if !bind(c, &in) {
		return
	}
	r, err := resolveAnomaly(s.DB, id, resolveInput{Reviewer: in.Reviewer, Resolution: in.Resolution, Note: in.Note})
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(200, r)
}

// ---------- 账单 / 确认 / 付款 / 异议 ----------

func (s *Server) genBill(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	b, err := generateBill(s.DB, id)
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(201, b)
}

func (s *Server) listBills(c *gin.Context) {
	var bills []Bill
	s.DB.Order("id desc").Find(&bills)
	c.JSON(200, bills)
}

func (s *Server) billDetail(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var b Bill
	if err := s.DB.Preload("Items").First(&b, id).Error; err != nil {
		errJSON(c, fail(404, "账单不存在"))
		return
	}
	var rcpt *PaymentReceipt
	if err := s.DB.Where("bill_id = ?", id).First(&rcpt).Error; err != nil {
		rcpt = nil
	}
	var disputes []Dispute
	s.DB.Where("bill_id = ?", id).Order("id").Find(&disputes)
	var notes []CreditNote
	s.DB.Where("bill_id = ?", id).Order("id").Find(&notes)
	snap := map[string]any{}
	if b.SnapshotJSON != "" {
		_ = json.Unmarshal([]byte(b.SnapshotJSON), &snap)
	}
	c.JSON(200, gin.H{
		"bill": b, "receipt": rcpt, "disputes": disputes,
		"credit_notes": notes, "snapshot": snap,
	})
}

func (s *Server) confirmBill(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		By string `json:"by"`
	}
	if !bind(c, &in) {
		return
	}
	if err := confirmBill(s.DB, id, in.By); err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) payBill(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		By string `json:"by"`
	}
	if !bind(c, &in) {
		return
	}
	r, err := payBill(s.DB, id, in.By)
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(201, r)
}

func (s *Server) raiseDispute(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		Reason   string `json:"reason"`
		Evidence string `json:"evidence"`
		By       string `json:"by"`
	}
	if !bind(c, &in) {
		return
	}
	d, err := raiseDispute(s.DB, id, disputeInput{Reason: in.Reason, Evidence: in.Evidence, RaisedBy: in.By})
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(201, d)
}

func (s *Server) listDisputes(c *gin.Context) {
	var ds []Dispute
	s.DB.Order("id desc").Find(&ds)
	c.JSON(200, ds)
}

func (s *Server) resolveDispute(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var in struct {
		Reviewer   string  `json:"reviewer"`
		Conclusion string  `json:"conclusion"`
		Action     string  `json:"action"`
		Amount     float64 `json:"amount"`
	}
	if !bind(c, &in) {
		return
	}
	d, note, err := resolveDispute(s.DB, id, resolveDisputeInput{
		Reviewer: in.Reviewer, Conclusion: in.Conclusion, Action: in.Action, Amount: in.Amount,
	})
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(200, gin.H{"dispute": d, "credit_note": note})
}

// ---------- 模拟器控制 ----------

func (s *Server) simStatus(c *gin.Context) {
	states := map[string]any{}
	for code, st := range s.sim {
		st.mu.Lock()
		states[code] = gin.H{"next_seq": st.NextSeq, "value": st.Value, "last_at": st.LastAt, "auto": st.AutoOn}
		st.mu.Unlock()
	}
	c.JSON(200, states)
}

// simAuto：开启后设备按间隔自动推进表码并回传（在后台 goroutine 里跑）
func (s *Server) simAuto(c *gin.Context) {
	var in struct {
		MeterCode   string  `json:"meter_code"`
		On          bool    `json:"on"`
		StepKWh     float64 `json:"step_kwh"`
		IntervalSec int     `json:"interval_sec"`
	}
	if !bind(c, &in) {
		return
	}
	st := s.getSim(in.MeterCode)
	st.mu.Lock()
	st.AutoOn = in.On
	st.StepKWh = in.StepKWh
	if in.IntervalSec > 0 {
		st.Interval = time.Duration(in.IntervalSec) * time.Second
	}
	st.mu.Unlock()
	c.JSON(200, gin.H{"ok": true})
}

// simReplay 重复回传：按原序列号重放某条历史读数，验证去重
func (s *Server) simReplay(c *gin.Context) {
	id, ok := uid(c)
	if !ok {
		return
	}
	var r0 MeterReading
	if err := s.DB.First(&r0, id).Error; err != nil {
		errJSON(c, fail(404, "读数不存在"))
		return
	}
	if r0.DeviceSeq == nil {
		errJSON(c, fail(400, "手工读数无设备序列号，无法重放"))
		return
	}
	r, dup, err := ingestReading(s.DB, readingInput{
		MeterCode: r0.MeterCode, Reading: r0.Reading, ReadAt: time.Now(),
		Source: SourceDevice, DeviceSeq: r0.DeviceSeq, ReportedBy: "simulator-replay",
	})
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(201, gin.H{"reading": r, "duplicated": dup})
}
