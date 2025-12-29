package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Temporal Server configuration
type TemporalConfig struct {
	Address   string `yaml:"address"`
	Namespace string `yaml:"namespace"`
}

// Email Notifier configuration
type EmailNotifierConfig struct {
	TemporalConfig
	SMTPServer             string `yaml:"smtp_server"`
	SMTPPort               int    `yaml:"smtp_port"`
	SMTPUsername           string
	SMTPPassword           string
	EmailNotifierTaskQueue string `yaml:"task_queue"`
}

// loadEnv loads environment variables from the specified file
func loadEnv(file string) {
	err := godotenv.Load(file)
	if err != nil {
		log.Fatalln("Error loading env file", file, err)
	}
}

// loadTemporalConfig loads Temporal configuration from temporal.yaml
func loadTemporalConfig() *TemporalConfig {
	baseConfigYaml, err := os.ReadFile("config/temporal.yaml")
	if err != nil {
		log.Fatalln("Error reading base config file:", err)
	}
	var temporalConfig = &TemporalConfig{}
	yaml.Unmarshal(baseConfigYaml, &temporalConfig)
	return temporalConfig
}

// LoadEmailNotifierConfig loads Email Notifier configuration from
// email_worker.yaml and environment variables
func LoadEmailNotifierConfig() *EmailNotifierConfig {
	var emailNotifierConfig = &EmailNotifierConfig{}

	// Email Notifier requires temporal config as base
	temporalConfig := loadTemporalConfig()
	emailNotifierConfig.TemporalConfig = *temporalConfig

	// Load email notifier specific config from yaml
	emailConfigYaml, err := os.ReadFile("config/email_worker.yaml")
	if err != nil {
		log.Fatalln("Error reading email worker config file:", err)
	}
	yaml.Unmarshal(emailConfigYaml, &emailNotifierConfig)

	// Load sensitive info from environment variables
	loadEnv(".env.email_worker")
	emailNotifierConfig.SMTPUsername = GetString("SMTP_CLIENT_USERNAME", "")
	emailNotifierConfig.SMTPPassword = GetString("SMTP_CLIENT_PASSWORD", "")
	return emailNotifierConfig
}
