package server

import (
	"encoding/json"
	"strings"
)

// HandleAPI routes one API call (shared by HTTP and the WS RPC bridge).
// Call with Mu held.
func (a *App) HandleAPI(method, route string, q map[string]string, rawBody []byte) (*apiResponse, error) {
	b := map[string]any{}
	var bRaw map[string]json.RawMessage
	if method == "POST" && len(strings.TrimSpace(string(rawBody))) > 0 {
		if err := json.Unmarshal(rawBody, &b); err != nil {
			return nil, errJSONParse(err)
		}
		if b == nil {
			b = map[string]any{}
		}
		_ = json.Unmarshal(rawBody, &bRaw)
	}
	if method == "GET" {
		resp, err := a.handleGet(route, q)
		if err != nil {
			return nil, err
		}
		if resp != nil {
			return resp, nil
		}
	} else if method == "POST" {
		resp, err := a.handlePost1(route, b, bRaw)
		if err != nil {
			return nil, err
		}
		if resp != nil {
			return resp, nil
		}
		resp, err = a.handlePost2(route, b, bRaw, rawBody)
		if err != nil {
			return nil, err
		}
		if resp != nil {
			return resp, nil
		}
	}
	return &apiResponse{Status: 404, JSON: jsonOf(map[string]string{"error": "Unknown API endpoint"})}, nil
}

func errJSONParse(err error) error {
	return &jsonParseError{msg: err.Error()}
}

type jsonParseError struct{ msg string }

func (e *jsonParseError) Error() string { return e.msg }
