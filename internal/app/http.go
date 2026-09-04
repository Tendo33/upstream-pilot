package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	webassets "github.com/Tendo33/upstream-pilot/internal/web"
)

type contextKey string

const identityKey contextKey = "identity"

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Message }

type handler func(http.ResponseWriter, *http.Request) error

func (a *App) Router() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(a.securityHeaders)

	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Ready(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
	})

	router.Route("/api/v1", func(api chi.Router) {
		api.Get("/setup/status", a.wrap(a.setupStatus))
		api.Post("/setup", a.wrap(a.setup))
		api.Post("/auth/login", a.wrap(a.login))

		api.Group(func(protected chi.Router) {
			protected.Use(a.authenticate)
			protected.Use(a.verifyCSRF)
			protected.Get("/auth/me", a.wrap(a.me))
			protected.Post("/auth/logout", a.wrap(a.logout))
			protected.Get("/version", a.wrap(a.versionStatusHandler))
			protected.Get("/overview", a.wrap(a.overview))
			protected.Get("/quality", a.wrap(a.qualityListHandler))
			protected.Get("/quality/groups", a.wrap(a.qualityGroupHandler))
			protected.Put("/quality/groups/{groupID}/policy", a.wrap(a.engineGroupPolicyHandler))
			protected.Post("/quality/{accountID}/reference", a.wrap(a.engineReferenceHandler))
			protected.Get("/quality/alerts", a.wrap(a.qualityAlertsHandler))
			protected.Put("/quality/alerts", a.wrap(a.qualityAlertsHandler))
			protected.Post("/quality/alerts/test", a.wrap(a.qualityAlertTestHandler))
			protected.Put("/quality/{accountID}/policy", a.wrap(a.qualityPolicyHandler))
			protected.Post("/quality/{accountID}/evaluate", a.wrap(a.qualityEvaluateHandler))
			protected.Post("/quality/{accountID}/release", a.wrap(a.qualityReleaseHandler))
			protected.Get("/quality/{accountID}/history", a.wrap(a.qualityHistoryHandler))
			protected.Get("/sites", a.wrap(a.listSites))
			protected.Post("/sites", a.wrap(a.createSite))
			protected.Route("/sites/{siteID}", func(site chi.Router) {
				site.Patch("/", a.wrap(a.updateSite))
				site.Delete("/", a.wrap(a.deleteSite))
				site.Post("/test", a.wrap(a.testSite))
				site.Post("/sync", a.wrap(a.syncSiteHandler))
				site.Post("/reconcile", a.wrap(a.reconcileSiteHandler))
				site.Post("/cache-sample", a.wrap(a.sampleSiteCacheHandler))
			})
			protected.Get("/accounts", a.wrap(a.listAccounts))
			protected.Get("/accounts/filter-options", a.wrap(a.accountFilterOptions))
			protected.Post("/accounts/balances", a.wrap(a.accountBalancesHandler))
			protected.Patch("/accounts/bulk-settings", a.wrap(a.bulkUpdateAccountSettings))
			protected.Route("/accounts/{accountID}", func(account chi.Router) {
				account.Put("/settings", a.wrap(a.updateAccountSettings))
				account.Put("/scheduling", a.wrap(a.updateAccountScheduling))
				account.Post("/probe", a.wrap(a.probeAccountHandler))
				account.Post("/rate-sync", a.wrap(a.rateSyncAccountHandler))
				account.Post("/cache-sample", a.wrap(a.sampleAccountCacheHandler))
				account.Get("/models", a.wrap(a.listAccountModels))
				account.Get("/source-groups", a.wrap(a.listSourceGroups))
				account.Post("/source-groups", a.wrap(a.previewSourceGroups))
			})
			protected.Get("/groups", a.wrap(a.listGroups))
			protected.Route("/groups/{groupID}", func(group chi.Router) {
				group.Get("/", a.wrap(a.getGroup))
				group.Put("/config", a.wrap(a.updateGroupRateConfig))
				group.Post("/apply", a.wrap(a.applyGroupRateHandler))
			})
			protected.Get("/events", a.wrap(a.listEvents))
			protected.Get("/settings/balance-alert", a.wrap(a.getBalanceAlertSettingsHandler))
			protected.Put("/settings/balance-alert", a.wrap(a.updateBalanceAlertSettingsHandler))
			protected.Post("/settings/balance-alert/test", a.wrap(a.testBalanceAlertWebhookHandler))
			protected.Get("/settings/audit-log", a.wrap(a.getAuditLogSettingsHandler))
			protected.Put("/settings/audit-log", a.wrap(a.updateAuditLogSettingsHandler))
			protected.Post("/settings/audit-log/purge", a.wrap(a.purgeAuditLogHandler))
			protected.Get("/users", a.wrap(a.listUsers))
			protected.Post("/users", a.wrap(a.createUser))
			protected.Patch("/users/{userID}", a.wrap(a.updateUser))
			protected.Delete("/users/{userID}", a.wrap(a.deleteUser))
		})
	})

	router.NotFound(webassets.SPAHandler())
	return router
}

func (a *App) wrap(next handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := next(w, r); err != nil {
			var apiErr *apiError
			if errors.As(err, &apiErr) {
				writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
				return
			}
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "NOT_FOUND", "资源不存在或无权访问")
				return
			}
			a.logger.Error("request failed", slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.String("request_id", middleware.GetReqID(r.Context())), slog.Any("error", err))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "服务器处理请求失败")
		}
	}
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &apiError{Status: http.StatusUnsupportedMediaType, Code: "JSON_CONTENT_TYPE_REQUIRED", Message: "请求必须使用 application/json 内容类型"}
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_JSON", Message: "请求内容不是有效的 JSON"}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_JSON", Message: "请求只能包含一个 JSON 对象"}
	}
	return nil
}

func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, map[string]any{"data": data})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func identityFrom(r *http.Request) Identity {
	return r.Context().Value(identityKey).(Identity)
}

func withIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey, identity)
}

func requiredID(r *http.Request, name string) (string, error) {
	value := strings.TrimSpace(chi.URLParam(r, name))
	if value == "" {
		return "", fmt.Errorf("missing route parameter %s", name)
	}
	return value, nil
}
