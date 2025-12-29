package main

import (
	"beacon/config"
	"beacon/internal/api"
	"beacon/utils"
	"log"
	"net/http"

	"go.temporal.io/sdk/client"
)

func main() {
	// setup email handler
	temporalClient := getTemporalClient()
	defer temporalClient.Close()
	email := &api.EmailHandler{
		TemporalClient:         temporalClient,
		EmailNotifierTaskQueue: config.LoadEmailNotifierConfig().EmailNotifierTaskQueue,
	}
	// setup routes
	mux := http.NewServeMux()
	mux.HandleFunc("/notify/email", email.HandlerRequest)
	// start HTTP server
	log.Fatal(http.ListenAndServe(":6969", mux))
}

// getTemporalClient creates and returns a Temporal client
func getTemporalClient() client.Client {
	temporalClient, err := utils.NewTemporalClient()
	if err != nil {
		panic("Unable to create Temporal client: " + err.Error())
	}
	return temporalClient
}
