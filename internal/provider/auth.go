package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

const (
	EnvUncoAPIURL   = "UNCO_API_URL"
	EnvUncoAPIToken = "UNCO_API_TOKEN"
	EnvUncoClientID = "UNCO_CLIENT_ID"
	EnvUncoTenantID = "UNCO_TENANT_ID"
)

type msalFileCache struct {
	filename string
}

func defaultMSALCacheFile() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "unco", "cache.json")
}

func checkCacheFilePermissions(filename string, fi os.FileInfo) error {
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("token cache %s must not be accessible by group or others (expected 0600)", filename)
	}
	return nil
}

func (f *msalFileCache) Replace(ctx context.Context, u cache.Unmarshaler, _ cache.ReplaceHints) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := os.Stat(f.filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat token cache: %w", err)
	}
	if err := checkCacheFilePermissions(f.filename, fi); err != nil {
		return err
	}
	data, err := os.ReadFile(f.filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read token cache: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	return u.Unmarshal(data)
}

func (f *msalFileCache) Export(ctx context.Context, m cache.Marshaler, _ cache.ExportHints) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := m.Marshal()
	if err != nil {
		return fmt.Errorf("marshal token cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(f.filename), 0o700); err != nil {
		return fmt.Errorf("create token cache dir: %w", err)
	}
	return os.WriteFile(f.filename, data, 0o600)
}

// readCachedAccessToken inspects an MSAL cache file for a non-expired access
// token matching clientID and tenantID (supporting sessions cached without an
// interactive Account entry).
func readCachedAccessToken(cachePath, clientID, tenantID string, now time.Time) (string, error) {
	fi, err := os.Stat(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat token cache: %w", err)
	}
	if err := checkCacheFilePermissions(cachePath, fi); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil || len(raw) == 0 {
		return "", err
	}

	var doc struct {
		AccessToken map[string]struct {
			Secret    string `json:"secret"`
			ClientID  string `json:"client_id"`
			Realm     string `json:"realm"`
			ExpiresOn string `json:"expires_on"`
		} `json:"AccessToken"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parse token cache: %w", err)
	}

	minExp := now.Unix() + 60
	var bestSecret string
	var bestExp int64
	for _, entry := range doc.AccessToken {
		if entry.Secret == "" || !strings.EqualFold(entry.ClientID, clientID) {
			continue
		}
		if entry.Realm != "" && !strings.EqualFold(entry.Realm, tenantID) {
			continue
		}
		exp, err := strconv.ParseInt(entry.ExpiresOn, 10, 64)
		if err != nil || exp <= minExp {
			continue
		}
		if exp > bestExp {
			bestExp = exp
			bestSecret = entry.Secret
		}
	}
	return bestSecret, nil
}

func acquireUncoToken(ctx context.Context, clientID, tenantID string) (string, error) {
	if clientID == "" {
		return "", fmt.Errorf("client_id is required (set in provider block, UNCO_CLIENT_ID, or %s)", defaultCLIConfigPath())
	}
	if tenantID == "" {
		return "", fmt.Errorf("tenant_id is required (set in provider block, UNCO_TENANT_ID, or %s)", defaultCLIConfigPath())
	}

	authority := "https://login.microsoftonline.com/" + tenantID
	cachePath := defaultMSALCacheFile()
	tokenCache := &msalFileCache{filename: cachePath}

	app, err := public.New(clientID, public.WithAuthority(authority), public.WithCache(tokenCache))
	if err != nil {
		return "", fmt.Errorf("create MSAL client: %w", err)
	}
	accounts, err := app.Accounts(ctx)
	if err != nil {
		return "", fmt.Errorf("read cached MSAL accounts: %w", err)
	}
	if len(accounts) > 0 {
		scopes := []string{fmt.Sprintf("api://%s/access", clientID)}
		res, err := app.AcquireTokenSilent(ctx, scopes, public.WithSilentAccount(accounts[0]))
		if err != nil {
			return "", fmt.Errorf("cached Unco token expired or invalid; run 'unco login' first: %w", err)
		}
		return res.AccessToken, nil
	}

	cachedToken, err := readCachedAccessToken(cachePath, clientID, tenantID, time.Now())
	if err != nil {
		return "", err
	}
	if cachedToken != "" {
		return cachedToken, nil
	}

	return "", errors.New("no cached Unco session found; run 'unco login' first or set UNCO_API_TOKEN")
}
