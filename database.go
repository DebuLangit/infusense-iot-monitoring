package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

var (
	db          *sql.DB
	dbConnected bool

	memDevices = make(map[string]DeviceConfig)
	memAlarms  = make([]AlarmLog, 0)
	memMutex   sync.RWMutex
)

func InitDB() {
	seedMemoryDefaults()

	user := getEnv("DB_USER", "root")
	pass := getEnv("DB_PASS", "")
	host := getEnv("DB_HOST", "127.0.0.1")
	port := getEnv("DB_PORT", "3306")
	name := getEnv("DB_NAME", "mediflow_infus_db")

	rootDSN := fmt.Sprintf("%s:%s@tcp(%s:%s)/?parseTime=true", user, pass, host, port)
	rootDB, err := sql.Open("mysql", rootDSN)
	if err != nil {
		log.Printf("[DB] Gagal inisialisasi driver MySQL: %v (mode memori aktif)", err)
		return
	}
	defer rootDB.Close()

	if err := rootDB.Ping(); err != nil {
		log.Printf("[DB] MySQL belum aktif (%v). Server berjalan dengan cache memori.", err)
		return
	}

	_, _ = rootDB.Exec("CREATE DATABASE IF NOT EXISTS " + name)

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true", user, pass, host, port, name)
	db, err = sql.Open("mysql", dsn)
	if err != nil {
		log.Printf("[DB] Gagal koneksi ke %s: %v", name, err)
		return
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		log.Printf("[DB] Ping ke %s gagal: %v", name, err)
		return
	}

	dbConnected = true
	log.Printf("[DB] Terhubung ke MySQL/MariaDB (%s)", name)
	createTablesIfNotExist()
}

func seedMemoryDefaults() {
	now := time.Now()
	dev := DeviceConfig{
		ID: 1, DeviceID: "ESP32-INFUS01", DeviceName: "Infus Otomatis #1",
		PatientName: "Pasien Bed 01", TargetTPM: 20, FaktorTetes: 20.0, BatasHabis: 50.0,
		CalFactor: 0, AutoMode: true, ServoTarget: 110, StatusStr: "Menunggu", UpdatedAt: now,
	}

	memMutex.Lock()
	memDevices[dev.DeviceID] = dev
	memMutex.Unlock()

	stateMutex.Lock()
	liveTelemetry["ESP32-INFUS01"] = TelemetryPayload{
		DeviceID: "ESP32-INFUS01", ActualTPM: 0, TargetTPM: 20, MlJam: 0,
		IntervalMs: 0, TotalTetes: 0, ServoPos: 110, ServoTarget: 110,
		AutoMode: true, StatusStr: "Menunggu", BeratTampil: 0, Gross: 0,
		RawNet: 0, CalFactor: 0, BatasHabis: 50.0, FaktorTetes: 20.0,
		WeightOk: false, Overload: false, AlarmHabis: false, Armed: false, RecordedAt: now,
	}
	stateMutex.Unlock()
}

func createTablesIfNotExist() {
	if !dbConnected {
		return
	}

	queries := []string{
		`CREATE TABLE IF NOT EXISTS devices (
			id INT AUTO_INCREMENT PRIMARY KEY,
			device_id VARCHAR(50) NOT NULL UNIQUE,
			device_name VARCHAR(120) NOT NULL,
			patient_name VARCHAR(100) NOT NULL DEFAULT '-',
			target_tpm INT NOT NULL DEFAULT 20,
			faktor_tetes FLOAT NOT NULL DEFAULT 20.0,
			batas_habis FLOAT NOT NULL DEFAULT 50.0,
			cal_factor FLOAT NOT NULL DEFAULT 420.5,
			auto_mode TINYINT(1) NOT NULL DEFAULT 1,
			servo_target INT NOT NULL DEFAULT 110,
			status_str VARCHAR(30) NOT NULL DEFAULT 'Menunggu',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS telemetry_logs (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			device_id VARCHAR(50) NOT NULL,
			actual_tpm FLOAT NOT NULL,
			target_tpm INT NOT NULL,
			ml_jam FLOAT NOT NULL,
			interval_ms BIGINT NOT NULL,
			total_tetes BIGINT NOT NULL,
			servo_pos INT NOT NULL,
			servo_target INT NOT NULL,
			auto_mode TINYINT(1) NOT NULL,
			status_str VARCHAR(30) NOT NULL,
			berat_tampil FLOAT NOT NULL,
			gross_gram FLOAT NOT NULL,
			raw_net BIGINT NOT NULL,
			cal_factor FLOAT NOT NULL,
			batas_habis FLOAT NOT NULL,
			faktor_tetes FLOAT NOT NULL,
			weight_ok TINYINT(1) NOT NULL,
			overload TINYINT(1) NOT NULL,
			alarm_habis TINYINT(1) NOT NULL,
			recorded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			INDEX idx_device_time (device_id, recorded_at)
		)`,
		`CREATE TABLE IF NOT EXISTS alarm_logs (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			device_id VARCHAR(50) NOT NULL,
			alarm_type VARCHAR(50) NOT NULL,
			message TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			INDEX idx_alarm_time (created_at)
		)`,
		`INSERT IGNORE INTO devices (
			device_id, device_name, patient_name, target_tpm, faktor_tetes, batas_habis, cal_factor, auto_mode, servo_target, status_str
		) VALUES
		('ESP32-INFUS01', 'Infus Otomatis #1 (GPIO 34/19/16/4)', 'Pasien Bed 01', 20, 20.0, 50.0, 420.5, 1, 58, 'Stabil')`,
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			log.Printf("[DB] Error migrasi tabel: %v", err)
		}
	}
}

func GetDeviceConfig(deviceID string) DeviceConfig {
	memMutex.RLock()
	dev, exists := memDevices[deviceID]
	memMutex.RUnlock()

	if !dbConnected {
		if exists {
			return dev
		}
		return DeviceConfig{
			DeviceID: deviceID, DeviceName: deviceID, TargetTPM: 20, FaktorTetes: 20.0,
			BatasHabis: 50.0, CalFactor: 420.5, AutoMode: true, ServoTarget: 110, StatusStr: "Menunggu",
		}
	}

	var d DeviceConfig
	var autoInt int
	err := db.QueryRow(`SELECT id, device_id, device_name, patient_name, target_tpm, faktor_tetes,
		batas_habis, cal_factor, auto_mode, servo_target, status_str, updated_at
		FROM devices WHERE device_id = ?`, deviceID).Scan(
		&d.ID, &d.DeviceID, &d.DeviceName, &d.PatientName, &d.TargetTPM, &d.FaktorTetes,
		&d.BatasHabis, &d.CalFactor, &autoInt, &d.ServoTarget, &d.StatusStr, &d.UpdatedAt,
	)
	if err != nil {
		if exists {
			return dev
		}
		return DeviceConfig{
			DeviceID: deviceID, DeviceName: deviceID, TargetTPM: 20, FaktorTetes: 20.0,
			BatasHabis: 50.0, CalFactor: 420.5, AutoMode: true, ServoTarget: 110, StatusStr: "Menunggu",
		}
	}
	d.AutoMode = autoInt == 1
	return d
}

func SaveCommandToDevice(deviceID string, cmd CommandPayload) DeviceConfig {
	dev := GetDeviceConfig(deviceID)

	if cmd.TargetTPM >= 1 && cmd.TargetTPM <= 200 {
		dev.TargetTPM = cmd.TargetTPM
	}
	if cmd.FaktorTetes == 10 || cmd.FaktorTetes == 15 || cmd.FaktorTetes == 20 || cmd.FaktorTetes == 60 {
		dev.FaktorTetes = cmd.FaktorTetes
	}
	if cmd.BatasHabis >= 0 {
		dev.BatasHabis = cmd.BatasHabis
	}
	if cmd.CalFactor > 0 {
		dev.CalFactor = cmd.CalFactor
	}
	dev.AutoMode = cmd.AutoMode
	if cmd.ServoTarget >= 0 && cmd.ServoTarget <= 180 {
		dev.ServoTarget = cmd.ServoTarget
	}
	if !dev.AutoMode {
		dev.StatusStr = "Manual"
	} else if dev.StatusStr == "Manual" {
		dev.StatusStr = "Menunggu"
	}
	dev.UpdatedAt = time.Now()

	memMutex.Lock()
	memDevices[deviceID] = dev
	memMutex.Unlock()

	if dbConnected {
		autoInt := 0
		if dev.AutoMode {
			autoInt = 1
		}
		_, err := db.Exec(`INSERT INTO devices (
			device_id, device_name, patient_name, target_tpm, faktor_tetes,
			batas_habis, cal_factor, auto_mode, servo_target, status_str
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			target_tpm = VALUES(target_tpm),
			faktor_tetes = VALUES(faktor_tetes),
			batas_habis = VALUES(batas_habis),
			cal_factor = VALUES(cal_factor),
			auto_mode = VALUES(auto_mode),
			servo_target = VALUES(servo_target),
			status_str = VALUES(status_str)`,
			dev.DeviceID, dev.DeviceName, dev.PatientName, dev.TargetTPM, dev.FaktorTetes,
			dev.BatasHabis, dev.CalFactor, autoInt, dev.ServoTarget, dev.StatusStr,
		)
		if err != nil {
			log.Printf("[DB] Gagal update devices: %v", err)
		}
	}

	return dev
}

func InsertTelemetry(t TelemetryPayload) {
	if !dbConnected {
		return
	}
	autoInt, wOk, ovl, alm := 0, 0, 0, 0
	if t.AutoMode {
		autoInt = 1
	}
	if t.WeightOk {
		wOk = 1
	}
	if t.Overload {
		ovl = 1
	}
	if t.AlarmHabis {
		alm = 1
	}
	_, err := db.Exec(`INSERT INTO telemetry_logs (
		device_id, actual_tpm, target_tpm, ml_jam, interval_ms, total_tetes,
		servo_pos, servo_target, auto_mode, status_str, berat_tampil, gross_gram,
		raw_net, cal_factor, batas_habis, faktor_tetes, weight_ok, overload, alarm_habis
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.DeviceID, t.ActualTPM, t.TargetTPM, t.MlJam, t.IntervalMs, t.TotalTetes,
		t.ServoPos, t.ServoTarget, autoInt, t.StatusStr, t.BeratTampil, t.Gross,
		t.RawNet, t.CalFactor, t.BatasHabis, t.FaktorTetes, wOk, ovl, alm,
	)
	if err != nil {
		log.Printf("[DB] Gagal simpan telemetri: %v", err)
	}
}

func InsertAlarm(deviceID, alarmType, message string) {
	alarm := AlarmLog{
		ID:        time.Now().UnixMilli(),
		DeviceID:  deviceID,
		AlarmType: alarmType,
		Message:   message,
		CreatedAt: time.Now(),
	}

	memMutex.Lock()
	memAlarms = append([]AlarmLog{alarm}, memAlarms...)
	if len(memAlarms) > 50 {
		memAlarms = memAlarms[:50]
	}
	memMutex.Unlock()

	if dbConnected {
		_, err := db.Exec(`INSERT INTO alarm_logs (device_id, alarm_type, message) VALUES (?, ?, ?)`,
			deviceID, alarmType, message)
		if err != nil {
			log.Printf("[DB] Gagal simpan alarm: %v", err)
		}
	}
}

func GetRecentTelemetry(deviceID string, limit int) []TelemetryPayload {
	if !dbConnected {
		return []TelemetryPayload{}
	}
	rows, err := db.Query(`SELECT device_id, actual_tpm, target_tpm, ml_jam, interval_ms, total_tetes,
		servo_pos, servo_target, auto_mode, status_str, berat_tampil, gross_gram, raw_net,
		cal_factor, batas_habis, faktor_tetes, weight_ok, overload, alarm_habis, recorded_at
		FROM telemetry_logs WHERE device_id = ? ORDER BY id DESC LIMIT ?`, deviceID, limit)
	if err != nil {
		return []TelemetryPayload{}
	}
	defer rows.Close()

	var logs []TelemetryPayload
	for rows.Next() {
		var t TelemetryPayload
		var a, w, o, al int
		if err := rows.Scan(&t.DeviceID, &t.ActualTPM, &t.TargetTPM, &t.MlJam, &t.IntervalMs, &t.TotalTetes,
			&t.ServoPos, &t.ServoTarget, &a, &t.StatusStr, &t.BeratTampil, &t.Gross, &t.RawNet,
			&t.CalFactor, &t.BatasHabis, &t.FaktorTetes, &w, &o, &al, &t.RecordedAt); err == nil {
			t.AutoMode = a == 1
			t.WeightOk = w == 1
			t.Overload = o == 1
			t.AlarmHabis = al == 1
			logs = append(logs, t)
		}
	}
	return logs
}

func GetRecentAlarms(limit int) []AlarmLog {
	if !dbConnected {
		memMutex.RLock()
		defer memMutex.RUnlock()
		return memAlarms
	}
	rows, err := db.Query(`SELECT id, device_id, alarm_type, message, created_at
		FROM alarm_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return []AlarmLog{}
	}
	defer rows.Close()

	var alarms []AlarmLog
	for rows.Next() {
		var a AlarmLog
		if err := rows.Scan(&a.ID, &a.DeviceID, &a.AlarmType, &a.Message, &a.CreatedAt); err == nil {
			alarms = append(alarms, a)
		}
	}
	return alarms
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}
