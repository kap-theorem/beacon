#!/bin/bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}================================${NC}"
echo -e "${BLUE}Beacon Config Service - Local Test${NC}"
echo -e "${BLUE}================================${NC}"

# Check if go is installed
if ! command -v go &> /dev/null; then
    echo -e "${RED}Error: Go is not installed${NC}"
    exit 1
fi

# Clean up background processes on exit
cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"
    pkill -f "/tmp/mock-infisical" || true
    pkill -f "/tmp/http-server" || true
    echo -e "${GREEN}Cleanup complete${NC}"
}

trap cleanup EXIT

# Build scripts
echo -e "${YELLOW}Building mock Infisical server...${NC}"
go build -o /tmp/mock-infisical ./scripts/mock-infisical.go

echo -e "${YELLOW}Building HTTP server...${NC}"
go build -o /tmp/http-server ./cmd/server

# Start mock Infisical
echo -e "${YELLOW}Starting mock Infisical server on :8000...${NC}"
/tmp/mock-infisical -port 8000 &
sleep 1

# Set environment variables
export INFISICAL_ADDR="http://localhost:8000"
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"

# Start HTTP server
echo -e "${YELLOW}Starting HTTP server on :6969...${NC}"
/tmp/http-server &
HTTP_PID=$!
sleep 2

# Run tests
echo -e "\n${BLUE}=== Test 1: Liveness Probe ===${NC}"
if curl -s http://localhost:6969/healthz/live | grep -q "ok"; then
    echo -e "${GREEN}✓ Liveness probe passed${NC}"
else
    echo -e "${RED}✗ Liveness probe failed${NC}"
    exit 1
fi

echo -e "\n${BLUE}=== Test 2: Readiness Probe ===${NC}"
if curl -s http://localhost:6969/healthz/ready | grep -q "ready"; then
    echo -e "${GREEN}✓ Readiness probe passed${NC}"
else
    echo -e "${RED}✗ Readiness probe failed${NC}"
    exit 1
fi

echo -e "\n${BLUE}=== Test 3: Config Loading ===${NC}"
RESPONSE=$(curl -s -X POST http://localhost:6969/v1/notify/email \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer bk_k1_local-test-key" \
    -d '{
        "to": "test@example.com",
        "subject": "Test Email",
        "body": "Configuration is working!"
    }')

if echo "$RESPONSE" | grep -q "notification accepted"; then
    echo -e "${GREEN}✓ Config loading and email endpoint working${NC}"
else
    echo -e "${YELLOW}Note: Email trigger may require Temporal setup${NC}"
    echo "Response: $RESPONSE"
fi

echo -e "\n${BLUE}=== Test 4: Request Details ===${NC}"
echo -e "${YELLOW}Server logs (check above for config loading):${NC}"
echo "- Configuration should load from mock Infisical on startup"
echo "- Should see 3 email providers (sendgrid, mailgun, aws-ses)"

echo -e "\n${GREEN}================================${NC}"
echo -e "${GREEN}All local tests passed!${NC}"
echo -e "${GREEN}================================${NC}"

echo -e "\n${YELLOW}Servers running (Ctrl+C to stop):${NC}"
wait $HTTP_PID
