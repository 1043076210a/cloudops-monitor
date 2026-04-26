package main

import (
	"encoding/json"
	"log"
	"time"
)

func logKV(level, message string, fields map[string]any) {
	entry := map[string]any{
		"ts":      time.Now().Format(time.RFC3339),
		"level":   level,
		"message": message,
	}
	for k, v := range fields {
		entry[k] = v
	}
	b, err := json.Marshal(entry)
	if err != nil {
		log.Printf("{\"level\":\"error\",\"message\":\"marshal_log_failed\",\"err\":%q}", err.Error())
		return
	}
	log.Println(string(b))
}
