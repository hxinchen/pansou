package keywordsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type browserGatewayRequest struct {
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers,omitempty"`
	Query          map[string]string `json:"query,omitempty"`
	BodyType       BodyType          `json:"body_type"`
	Body           string            `json:"body,omitempty"`
	Form           map[string]string `json:"form,omitempty"`
	ProxyURL       string            `json:"proxy_url,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Session        string            `json:"session,omitempty"`
}

type browserGatewayResponse struct {
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type,omitempty"`
	Body        string `json:"body"`
}

// ExecuteViaBrowser delegates a source request to a configured browser gateway
// and then decodes the upstream body using the same JSON rules as Execute.
func ExecuteViaBrowser(ctx context.Context, config RequestConfig) (response Response, err error) {
	gatewayURL := effectiveBrowserGateway(config)
	payload := browserGatewayRequest{
		Method: canonicalMethod(config.Method), URL: strings.TrimSpace(config.URL),
		Headers: cloneRequestStringMap(config.Headers), Query: cloneRequestStringMap(config.Query),
		BodyType: canonicalBodyType(config.BodyType), Body: config.Body, Form: cloneRequestStringMap(config.Form),
		ProxyURL: strings.TrimSpace(config.ProxyURL), TimeoutSeconds: effectiveTimeout(config.TimeoutSeconds),
		Session: strings.TrimSpace(config.BrowserSession),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL, bytes.NewReader(data))
	if err != nil {
		return Response{}, RedactError(fmt.Errorf("build browser gateway request: %w", err), config)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: time.Duration(effectiveTimeout(config.TimeoutSeconds)+10) * time.Second}
	started := time.Now()
	httpResponse, err := client.Do(req)
	response.Duration = time.Since(started)
	if err != nil {
		return response, RedactError(fmt.Errorf("browser gateway request failed: %w", err), config)
	}
	defer httpResponse.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, MaxResponseBytes+1))
	response.SizeBytes = len(body)
	if err != nil {
		return response, RedactError(fmt.Errorf("read browser gateway response: %w", err), config)
	}
	if len(body) > MaxResponseBytes {
		response.SizeBytes = MaxResponseBytes
		return response, ErrResponseTooLarge
	}
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		return response, fmt.Errorf("browser gateway returned HTTP status %d", httpResponse.StatusCode)
	}

	var gateway browserGatewayResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&gateway); err != nil {
		return response, fmt.Errorf("%w: browser gateway response decode failed", ErrInvalidJSON)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return response, fmt.Errorf("%w: multiple browser gateway JSON values", ErrInvalidJSON)
	}
	response.StatusCode = gateway.StatusCode
	response.ContentType = gateway.ContentType
	response.SizeBytes = len(gateway.Body)

	targetDecoder := json.NewDecoder(strings.NewReader(gateway.Body))
	targetDecoder.UseNumber()
	if err := targetDecoder.Decode(&response.JSON); err != nil {
		return response, fmt.Errorf("%w: %v", ErrInvalidJSON, compactJSONError(err))
	}
	var targetTrailing any
	if err := targetDecoder.Decode(&targetTrailing); !errors.Is(err, io.EOF) {
		return response, fmt.Errorf("%w: multiple JSON values", ErrInvalidJSON)
	}
	if gateway.StatusCode < http.StatusOK || gateway.StatusCode >= http.StatusMultipleChoices {
		return response, &HTTPStatusError{StatusCode: gateway.StatusCode}
	}
	return response, nil
}

func cloneRequestStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
