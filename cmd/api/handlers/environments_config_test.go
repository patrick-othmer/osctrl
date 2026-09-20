package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmpsec/osctrl/pkg/auditlog"
	"github.com/jmpsec/osctrl/pkg/config"
	"github.com/jmpsec/osctrl/pkg/environments"
	"github.com/jmpsec/osctrl/pkg/users"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupEnvConfigHandlers(t *testing.T) (*gorm.DB, *HandlersApi, environments.TLSEnvironment) {
	t.Helper()

	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	envs := environments.CreateEnvironment(db)
	userManager := users.CreateUserManager(db)
	auditLog, err := auditlog.CreateAuditLogManager(db, "api", true)
	require.NoError(t, err)

	env := envs.Empty("dev", "dev.example.com")
	env.UUID = "11111111-2222-3333-4444-555555555555"
	require.NoError(t, envs.Create(&env))
	require.NoError(t, userManager.Create(users.AdminUser{Username: "alice"}))
	require.NoError(t, userManager.Create(users.AdminUser{Username: "bob"}))
	require.NoError(t, userManager.CreatePermission(users.UserPermission{
		Username:      "alice",
		AccessType:    int(users.AdminLevel),
		AccessValue:   true,
		Environment:   env.UUID,
		EnvironmentID: env.ID,
	}))

	h := CreateHandlersApi(
		WithDB(db),
		WithEnvs(envs),
		WithUsers(userManager),
		WithAuditLog(auditLog),
		WithDebugHTTP(&config.YAMLConfigurationDebug{}),
	)
	return db, h, env
}

func envConfigPatchRequest(t *testing.T, envName string, body map[string]any, username string) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/environments/config/"+envName, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("env", envName)
	ctx := context.WithValue(req.Context(), ContextKey(contextAPI), ContextValue{ctxUser: username})
	return req.WithContext(ctx)
}

// TestEnvConfigPatchPlaintextFlags is the regression test for the
// `section "flags" is not valid JSON: invalid character '-' in numeric
// literal` 400. The flags section is a plaintext osquery flags file, not
// JSON: removing a `--flag=value` line in the SPA and saving used to reject
// the whole patch because the handler JSON-validated every section (the `--`
// prefix trips the JSON parser). Plaintext flags content must round-trip.
func TestEnvConfigPatchPlaintextFlags(t *testing.T) {
	db, h, env := setupEnvConfigHandlers(t)

	flags := "\n--host_identifier=uuid\n--force=true\n--tls_hostname=dev.example.com\n"
	req := envConfigPatchRequest(t, env.Name, map[string]any{"flags": flags}, "alice")
	rr := httptest.NewRecorder()

	h.EnvironmentConfigPatchHandler(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var updated environments.TLSEnvironment
	require.NoError(t, db.First(&updated, "uuid = ?", env.UUID).Error)
	require.Equal(t, strings.TrimSpace(flags), updated.Flags)
}

// TestEnvConfigPatchEmptyFlags locks the empty-value behavior: an emptied
// editor must store "" (no flags), not the "{}" placeholder used for the
// JSON sections — "{}" would be a malformed line in a plaintext flags file.
func TestEnvConfigPatchEmptyFlags(t *testing.T) {
	db, h, env := setupEnvConfigHandlers(t)

	flags := "--host_identifier=uuid\n"
	require.NoError(t, h.Envs.DB.Model(&env).Update("flags", flags).Error)

	req := envConfigPatchRequest(t, env.Name, map[string]any{"flags": "   "}, "alice")
	rr := httptest.NewRecorder()

	h.EnvironmentConfigPatchHandler(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var updated environments.TLSEnvironment
	require.NoError(t, db.First(&updated, "uuid = ?", env.UUID).Error)
	require.Equal(t, "", updated.Flags)
}

// TestEnvConfigPatchMixedSections checks the common SPA save path: a JSON
// section and plaintext flags in one PATCH both persist, and the composed
// configuration is recomposed from the JSON parts only.
func TestEnvConfigPatchMixedSections(t *testing.T) {
	db, h, env := setupEnvConfigHandlers(t)

	body := map[string]any{
		"options": `{"aio_sync_timeout": 300}`,
		"flags":   "--tls_hostname=dev.example.com\n",
	}
	req := envConfigPatchRequest(t, env.Name, body, "alice")
	rr := httptest.NewRecorder()

	h.EnvironmentConfigPatchHandler(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var updated environments.TLSEnvironment
	require.NoError(t, db.First(&updated, "uuid = ?", env.UUID).Error)
	require.JSONEq(t, `{"aio_sync_timeout": 300}`, updated.Options)
	require.Equal(t, "--tls_hostname=dev.example.com", updated.Flags)
	require.True(t, strings.Contains(updated.Configuration, "aio_sync_timeout"),
		"composed configuration must reflect the options edit")
}

// TestEnvConfigPatchStillValidatesJSONSections guards the other direction:
// the flags exemption must not loosen validation of the JSON sections.
func TestEnvConfigPatchStillValidatesJSONSections(t *testing.T) {
	_, h, env := setupEnvConfigHandlers(t)

	req := envConfigPatchRequest(t, env.Name, map[string]any{"options": "{broken"}, "alice")
	rr := httptest.NewRecorder()

	h.EnvironmentConfigPatchHandler(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	// the error is embedded in a JSON string, so the quotes are escaped
	require.Contains(t, rr.Body.String(), `section \"options\" is not valid JSON`)
}

// TestEnvConfigPatchFlagsPatchDeniedWithoutAdminPermission guards that the
// validation change did not skip the authorization gate: a user without
// admin rights on the environment cannot patch its config.
func TestEnvConfigPatchFlagsPatchDeniedWithoutAdminPermission(t *testing.T) {
	_, h, env := setupEnvConfigHandlers(t)

	// bob has no permission row for this environment.
	req := envConfigPatchRequest(t, env.Name, map[string]any{"flags": "--host_identifier=uuid\n"}, "bob")
	rr := httptest.NewRecorder()

	h.EnvironmentConfigPatchHandler(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
}
