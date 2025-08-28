package retry

import (
	"context"
	"fmt"
	"log"
	"time"
)

type Config struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
}

func DefaultConfig() *Config {
	return &Config{
		MaxRetries: 3,
		BaseDelay:  time.Second,
		MaxDelay:   time.Minute,
	}
}

func WithRetry(ctx context.Context, cfg *Config, operation func() error) error {
	var lastErr error
	
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * cfg.BaseDelay
			if delay > cfg.MaxDelay {
				delay = cfg.MaxDelay
			}
			
			log.Printf("Retrying operation in %v (attempt %d/%d)", delay, attempt, cfg.MaxRetries)
			
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		
		if err := operation(); err != nil {
			lastErr = err
			log.Printf("Operation failed (attempt %d/%d): %v", attempt+1, cfg.MaxRetries+1, err)
			continue
		}
		
		return nil
	}
	
	return fmt.Errorf("operation failed after %d retries: %w", cfg.MaxRetries, lastErr)
}