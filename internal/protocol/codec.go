package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type responseV2Wire struct {
	Status        string           `json:"status"`
	Error         string           `json:"error,omitempty"`
	Retry         *bool            `json:"retry,omitempty"`
	Result        string           `json:"result,omitempty"`
	Events        []Event          `json:"events,omitempty"`
	StateUpdates  map[string]any   `json:"state_updates,omitempty"`
	Logs          []LogEntry       `json:"logs,omitempty"`
	StopwatchSubs []map[string]any `json:"ductile_stopwatch_subs,omitempty"`
}

func (w responseV2Wire) response() *Response {
	return &Response{
		Status:        w.Status,
		Error:         w.Error,
		Result:        w.Result,
		Events:        w.Events,
		StateUpdates:  w.StateUpdates,
		Logs:          w.Logs,
		StopwatchSubs: w.StopwatchSubs,
	}
}

func (w responseV2Wire) compat() ResponseCompat {
	return ResponseCompat{Retry: w.Retry}
}

func validateResponse(resp *Response) error {
	// Validate required fields
	if resp.Status == "" {
		return fmt.Errorf("response missing required field: status")
	}

	if resp.Status != "ok" && resp.Status != "error" {
		return fmt.Errorf("invalid status value: %q (must be 'ok' or 'error')", resp.Status)
	}

	// If status is error, error message should be present
	if resp.Status == "error" && resp.Error == "" {
		return fmt.Errorf("response has status=error but no error message")
	}
	if resp.Status == "ok" && strings.TrimSpace(resp.Result) == "" {
		return fmt.Errorf("response has status=ok but no result message")
	}

	return nil
}

// EncodeRequest serializes a Request to JSON and writes it to w.
// Returns an error if marshaling or writing fails.
func EncodeRequest(w io.Writer, req *Request) error {
	if req.Protocol != 2 {
		return fmt.Errorf("unsupported protocol version: %d", req.Protocol)
	}

	encoder := json.NewEncoder(w)
	if err := encoder.Encode(req); err != nil {
		return fmt.Errorf("failed to encode request: %w", err)
	}

	return nil
}

// DecodeResponseLenient is like DecodeResponse but captures any JSON on stdout.
// Used when debugging protocol errors - returns raw bytes if strict decode fails.
func DecodeResponseLenient(r io.Reader) (*Response, ResponseCompat, []byte, error) {
	// Read all bytes first
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, ResponseCompat{}, nil, fmt.Errorf("failed to read response: %w", err)
	}

	if len(data) == 0 {
		return nil, ResponseCompat{}, data, fmt.Errorf("plugin produced no output on stdout")
	}

	var wire responseV2Wire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, ResponseCompat{}, data, fmt.Errorf("plugin output is not valid JSON: %w", err)
	}
	resp := wire.response()
	if err := validateResponse(resp); err != nil {
		return nil, ResponseCompat{}, data, err
	}

	return resp, wire.compat(), data, nil
}
