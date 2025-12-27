package main

import (
	"beacon/internal/config"
	"beacon/internal/notifier"
	"beacon/internal/temporal"
	"log"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
	"go.temporal.io/sdk/worker"
)

func main() {
	// load email notifier config
	cfg := config.LoadEmailNotifierConfig()

	// create Temporal client
	c, err := client.Dial(envconfig.MustLoadDefaultClientOptions())
	if err != nil {
		log.Fatalln("Unable to create Temporal client", err)
	}
	defer c.Close()

	// create temporal worker
	w := worker.New(c, cfg.EmailNotifierTaskQueue, worker.Options{})

	// create email service
	emailActivities := &temporal.EmailActivities{
		EmailService: notifier.NewEmailService(
			cfg.SMTPServer,
			cfg.SMTPPort,
			cfg.SMTPUsername,
			cfg.SMTPPassword,
		),
	}

	// register workflows and activities
	w.RegisterWorkflow(temporal.SendEmailWorkflow)
	w.RegisterActivity(emailActivities.SendEmailActivity)

	log.Println("Starting email worker on task queue:", cfg.EmailNotifierTaskQueue)
	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}
}
