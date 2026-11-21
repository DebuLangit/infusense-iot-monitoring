package main

import (
	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	log.Println("=== Memulai MediFlow ESP32 Gateway (Go Fiber + MQTT + MySQL) ===")
	InitDB()
	InitMQTT()

	app := fiber.New(fiber.Config{
		AppName: "MediFlow ESP32 — Sistem Infus Otomatis",
	})

	app.Use(cors.New())
	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${method} ${path} (${latency})\n",
	}))

	app.Static("/", "./public")
	SetupRoutes(app)

	port := getEnv("APP_PORT", "3001")
	log.Printf("[HTTP] Dashboard siap diakses di http://localhost:%s", port)
	log.Fatal(app.Listen(":" + port))
}
