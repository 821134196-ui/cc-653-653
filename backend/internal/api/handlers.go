package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"shorepower/internal/models"
	"shorepower/internal/service"
)

type Handler struct{ Svc *service.Service }

func New(svc *service.Service) *Handler { return &Handler{Svc: svc} }

func errResp(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"error": msg})
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.ParseInLocation("2006-01-06T15:04", s, time.Local)
		if err != nil {
			return time.Time{}
		}
	}
	return t
}

func uid(c *gin.Context) uint {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(id)
}

func (h *Handler) Register(r *gin.Engine) {
	api := r.Group("/api")
	{
		api.GET("/berths", h.listBerths)
		api.GET("/vessels", h.listVessels)

		api.POST("/sessions", h.registerSession)
		api.GET("/sessions", h.listSessions)
		api.GET("/sessions/:id", h.sessionDetail)
		api.POST("/sessions/:id/readings", h.addReading)
		api.POST("/sessions/:id/change-berth", h.changeBerth)
		api.POST("/sessions/:id/outages/start", h.startOutage)
		api.POST("/sessions/:id/outages/end", h.endOutage)
		api.POST("/sessions/:id/end", h.endSession)
		api.POST("/sessions/:id/supplements", h.supplement)
		api.GET("/sessions/:id/bill-preview", h.billPreview)
		api.POST("/sessions/:id/bills", h.confirmBill)

		api.GET("/readings/pending", h.pendingReadings)
		api.POST("/readings/:id/review", h.reviewReading)
		api.GET("/anomalies", h.listAnomalies)

		api.GET("/bills", h.listBills)
		api.GET("/bills/:id", h.billDetail)
		api.GET("/bills/:id/snapshot", h.billSnapshot)
		api.POST("/bills/:id/confirm", h.shipConfirm)
		api.POST("/bills/:id/disputes", h.raiseDispute)
		api.POST("/bills/:id/pay", h.pay)

		api.GET("/disputes", h.listDisputes)
		api.POST("/disputes/:id/resolve", h.resolveDispute)
	}
}

func (h *Handler) listBerths(c *gin.Context) {
	var berths []models.Berth
	h.Svc.DB.Order("id ASC").Find(&berths)
	c.JSON(200, berths)
}

func (h *Handler) listVessels(c *gin.Context) {
	var vs []models.Vessel
	h.Svc.DB.Order("id DESC").Find(&vs)
	c.JSON(200, vs)
}

type registerReq struct {
	VesselName   string  `json:"vessel_name" binding:"required"`
	Agent        string  `json:"agent"`
	BerthID      uint    `json:"berth_id" binding:"required"`
	Operator     string  `json:"operator" binding:"required"`
	StartTime    string  `json:"start_time"`
	StartReading float64 `json:"start_reading"`
}

func (h *Handler) registerSession(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errResp(c, 400, err.Error())
		return
	}
	sess, err := h.Svc.RegisterSession(req.VesselName, req.Agent, req.BerthID, req.Operator, parseTime(req.StartTime), req.StartReading)
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(201, sess)
}

func (h *Handler) listSessions(c *gin.Context) {
	var sessions []models.Session
	q := h.Svc.DB.Preload("Vessel").Preload("Berth").Order("id DESC")
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	q.Find(&sessions)
	c.JSON(200, sessions)
}

func (h *Handler) sessionDetail(c *gin.Context) {
	id := uid(c)
	var sess models.Session
	if err := h.Svc.DB.Preload("Vessel").Preload("Berth").First(&sess, id).Error; err != nil {
		errResp(c, 404, "会话不存在")
		return
	}
	var segs []models.MeterSegment
	h.Svc.DB.Preload("Berth").Where("session_id = ?", id).Order("seq ASC").Find(&segs)
	var readings []models.MeterReading
	h.Svc.DB.Where("session_id = ?", id).Order("read_at ASC, id ASC").Find(&readings)
	var outages []models.PowerOutage
	h.Svc.DB.Where("session_id = ?", id).Order("start ASC").Find(&outages)
	var anomalies []models.AnomalyEvent
	h.Svc.DB.Where("session_id = ?", id).Order("id DESC").Find(&anomalies)
	c.JSON(200, gin.H{
		"session": sess, "segments": segs, "readings": readings,
		"outages": outages, "anomalies": anomalies,
	})
}

type readingReq struct {
	Source     string  `json:"source"`
	Value      float64 `json:"value" binding:"required"`
	ReadAt     string  `json:"read_at"`
	Operator   string  `json:"operator"`
	DeviceID   string  `json:"device_id"`
	MessageUID string  `json:"message_uid"`
}

func (h *Handler) addReading(c *gin.Context) {
	var req readingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errResp(c, 400, err.Error())
		return
	}
	if req.Source == "" {
		req.Source = models.ReadingManual
	}
	if req.Operator == "" && req.Source == models.ReadingManual {
		errResp(c, 400, "手工抄录需填写值班员")
		return
	}
	res, err := h.Svc.AddReading(uid(c), req.Source, req.Value, parseTime(req.ReadAt), req.Operator, req.DeviceID, req.MessageUID)
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(201, res)
}

type changeReq struct {
	BerthID  uint    `json:"berth_id" binding:"required"`
	Value    float64 `json:"value" binding:"required"`
	At       string  `json:"at"`
	Operator string  `json:"operator" binding:"required"`
}

func (h *Handler) changeBerth(c *gin.Context) {
	var req changeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errResp(c, 400, err.Error())
		return
	}
	segs, err := h.Svc.ChangeBerth(uid(c), req.BerthID, req.Value, parseTime(req.At), req.Operator)
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(200, segs)
}

type outageReq struct {
	At       string `json:"at"`
	Reason   string `json:"reason"`
	Operator string `json:"operator" binding:"required"`
}

func (h *Handler) startOutage(c *gin.Context) {
	var req outageReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errResp(c, 400, err.Error())
		return
	}
	o, err := h.Svc.StartOutage(uid(c), parseTime(req.At), req.Reason, req.Operator)
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(201, o)
}

func (h *Handler) endOutage(c *gin.Context) {
	var req struct {
		At       string `json:"at"`
		Operator string `json:"operator" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		errResp(c, 400, err.Error())
		return
	}
	o, err := h.Svc.EndOutage(uid(c), parseTime(req.At), req.Operator)
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(200, o)
}

type endReq struct {
	Value    float64 `json:"value" binding:"required"`
	At       string  `json:"at"`
	Operator string  `json:"operator" binding:"required"`
}

func (h *Handler) endSession(c *gin.Context) {
	var req endReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errResp(c, 400, err.Error())
		return
	}
	sess, err := h.Svc.EndSession(uid(c), req.Value, parseTime(req.At), req.Operator)
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(200, sess)
}

type suppReq struct {
	Operator string  `json:"operator" binding:"required"`
	Value    float64 `json:"value" binding:"required"`
	ReadAt   string  `json:"read_at"`
	Note     string  `json:"note"`
}

func (h *Handler) supplement(c *gin.Context) {
	var req suppReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errResp(c, 400, err.Error())
		return
	}
	r, err := h.Svc.SubmitSupplement(uid(c), req.Operator, req.Value, parseTime(req.ReadAt), req.Note)
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(201, r)
}

func (h *Handler) pendingReadings(c *gin.Context) {
	var rs []models.MeterReading
	h.Svc.DB.Where("status = ?", models.ReadingPending).Order("id ASC").Find(&rs)
	c.JSON(200, rs)
}

type reviewReq struct {
	Reviewer string `json:"reviewer" binding:"required"`
	Approve  bool   `json:"approve"`
	Note     string `json:"note"`
}

func (h *Handler) reviewReading(c *gin.Context) {
	var req reviewReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errResp(c, 400, err.Error())
		return
	}
	rid, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	r, err := h.Svc.ReviewSupplement(uint(rid), req.Reviewer, req.Approve, req.Note)
	if err != nil {
		code := 400
		if errors.Is(err, service.ErrNotFound) {
			code = 404
		}
		errResp(c, code, err.Error())
		return
	}
	c.JSON(200, r)
}

func (h *Handler) listAnomalies(c *gin.Context) {
	var evs []models.AnomalyEvent
	q := h.Svc.DB.Order("id DESC")
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	} else {
		q = q.Where("status = ?", models.AnomalyOpen)
	}
	q.Find(&evs)
	c.JSON(200, evs)
}

func (h *Handler) billPreview(c *gin.Context) {
	p, err := h.Svc.PreviewBill(uid(c), c.Query("open") == "1")
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(200, p)
}

func (h *Handler) confirmBill(c *gin.Context) {
	b, err := h.Svc.ConfirmBill(uid(c))
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(201, b)
}

func (h *Handler) listBills(c *gin.Context) {
	var bills []models.Bill
	q := h.Svc.DB.Order("id DESC")
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	q.Find(&bills)
	c.JSON(200, bills)
}

func (h *Handler) billDetail(c *gin.Context) {
	id := uid(c)
	b, err := h.Svc.GetBill(id)
	if err != nil {
		errResp(c, 404, err.Error())
		return
	}
	items, _ := h.Svc.BillItems(id)
	var d *models.Dispute
	h.Svc.DB.Where("bill_id = ?", id).Order("id DESC").First(&d)
	var rcpt *models.PaymentReceipt
	h.Svc.DB.Where("bill_id = ?", id).First(&rcpt)
	c.JSON(200, gin.H{"bill": b, "items": items, "dispute": d, "receipt": rcpt})
}

func (h *Handler) billSnapshot(c *gin.Context) {
	snap, err := h.Svc.BillSnapshot(uid(c))
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(200, snap)
}

func (h *Handler) shipConfirm(c *gin.Context) {
	b, err := h.Svc.ShipConfirm(uid(c))
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(200, b)
}

type disputeReq struct {
	Reason   string `json:"reason" binding:"required"`
	Evidence string `json:"evidence"`
}

func (h *Handler) raiseDispute(c *gin.Context) {
	var req disputeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errResp(c, 400, err.Error())
		return
	}
	d, err := h.Svc.RaiseDispute(uid(c), req.Reason, req.Evidence)
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(201, d)
}

type payReq struct {
	Channel string `json:"channel"`
}

func (h *Handler) pay(c *gin.Context) {
	var req payReq
	_ = c.ShouldBindJSON(&req)
	r, err := h.Svc.SimulatePay(uid(c), req.Channel)
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(201, r)
}

func (h *Handler) listDisputes(c *gin.Context) {
	var ds []models.Dispute
	q := h.Svc.DB.Order("id DESC")
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	q.Find(&ds)
	c.JSON(200, ds)
}

type resolveReq struct {
	Handler    string `json:"handler" binding:"required"`
	Conclusion string `json:"conclusion" binding:"required"`
	Action     string `json:"action" binding:"required"`
}

func (h *Handler) resolveDispute(c *gin.Context) {
	var req resolveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errResp(c, http.StatusBadRequest, err.Error())
		return
	}
	did, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	d, err := h.Svc.ResolveDispute(uint(did), req.Handler, req.Conclusion, req.Action)
	if err != nil {
		errResp(c, 400, err.Error())
		return
	}
	c.JSON(200, d)
}
