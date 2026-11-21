package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

var (
	mqttClient    mqtt.Client
	mqttConnected bool
	topicPrefix   string

	liveTelemetry = make(map[string]TelemetryPayload)
	lastDBSave    = make(map[string]time.Time)
	lastAlarmSent = make(map[string]time.Time)
	stateMutex    sync.RWMutex
)

func InitMQTT() {
	broker := getEnv("MQTT_BROKER", "tcp://broker.hivemq.com:1883")
	clientID := getEnv("MQTT_CLIENT_ID", "mediflow-esp32-server") + "-" + strconv.FormatInt(time.Now().Unix()%10000, 10)
	topicPrefix = getEnv("MQTT_TOPIC_PREFIX", "mediflow")

	opts := mqtt.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(clientID)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(5 * time.Second)

	opts.OnConnect = func(c mqtt.Client) {
		mqttConnected = true
		log.Printf("[MQTT] Terhubung ke broker %s", broker)
		topic := fmt.Sprintf("%s/+/telemetry", topicPrefix)
		if token := c.Subscribe(topic, 0, handleTelemetryMessage); token.Wait() && token.Error() != nil {
			log.Printf("[MQTT] Gagal subscribe %s: %v", topic, token.Error())
		} else {
			log.Printf("[MQTT] Subscribe aktif pada topik: %s", topic)
		}
	}

	opts.OnConnectionLost = func(c mqtt.Client, err error) {
		mqttConnected = false
		log.Printf("[MQTT] Koneksi terputus: %v", err)
	}

	mqttClient = mqtt.NewClient(opts)
	go func() {
		if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
			log.Printf("[MQTT] Koneksi awal gagal (%v), mencoba reconnect di background...", token.Error())
		}
	}()
}

func handleTelemetryMessage(_ mqtt.Client, msg mqtt.Message) {
	var payload TelemetryPayload
	if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
		log.Printf("[MQTT] Payload JSON tidak valid: %v", err)
		return
	}
	ProcessIncomingTelemetry(payload)
}

func BuildDeviceState(deviceID string) DeviceStateResponse {
	dev := GetDeviceConfig(deviceID)

	stateMutex.RLock()
	tel, ok := liveTelemetry[deviceID]
	stateMutex.RUnlock()

	if !ok {
		tel = TelemetryPayload{
			DeviceID:    deviceID,
			TargetTPM:   dev.TargetTPM,
			FaktorTetes: dev.FaktorTetes,
			BatasHabis:  dev.BatasHabis,
			CalFactor:   dev.CalFactor,
			AutoMode:    dev.AutoMode,
			ServoPos:    dev.ServoTarget,
			ServoTarget: dev.ServoTarget,
			StatusStr:   dev.StatusStr,
			WeightOk:    true,
			RecordedAt:  time.Now(),
		}
	}

	cmd := CommandPayload{
		TargetTPM:   dev.TargetTPM,
		FaktorTetes: dev.FaktorTetes,
		AutoMode:    dev.AutoMode,
		ServoTarget: dev.ServoTarget,
		BatasHabis:  dev.BatasHabis,
		CalFactor:   dev.CalFactor,
	}

	return DeviceStateResponse{
		Telemetry: tel,
		Command:   cmd,
	}
}

func ProcessIncomingTelemetry(payload TelemetryPayload) {
	if payload.DeviceID == "" {
		payload.DeviceID = "ESP32-INFUS01"
	}
	if payload.StatusStr == "" {
		payload.StatusStr = "Menunggu"
	}
	if payload.FaktorTetes > 0 && payload.MlJam == 0 && payload.ActualTPM > 0 {
		payload.MlJam = (payload.ActualTPM * 60.0) / payload.FaktorTetes
	}
	payload.RecordedAt = time.Now()

	intervalSec, _ := strconv.Atoi(getEnv("DB_SAVE_INTERVAL_SEC", "10"))
	if intervalSec <= 0 {
		intervalSec = 10
	}

	stateMutex.Lock()
	liveTelemetry[payload.DeviceID] = payload
	shouldSaveDB := time.Since(lastDBSave[payload.DeviceID]) >= time.Duration(intervalSec)*time.Second
	if shouldSaveDB {
		lastDBSave[payload.DeviceID] = time.Now()
	}
	stateMutex.Unlock()

	BroadcastState(BuildDeviceState(payload.DeviceID))

	if shouldSaveDB {
		go InsertTelemetry(payload)
	}

	checkAndTriggerAlarm(payload)
}

func checkAndTriggerAlarm(p TelemetryPayload) {
	alarmType := ""
	message := ""

	switch {
	case p.AlarmHabis || p.StatusStr == "HABIS":
		alarmType = "CAIRAN_HABIS"
		message = fmt.Sprintf("%s: CAIRAN HABIS! Berat total %.1f g < batasHabis %.0f g. Servo menutup penuh ke 110°.", p.DeviceID, p.Gross, p.BatasHabis)
	case p.Overload:
		alarmType = "OVERLOAD"
		message = fmt.Sprintf("%s: OVERLOAD! Berat total %.1f g melebihi kapasitas maksimal 5000 g.", p.DeviceID, p.Gross)
	case !p.WeightOk:
		alarmType = "HX711_ERROR"
		message = fmt.Sprintf("%s: HX711 ERROR! Sensor load cell tidak merespons > 2 detik.", p.DeviceID)
	case p.StatusStr == "HENTI" || p.StatusStr == "Cek selg":
		alarmType = "ALIRAN_HENTI"
		message = fmt.Sprintf("%s: Status '%s' — Tidak ada tetesan terdeteksi pada GPIO 34!", p.DeviceID, p.StatusStr)
	}

	if alarmType == "" {
		return
	}

	key := p.DeviceID + ":" + alarmType
	stateMutex.Lock()
	if time.Since(lastAlarmSent[key]) < 45*time.Second {
		stateMutex.Unlock()
		return
	}
	lastAlarmSent[key] = time.Now()
	stateMutex.Unlock()

	go InsertAlarm(p.DeviceID, alarmType, message)
}

func PublishCommand(deviceID string, cmd CommandPayload) error {
	dev := SaveCommandToDevice(deviceID, cmd)

	stateMutex.Lock()
	tel, ok := liveTelemetry[deviceID]
	if ok {
		tel.TargetTPM = dev.TargetTPM
		tel.FaktorTetes = dev.FaktorTetes
		tel.BatasHabis = dev.BatasHabis
		tel.AutoMode = dev.AutoMode
		tel.ServoTarget = dev.ServoTarget
		if !dev.AutoMode {
			tel.ServoPos = dev.ServoTarget
			tel.StatusStr = "Manual"
		}
		if cmd.Action == "TARE" {
			tel.BeratTampil = 0
			tel.RawNet = 0
		} else if cmd.Action == "ZERO" {
			tel.BeratTampil = 0
			tel.Gross = 0
			tel.RawNet = 0
			tel.AlarmHabis = false
		}
		liveTelemetry[deviceID] = tel
	}
	stateMutex.Unlock()

	if cmd.Action == "EMERGENCY_CLOSE" {
		go InsertAlarm(deviceID, "EMERGENCY_CLOSE", fmt.Sprintf("%s: Klem penuh darurat (a110) diaktifkan.", deviceID))
	}

	BroadcastState(BuildDeviceState(deviceID))

	if mqttClient == nil || !mqttClient.IsConnected() {
		log.Printf("[MQTT] Broker offline, perintah untuk %s tersimpan di DB/memori lokal", deviceID)
		return nil
	}

	bytes, err := json.Marshal(cmd)
	if err != nil {
		return err
	}

	topic := fmt.Sprintf("%s/%s/command", topicPrefix, deviceID)
	token := mqttClient.Publish(topic, 1, false, bytes)
	token.Wait()
	log.Printf("[MQTT] Command terkirim ke %s -> %s", topic, string(bytes))
	return token.Error()
}
