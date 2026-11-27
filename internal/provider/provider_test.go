package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ = map[string]func() (tfprotov6.ProviderServer, error){
	"unco": providerserver.NewProtocol6WithError(New("test")()),
}

func stringVal(s string) types.String { return types.StringValue(s) }
func int64Val(n int64) types.Int64    { return types.Int64Value(n) }

type recordedAction struct {
	Warehouse string
	Stack     string
	Action    string
	Request   ActionRequest
}

type fakeUncoAPIServer struct {
	t                 *testing.T
	mu                sync.Mutex
	sets              map[string]VariableSet
	actions           []recordedAction
	injectOneConflict atomic.Bool
}

func newFakeUncoAPIServer(t *testing.T) (*fakeUncoAPIServer, *ProviderClient) {
	t.Helper()
	fake := &fakeUncoAPIServer{t: t, sets: map[string]VariableSet{}}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)

	return fake, NewProviderClient(srv.URL, "test-token", "terraform-provider-unco/test")
}

func (f *fakeUncoAPIServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-token" {
		http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if r.Header.Get(apiVersionHeader) != apiVersion {
		f.t.Errorf("expected %s: %s, got %q", apiVersionHeader, apiVersion, r.Header.Get(apiVersionHeader))
	}

	body, _ := io.ReadAll(r.Body)
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")

	if len(parts) == 4 && parts[0] == "warehouses" && parts[2] == "variables" {
		wh, id := parts[1], parts[3]
		key := wh + "/" + id

		f.mu.Lock()
		defer f.mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			s, ok := f.sets[key]
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(APIError{Code: http.StatusNotFound, Reason: "not_found", Message: "not found"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s)
		case http.MethodPost:
			if _, ok := f.sets[key]; ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(APIError{Code: http.StatusConflict, Reason: ReasonExists, Message: "exists"})
				return
			}
			var spec VariableSetSpec
			if err := json.Unmarshal(body, &spec); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			created := VariableSet{
				WarehouseName: wh,
				ID:            id,
				Description:   spec.Description,
				Variables:     spec.Variables,
				Version:       1,
			}
			f.sets[key] = created
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"warehouseName": wh, "id": id})
		case http.MethodPut:
			cur, ok := f.sets[key]
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(APIError{Code: http.StatusNotFound, Reason: "not_found", Message: "not found"})
				return
			}
			var spec VariableSetSpec
			if err := json.Unmarshal(body, &spec); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if f.injectOneConflict.CompareAndSwap(true, false) || spec.Version != cur.Version {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(APIError{Code: http.StatusConflict, Reason: ReasonVersionConflict, Message: "version conflict"})
				return
			}
			updated := VariableSet{
				WarehouseName: wh,
				ID:            id,
				Description:   spec.Description,
				Variables:     spec.Variables,
				Version:       cur.Version + 1,
			}
			f.sets[key] = updated
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}

	if len(parts) == 6 && parts[0] == "warehouses" && parts[2] == "stacks" && parts[4] == "actions" && r.Method == http.MethodPost {
		var req ActionRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.actions = append(f.actions, recordedAction{
			Warehouse: parts[1],
			Stack:     parts[3],
			Action:    parts[5],
			Request:   req,
		})
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		return
	}

	http.NotFound(w, r)
}

func TestConcurrentVariableEntriesAndConflictRetry(t *testing.T) {
	fake, pc := newFakeUncoAPIServer(t)
	ctx := context.Background()

	fake.sets["sample-warehouse/shared-outputs"] = VariableSet{
		WarehouseName: "sample-warehouse",
		ID:            "shared-outputs",
		Version:       1,
		Variables: map[string]VariableValue{
			"ServiceConfig": {Kind: "json", Target: TargetUnexported, Value: json.RawMessage(`{"tenant":"example"}`)},
		},
	}
	fake.injectOneConflict.Store(true)

	entries := map[string]string{
		"EventsEndpointURL": "https://events.example.invalid/events",
		"AuditEndpointURL":  "https://events.example.invalid/audit",
		"ExternalID":        "ext-id-123",
	}

	var wg sync.WaitGroup
	for name, val := range entries {
		wg.Add(1)
		go func(n, v string) {
			defer wg.Done()
			r := &VariableEntryResource{client: pc}
			model := VariableEntryResourceModel{
				Warehouse: stringVal("sample-warehouse"),
				SetID:     stringVal("shared-outputs"),
				Name:      stringVal(n),
				Kind:      stringVal(KindString),
				Target:    stringVal(TargetUnexported),
				Value:     stringVal(v),
			}
			require.NoError(t, r.upsert(ctx, &model))
		}(name, val)
	}
	wg.Wait()

	stored := fake.sets["sample-warehouse/shared-outputs"]
	assert.Equal(t, int64(4), stored.Version)
	assert.Contains(t, stored.Variables, "ServiceConfig", "sibling ServiceConfig entry must be preserved")
	for k, want := range entries {
		got, ok := stored.Variables[k].AsString()
		require.True(t, ok)
		assert.Equal(t, want, got)
	}
}

func TestMergePatchAndDeleteAndPlanAction(t *testing.T) {
	fake, pc := newFakeUncoAPIServer(t)
	ctx := context.Background()

	fake.sets["sample-warehouse/acc-sandbox"] = VariableSet{
		WarehouseName: "sample-warehouse",
		ID:            "acc-sandbox",
		Version:       1,
		Variables: map[string]VariableValue{
			"config":       {Kind: "acc-config", Target: TargetUnexported, Value: json.RawMessage(`{"name":"sandbox","email":"sandbox@example.com"}`)},
			"needs_import": {Kind: KindString, Target: TargetUnexported, Value: jsonStringValue("true")},
		},
	}

	patchRes := &VariableEntryPatchResource{client: pc}
	patchModel := VariableEntryPatchResourceModel{
		Warehouse: stringVal("sample-warehouse"),
		SetID:     stringVal("acc-sandbox"),
		Name:      stringVal("config"),
		PatchJSON: stringVal(`{"id":"123456789012"}`),
	}
	require.NoError(t, patchRes.applyPatch(ctx, &patchModel))
	assert.Equal(t, int64(2), patchModel.Version.ValueInt64())

	var cfgMap map[string]any
	require.NoError(t, json.Unmarshal(fake.sets["sample-warehouse/acc-sandbox"].Variables["config"].Value, &cfgMap))
	assert.Equal(t, map[string]any{
		"name":  "sandbox",
		"email": "sandbox@example.com",
		"id":    "123456789012",
	}, maps.Clone(cfgMap))

	require.NoError(t, patchRes.applyPatch(ctx, &patchModel))
	assert.Equal(t, int64(2), patchModel.Version.ValueInt64())

	delRes := &VariableEntryDeleteResource{client: pc}
	delModel := VariableEntryDeleteResourceModel{
		Warehouse: stringVal("sample-warehouse"),
		SetID:     stringVal("acc-sandbox"),
		Name:      stringVal("needs_import"),
	}
	require.NoError(t, delRes.deleteEntry(ctx, &delModel))
	assert.NotContains(t, fake.sets["sample-warehouse/acc-sandbox"].Variables, "needs_import")
	assert.Contains(t, fake.sets["sample-warehouse/acc-sandbox"].Variables, "config")
	require.NoError(t, delRes.deleteEntry(ctx, &delModel))

	planRes := &PlanActionResource{client: pc}
	planModel := PlanActionResourceModel{
		Warehouse:    stringVal("sample-warehouse"),
		Stack:        stringVal("customs-sandbox"),
		DelaySeconds: int64Val(60),
	}
	require.NoError(t, planRes.sendPlan(ctx, &planModel, true))
	require.Len(t, fake.actions, 1)
	assert.Equal(t, recordedAction{
		Warehouse: "sample-warehouse",
		Stack:     "customs-sandbox",
		Action:    "plan",
		Request:   ActionRequest{DelaySeconds: 60},
	}, fake.actions[0])
}

func TestVariableEntryReadDeleteImportAndCLIConfig(t *testing.T) {
	fake, pc := newFakeUncoAPIServer(t)
	ctx := context.Background()

	res, ok := NewVariableEntryResource().(*VariableEntryResource)
	require.True(t, ok)
	res.client = pc

	var schemaResp resource.SchemaResponse
	res.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	require.False(t, schemaResp.Diagnostics.HasError())

	prettyJSON := "{\n  \"port\": 8080,\n  \"host\": \"a\"\n}"
	createModel := VariableEntryResourceModel{
		Warehouse: stringVal("sample-warehouse"),
		SetID:     stringVal("domain-test"),
		Name:      stringVal("cfg"),
		Kind:      stringVal("custom-kind"),
		Target:    stringVal(TargetUnexported),
		Value:     stringVal(prettyJSON),
	}
	require.NoError(t, res.upsert(ctx, &createModel))
	assert.Equal(t, "sample-warehouse/domain-test/cfg", createModel.ID.ValueString())
	assert.Equal(t, int64(1), fake.sets["sample-warehouse/domain-test"].Version)

	state := tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}
	require.False(t, state.Set(ctx, &createModel).HasError())
	readResp := resource.ReadResponse{State: state}
	res.Read(ctx, resource.ReadRequest{State: state}, &readResp)
	require.False(t, readResp.Diagnostics.HasError())
	var afterRead VariableEntryResourceModel
	require.False(t, readResp.State.Get(ctx, &afterRead).HasError())
	assert.Equal(t, prettyJSON, afterRead.Value.ValueString())

	fake.sets["sample-warehouse/domain-test"].Variables["cfg"] = VariableValue{
		Kind:   "custom-kind",
		Target: TargetUnexported,
		Value:  json.RawMessage(`{"host":"b","port":9090}`),
	}
	readResp2 := resource.ReadResponse{State: readResp.State}
	res.Read(ctx, resource.ReadRequest{State: readResp.State}, &readResp2)
	require.False(t, readResp2.Diagnostics.HasError())
	require.False(t, readResp2.State.Get(ctx, &afterRead).HasError())
	assert.Equal(t, `{"host":"b","port":9090}`, afterRead.Value.ValueString())

	impResp := resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}}
	res.ImportState(ctx, resource.ImportStateRequest{ID: "sample-warehouse/domain-test/cfg"}, &impResp)
	require.False(t, impResp.Diagnostics.HasError())

	badImpResp := resource.ImportStateResponse{State: impResp.State}
	res.ImportState(ctx, resource.ImportStateRequest{ID: "bad-id"}, &badImpResp)
	assert.True(t, badImpResp.Diagnostics.HasError())

	fake.sets["sample-warehouse/domain-test"].Variables["sibling"] = VariableValue{
		Kind:   KindString,
		Target: TargetUnexported,
		Value:  jsonStringValue("keep-me"),
	}
	var delResp resource.DeleteResponse
	res.Delete(ctx, resource.DeleteRequest{State: readResp2.State}, &delResp)
	require.False(t, delResp.Diagnostics.HasError())
	assert.NotContains(t, fake.sets["sample-warehouse/domain-test"].Variables, "cfg")
	assert.Contains(t, fake.sets["sample-warehouse/domain-test"].Variables, "sibling")

	tmpHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpHome)
	apiURL, clientID, tenantID := loadCLIConfigDefaults()
	assert.Empty(t, apiURL)
	assert.Empty(t, clientID)
	assert.Empty(t, tenantID)

	require.NoError(t, os.MkdirAll(filepath.Join(tmpHome, "unco"), 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpHome, "unco", "config.yaml"),
		[]byte("api:\n  url: https://api.example.invalid\n  clientId: 00000000-0000-0000-0000-000000000001\n  tenantId: 00000000-0000-0000-0000-000000000002\n"),
		0o600,
	))
	apiURL, clientID, tenantID = loadCLIConfigDefaults()
	assert.Equal(t, "https://api.example.invalid", apiURL)
	assert.Equal(t, "00000000-0000-0000-0000-000000000001", clientID)
	assert.Equal(t, "00000000-0000-0000-0000-000000000002", tenantID)
}

type fakeMSALCache struct {
	payload []byte
}

func (f *fakeMSALCache) Marshal() ([]byte, error) { return f.payload, nil }
func (f *fakeMSALCache) Unmarshal(b []byte) error {
	f.payload = append([]byte(nil), b...)
	return nil
}

func TestStrictJSONCachePermissionsProviderConfigureAndCRUDEntrypoints(t *testing.T) {
	ctx := context.Background()

	for _, bad := range []string{
		``,
		`{"id":"123"} {"extra":1}`,
		`{"id":"123"} garbage`,
		`[1,2,3]`,
	} {
		_, _, err := applyMergePatch([]byte(`{"name":"sandbox"}`), []byte(bad))
		require.Error(t, err, "expected error for %q", bad)
	}

	now := time.Unix(1_700_000_000, 0)
	cachePath := filepath.Join(t.TempDir(), "unco", "cache.json")
	fc := &msalFileCache{filename: cachePath}
	dst := &fakeMSALCache{}
	require.NoError(t, fc.Replace(ctx, dst, cache.ReplaceHints{}), "missing cache file is a no-op")
	cacheDoc := fmt.Sprintf(`{"AccessToken":{"entry1":{"secret":"cached-jwt","client_id":"cid-1","realm":"tid-1","expires_on":"%d"}}}`, now.Unix()+3600)
	require.NoError(t, fc.Export(ctx, &fakeMSALCache{payload: []byte(cacheDoc)}, cache.ExportHints{}))
	require.NoError(t, fc.Replace(ctx, dst, cache.ReplaceHints{}))
	assert.Equal(t, cacheDoc, string(dst.payload))

	tok, err := readCachedAccessToken(cachePath, "cid-1", "tid-1", now)
	require.NoError(t, err)
	assert.Equal(t, "cached-jwt", tok)

	expiredTok, err := readCachedAccessToken(cachePath, "cid-1", "tid-1", now.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Empty(t, expiredTok)

	if runtime.GOOS != "windows" {
		require.NoError(t, os.Chmod(cachePath, 0o644))
		err := fc.Replace(ctx, dst, cache.ReplaceHints{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be accessible by group or others")
	}

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(EnvUncoAPIURL, "")
	t.Setenv(EnvUncoAPIToken, "")

	prov := New("0.1.0")()
	var provSchema provider.SchemaResponse
	prov.Schema(ctx, provider.SchemaRequest{}, &provSchema)
	require.False(t, provSchema.Diagnostics.HasError())

	makeConfig := func(val UncoProviderModel) tfsdk.Config {
		cfg := tfsdk.Config{
			Schema: provSchema.Schema,
			Raw:    tftypes.NewValue(provSchema.Schema.Type().TerraformType(ctx), nil),
		}
		rawMap := map[string]tftypes.Value{
			"api_url":   tftypes.NewValue(tftypes.String, nil),
			"client_id": tftypes.NewValue(tftypes.String, nil),
			"tenant_id": tftypes.NewValue(tftypes.String, nil),
			"token":     tftypes.NewValue(tftypes.String, nil),
		}
		setStr := func(k string, s types.String) {
			switch {
			case s.IsUnknown():
				rawMap[k] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
			case !s.IsNull():
				rawMap[k] = tftypes.NewValue(tftypes.String, s.ValueString())
			}
		}
		setStr("api_url", val.APIURL)
		setStr("client_id", val.ClientID)
		setStr("tenant_id", val.TenantID)
		setStr("token", val.Token)
		cfg.Raw = tftypes.NewValue(provSchema.Schema.Type().TerraformType(ctx), rawMap)
		return cfg
	}

	var missingResp provider.ConfigureResponse
	prov.Configure(ctx, provider.ConfigureRequest{Config: makeConfig(UncoProviderModel{})}, &missingResp)
	require.True(t, missingResp.Diagnostics.HasError())
	assert.Equal(t, "Missing Unco API URL", missingResp.Diagnostics[0].Summary())
	assert.Contains(t, missingResp.Diagnostics[0].Detail(), defaultCLIConfigPath())

	var unknownResp provider.ConfigureResponse
	prov.Configure(ctx, provider.ConfigureRequest{Config: makeConfig(UncoProviderModel{APIURL: types.StringUnknown()})}, &unknownResp)
	assert.False(t, unknownResp.Diagnostics.HasError())
	assert.Nil(t, unknownResp.ResourceData)

	fake, _ := newFakeUncoAPIServer(t)
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)

	var okResp provider.ConfigureResponse
	prov.Configure(ctx, provider.ConfigureRequest{Config: makeConfig(UncoProviderModel{
		APIURL: types.StringValue(srv.URL),
		Token:  types.StringValue("test-token"),
	})}, &okResp)
	require.False(t, okResp.Diagnostics.HasError())
	require.NotNil(t, okResp.ResourceData)

	unconfiguredEntry := NewVariableEntryResource()
	var veSchema resource.SchemaResponse
	unconfiguredEntry.Schema(ctx, resource.SchemaRequest{}, &veSchema)
	plan := tfsdk.Plan{Schema: veSchema.Schema, Raw: tftypes.NewValue(veSchema.Schema.Type().TerraformType(ctx), nil)}
	require.False(t, plan.Set(ctx, &VariableEntryResourceModel{
		Warehouse: stringVal("sample-warehouse"),
		SetID:     stringVal("s1"),
		Name:      stringVal("k1"),
		Kind:      stringVal(KindString),
		Target:    stringVal(TargetUnexported),
		Value:     stringVal("v1"),
	}).HasError())

	var nilCreateResp resource.CreateResponse
	unconfiguredEntry.Create(ctx, resource.CreateRequest{Plan: plan}, &nilCreateResp)
	assert.True(t, nilCreateResp.Diagnostics.HasError())

	configuredEntry, ok := NewVariableEntryResource().(*VariableEntryResource)
	require.True(t, ok)
	var cfgResResp resource.ConfigureResponse
	configuredEntry.Configure(ctx, resource.ConfigureRequest{ProviderData: okResp.ResourceData}, &cfgResResp)
	require.False(t, cfgResResp.Diagnostics.HasError())

	createResp := resource.CreateResponse{
		State: tfsdk.State{Schema: veSchema.Schema, Raw: tftypes.NewValue(veSchema.Schema.Type().TerraformType(ctx), nil)},
	}
	configuredEntry.Create(ctx, resource.CreateRequest{Plan: plan}, &createResp)
	require.False(t, createResp.Diagnostics.HasError())

	require.False(t, plan.Set(ctx, &VariableEntryResourceModel{
		ID:        stringVal("sample-warehouse/s1/k1"),
		Warehouse: stringVal("sample-warehouse"),
		SetID:     stringVal("s1"),
		Name:      stringVal("k1"),
		Kind:      stringVal(KindString),
		Target:    stringVal(TargetUnexported),
		Value:     stringVal("v2"),
	}).HasError())
	updateResp := resource.UpdateResponse{State: createResp.State}
	configuredEntry.Update(ctx, resource.UpdateRequest{Plan: plan, State: createResp.State}, &updateResp)
	require.False(t, updateResp.Diagnostics.HasError())
	gotStr, isStr := fake.sets["sample-warehouse/s1"].Variables["k1"].AsString()
	require.True(t, isStr)
	assert.Equal(t, "v2", gotStr)
}

func TestDefaultCLIConfigPathAndAcquireUncoTokenErrors(t *testing.T) {
	ctx := context.Background()

	tmpCfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpCfg)
	cfgDir, err := os.UserConfigDir()
	require.NoError(t, err)

	expectedPath := filepath.Join(cfgDir, "unco", "config.yaml")
	assert.Equal(t, expectedPath, defaultCLIConfigPath())

	_, err = acquireUncoToken(ctx, "", "00000000-0000-0000-0000-000000000002")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client_id is required")
	assert.Contains(t, err.Error(), defaultCLIConfigPath())

	_, err = acquireUncoToken(ctx, "00000000-0000-0000-0000-000000000001", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant_id is required")
	assert.Contains(t, err.Error(), defaultCLIConfigPath())

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("AppData", "")
	if _, uErr := os.UserConfigDir(); uErr != nil {
		assert.Equal(t, "<user config dir>/unco/config.yaml", defaultCLIConfigPath())
		apiURL, clientID, tenantID := loadCLIConfigDefaults()
		assert.Empty(t, apiURL)
		assert.Empty(t, clientID)
		assert.Empty(t, tenantID)

		_, err = acquireUncoToken(ctx, "", "00000000-0000-0000-0000-000000000002")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "<user config dir>/unco/config.yaml")
	}
}
