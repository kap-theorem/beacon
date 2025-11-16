package main

import (
	"beacon/internal/notifier"
	"beacon/internal/temporal"
	"log"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

const EMAIL_TASK_QUEUE = "email-task-queue"

func main() {
	c, err := client.Dial(client.Options{
		HostPort: client.DefaultHostPort,
	})
	if err != nil {
		log.Fatalln("Unable to create Temporal client", err)
	}
	defer c.Close()

	emailService := notifier.NewEmailService("smtp.example.com")

	w := worker.New(c, EMAIL_TASK_QUEUE, worker.Options{})

	emailActivities := &temporal.EmailActivites{
		EmailService: emailService,
	}

	// register workflows and activities
	w.RegisterWorkflow(temporal.SendEmailWorkflow)
	w.RegisterActivity(emailActivities.SendEmailActivity)

	log.Println("Starting email worker on task queue:", EMAIL_TASK_QUEUE)
	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}
}
