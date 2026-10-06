package store

import (
	"log"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"shorepower/internal/models"
)

// Open 打开/创建 SQLite 数据库并执行迁移与种子数据。
func Open(path string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(
		&models.Berth{},
		&models.Vessel{},
		&models.Session{},
		&models.MeterSegment{},
		&models.MeterReading{},
		&models.PowerOutage{},
		&models.AnomalyEvent{},
		&models.Bill{},
		&models.BillItem{},
		&models.Dispute{},
		&models.PaymentReceipt{},
	); err != nil {
		return nil, err
	}
	if err := EnsureIndexes(db); err != nil {
		return nil, err
	}
	seed(db)
	return db, nil
}

// EnsureIndexes 建立部分唯一索引：仅对非空设备报文ID去重，
// 手工/补录读数的空 message_uid 不受唯一约束限制。
func EnsureIndexes(db *gorm.DB) error {
	return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_meter_reading_msg_uid
		ON meter_readings(message_uid) WHERE message_uid <> ''`).Error
}

func seed(db *gorm.DB) {
	var n int64
	db.Model(&models.Berth{}).Count(&n)
	if n == 0 {
		berths := []models.Berth{
			{Name: "A1泊位", RatePerKWh: 1.20, ServicePerMin: 0.50},
			{Name: "A2泊位", RatePerKWh: 1.35, ServicePerMin: 0.60},
			{Name: "B1泊位", RatePerKWh: 1.50, ServicePerMin: 0.80},
		}
		if err := db.Create(&berths).Error; err != nil {
			log.Printf("seed berths: %v", err)
		}
	}
}
