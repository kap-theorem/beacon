package main

import (
	"beacon/internal/models"
	"beacon/internal/temporal"
	"context"
	"log"

	"go.temporal.io/sdk/client"
)

const EMAIL_TASK_QUEUE = "email-task-queue"

func main() {
	// Create Temporal client
	c, err := client.Dial(client.Options{
		HostPort: client.DefaultHostPort,
	})
	if err != nil {
		log.Fatalln("Unable to create Temporal client", err)
	}
	defer c.Close()

	// Create email request
	emailReq := &models.EmailMessage{
		To:      "user@example.com",
		Subject: "Test Email from Beacon",
		Body:    "This is a test email sent via Temporal workflow",
	}

	// Start workflow execution
	workflowOptions := client.StartWorkflowOptions{
		ID:        "email-workflow-1",
		TaskQueue: EMAIL_TASK_QUEUE,
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
