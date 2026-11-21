package main

import "time"

// TelemetryPayload mencerminkan seluruh variabel state dari kode ESP32
type TelemetryPayload struct {
	DeviceID    string    `json:"device_id"`
	ActualTPM   float64   `json:"actual_tpm"`
	TargetTPM   int       `json:"target_tpm"`
	MlJam       float64   `json:"ml_jam"`
	IntervalMs  int64     `json:"interval_ms"`
	TotalTetes  int64     `json:"total_tetes"`
	ServoPos    int       `json:"servo_pos"`
	ServoTarget int       `json:"servo_target"`
	AutoMode    bool      `json:"auto_mode"`
	StatusStr   string    `json:"status_str"`   // Menunggu, Stabil, Cepat, Lambat, HENTI, HABIS, Manual, Cek selg
	BeratTampil float64   `json:"berat_tampil"` // gram bersih setelah tareOffset
	Gross       float64   `json:"gross"`        // gram total dari baseOffset
	RawNet      int64     `json:"raw_net"`      // raw - tareOffset
	CalFactor   float64   `json:"cal_factor"`   // raw per gram (0 = belum kalibrasi)
	BatasHabis  float64   `json:"batas_habis"`  // gram (default 50.0, 0 = nonaktif)
	FaktorTetes float64   `json:"faktor_tetes"` // 10, 15, 20 (makro), 60 (mikro)
	WeightOk    bool      `json:"weight_ok"`
	Overload    bool      `json:"overload"`
	AlarmHabis  bool      `json:"alarm_habis"`
	Armed       bool      `json:"armed"`
	RecordedAt  time.Time `json:"recorded_at,omitempty"`
}

// CommandPayload memetakan seluruh perintah Serial ESP32 (s, f, a, o, t, k, z, h) ke JSON MQTT
type CommandPayload struct {
	TargetTPM     int     `json:"target_tpm"`               // perintah 's<val>' (1..200)
	FaktorTetes   float64 `json:"faktor_tetes"`             // perintah 'f<val>' (10, 15, 20, 60)
	AutoMode      bool    `json:"auto_mode"`                // perintah 'o' (true) / 'a<val>' (false)
	ServoTarget   int     `json:"servo_target"`             // perintah 'a<val>' (10..110)
	BatasHabis    float64 `json:"batas_habis"`              // perintah 'h<val>' (gram)
	CalFactor     float64 `json:"cal_factor,omitempty"`     // simpan calFactor manual
	Action        string  `json:"action,omitempty"`         // "TARE" ('t'), "ZERO" ('z'), "CALIBRATE" ('k'), "EMERGENCY_CLOSE"
	CalibrateGram float64 `json:"calibrate_gram,omitempty"` // argumen gram untuk 'k<gram>'
	SerialRaw     string  `json:"serial_raw,omitempty"`     // perintah serial mentah (misal "s20", "k500")
}

type DeviceConfig struct {
	ID          int       `json:"id"`
	DeviceID    string    `json:"device_id"`
	DeviceName  string    `json:"device_name"`
	PatientName string    `json:"patient_name"`
	TargetTPM   int       `json:"target_tpm"`
	FaktorTetes float64   `json:"faktor_tetes"`
	BatasHabis  float64   `json:"batas_habis"`
	CalFactor   float64   `json:"cal_factor"`
	AutoMode    bool      `json:"auto_mode"`
	ServoTarget int       `json:"servo_target"`
	StatusStr   string    `json:"status_str"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AlarmLog struct {
	ID        int64     `json:"id"`
	DeviceID  string    `json:"device_id"`
	AlarmType string    `json:"alarm_type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type DeviceStateResponse struct {
	Telemetry TelemetryPayload `json:"telemetry"`
	Command   CommandPayload   `json:"command"`
}
