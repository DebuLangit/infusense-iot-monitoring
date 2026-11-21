package main

import (
	"encoding/json"
	"sync"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
)

var (
	wsClients = make(map[*websocket.Conn]bool)
	wsMutex   sync.Mutex
)

func BroadcastState(state DeviceStateResponse) {
	data, err := json.Marshal(state)
	if err != nil {
		return
	}

	wsMutex.Lock()
	defer wsMutex.Unlock()

	for client := range wsClients {
		if err := client.WriteMessage(websocket.TextMessage, data); err != nil {
			client.Close()
			delete(wsClients, client)
		}
	}
}

func SetupRoutes(app *fiber.App) {
	app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	app.Get("/ws", websocket.New(func(c *websocket.Conn) {
		wsMutex.Lock()
		wsClients[c] = true
		wsMutex.Unlock()

		if raw, err := json.Marshal(BuildDeviceState("ESP32-INFUS01")); err == nil {
			_ = c.WriteMessage(websocket.TextMessage, raw)
		}

		defer func() {
			wsMutex.Lock()
			delete(wsClients, c)
			wsMutex.Unlock()
			c.Close()
		}()

		for {
			if _, _, err := c.ReadMessage(); err != nil {
				break
			}
		}
	}))

	api := app.Group("/api")

	api.Get("/status", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":         "ok",
			"db_connected":   dbConnected,
			"mqtt_connected": mqttConnected,
		})
	})

	api.Get("/state/:id", func(c *fiber.Ctx) error {
		deviceID := c.Params("id")
		return c.JSON(BuildDeviceState(deviceID))
	})

	api.Post("/command/:id", func(c *fiber.Ctx) error {
		deviceID := c.Params("id")
		var cmd CommandPayload
		if err := c.BodyParser(&cmd); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Payload JSON tidak valid"})
		}
		if err := PublishCommand(deviceID, cmd); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(BuildDeviceState(deviceID))
	})

	api.Post("/telemetry", func(c *fiber.Ctx) error {
		var payload TelemetryPayload
		if err := c.BodyParser(&payload); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Payload telemetri tidak valid"})
		}
		ProcessIncomingTelemetry(payload)
		return c.JSON(fiber.Map{"status": "received"})
	})

	api.Get("/telemetry/:id", func(c *fiber.Ctx) error {
		deviceID := c.Params("id")
		return c.JSON(GetRecentTelemetry(deviceID, 100))
	})

	api.Get("/alarms", func(c *fiber.Ctx) error {
		return c.JSON(GetRecentAlarms(50))
	})
}
