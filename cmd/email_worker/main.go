package main

import (
	"beacon/internal/config"
	"beacon/internal/notifier"
	"beacon/internal/temporal"
	"log"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func main() {
	// create Temporal client
	c, err := client.Dial(client.Options{
		HostPort: client.DefaultHostPort,
	})
	if err != nil {
		log.Fatalln("Unable to create Temporal client", err)
	}
	defer c.Close()

	// create temporal worker
	emailNotifierConfig := config.GetTemporalConfig()
	w := worker.New(c, emailNotifierConfig.EmailNotifierTaskQueue, worker.Options{})

	// create email service
	emailServiceConfig := config.GetEmailServiceConfig()
	emailActivities := &temporal.EmailActivities{
		EmailService: notifier.NewEmailService(
			emailServiceConfig.SMTPServer,
			emailServiceConfig.SMTPPort,
			emailServiceConfig.EmailUsername,
			emailServiceConfig.EmailPassword,
		),
	}

	// register workflows and activities
	w.RegisterWorkflow(temporal.SendEmailWorkflow)
	w.RegisterActivity(emailActivities.SendEmailActivity)

	log.Println("Starting email worker on task queue:", emailNotifierConfig.EmailNotifierTaskQueue)
	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}
}
