CREATE DATABASE IF NOT EXISTS mediflow_infus_db;
USE mediflow_infus_db;

CREATE TABLE IF NOT EXISTS devices (
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
);

CREATE TABLE IF NOT EXISTS telemetry_logs (
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
);

CREATE TABLE IF NOT EXISTS alarm_logs (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    device_id VARCHAR(50) NOT NULL,
    alarm_type VARCHAR(50) NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_alarm_time (created_at)
);

INSERT IGNORE INTO devices (
    device_id, device_name, patient_name, target_tpm, faktor_tetes, batas_habis, cal_factor, auto_mode, servo_target, status_str
) VALUES
('ESP32-INFUS01', 'Infus Otomatis #1 (GPIO 34/19/16/4)', 'Pasien Bed 01', 20, 20.0, 50.0, 420.5, 1, 58, 'Stabil');
