package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"github.com/google/uuid"
)

type ServiceObjectives struct {
	SuccessPercent            float64  `json:"success_percent"`
	FirstContentMS            int      `json:"first_content_ms"`
	RequireComplete           bool     `json:"require_complete"`
	MinimumSamples            int      `json:"minimum_samples"`
	MinimumIndependentBackups int      `json:"minimum_independent_backups"`
	MaxRequestCost            *float64 `json:"max_request_cost"`
}
type ProbeBudget struct {
	DailyRequests      int      `json:"daily_requests"`
	DailyTokens        int      `json:"daily_tokens"`
	DailyCost          *float64 `json:"daily_cost"`
	RequestCostReserve *float64 `json:"request_cost_reserve"`
	Currency           string   `json:"currency"`
	CostBasis          string   `json:"cost_basis"`
}
type ServiceProfileConfig struct {
	Adaptive              bool `json:"adaptive"`
	DirectSourceConfirmed bool `json:"direct_source_confirmed"`
	upstream.CanarySpec
	Name              string            `json:"name"`
	BaseURL           string            `json:"base_url"`
	GroupKeyConfirmed bool              `json:"group_key_confirmed"`
	IntervalSeconds   int               `json:"interval_seconds"`
	TimeoutSeconds    int               `json:"timeout_seconds"`
	Objectives        ServiceObjectives `json:"objectives"`
	Budget            ProbeBudget       `json:"budget"`
}

func defaultServiceProfile() ServiceProfileConfig {
	return ServiceProfileConfig{Adaptive: true, CanarySpec: upstream.CanarySpec{Protocol: "responses", Stream: true, MaxOutputTokens: 512}, IntervalSeconds: 3600, TimeoutSeconds: 45, Objectives: ServiceObjectives{SuccessPercent: 99, FirstContentMS: 8000, RequireComplete: true, MinimumSamples: 5, MinimumIndependentBackups: 1}, Budget: ProbeBudget{DailyRequests: 24, DailyTokens: 120000}}
}

// Input reservation is deliberately conservative for the fixed short prompt and
// tool schema. Unknown provider-added tokens/costs are never reported as measured.
func (p ServiceProfileConfig) reservedTokens() int { return 4096 + p.MaxOutputTokens }
func (p ServiceProfileConfig) Validate() error {
	if err := p.CanarySpec.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 120 {
		return errors.New("档案名称须为 1–120 个字符")
	}
	if p.IntervalSeconds < 30 || p.IntervalSeconds > 86400 || p.TimeoutSeconds < 3 || p.TimeoutSeconds > 300 || p.TimeoutSeconds >= p.IntervalSeconds {
		return errors.New("超时须小于探测间隔；间隔至少 30 秒")
	}
	o := p.Objectives
	if !isFinite(o.SuccessPercent) || o.SuccessPercent <= 0 || o.SuccessPercent > 100 || o.FirstContentMS < 100 || o.FirstContentMS > 300000 || o.MinimumSamples < 2 || o.MinimumSamples > 300 || o.MinimumIndependentBackups < 1 || o.MinimumIndependentBackups > 100 {
		return errors.New("分组服务目标无效")
	}
	b := p.Budget
	if b.DailyRequests < 1 || b.DailyRequests > 10000 || b.DailyTokens < p.reservedTokens() || b.DailyTokens > 100000000 {
		return errors.New("每日请求或 token 预算无效，须至少容纳一次探测预留")
	}
	for _, v := range []*float64{b.DailyCost, b.RequestCostReserve, o.MaxRequestCost} {
		if v != nil && (!isFinite(*v) || *v <= 0 || *v > 1000000) {
			return errors.New("成本预算须为正的有限数值")
		}
	}
	if b.DailyCost != nil && (b.RequestCostReserve == nil || *b.RequestCostReserve > *b.DailyCost) {
		return errors.New("启用金额预算须提供可容纳的单次成本预留；价格未知时不能开始付费探测")
	}
	if b.RequestCostReserve != nil && (len(b.Currency) != 3 || strings.TrimSpace(b.CostBasis) == "" || len(b.CostBasis) > 256) {
		return errors.New("成本预留须注明三位币种与计价依据")
	}
	return nil
}

type serviceProfileWork struct {
	AccountID     string               `json:"account_id"`
	ID            string               `json:"id"`
	GroupID       string               `json:"group_id"`
	SiteID        string               `json:"site_id"`
	GroupName     string               `json:"group_name"`
	OwnerID       string               `json:"-"`
	SiteBaseURL   string               `json:"-"`
	KeyCiphertext string               `json:"-"`
	KeyConfigured bool                 `json:"key_configured"`
	Generation    int64                `json:"generation"`
	Enabled       bool                 `json:"enabled"`
	NextProbeAt   time.Time            `json:"next_probe_at"`
	LastProbeAt   *time.Time           `json:"last_probe_at"`
	LastError     string               `json:"last_error"`
	Config        ServiceProfileConfig `json:"config"`
}

const serviceProfileParentLive = `((p.group_id IS NOT NULL AND g.deleted_at IS NULL) OR(p.account_id IS NOT NULL AND a.deleted_at IS NULL))`
const serviceProfileJoins = ` FROM service_profiles p LEFT JOIN upstream_groups g ON g.id=p.group_id LEFT JOIN upstream_accounts a ON a.id=p.account_id JOIN sites s ON s.id=COALESCE(g.site_id,a.site_id)`
const serviceProfileSelect = `SELECT p.id::text,COALESCE(p.group_id::text,''),COALESCE(p.account_id::text,''),s.id::text,COALESCE(g.name,a.name),s.owner_id::text,s.base_url,p.key_ciphertext,p.generation,p.enabled,p.next_probe_at,p.last_probe_at,p.last_error,p.config` + serviceProfileJoins

func scanServiceProfile(row rowScanner) (serviceProfileWork, error) {
	var p serviceProfileWork
	var raw []byte
	err := row.Scan(&p.ID, &p.GroupID, &p.AccountID, &p.SiteID, &p.GroupName, &p.OwnerID, &p.SiteBaseURL, &p.KeyCiphertext, &p.Generation, &p.Enabled, &p.NextProbeAt, &p.LastProbeAt, &p.LastError, &raw)
	if err == nil {
		p.Config = defaultServiceProfile()
		err = json.Unmarshal(raw, &p.Config)
	}
	p.KeyConfigured = p.KeyCiphertext != ""
	return p, err
}
func (a *App) loadServiceProfile(ctx context.Context, id, owner string) (serviceProfileWork, error) {
	return scanServiceProfile(a.db.QueryRow(ctx, serviceProfileSelect+` WHERE p.id=$1 AND s.owner_id=COALESCE(NULLIF($2,'')::uuid,s.owner_id) AND `+serviceProfileParentLive, id, owner))
}
func (a *App) serviceProfilesHandler(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.db.Query(r.Context(), serviceProfileSelect+` WHERE s.owner_id=$1 AND `+serviceProfileParentLive+` ORDER BY COALESCE(g.name,a.name),p.created_at`, identityFrom(r).ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	profiles := []serviceProfileWork{}
	for rows.Next() {
		p, err := scanServiceProfile(rows)
		if err != nil {
			return err
		}
		profiles = append(profiles, p)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeData(w, 200, profiles)
	return nil
}
func (a *App) saveServiceProfileHandler(w http.ResponseWriter, r *http.Request) error {
	input := struct {
		AccountID  string               `json:"account_id"`
		Generation int64                `json:"generation"`
		ID         string               `json:"id"`
		GroupID    string               `json:"group_id"`
		Enabled    bool                 `json:"enabled"`
		Key        string               `json:"key"`
		ClearKey   bool                 `json:"clear_key"`
		Config     ServiceProfileConfig `json:"config"`
	}{Config: defaultServiceProfile()}
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	if input.ID != "" {
		if _, err := uuid.Parse(input.ID); err != nil {
			return &apiError{Status: 400, Code: "INVALID_PROFILE_ID", Message: "档案标识无效"}
		}
	}
	if (input.GroupID == "") == (input.AccountID == "") {
		return &apiError{Status: 400, Code: "INVALID_TARGET", Message: "请选择一个分组或账号作为探测目标"}
	}
	targetID := input.GroupID
	if input.AccountID != "" {
		targetID = input.AccountID
	}
	if _, err := uuid.Parse(targetID); err != nil {
		return &apiError{Status: 400, Code: "INVALID_TARGET", Message: "探测目标标识无效"}
	}
	if err := input.Config.Validate(); err != nil {
		return &apiError{Status: 400, Code: "INVALID_PROFILE", Message: err.Error()}
	}
	if input.Config.BaseURL != "" {
		normalized, err := upstream.NormalizeBaseURL(input.Config.BaseURL)
		if err != nil {
			return &apiError{Status: 400, Code: "INVALID_ENTRY", Message: "实际入口地址无效"}
		}
		input.Config.BaseURL = normalized
	}
	owner := identityFrom(r).ID
	var site string
	targetQuery := `SELECT g.site_id::text FROM upstream_groups g JOIN sites s ON s.id=g.site_id WHERE g.id=$1 AND s.owner_id=$2 AND g.deleted_at IS NULL`
	if input.AccountID != "" {
		targetQuery = `SELECT a.site_id::text FROM upstream_accounts a JOIN sites s ON s.id=a.site_id WHERE a.id=$1 AND s.owner_id=$2 AND a.account_type='apikey' AND a.deleted_at IS NULL`
		if input.Key != "" || input.Config.BaseURL != "" {
			return &apiError{Status: 400, Code: "ACCOUNT_SOURCE_MANAGED", Message: "账号直连档案使用库存来源和按需读取的凭据，不能另填地址或 Key"}
		}
	}
	if err := a.db.QueryRow(r.Context(), targetQuery, targetID, owner).Scan(&site); err != nil {
		return err
	}
	keyCipher := ""
	generation := int64(0)
	if input.ID == "" {
		input.ID = uuid.NewString()
	} else {
		old, err := a.loadServiceProfile(r.Context(), input.ID, owner)
		if err != nil {
			return err
		}
		if old.GroupID != input.GroupID || old.AccountID != input.AccountID {
			return &apiError{Status: 400, Code: "GROUP_CHANGED", Message: "更换探测目标请新建档案"}
		}
		if input.Generation != old.Generation {
			return &apiError{Status: 409, Code: "PROFILE_CHANGED", Message: "档案已变化，请刷新后再编辑"}
		}
		keyCipher = old.KeyCiphertext
		generation = old.Generation
	}
	if len(input.Key) > 16384 {
		return &apiError{Status: 400, Code: "INVALID_KEY", Message: "Key 长度超限"}
	}
	if input.ClearKey {
		keyCipher = ""
	} else if input.Key != "" {
		var err error
		keyCipher, err = a.cipher.Encrypt(input.Key, "service-profile:"+input.ID)
		if err != nil {
			return err
		}
	}
	if input.Enabled && input.AccountID == "" && (!input.Config.GroupKeyConfirmed || keyCipher == "") {
		return &apiError{Status: 400, Code: "GROUP_KEY_REQUIRED", Message: "开启前须保存并确认此 Key 专属于所选分组"}
	}
	if input.Enabled && input.AccountID != "" && !input.Config.DirectSourceConfirmed {
		return &apiError{Status: 400, Code: "DIRECT_SOURCE_REQUIRED", Message: "请确认直连探测视角与凭据读取"}
	}
	raw, _ := json.Marshal(input.Config)
	command, err := a.db.Exec(r.Context(), `INSERT INTO service_profiles(id,group_id,config,key_ciphertext,enabled,account_id) VALUES($1,NULLIF($2,'')::uuid,$3,$4,$5,NULLIF($7,'')::uuid) ON CONFLICT(id) DO UPDATE SET config=excluded.config,key_ciphertext=excluded.key_ciphertext,enabled=excluded.enabled,generation=service_profiles.generation+1,next_probe_at=now(),updated_at=now() WHERE service_profiles.generation=$6`, input.ID, input.GroupID, raw, keyCipher, input.Enabled, generation, input.AccountID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return &apiError{Status: 409, Code: "PROFILE_CHANGED", Message: "档案已被其他操作修改，请刷新"}
	}
	_ = a.audit(r.Context(), owner, owner, site, "", "service_profile.save", "success", map[string]any{"profile_id": input.ID, "group_id": input.GroupID, "enabled": input.Enabled})
	p, err := a.loadServiceProfile(r.Context(), input.ID, owner)
	if err != nil {
		return err
	}
	writeData(w, 200, p)
	return nil
}
