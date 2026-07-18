package utils

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWriteJSON_DirectCall exercises WriteJSON directly with a hand-crafted
// APIResponse so that the function is covered independently of the helpers.
func TestWriteJSON_DirectCall(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		resp       APIResponse
		wantCode   int
		wantCT     string
		wantFields APIResponse
	}{
		{
			name:   "200 success payload",
			status: http.StatusOK,
			resp: APIResponse{
				Success: true,
				Message: "hello",
				Data:    "world",
			},
			wantCode:   http.StatusOK,
			wantCT:     "application/json",
			wantFields: APIResponse{Success: true, Message: "hello"},
		},
		{
			name:   "404 error payload",
			status: http.StatusNotFound,
			resp: APIResponse{
				Success: false,
				Error:   "not found",
			},
			wantCode:   http.StatusNotFound,
			wantCT:     "application/json",
			wantFields: APIResponse{Success: false, Error: "not found"},
		},
		{
			name:   "500 internal error",
			status: http.StatusInternalServerError,
			resp: APIResponse{
				Success: false,
				Error:   "internal server error",
			},
			wantCode:   http.StatusInternalServerError,
			wantCT:     "application/json",
			wantFields: APIResponse{Success: false, Error: "internal server error"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteJSON(rec, tc.status, tc.resp)

			if rec.Code != tc.wantCode {
				t.Fatalf("status code: got %d, want %d", rec.Code, tc.wantCode)
			}
			ct := rec.Header().Get("Content-Type")
			if ct != tc.wantCT {
				t.Fatalf("Content-Type: got %q, want %q", ct, tc.wantCT)
			}
			var decoded APIResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("failed to decode response body: %v", err)
			}
			if decoded.Success != tc.wantFields.Success {
				t.Errorf("Success: got %v, want %v", decoded.Success, tc.wantFields.Success)
			}
			if tc.wantFields.Message != "" && decoded.Message != tc.wantFields.Message {
				t.Errorf("Message: got %q, want %q", decoded.Message, tc.wantFields.Message)
			}
			if tc.wantFields.Error != "" && decoded.Error != tc.wantFields.Error {
				t.Errorf("Error: got %q, want %q", decoded.Error, tc.wantFields.Error)
			}
		})
	}
}

// TestWriteSuccess verifies the convenience wrapper for successful responses.
func TestWriteSuccess(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		message  string
		data     any
		wantCode int
		wantMsg  string
		wantSucc bool
	}{
		{
			name:     "202 accepted with map data",
			status:   http.StatusAccepted,
			message:  "ok",
			data:     map[string]string{"k": "v"},
			wantCode: http.StatusAccepted,
			wantMsg:  "ok",
			wantSucc: true,
		},
		{
			name:     "200 ok with string data",
			status:   http.StatusOK,
			message:  "created",
			data:     "some-id",
			wantCode: http.StatusOK,
			wantMsg:  "created",
			wantSucc: true,
		},
		{
			name:     "201 created with nil data",
			status:   http.StatusCreated,
			message:  "resource created",
			data:     nil,
			wantCode: http.StatusCreated,
			wantMsg:  "resource created",
			wantSucc: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteSuccess(rec, tc.status, tc.message, tc.data)

			if rec.Code != tc.wantCode {
				t.Fatalf("status code: got %d, want %d", rec.Code, tc.wantCode)
			}
			ct := rec.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Fatalf("Content-Type: got %q, want \"application/json\"", ct)
			}
			var resp APIResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to decode response body: %v", err)
			}
			if resp.Success != tc.wantSucc {
				t.Errorf("Success: got %v, want %v", resp.Success, tc.wantSucc)
			}
			if resp.Message != tc.wantMsg {
				t.Errorf("Message: got %q, want %q", resp.Message, tc.wantMsg)
			}
			// Error field must be absent on success responses.
			if resp.Error != "" {
				t.Errorf("Error field should be empty on success response, got %q", resp.Error)
			}
		})
	}
}

// TestWriteError verifies the convenience wrapper for error responses.
func TestWriteError(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		message  string
		wantCode int
		wantErr  string
		wantSucc bool
	}{
		{
			name:     "400 bad request",
			status:   http.StatusBadRequest,
			message:  "invalid input",
			wantCode: http.StatusBadRequest,
			wantErr:  "invalid input",
			wantSucc: false,
		},
		{
			name:     "401 unauthorized",
			status:   http.StatusUnauthorized,
			message:  "unauthorized",
			wantCode: http.StatusUnauthorized,
			wantErr:  "unauthorized",
			wantSucc: false,
		},
		{
			name:     "500 internal server error",
			status:   http.StatusInternalServerError,
			message:  "something went wrong",
			wantCode: http.StatusInternalServerError,
			wantErr:  "something went wrong",
			wantSucc: false,
		},
		{
			name:     "404 not found",
			status:   http.StatusNotFound,
			message:  "resource not found",
			wantCode: http.StatusNotFound,
			wantErr:  "resource not found",
			wantSucc: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteError(rec, tc.status, tc.message)

			if rec.Code != tc.wantCode {
				t.Fatalf("status code: got %d, want %d", rec.Code, tc.wantCode)
			}
			ct := rec.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Fatalf("Content-Type: got %q, want \"application/json\"", ct)
			}
			var resp APIResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to decode response body: %v", err)
			}
			if resp.Success != tc.wantSucc {
				t.Errorf("Success: got %v, want %v", resp.Success, tc.wantSucc)
			}
			if resp.Error != tc.wantErr {
				t.Errorf("Error: got %q, want %q", resp.Error, tc.wantErr)
			}
			// Message field must be absent on pure error responses.
			if resp.Message != "" {
				t.Errorf("Message field should be empty on error response, got %q", resp.Message)
			}
		})
	}
}

// TestAPIResponse_JSONRoundtrip ensures the struct serializes and deserializes
// correctly for all field combinations.
func TestAPIResponse_JSONRoundtrip(t *testing.T) {
	tests := []struct {
		name  string
		input APIResponse
	}{
		{
			name:  "success with all fields",
			input: APIResponse{Success: true, Message: "msg", Data: "data", Error: ""},
		},
		{
			name:  "error with no data",
			input: APIResponse{Success: false, Error: "err msg"},
		},
		{
			name:  "zero value",
			input: APIResponse{},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.input)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var out APIResponse
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if out.Success != tc.input.Success {
				t.Errorf("Success mismatch: got %v, want %v", out.Success, tc.input.Success)
			}
			if out.Message != tc.input.Message {
				t.Errorf("Message mismatch: got %q, want %q", out.Message, tc.input.Message)
			}
			if out.Error != tc.input.Error {
				t.Errorf("Error mismatch: got %q, want %q", out.Error, tc.input.Error)
			}
		})
	}
}
