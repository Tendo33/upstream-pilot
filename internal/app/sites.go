package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/langrenjh-alt/S2AM-GO/internal/upstream"
)

type siteInput struct {
	Name                     string  `json:"name"`
	BaseURL                  string  `json:"base_url"`
	APIKey                   string  `json:"api_key"`
	Enabled                  *bool   `json:"enabled"`
	InventoryIntervalSeconds int     `json:"inventory_interval_seconds"`
	PriorityStart            int     `json:"priority_start"`
	PriorityStep             int     `json:"priority_step"`
	ReconcileIntervalSeconds int     `json:"reconcile_interval_seconds"`
	CacheRatePriorityEnabled bool    `json:"cache_rate_priority_enabled"`
	CacheRateWindowSeconds   int     `json:"cache_rate_window_seconds"`
	RatePriorityWeight       float64 `json:"rate_priority_weight"`
	CacheRatePriorityWeight  float64 `json:"cache_rate_priority_weight"`
}

func normalizeSiteInput(input *siteInput, creating bool) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_SITE_NAME", Message: "站点名称长度必须为 1 到 80 个字符"}
	}
	baseURL, err := upstream.NormalizeBaseURL(input.BaseURL)
	if err != nil {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_SITE_URL", Message: "站点地址必须是有效的 HTTP 或 HTTPS 根地址"}
	}
	input.BaseURL = baseURL
	input.APIKey = strings.TrimSpace(input.APIKey)
	if creating && input.APIKey == "" {
		return &apiError{Status: http.StatusBadRequest, Code: "API_KEY_REQUIRED", Message: "请填写 Sub2API 管理 API Key"}
	}
	if input.InventoryIntervalSeconds == 0 {
		input.InventoryIntervalSeconds = 300
	}
	if input.PriorityStep == 0 {
		input.PriorityStep = 1
	}
	if input.ReconcileIntervalSeconds == 0 {
		input.ReconcileIntervalSeconds = 60
	}
	if input.CacheRateWindowSeconds == 0 {
		input.CacheRateWindowSeconds = defaultCacheRateWindowSeconds
	}
	if input.RatePriorityWeight == 0 && input.CacheRatePriorityWeight == 0 {
		input.RatePriorityWeight = 1
		input.CacheRatePriorityWeight = 1
	}
	if input.InventoryIntervalSeconds < 30 || input.InventoryIntervalSeconds > 86400 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_INTERVAL", Message: "库存同步间隔必须为 30 到 86400 秒"}
	}
	if input.PriorityStart < 0 || input.PriorityStart > 1_000_000 || input.PriorityStep < 1 || input.PriorityStep > 100_000 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_PRIORITY", Message: "优先级起始值或步长超出范围"}
	}
	if input.ReconcileIntervalSeconds < 10 || input.ReconcileIntervalSeconds > 86400 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_INTERVAL", Message: "排序执行间隔必须为 10 到 86400 秒"}
	}
	if input.CacheRateWindowSeconds < minCacheRateWindowSeconds || input.CacheRateWindowSeconds > maxCacheRateWindowSeconds {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_CACHE_WINDOW", Message: "缓存率统计窗口必须为 300 到 86400 秒"}
	}
	if !isFinite(input.RatePriorityWeight) || input.RatePriorityWeight < 0 || input.RatePriorityWeight > maxPriorityWeight {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_PRIORITY_WEIGHT", Message: "倍率权重必须为 0 到 100"}
	}
	if !isFinite(input.CacheRatePriorityWeight) || input.CacheRatePriorityWeight < 0 || input.CacheRatePriorityWeight > maxPriorityWeight {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_PRIORITY_WEIGHT", Message: "缓存率权重必须为 0 到 100"}
	}
	return nil
}

const siteSelect = `
	SELECT s.id,s.owner_id,s.name,s.base_url,s.enabled,s.connection_state,s.last_error,s.version_hint,
	       s.inventory_interval_seconds,s.priority_start,s.priority_step,s.reconcile_interval_seconds,
	       s.cache_rate_priority_enabled,s.cache_rate_window_seconds,s.rate_priority_weight,s.cache_rate_priority_weight,
	       s.last_inventory_at,s.last_reconcile_at,s.last_cache_sample_at,
	       count(a.id) FILTER (WHERE a.deleted_at IS NULL),
	       count(a.id) FILTER (WHERE a.deleted_at IS NULL AND (a.health_enabled OR a.rate_sync_enabled OR a.priority_enabled OR a.guard_enabled)),
	       s.created_at
	FROM sites s LEFT JOIN upstream_accounts a ON a.site_id=s.id`

func scanSite(row pgx.Row) (Site, error) {
	var site Site
	err := row.Scan(&site.ID, &site.OwnerID, &site.Name, &site.BaseURL, &site.Enabled, &site.ConnectionState, &site.LastError, &site.VersionHint,
		&site.InventoryIntervalSeconds, &site.PriorityStart, &site.PriorityStep, &site.ReconcileIntervalSeconds,
		&site.CacheRatePriorityEnabled, &site.CacheRateWindowSeconds, &site.RatePriorityWeight, &site.CacheRatePriorityWeight,
		&site.LastInventoryAt, &site.LastReconcileAt, &site.LastCacheSampleAt, &site.AccountCount, &site.EnabledAutomationCount, &site.CreatedAt)
	return site, err
}

func (a *App) listSites(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	rows, err := a.db.Query(r.Context(), siteSelect+` WHERE s.owner_id=$1 GROUP BY s.id ORDER BY s.created_at`, identity.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	sites := make([]Site, 0)
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return err
		}
		sites = append(sites, site)
	}
	writeData(w, http.StatusOK, sites)
	return rows.Err()
}

func (a *App) createSite(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	var input siteInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	if err := normalizeSiteInput(&input, true); err != nil {
		return err
	}
	siteID := uuid.NewString()
	sealed, err := a.cipher.Encrypt(input.APIKey, "site:"+siteID)
	if err != nil {
		return err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	_, err = a.db.Exec(r.Context(), `
		INSERT INTO sites(id,owner_id,name,base_url,api_key_ciphertext,enabled,inventory_interval_seconds,priority_start,priority_step,reconcile_interval_seconds,cache_rate_priority_enabled,cache_rate_window_seconds,rate_priority_weight,cache_rate_priority_weight,next_cache_sample_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now())`, siteID, identity.ID, input.Name, input.BaseURL, sealed, enabled, input.InventoryIntervalSeconds, input.PriorityStart, input.PriorityStep, input.ReconcileIntervalSeconds, input.CacheRatePriorityEnabled, input.CacheRateWindowSeconds, input.RatePriorityWeight, input.CacheRatePriorityWeight)
	if err != nil {
		if strings.Contains(err.Error(), "sites_owner_id_base_url_key") {
			return &apiError{Status: http.StatusConflict, Code: "SITE_EXISTS", Message: "该站点地址已经存在"}
		}
		return err
	}
	_ = a.audit(r.Context(), identity.ID, identity.ID, siteID, "", "site.create", "success", map[string]any{"name": input.Name})
	// Creation succeeds even if the upstream is temporarily unavailable. The state and error remain visible.
	_ = a.syncSite(r.Context(), siteID, identity.ID, identity.ID, "manual")
	site, err := a.getSite(r.Context(), siteID, identity.ID)
	if err != nil {
		return err
	}
	writeData(w, http.StatusCreated, site)
	return nil
}

func (a *App) getSite(ctx context.Context, siteID, ownerID string) (Site, error) {
	return scanSite(a.db.QueryRow(ctx, siteSelect+` WHERE s.id=$1 AND s.owner_id=$2 GROUP BY s.id`, siteID, ownerID))
}

func (a *App) updateSite(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	siteID, err := requiredID(r, "siteID")
	if err != nil {
		return err
	}
	var input siteInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	if err := normalizeSiteInput(&input, false); err != nil {
		return err
	}
	sealed := ""
	if input.APIKey != "" {
		sealed, err = a.cipher.Encrypt(input.APIKey, "site:"+siteID)
		if err != nil {
			return err
		}
	}
	command, err := a.db.Exec(r.Context(), `
		UPDATE sites SET name=$3,base_url=$4,
		 api_key_ciphertext=CASE WHEN $5<>'' THEN $5 ELSE api_key_ciphertext END,
		 enabled=CASE WHEN $6::boolean THEN $7 ELSE enabled END,
		 inventory_interval_seconds=$8,priority_start=$9,priority_step=$10,reconcile_interval_seconds=$11,
		 cache_rate_priority_enabled=$12,cache_rate_window_seconds=$13,rate_priority_weight=$14,cache_rate_priority_weight=$15,
		 next_inventory_at=LEAST(next_inventory_at,now()),next_reconcile_at=LEAST(next_reconcile_at,now()),
		 next_cache_sample_at=CASE WHEN $12 THEN LEAST(next_cache_sample_at,now()) ELSE next_cache_sample_at END,updated_at=now()
		WHERE id=$1 AND owner_id=$2`, siteID, identity.ID, input.Name, input.BaseURL, sealed, input.Enabled != nil, boolValue(input.Enabled), input.InventoryIntervalSeconds, input.PriorityStart, input.PriorityStep, input.ReconcileIntervalSeconds, input.CacheRatePriorityEnabled, input.CacheRateWindowSeconds, input.RatePriorityWeight, input.CacheRatePriorityWeight)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	_ = a.audit(r.Context(), identity.ID, identity.ID, siteID, "", "site.update", "success", map[string]any{})
	site, err := a.getSite(r.Context(), siteID, identity.ID)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, site)
	return nil
}

func (a *App) deleteSite(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	siteID, err := requiredID(r, "siteID")
	if err != nil {
		return err
	}
	command, err := a.db.Exec(r.Context(), `DELETE FROM sites WHERE id=$1 AND owner_id=$2`, siteID, identity.ID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *App) siteSecret(ctx context.Context, siteID, ownerID string) (SiteSecret, error) {
	var site SiteSecret
	err := a.db.QueryRow(ctx, `SELECT id,owner_id,name,base_url,api_key_ciphertext,enabled FROM sites WHERE id=$1 AND owner_id=COALESCE(NULLIF($2,'')::uuid,owner_id)`, siteID, ownerID).Scan(&site.ID, &site.OwnerID, &site.Name, &site.BaseURL, &site.APIKeyCiphertext, &site.Enabled)
	return site, err
}

func (a *App) sub2Client(site SiteSecret) (*upstream.Sub2Client, error) {
	apiKey, err := a.cipher.Decrypt(site.APIKeyCiphertext, "site:"+site.ID)
	if err != nil {
		return nil, err
	}
	return upstream.NewSub2Client(site.BaseURL, apiKey, a.httpClient)
}

func (a *App) testSite(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	siteID, err := requiredID(r, "siteID")
	if err != nil {
		return err
	}
	site, err := a.siteSecret(r.Context(), siteID, identity.ID)
	if err != nil {
		return err
	}
	client, err := a.sub2Client(site)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	groups, testErr := client.ListGroups(ctx)
	if testErr != nil {
		a.recordSiteFailure(r.Context(), siteID, testErr)
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "CONNECTION_FAILED", Message: "无法连接 Sub2API，请检查地址和管理 API Key"}
	}
	version := client.Version(ctx)
	_, _ = a.db.Exec(r.Context(), `UPDATE sites SET connection_state='healthy',last_error=NULL,version_hint=NULLIF($2,''),updated_at=now() WHERE id=$1`, siteID, version)
	_ = a.audit(r.Context(), identity.ID, identity.ID, siteID, "", "site.test", "success", map[string]any{"groups": len(groups), "version": version})
	writeData(w, http.StatusOK, map[string]any{"healthy": true, "groups": len(groups), "version": version})
	return nil
}

func (a *App) syncSiteHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	siteID, err := requiredID(r, "siteID")
	if err != nil {
		return err
	}
	if err := a.syncSite(r.Context(), siteID, identity.ID, identity.ID, "manual"); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "SYNC_FAILED", Message: err.Error()}
	}
	site, err := a.getSite(r.Context(), siteID, identity.ID)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, site)
	return nil
}

func (a *App) recordSiteFailure(ctx context.Context, siteID string, err error) {
	state := "unreachable"
	var httpErr *upstream.HTTPError
	if errors.As(err, &httpErr) && (httpErr.Status == 401 || httpErr.Status == 403) {
		state = "auth_error"
	}
	message := err.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, _ = a.db.Exec(ctx, `UPDATE sites SET connection_state=$2,last_error=$3,updated_at=now() WHERE id=$1`, siteID, state, message)
}

func statusText(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
