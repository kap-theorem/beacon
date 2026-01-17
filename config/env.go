package config

import (
	"fmt"
	"os"
	"strconv"
)

func GetInt(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}

	intValue, err := strconv.Atoi(value)
	if err != nil {
		panic(fmt.Errorf("environment variable %s=%q cannot be converted to an int", key, value))
	}
	return intValue
}

func GetString(key string, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
