package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

const (
	apiVersionHeader = "Unco-Api-Version"
	apiVersion       = "2"

	KindString = "string"
	KindBytes  = "bytes"

	TargetUnexported = "unexported"

	MaxActionDelaySeconds = 900
	maxConflictRetries    = 6

	ReasonVersionConflict      = "version_conflict"
	ReasonPreconditionRequired = "precondition_required"
	ReasonExists               = "exists"
)

var (
	ErrNotFound = errors.New("not found")
)

type VariableValue struct {
	Kind   string          `json:"kind,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
	Target string          `json:"target,omitempty"`
}

func (v VariableValue) KindOrDefault() string {
	if v.Kind == "" {
		return KindString
	}
	return v.Kind
}

func (v VariableValue) TargetOrDefault() string {
	if v.Target == "" {
		return "tfvar"
	}
	return v.Target
}

func (v VariableValue) AsString() (string, bool) {
	raw := bytes.TrimSpace(v.Value)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func isBuiltinKind(kind string) bool {
	return kind == "" || kind == KindString || kind == KindBytes
}

func jsonStringValue(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

type VariableSet struct {
	WarehouseName string                   `json:"warehouseName"`
	ID            string                   `json:"id"`
	Description   string                   `json:"description"`
	Variables     map[string]VariableValue `json:"variables"`
	Version       int64                    `json:"version,omitempty"`
}

type VariableSetSpec struct {
	Description string                   `json:"description"`
	Variables   map[string]VariableValue `json:"variables"`
	Version     int64                    `json:"version,omitempty"`
}

func (s VariableSet) Spec() VariableSetSpec {
	vars := s.Variables
	if vars == nil {
		vars = map[string]VariableValue{}
	}
	return VariableSetSpec{
		Description: s.Description,
		Variables:   vars,
		Version:     s.Version,
	}
}

type ActionRequest struct {
	Targets      []string `json:"targets,omitempty"`
	Destroy      bool     `json:"destroy,omitempty"`
	Ref          string   `json:"ref,omitempty"`
	Rev          int      `json:"rev,omitempty"`
	DelaySeconds int      `json:"delaySeconds,omitempty"`
}

type Violation struct {
	Store    string `json:"store,omitempty"`
	SetID    string `json:"setId,omitempty"`
	Variable string `json:"variable,omitempty"`
	Message  string `json:"message"`
}

func (v Violation) String() string {
	s := v.Message
	if v.Variable != "" {
		s = v.Variable + ": " + s
	}
	if v.SetID != "" {
		s = v.Store + "/" + v.SetID + ": " + s
	}
	return s
}

type APIError struct {
	Code       int         `json:"code"`
	Reason     string      `json:"reason"`
	Message    string      `json:"message"`
	Violations []Violation `json:"violations,omitempty"`
}

func (e APIError) Error() string {
	if len(e.Violations) == 0 {
		return fmt.Sprintf("unco api error (%d): %s", e.Code, e.Message)
	}
	parts := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		parts[i] = v.String()
	}
	return fmt.Sprintf("unco api error (%d): %s: %s", e.Code, e.Message, strings.Join(parts, "; "))
}

func decodeAPIError(status int, body []byte) APIError {
	var e APIError
	_ = json.Unmarshal(body, &e)
	if e.Code == 0 {
		e.Code = status
	}
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}

type ProviderClient struct {
	baseURL   string
	token     string
	userAgent string
	http      *http.Client
	locks     sync.Map
}

func NewProviderClient(baseURL, token, userAgent string) *ProviderClient {
	rc := retryablehttp.NewClient()
	rc.RetryMax = 2
	rc.Logger = nil
	rc.ErrorHandler = retryablehttp.PassthroughErrorHandler
	hc := rc.StandardClient()
	hc.Timeout = 30 * time.Second

	if userAgent == "" {
		userAgent = "terraform-provider-unco/dev"
	}

	return &ProviderClient{
		baseURL:   strings.TrimRight(baseURL, "/"),
		token:     token,
		userAgent: userAgent,
		http:      hc,
	}
}

func (c *ProviderClient) lockSet(warehouse, setID string) func() {
	key := warehouse + "/" + setID
	val, _ := c.locks.LoadOrStore(key, &sync.Mutex{})
	mu := val.(*sync.Mutex) //nolint:forcetypeassert // only *sync.Mutex stored
	mu.Lock()
	return mu.Unlock
}

func (c *ProviderClient) doJSON(ctx context.Context, method, relPath string, reqBody any) (int, []byte, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return 0, nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+relPath, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set(apiVersionHeader, apiVersion)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, respBytes, nil
}

func variablePath(warehouse, setID string) string {
	return fmt.Sprintf("/warehouses/%s/variables/%s", url.PathEscape(warehouse), url.PathEscape(setID))
}

func (c *ProviderClient) GetSet(ctx context.Context, warehouse, setID string) (*VariableSet, error) {
	status, body, err := c.doJSON(ctx, http.MethodGet, variablePath(warehouse, setID), nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if status >= 400 {
		return nil, decodeAPIError(status, body)
	}
	var set VariableSet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("decode variable set %s/%s: %w", warehouse, setID, err)
	}
	return &set, nil
}

func (c *ProviderClient) MutateSet(
	ctx context.Context,
	warehouse, setID string,
	mutate func(set *VariableSet, exists bool) (changed bool, err error),
) (int64, error) {
	unlock := c.lockSet(warehouse, setID)
	defer unlock()

	for attempt := 0; attempt <= maxConflictRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(time.Duration(attempt*25) * time.Millisecond):
			}
		}

		loaded, err := c.GetSet(ctx, warehouse, setID)
		exists := true
		var working VariableSet
		if errors.Is(err, ErrNotFound) {
			exists = false
			working = VariableSet{
				WarehouseName: warehouse,
				ID:            setID,
				Variables:     map[string]VariableValue{},
			}
		} else if err != nil {
			return 0, err
		} else {
			working = *loaded
			working.Variables = maps.Clone(loaded.Variables)
			if working.Variables == nil {
				working.Variables = map[string]VariableValue{}
			}
		}

		changed, err := mutate(&working, exists)
		if err != nil {
			return 0, err
		}
		if !changed {
			return working.Version, nil
		}

		if !exists {
			status, body, err := c.doJSON(ctx, http.MethodPost, variablePath(warehouse, setID), working.Spec())
			if err != nil {
				return 0, err
			}
			if status >= 400 {
				apiErr := decodeAPIError(status, body)
				if apiErr.Reason == ReasonExists {
					continue
				}
				return 0, apiErr
			}
			return 1, nil
		}

		status, body, err := c.doJSON(ctx, http.MethodPut, variablePath(warehouse, setID), working.Spec())
		if err != nil {
			return 0, err
		}
		if status >= 400 {
			apiErr := decodeAPIError(status, body)
			if apiErr.Reason == ReasonVersionConflict || apiErr.Reason == ReasonPreconditionRequired {
				continue
			}
			return 0, apiErr
		}
		return working.Version + 1, nil
	}

	return 0, fmt.Errorf("exceeded %d retries updating variable set %s/%s due to concurrent version conflicts", maxConflictRetries, warehouse, setID)
}

func (c *ProviderClient) TriggerPlan(ctx context.Context, warehouse, stackName string, req ActionRequest) error {
	path := fmt.Sprintf("/warehouses/%s/stacks/%s/actions/plan", url.PathEscape(warehouse), url.PathEscape(stackName))
	status, body, err := c.doJSON(ctx, http.MethodPost, path, req)
	if err != nil {
		return err
	}
	if status >= 400 {
		return decodeAPIError(status, body)
	}
	return nil
}

func encodeEntryValue(kind, raw string) (json.RawMessage, error) {
	if isBuiltinKind(kind) {
		if raw == "" {
			return nil, errors.New("value must not be empty for built-in string/bytes kinds")
		}
		if kind == KindBytes {
			if _, err := base64.StdEncoding.DecodeString(raw); err != nil {
				return nil, fmt.Errorf("value must be base64-encoded for kind bytes: %w", err)
			}
		}
		return jsonStringValue(raw), nil
	}
	trimmed := bytes.TrimSpace([]byte(raw))
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil, fmt.Errorf("value for custom kind %q must be valid JSON", kind)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

func decodeEntryValue(entry VariableValue) string {
	if isBuiltinKind(entry.KindOrDefault()) {
		if s, ok := entry.AsString(); ok {
			return s
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, bytes.TrimSpace(entry.Value)); err == nil {
		return compact.String()
	}
	return string(bytes.TrimSpace(entry.Value))
}

func equalJSONStrings(a, b string) bool {
	if a == b {
		return true
	}
	va, err := parseJSONNumberPreserving([]byte(a))
	if err != nil {
		return false
	}
	vb, err := parseJSONNumberPreserving([]byte(b))
	if err != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

func parseJSONNumberPreserving(raw []byte) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil, errors.New("invalid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected trailing JSON data")
	}
	return out, nil
}

func applyMergePatch(targetRaw, patchRaw []byte) (json.RawMessage, bool, error) {
	targetAny, err := parseJSONNumberPreserving(targetRaw)
	if err != nil {
		return nil, false, fmt.Errorf("existing entry value is not valid JSON: %w", err)
	}
	targetObj, ok := targetAny.(map[string]any)
	if !ok {
		return nil, false, errors.New("existing entry value is not a JSON object")
	}

	patchAny, err := parseJSONNumberPreserving(patchRaw)
	if err != nil {
		return nil, false, fmt.Errorf("patch_json is not valid JSON: %w", err)
	}
	patchObj, ok := patchAny.(map[string]any)
	if !ok {
		return nil, false, errors.New("patch_json must be a JSON object")
	}

	merged := mergeObjects(targetObj, patchObj)
	changed := !reflect.DeepEqual(targetObj, merged)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, false, err
	}
	return encoded, changed, nil
}

func mergeObjects(target, patch map[string]any) map[string]any {
	out := maps.Clone(target)
	if out == nil {
		out = map[string]any{}
	}
	for k, pv := range patch {
		if pv == nil {
			delete(out, k)
			continue
		}
		pm, pIsObj := pv.(map[string]any)
		if pIsObj {
			if tm, tIsObj := out[k].(map[string]any); tIsObj {
				out[k] = mergeObjects(tm, pm)
				continue
			}
			out[k] = mergeObjects(nil, pm)
			continue
		}
		out[k] = pv
	}
	return out
}
