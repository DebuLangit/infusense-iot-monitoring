#include <Arduino.h>
#include <Wire.h>
#include <LiquidCrystal_I2C.h>
#include <Preferences.h>
#include <ESP32Servo.h>
#include <WiFi.h>
#include <PubSubClient.h>
#include <ArduinoJson.h>
#include "HX711.h"

// =====================================================================
//  KONFIGURASI WIFI & MQTT BROKER GRATIS (MOSQUITTO)
// =====================================================================
const char* WIFI_SSID   = "NAMA_WIFI_TUAN";      // <-- Ganti dengan nama WiFi / Hotspot
const char* WIFI_PASS   = "PASSWORD_WIFI_TUAN";  // <-- Ganti dengan password WiFi
const char* MQTT_BROKER = "broker.hivemq.com";  // Broker publik gratis Mosquitto
const int   MQTT_PORT   = 1883;
const char* DEVICE_ID   = "ESP32-INFUS01";

const char* TOPIC_TELEMETRY = "mediflow/ESP32-INFUS01/telemetry";
const char* TOPIC_COMMAND   = "mediflow/ESP32-INFUS01/command";

WiFiClient espClient;
PubSubClient mqtt(espClient);
unsigned long lastMqttAttempt = 0;

// =====================================================================
//  PIN
// =====================================================================
const int PIN_PHOTODIODE = 34;  // OUT sensor photodiode (GPIO 34 = input only)
const int PIN_SERVO      = 19;  // sinyal servo (pindah dari 18 karena 18 dipakai tombol tare)
const int DT_PIN         = 16;  // HX711 DT
const int SCK_PIN        = 4;   // HX711 SCK
const int TARE_PIN       = 18;  // tombol tare: satu kaki ke GPIO 18, satu kaki ke GND
const int SDA_PIN        = 21;  // LCD I2C
const int SCL_PIN        = 22;  // LCD I2C

// =====================================================================
//  PENGATURAN SERVO (SESUAIKAN DENGAN MEKANIK PENJEPIT)
// =====================================================================
const int SUDUT_BUKA_PENUH  = 10;   // selang terbuka (tetesan cepat)
const int SUDUT_TUTUP_PENUH = 110;  // selang terjepit rapat (tetesan berhenti)
const unsigned long SERVO_STEP_MS = 20;  // 1 derajat tiap 20 ms (gerak halus)

// =====================================================================
//  PENGATURAN KONTROL TETESAN
// =====================================================================
const int  TOLERANSI_TPM = 2;           // selisih yang masih dianggap stabil
const unsigned long KONTROL_MS = 6000;  // jeda antar koreksi (beri waktu aliran menyesuaikan)

// =====================================================================
//  PENGATURAN PHOTODIODE
// =====================================================================
const unsigned long DEBOUNCE_MS     = 200;    // jeda minimal antar tetes
const unsigned long TIMEOUT_MIN_MS  = 10000;  // batas minimal "aliran berhenti"
const int IV_N = 4;                           // jumlah interval tetes yang dirata-rata

// =====================================================================
//  PENGATURAN LOAD CELL
// =====================================================================
const float MAX_GRAM   = 5000.0;  // kapasitas 5 kg
const float EMA_ALPHA  = 0.35;
const float ZERO_BAND  = 2.0;     // di bawah ini ditampilkan 0 g
const float JUMP_RESET = 30.0;
const int   BUF_N      = 9;       // jumlah sampel filter

// =====================================================================
//  OBJEK
// =====================================================================
LiquidCrystal_I2C lcd(0x27, 16, 2);  // ganti 0x3F jika alamat berbeda
HX711 scale;
Preferences prefs;
Servo servoInfus;

// ---------- parameter yang bisa diubah via Serial & tersimpan ----------
int   targetTPM   = 20;     // target tetes per menit
float faktorTetes = 20.0;   // 20 tetes/mL (makro) atau 60 tetes/mL (mikro)
float batasHabis  = 50.0;   // alarm jika berat total < nilai ini (gram), 0 = nonaktif

// ---------- status servo ----------
int  servoPos    = SUDUT_TUTUP_PENUH;
int  servoTarget = SUDUT_TUTUP_PENUH;
bool autoMode    = true;
const char* statusStr = "Menunggu";
unsigned long lastKontrol = 0;

// ---------- data photodiode (diisi oleh ISR) ----------
volatile unsigned long totalTetes = 0;
volatile unsigned long waktuTetesTerakhir = 0;
volatile unsigned long intervalTetes = 0;
volatile bool adaTetesBaru = false;

unsigned long ivBuf[IV_N];
int ivCount = 0;
int ivIdx   = 0;

// ---------- data load cell ----------
float calFactor  = 0;    // raw per gram (0 = belum kalibrasi)
long  baseOffset = 0;    // nol sebenarnya (timbangan kosong saat dinyalakan)
long  tareOffset = 0;    // nol hasil tare
bool  baseSet    = false;
long  rawBuf[BUF_N];
int   rawCount = 0;
int   rawIdx   = 0;
unsigned long lastSampleMs = 0;
float smooth = 0;
bool  firstReading = true;
float beratTampil = 0;   // berat setelah tare (g)
float gross = 0;         // berat total sebenarnya (g)
long  rawNet = 0;
bool  overload = false;
bool  armed = false;
bool  alarmHabis = false;

// =====================================================================
//  ISR PHOTODIODE
// =====================================================================
void IRAM_ATTR deteksiTetesISR() {
  unsigned long sekarang = millis();
  unsigned long selisih = sekarang - waktuTetesTerakhir;
  if (selisih >= DEBOUNCE_MS) {
    if (waktuTetesTerakhir > 0) intervalTetes = selisih;
    waktuTetesTerakhir = sekarang;
    totalTetes++;
    adaTetesBaru = true;
  }
}

unsigned long bacaWaktuTetes() {
  noInterrupts();
  unsigned long t = waktuTetesTerakhir;
  interrupts();
  return t;
}

unsigned long timeoutAktif() {
  unsigned long t = TIMEOUT_MIN_MS;
  if (targetTPM > 0) {
    unsigned long t3 = 180000UL / (unsigned long)targetTPM;  // 3x interval target
    if (t3 > t) t = t3;
  }
  return t;
}

// Proses tetesan baru -> simpan interval ke buffer
void prosesTetes() {
  unsigned long total, interval;
  bool baru;
  noInterrupts();
  total = totalTetes;
  interval = intervalTetes;
  baru = adaTetesBaru;
  adaTetesBaru = false;
  interrupts();

  if (!baru) return;
  if (interval == 0) return;                 // tetes pertama, belum ada interval
  if (interval > timeoutAktif()) {           // aliran baru mulai lagi setelah berhenti
    ivCount = 0;
    ivIdx = 0;
    return;
  }
  ivBuf[ivIdx] = interval;
  ivIdx = (ivIdx + 1) % IV_N;
  if (ivCount < IV_N) ivCount++;

  Serial.print("Tetes ke-");
  Serial.print(total);
  Serial.print(" | interval ");
  Serial.print(interval);
  Serial.println(" ms");
}

// Hitung TPM dari rata-rata interval terakhir
float getTPM() {
  if (ivCount == 0) return 0;
  unsigned long sum = 0;
  for (int i = 0; i < ivCount; i++) sum += ivBuf[i];
  float avg = (float)sum / ivCount;
  float tpm = 60000.0 / avg;

  // Jika tetes terakhir sudah lama, turunkan estimasi agar cepat bereaksi
  unsigned long sejak = millis() - bacaWaktuTetes();
  if (sejak > avg * 1.5) {
    float tpmMax = 60000.0 / (float)sejak;
    if (tpmMax < tpm) tpm = tpmMax;
  }
  return tpm;
}

// =====================================================================
//  SERVO (non-blocking, gerak halus)
// =====================================================================
void updateServo() {
  static unsigned long t = 0;
  if (servoPos == servoTarget) return;
  if (millis() - t < SERVO_STEP_MS) return;
  t = millis();
  servoPos += (servoTarget > servoPos) ? 1 : -1;
  servoInfus.write(servoPos);
}

// Koreksi otomatis sudut servo berdasarkan TPM
void kontrolTetesan() {
  if (!autoMode) { statusStr = "Manual"; return; }

  if (alarmHabis) {
    servoTarget = SUDUT_TUTUP_PENUH;   // jepit penuh agar udara tidak masuk selang
    statusStr = "HABIS";
    return;
  }

  unsigned long now = millis();
  if (now - lastKontrol < KONTROL_MS) return;
  lastKontrol = now;

  unsigned long last = bacaWaktuTetes();
  unsigned long tout = timeoutAktif();
  float tpm;
  bool henti = false;

  if (now - last > tout) {          // tidak ada tetes terlalu lama
    tpm = 0;
    henti = true;
  } else if (ivCount >= 2) {        // data cukup
    tpm = getTPM();
  } else {
    statusStr = "Menunggu";         // tunggu data tetesan
    return;
  }

  int selisih = (int)roundf(tpm) - targetTPM;

  if (abs(selisih) <= TOLERANSI_TPM) {
    statusStr = "Stabil";
    Serial.println("[STATUS] Tetesan stabil sesuai target.");
    return;
  }

  int langkah = map(abs(selisih), 1, 30, 1, 8);
  langkah = constrain(langkah, 1, 10);

  int sudutBaru = servoTarget;
  if (selisih > 0) {
    sudutBaru += langkah;           // terlalu cepat -> jepit lebih rapat
    statusStr = "Cepat";
    Serial.print("[ADJUST] Terlalu cepat (+");
    Serial.print(selisih);
    Serial.print(" TPM). Menjepit ke sudut: ");
  } else {
    sudutBaru -= langkah;           // terlalu lambat -> longgarkan
    statusStr = henti ? "HENTI" : "Lambat";
    Serial.print("[ADJUST] Terlalu lambat (");
    Serial.print(selisih);
    Serial.print(" TPM). Melonggarkan ke sudut: ");
  }
  sudutBaru = constrain(sudutBaru, SUDUT_BUKA_PENUH, SUDUT_TUTUP_PENUH);
  Serial.println(sudutBaru);
  servoTarget = sudutBaru;

  if (henti && servoPos <= SUDUT_BUKA_PENUH) {
    statusStr = "Cek selg";
    Serial.println("[PERINGATAN] Servo sudah terbuka penuh tapi tidak ada tetesan. Cek selang/kantong.");
  }
}

// =====================================================================
//  LOAD CELL
// =====================================================================
bool readRawBlocking(long &result, int n) {
  long v[24];
  int count = 0;
  if (n > 24) n = 24;
  for (int i = 0; i < n; i++) {
    unsigned long t0 = millis();
    while (!scale.is_ready()) {
      if (millis() - t0 > 1000) break;
      delay(1);
    }
    if (!scale.is_ready()) return false;
    v[count++] = scale.read();
  }
  if (count < 5) return false;
  for (int i = 1; i < count; i++) {
    long key = v[i];
    int j = i - 1;
    while (j >= 0 && v[j] > key) { v[j + 1] = v[j]; j--; }
    v[j + 1] = key;
  }
  long long sum = 0;
  int used = 0;
  for (int i = 2; i < count - 2; i++) { sum += v[i]; used++; }
  result = (long)(sum / used);
  return true;
}

bool ambilDariBuffer(long &result) {
  if (rawCount < 5) return false;
  long v[BUF_N];
  for (int i = 0; i < rawCount; i++) v[i] = rawBuf[i];
  for (int i = 1; i < rawCount; i++) {
    long key = v[i];
    int j = i - 1;
    while (j >= 0 && v[j] > key) { v[j + 1] = v[j]; j--; }
    v[j + 1] = key;
  }
  long long sum = 0;
  int used = 0;
  for (int i = 2; i < rawCount - 2; i++) { sum += v[i]; used++; }
  result = (long)(sum / used);
  return true;
}

void hitungBerat() {
  long raw;
  if (!ambilDariBuffer(raw)) return;

  if (!baseSet) {
    baseOffset = raw;
    tareOffset = raw;
    baseSet = true;
  }

  rawNet = raw - tareOffset;
  if (calFactor == 0) return;

  float net = (float)(raw - tareOffset) / calFactor;
  gross = (float)(raw - baseOffset) / calFactor;
  overload = gross > (MAX_GRAM + 50);

  if (firstReading || fabs(net - smooth) > JUMP_RESET) {
    smooth = net;
    firstReading = false;
  } else {
    smooth = EMA_ALPHA * net + (1.0 - EMA_ALPHA) * smooth;
  }
  beratTampil = (fabs(smooth) < ZERO_BAND) ? 0 : smooth;

  if (batasHabis > 0) {
    if (!armed && gross > batasHabis + 100) armed = true;
    if (armed && gross < batasHabis) alarmHabis = true;
    if (gross > batasHabis + 100) alarmHabis = false;
  } else {
    alarmHabis = false;
  }
}

void sampleScale() {
  if (!scale.is_ready()) return;
  rawBuf[rawIdx] = scale.read();
  rawIdx = (rawIdx + 1) % BUF_N;
  if (rawCount < BUF_N) rawCount++;
  lastSampleMs = millis();
  hitungBerat();
}

bool weightOk() {
  return (millis() - lastSampleMs) < 2000;
}

bool doTare() {
  long raw;
  if (!readRawBlocking(raw, 20)) return false;
  tareOffset = raw;
  firstReading = true;
  return true;
}

bool doCalibrate(float knownGram) {
  if (knownGram <= 0) return false;
  long raw;
  if (!readRawBlocking(raw, 20)) return false;
  float f = (float)(raw - tareOffset) / knownGram;
  if (fabs(f) < 1.0) return false;
  calFactor = f;
  prefs.putFloat("cal", calFactor);
  firstReading = true;
  return true;
}

// =====================================================================
//  LCD
// =====================================================================
void lcdLine(uint8_t row, const char* s) {
  char b[24];
  snprintf(b, sizeof(b), "%-16.16s", s);
  lcd.setCursor(0, row);
  lcd.print(b);
}

void updateLCD() {
  static unsigned long t = 0;
  if (millis() - t < 400) return;
  t = millis();

  char b[40];
  snprintf(b, sizeof(b), "TPM:%4.1f/%d S%3d", getTPM(), targetTPM, servoPos);
  lcdLine(0, b);

  if (alarmHabis)            lcdLine(1, "CAIRAN HABIS!");
  else if (!weightOk())      lcdLine(1, "HX711 ERROR");
  else if (calFactor == 0)   lcdLine(1, "Blm kalibrasi");
  else if (overload)         lcdLine(1, "OVERLOAD! maks5kg");
  else {
    snprintf(b, sizeof(b), "%5.0fg %s", beratTampil, statusStr);
    lcdLine(1, b);
  }
}

// =====================================================================
//  TOMBOL TARE
// =====================================================================
void handleButton() {
  static bool lastState = HIGH;
  static unsigned long lastChange = 0;
  bool state = digitalRead(TARE_PIN);

  if (state != lastState && millis() - lastChange > 50) {
    lastChange = millis();
    lastState = state;
    if (state == LOW) {
      lcdLine(0, "Tare...");
      lcdLine(1, "");
      bool ok = doTare();
      lcdLine(1, ok ? "Selesai: 0 g" : "Gagal!");
      Serial.println(ok ? "Tare OK" : "Tare GAGAL");
      delay(600);
    }
  }
}

// =====================================================================
//  PERINTAH SERIAL (BISA DIPANGGIL DARI SERIAL MONITOR MAUPUN MQTT)
// =====================================================================
void tampilkanBantuan() {
  Serial.println("=== PERINTAH ===");
  Serial.println("  s20     -> set target 20 tetes/menit");
  Serial.println("  f20     -> faktor tetes 20 (makro) / f60 (mikro)");
  Serial.println("  a60     -> servo manual ke sudut 60 (mode manual, untuk cari batas jepit)");
  Serial.println("  o       -> kembali ke mode otomatis");
  Serial.println("  t       -> tare (boleh ada beban)");
  Serial.println("  k500    -> kalibrasi load cell dgn beban 500 g (tare dulu saat kosong)");
  Serial.println("  z       -> set nol sebenarnya (timbangan kosong)");
  Serial.println("  h50     -> alarm cairan habis bila berat total < 50 g (h0 = nonaktif)");
  Serial.println("  r       -> lihat nilai raw load cell");
}

void prosesPerintahTeks(String cmd) {
  cmd.trim();
  if (cmd.length() == 0) return;

  char c = tolower(cmd[0]);
  String arg = cmd.substring(1);

  if (c == 's') {
    int v = arg.toInt();
    if (v >= 1 && v <= 200) {
      targetTPM = v;
      prefs.putInt("tpm", targetTPM);
      lastKontrol = 0;
      Serial.print("Target diset: ");
      Serial.print(targetTPM);
      Serial.println(" tetes/menit");
    } else {
      Serial.println("Target harus 1 - 200 TPM");
    }
  }
  else if (c == 'f') {
    float v = arg.toFloat();
    if (v == 20 || v == 60 || v == 15 || v == 10) {
      faktorTetes = v;
      prefs.putFloat("fk", faktorTetes);
      Serial.print("Faktor tetes: ");
      Serial.print(faktorTetes, 0);
      Serial.println(" tetes/mL");
    } else {
      Serial.println("Faktor umum: 10, 15, 20 (makro) atau 60 (mikro)");
    }
  }
  else if (c == 'a') {
    int v = arg.toInt();
    if (v >= 0 && v <= 180) {
      autoMode = false;
      servoTarget = v;
      Serial.print("Mode MANUAL. Servo ke sudut ");
      Serial.println(v);
    }
  }
  else if (c == 'o') {
    autoMode = true;
    lastKontrol = 0;
    Serial.println("Mode OTOMATIS aktif");
  }
  else if (c == 't') {
    Serial.println("Tare... jangan sentuh timbangan");
    Serial.println(doTare() ? "Tare OK" : "Tare GAGAL");
  }
  else if (c == 'k') {
    float g = arg.toFloat();
    if (doCalibrate(g)) {
      Serial.print("Kalibrasi OK, faktor = ");
      Serial.println(calFactor, 4);
    } else {
      Serial.println("Kalibrasi GAGAL. Pastikan beban terpasang & wiring benar.");
    }
  }
  else if (c == 'z') {
    long raw;
    if (readRawBlocking(raw, 20)) {
      baseOffset = raw;
      tareOffset = raw;
      baseSet = true;
      firstReading = true;
      Serial.println("Nol sebenarnya diset (timbangan harus kosong)");
    } else {
      Serial.println("Gagal baca HX711");
    }
  }
  else if (c == 'h') {
    batasHabis = arg.toFloat();
    prefs.putFloat("hbs", batasHabis);
    armed = false;
    alarmHabis = false;
    Serial.print("Batas habis: ");
    Serial.print(batasHabis, 0);
    Serial.println(" g");
  }
  else if (c == 'r') {
    long raw;
    if (readRawBlocking(raw, 9)) {
      Serial.print("Raw: "); Serial.print(raw);
      Serial.print(" | Tare: "); Serial.println(tareOffset);
    }
  }
  else {
    tampilkanBantuan();
  }
}

void handleSerial() {
  if (!Serial.available()) return;
  String cmd = Serial.readStringUntil('\n');
  prosesPerintahTeks(cmd);
}

// =====================================================================
//  WIFI & MQTT (MOSQUITTO BROKER)
// =====================================================================
void onMqttMessage(char* topic, byte* payload, unsigned int length) {
  StaticJsonDocument<512> doc;
  DeserializationError err = deserializeJson(doc, payload, length);

  // Jika pesan dari MQTT berupa teks mentah (misal "s25" atau "t")
  if (err) {
    String rawCmd = "";
    for (unsigned int i = 0; i < length; i++) rawCmd += (char)payload[i];
    prosesPerintahTeks(rawCmd);
    return;
  }

  Serial.println("[MQTT] Perintah diterima dari dashboard");

  if (doc.containsKey("target_tpm")) {
    int v = doc["target_tpm"];
    if (v >= 1 && v <= 200 && v != targetTPM) {
      targetTPM = v;
      prefs.putInt("tpm", targetTPM);
      lastKontrol = 0;
    }
  }

  if (doc.containsKey("faktor_tetes")) {
    float v = doc["faktor_tetes"];
    if ((v == 10 || v == 15 || v == 20 || v == 60) && v != faktorTetes) {
      faktorTetes = v;
      prefs.putFloat("fk", faktorTetes);
    }
  }

  if (doc.containsKey("batas_habis")) {
    float v = doc["batas_habis"];
    if (v >= 0 && v != batasHabis) {
      batasHabis = v;
      prefs.putFloat("hbs", batasHabis);
      armed = false;
      alarmHabis = false;
    }
  }

  if (doc.containsKey("auto_mode")) {
    bool newAuto = doc["auto_mode"];
    if (newAuto != autoMode) {
      autoMode = newAuto;
      if (autoMode) lastKontrol = 0;
    }
    if (!autoMode && doc.containsKey("servo_target")) {
      servoTarget = constrain((int)doc["servo_target"], 0, 180);
    }
  }

  String action = doc["action"] | "";
  if (action == "TARE") {
    doTare();
  } else if (action == "ZERO") {
    long raw;
    if (readRawBlocking(raw, 20)) {
      baseOffset = raw;
      tareOffset = raw;
      baseSet = true;
      firstReading = true;
    }
  } else if (action == "CALIBRATE") {
    float g = doc["calibrate_gram"] | 0.0;
    doCalibrate(g);
  } else if (action == "EMERGENCY_CLOSE") {
    autoMode = false;
    servoTarget = SUDUT_TUTUP_PENUH;
    Serial.println("[MQTT] TUTUP PENUH DARURAT (110 derajat)!");
  }
}

void maintainMQTT() {
  if (WiFi.status() != WL_CONNECTED) return;

  if (!mqtt.connected()) {
    unsigned long now = millis();
    if (now - lastMqttAttempt > 5000) {
      lastMqttAttempt = now;
      // Client ID unik wajib dipakai pada broker publik (test.mosquitto.org) agar tidak bentrok
      String clientId = String(DEVICE_ID) + "-" + String((uint32_t)esp_random(), HEX);
      Serial.print("[MQTT] Menghubungkan ke ");
      Serial.print(MQTT_BROKER);
      Serial.print("... ");

      if (mqtt.connect(clientId.c_str())) {
        Serial.println("TERHUBUNG!");
        mqtt.subscribe(TOPIC_COMMAND);
      } else {
        Serial.print("gagal (rc=");
        Serial.print(mqtt.state());
        Serial.println("), coba lagi 5 dtk");
      }
    }
  } else {
    mqtt.loop();
  }
}

void publishTelemetryMQTT() {
  static unsigned long t = 0;
  if (millis() - t < 1000) return;
  t = millis();

  if (!mqtt.connected()) return;

  float tpm = getTPM();
  float mlJam = (tpm * 60.0) / faktorTetes;

  StaticJsonDocument<512> doc;
  doc["device_id"]    = DEVICE_ID;
  doc["actual_tpm"]   = roundf(tpm * 10.0) / 10.0;
  doc["target_tpm"]   = targetTPM;
  doc["ml_jam"]       = roundf(mlJam * 10.0) / 10.0;
  doc["interval_ms"]  = intervalTetes;
  doc["total_tetes"]  = totalTetes;
  doc["servo_pos"]    = servoPos;
  doc["servo_target"] = servoTarget;
  doc["auto_mode"]    = autoMode;
  doc["status_str"]   = statusStr;
  doc["berat_tampil"] = roundf(beratTampil * 10.0) / 10.0;
  doc["gross"]        = roundf(gross * 10.0) / 10.0;
  doc["raw_net"]      = rawNet;
  doc["cal_factor"]   = calFactor;
  doc["batas_habis"]  = batasHabis;
  doc["faktor_tetes"] = faktorTetes;
  doc["weight_ok"]    = weightOk();
  doc["overload"]     = overload;
  doc["alarm_habis"]  = alarmHabis;
  doc["armed"]        = armed;

  char jsonBuffer[512];
  serializeJson(doc, jsonBuffer);
  mqtt.publish(TOPIC_TELEMETRY, jsonBuffer);
}

// Log ringkas tiap 1 detik
void logSerial() {
  static unsigned long t = 0;
  if (millis() - t < 1000) return;
  t = millis();
  float tpm = getTPM();
  float mlJam = (tpm * 60.0) / faktorTetes;
  Serial.print("TPM ");
  Serial.print(tpm, 1);
  Serial.print("/");
  Serial.print(targetTPM);
  Serial.print(" | ");
  Serial.print(mlJam, 1);
  Serial.print(" mL/jam | Servo ");
  Serial.print(servoPos);
  Serial.print(" | Berat ");
  if (calFactor == 0) {
    Serial.print("raw ");
    Serial.print(rawNet);
  } else {
    Serial.print(beratTampil, 1);
    Serial.print(" g");
  }
  Serial.print(" | ");
  Serial.print(statusStr);
  Serial.print(" | MQTT: ");
  Serial.println(mqtt.connected() ? "OK" : "OFF");
}

// =====================================================================
//  SETUP & LOOP
// =====================================================================
void setup() {
  Serial.begin(115200);
  Serial.setTimeout(50);
  Wire.begin(SDA_PIN, SCL_PIN);
  pinMode(TARE_PIN, INPUT_PULLUP);
  pinMode(PIN_PHOTODIODE, INPUT);

  lcd.init();
  lcd.backlight();
  lcdLine(0, "Infus Otomatis");
  lcdLine(1, "Pemanasan 5 dtk");

  // Muat pengaturan tersimpan
  prefs.begin("infus", false);
  calFactor   = prefs.getFloat("cal", 0);
  targetTPM   = prefs.getInt("tpm", 20);
  faktorTetes = prefs.getFloat("fk", 20.0);
  batasHabis  = prefs.getFloat("hbs", 50.0);

  // Servo: mulai dari posisi tertutup agar tidak mengalir bebas
  ESP32PWM::allocateTimer(0);
  servoInfus.setPeriodHertz(50);
  servoInfus.attach(PIN_SERVO, 500, 2400);
  servoInfus.write(SUDUT_TUTUP_PENUH);
  servoPos = SUDUT_TUTUP_PENUH;
  servoTarget = SUDUT_TUTUP_PENUH;

  scale.begin(DT_PIN, SCK_PIN);
  delay(5000);  // JANGAN taruh beban saat pemanasan

  long raw;
  if (readRawBlocking(raw, 20)) {
    baseOffset = raw;
    tareOffset = raw;
    baseSet = true;
  } else {
    lcdLine(0, "HX711 ERROR!");
    lcdLine(1, "Cek kabel DT/SCK");
    Serial.println("HX711 tidak terdeteksi. Cek DT, SCK, VCC, GND.");
    delay(2000);
  }

  // Aktifkan deteksi tetesan setelah pemanasan selesai
  attachInterrupt(digitalPinToInterrupt(PIN_PHOTODIODE), deteksiTetesISR, FALLING);

  // Mulai koneksi WiFi (non-blocking) & set broker Mosquitto
  WiFi.mode(WIFI_STA);
  WiFi.begin(WIFI_SSID, WIFI_PASS);
  mqtt.setServer(MQTT_BROKER, MQTT_PORT);
  mqtt.setBufferSize(512);
  mqtt.setCallback(onMqttMessage);

  lcd.clear();
  Serial.println("=== INFUS OTOMATIS SIAP ===");
  Serial.print("Menghubungkan ke WiFi: ");
  Serial.println(WIFI_SSID);
  Serial.print("Target: ");
  Serial.print(targetTPM);
  Serial.println(" TPM. Kirim huruf apa saja untuk daftar perintah.");
  if (calFactor == 0) {
    Serial.println("Load cell BELUM KALIBRASI. Taruh beban diketahui, kirim: k<gram>, contoh k500");
  }
}

void loop() {
  handleButton();
  handleSerial();
  sampleScale();
  prosesTetes();
  kontrolTetesan();
  updateServo();
  updateLCD();
  maintainMQTT();
  publishTelemetryMQTT();
  logSerial();
}