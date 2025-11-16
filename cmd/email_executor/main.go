package main

import (
	"beacon/internal/models"
	"beacon/internal/temporal"
	"context"
	"log"

	"github.com/joho/godotenv"
	"go.temporal.io/sdk/client"
)

func main() {
	// load envs
	envFile, err := godotenv.Read(".env.mail.notifier")
	if err != nil {
		log.Fatalln("Error loading env file", err)
	}

	// create Temporal client
	c, err := client.Dial(client.Options{
		HostPort: client.DefaultHostPort,
	})
	if err != nil {
		log.Fatalln("Unable to create Temporal client", err)
	}
	defer c.Close()

	// create sample email request
	emailReq := &models.EmailMessage{
		To:      envFile["SAMPLE_EMAIL_TO"],
		Subject: "Test Email from Beacon",
		Body:    "Hi! this is a test email from Beacon. Have a great day!",
	}

	// start workflow execution
	workflowOptions := client.StartWorkflowOptions{
		ID:        "email-workflow-" + envFile["SAMPLE_EMAIL_TO"],
		TaskQueue: envFile["TEMPORAL_EMAIL_NOTIFIER_TASK_QUEUE"],
	}
	we, err := c.ExecuteWorkflow(context.Background(), workflowOptions, temporal.SendEmailWorkflow, emailReq)
	if err != nil {
		log.Fatalln("Unable to execute workflow", err)
	}
	log.Printf("Started workflow with ID: %s, RunID: %s\n", we.GetID(), we.GetRunID())

	// Wait for workflow to complete
	var result interface{}
	err = we.Get(context.Background(), &result)
	if err != nil {
		log.Fatalln("Unable to get workflow result", err)
	}
	log.Println("Workflow completed successfully!")
}
