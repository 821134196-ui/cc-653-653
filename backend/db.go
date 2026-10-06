package main

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openDB() *gorm.DB {
	dbPath := os.Getenv("SP_DB")
	if dbPath == "" {
		dbPath = filepath.Join("data", "shorepower.db")
	}
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	db, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"), &gorm.Config{
		Logger:  logger.Default.LogMode(logger.Warn),
		NowFunc: func() time.Time { return time.Now().Local() },
	})
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&User{}, &Berth{}, &Meter{}, &Connection{}, &MeterSegment{},
		&MeterReading{}, &OutageRecord{}, &Bill{}, &BillItem{},
		&PaymentReceipt{}, &Dispute{}, &CreditNote{},
	); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	seed(db)
	return db
}

func seed(db *gorm.DB) {
	users := []User{
		{Code: "op01", Name: "值班员-王敏", Role: RoleOperator},
		{Code: "rv01", Name: "复核员-李静", Role: RoleReviewer},
		{Code: "ship", Name: "船方代表", Role: RoleVessel},
	}
	for i := range users {
		db.Where(User{Code: users[i].Code}).FirstOrCreate(&users[i])
	}

	berths := []Berth{
		{Code: "A1", Name: "A区1号泊位", RatePerKWh: 1.20, ServiceFeePerHr: 8},
		{Code: "A2", Name: "A区2号泊位", RatePerKWh: 1.20, ServiceFeePerHr: 8},
		{Code: "B1", Name: "B区1号泊位（高压）", RatePerKWh: 1.55, ServiceFeePerHr: 12},
		{Code: "B2", Name: "B区2号泊位（高压）", RatePerKWh: 1.55, ServiceFeePerHr: 12},
	}
	for i := range berths {
		db.Where(Berth{Code: berths[i].Code}).FirstOrCreate(&berths[i])
	}

	meters := []Meter{
		{Code: "M-A1", Name: "A1 岸电表", BerthCode: "A1", MaxPowerKW: 200},
		{Code: "M-A2", Name: "A2 岸电表", BerthCode: "A2", MaxPowerKW: 200},
		{Code: "M-B1", Name: "B1 岸电表", BerthCode: "B1", MaxPowerKW: 500},
		{Code: "M-B2", Name: "B2 岸电表", BerthCode: "B2", MaxPowerKW: 500},
	}
	for i := range meters {
		db.Where(Meter{Code: meters[i].Code}).FirstOrCreate(&meters[i])
	}
}

// ---------- 小工具 ----------

type apiError struct {
	Code int
	Msg  string
}

func (e *apiError) Error() string { return e.Msg }

func fail(code int, msg string) error { return &apiError{Code: code, Msg: msg} }

func mustFind[T any](db *gorm.DB, id uint) (*T, error) {
	var v T
	if err := db.First(&v, id).Error; err != nil {
		return nil, fail(404, "记录不存在")
	}
	return &v, nil
}

func round2(x float64) float64 { return float64(int64(x*100+0.5)) / 100 }

// hoursBetween 服务时长（小时，保留4位）
func hoursBetween(a, b time.Time) float64 {
	return b.Sub(a).Hours()
}
