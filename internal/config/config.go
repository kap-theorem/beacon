package config

import (
	"beacon/internal/providers/email"
	"beacon/internal/providers/push"
	"beacon/internal/providers/sms"
)

type Config struct {
	SMS   *sms.Config   `json:"sms,omitempty"`
	Email *email.Config `json:"email,omitempty"`
	Push  *push.Config  `json:"push,omitempty"`
	Queue QueueConfig   `json:"queue"`
}

type QueueConfig struct {
	Type     string            `json:"type"`
	Settings map[string]string `json:"settings"`
}

func DefaultConfig() *Config {
	return &Config{
		SMS: &sms.Config{
			APIKey:     "",
			APISecret:  "",
			BaseURL:    "https://api.twilio.com",
			FromNumber: "",
		},
		Email: &email.Config{
			SMTPHost:    "smtp.gmail.com",
			SMTPPort:    587,
			Username:    "",
			Password:    "",
			FromAddress: "",
			FromName:    "Beacon Notifications",
		},
		Push: &push.Config{
			FCMServerKey: "",
			APNSKeyID:    "",
			APNSTeamID:   "",
			APNSKey:      nil,
		},
		Queue: QueueConfig{
			Type: "mock",
			Settings: map[string]string{
				"url": "localhost:5672",
			},
		},
	}
}