package config

import (
	"log"
	"sync"

	"github.com/joho/godotenv"
)

// Config holds all application configuration
type Config struct {
	Temporal      *TemporalConfig
	EmailNotifier *EmailNotifierConfig
}

type TemporalConfig struct {
	EmailNotifierTaskQueue string
}

type EmailNotifierConfig struct {
	SMTPServer    string
	SMTPPort      int
	EmailUsername string
	EmailPassword string
}

var (
	instance *Config
	once     sync.Once
	envFile  string
)

// loadEnv loads environment variables from the specified file
func loadEnv(file string) {
	err := godotenv.Load(file)
	if err != nil {
		log.Fatalln("Error loading env file", file, err)
	}
}

// Initialize sets the env file path and should be called once at application startup
func Initialize(file string) {
	envFile = file
}

// GetInstance returns the singleton config instance
// It initializes the config on first call using the env file set by Initialize
func GetInstance() *Config {
	once.Do(func() {
		if envFile == "" {
			log.Fatalln("Config not initialized. Call config.Initialize(envFile) first")
		}
		loadEnv(envFile)
		instance = &Config{
			Temporal: &TemporalConfig{
				EmailNotifierTaskQueue: GetString("TEMPORAL_EMAIL_NOTIFIER_TASK_QUEUE", ""),
			},
			EmailNotifier: &EmailNotifierConfig{
				SMTPServer:    GetString("SMTP_SERVER", ""),
				SMTPPort:      GetInt("SMTP_PORT", 587),
				EmailUsername: GetString("USERNAME", ""),
				EmailPassword: GetString("PASSWORD", ""),
			},
		}
	})
	return instance
}

// GetTemporalConfig returns the temporal configuration (deprecated: use GetInstance().Temporal)
func GetTemporalConfig() *TemporalConfig {
	Initialize(".env.mail.notifier")
	return GetInstance().Temporal
}

// GetEmailServiceConfig returns the email service configuration (deprecated: use GetInstance().EmailNotifier)
func GetEmailServiceConfig() *EmailNotifierConfig {
	Initialize(".env.mail.notifier")
	return GetInstance().EmailNotifier
}
