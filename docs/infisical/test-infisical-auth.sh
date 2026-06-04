#!/bin/bash
set -e

echo "=== Infisical Machine Identity Authentication Test ==="
echo ""

# Load environment variables from .env
if [ ! -f .env ]; then
    echo "❌ .env file not found"
    exit 1
fi

source .env

# Validate required variables
echo "1️⃣  Validating environment variables..."
if [ -z "$INFISICAL_ADDR" ]; then
    echo "❌ INFISICAL_ADDR not set"
    exit 1
fi
if [ -z "$INFISICAL_PROJECT_ID" ]; then
    echo "❌ INFISICAL_PROJECT_ID not set"
    exit 1
fi
if [ -z "$INFISICAL_CLIENT_ID" ]; then
    echo "❌ INFISICAL_CLIENT_ID not set"
    exit 1
fi
if [ -z "$INFISICAL_CLIENT_SECRET" ]; then
    echo "❌ INFISICAL_CLIENT_SECRET not set"
    exit 1
fi

echo "✅ All environment variables set"
echo "   INFISICAL_ADDR: $INFISICAL_ADDR"
echo "   INFISICAL_PROJECT_ID: $INFISICAL_PROJECT_ID"
echo "   INFISICAL_CLIENT_ID: ${INFISICAL_CLIENT_ID:0:20}..."
echo ""

# Step 1: Test login endpoint
echo "2️⃣  Testing login endpoint..."
echo "   POST $INFISICAL_ADDR/api/v1/auth/universal-auth/login"

LOGIN_RESPONSE=$(curl -s -X POST \
  "$INFISICAL_ADDR/api/v1/auth/universal-auth/login" \
  -H "Content-Type: application/json" \
  -d '{
    "clientId": "'$INFISICAL_CLIENT_ID'",
    "clientSecret": "'$INFISICAL_CLIENT_SECRET'"
  }')

echo "   Response: $LOGIN_RESPONSE"
echo ""

# Check if login was successful
if echo "$LOGIN_RESPONSE" | grep -q "accessToken"; then
    echo "✅ Login successful, received access token"

    # Extract the access token
    ACCESS_TOKEN=$(echo "$LOGIN_RESPONSE" | grep -o '"accessToken":"[^"]*' | cut -d'"' -f4)
    TOKEN_EXPIRES=$(echo "$LOGIN_RESPONSE" | grep -o '"expiresIn":[0-9]*' | cut -d':' -f2)

    echo "   Token: ${ACCESS_TOKEN:0:20}..."
    echo "   Expires in: $TOKEN_EXPIRES seconds"
    echo ""

    # Step 2: Test secrets API
    echo "3️⃣  Testing secrets API..."
    echo "   GET $INFISICAL_ADDR/api/v4/secrets?projectId=$INFISICAL_PROJECT_ID&environment=prod&secretPath=/beacon/smtp"

    SECRETS_RESPONSE=$(curl -s -X GET \
      -H "Authorization: Bearer $ACCESS_TOKEN" \
      "$INFISICAL_ADDR/api/v4/secrets?projectId=$INFISICAL_PROJECT_ID&environment=prod&secretPath=/beacon/smtp")

    echo "   Response: $SECRETS_RESPONSE"
    echo ""

    # Check if we got secrets
    if echo "$SECRETS_RESPONSE" | grep -q '"secrets"'; then
        echo "✅ Secrets API successful!"

        # Count the number of secrets
        SECRET_COUNT=$(echo "$SECRETS_RESPONSE" | grep -o '"secretKey"' | wc -l)
        echo "   Found $SECRET_COUNT secret(s)"
        echo ""
        echo "🎉 Authentication flow works! Beacon can authenticate with Infisical"
    else
        echo "❌ Secrets API returned an error:"
        echo "$SECRETS_RESPONSE"
    fi
else
    echo "❌ Login failed:"
    echo "$LOGIN_RESPONSE"
    exit 1
fi
