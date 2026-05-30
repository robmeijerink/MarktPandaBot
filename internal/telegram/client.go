package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

func DispatchTelegramAlert(token, chatID, msg string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]string{
		"chat_id":    chatID,
		"text":       msg,
		"parse_mode": "Markdown",
	}
	jsonPayload, _ := json.Marshal(payload)
	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		log.Printf("Failed to send HTTP request to Telegram: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("Telegram API returned non-200 status code: %d", resp.StatusCode)
	}
}
