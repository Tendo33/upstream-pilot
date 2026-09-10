package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (a *App) requireSource(r *http.Request) error {
	if err := requireAdmin(identityFrom(r)); err != nil {
		return err
	}
	if a.sourceDB == nil {
		return &apiError{Status: http.StatusServiceUnavailable, Code: "SOURCE_DATABASE_REQUIRED", Message: "请配置 PILOT_SUB2API_DATABASE_URL，连接 Sub2API 只读数据库"}
	}
	return nil
}

func (a *App) sourceLogPool() *pgxpool.Pool {
	if a.sourceLogDB != nil {
		return a.sourceLogDB
	}
	return a.sourceDB
}

func sourceCapabilityError(err error, message string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42P01":
			return &apiError{Status: http.StatusNotFound, Code: "SOURCE_CAPABILITY_UNAVAILABLE", Message: message}
		case "42703":
			return &apiError{Status: http.StatusNotFound, Code: "SOURCE_CAPABILITY_UNAVAILABLE", Message: "Sub2API 数据库字段不完整，无法读取该页面所需的列"}
		}
	}
	if strings.Contains(err.Error(), "does not exist") {
		return &apiError{Status: http.StatusNotFound, Code: "SOURCE_CAPABILITY_UNAVAILABLE", Message: message}
	}
	return err
}

func (a *App) serviceUsersHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		page = 100000
	}
	var raw []byte
	err := a.sourceDB.QueryRow(r.Context(), `SELECT jsonb_build_object('items',COALESCE(jsonb_agg(v),'[]'),'page',$2::int,'page_size',50,'total',(SELECT count(*) FROM users WHERE deleted_at IS NULL AND ($1='' OR email ILIKE '%'||$1||'%' OR username ILIKE '%'||$1||'%'))) FROM (
	 SELECT id,username,email,role,status,balance,concurrency,created_at,last_login_at,last_active_at
	 FROM users WHERE deleted_at IS NULL AND ($1='' OR email ILIKE '%'||$1||'%' OR username ILIKE '%'||$1||'%')
	 ORDER BY id DESC LIMIT 50 OFFSET (($2::int-1)*50)
	) v`, r.URL.Query().Get("search"), page).Scan(&raw)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, json.RawMessage(raw))
	return nil
}

func (a *App) sourceCapabilitiesHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	var raw []byte
	err := a.sourceDB.QueryRow(r.Context(), `SELECT jsonb_build_object(
	 'users',to_regclass('public.users') IS NOT NULL,
	 'usage_logs',to_regclass('public.usage_logs') IS NOT NULL,
	 'api_keys',to_regclass('public.api_keys') IS NOT NULL,
	 'audit_logs',to_regclass('public.audit_logs') IS NOT NULL,
	 'content_moderation_logs',to_regclass('public.content_moderation_logs') IS NOT NULL
	)`).Scan(&raw)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, json.RawMessage(raw))
	return nil
}

func (a *App) sourceOverviewHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	var raw []byte
	err := a.sourceLogPool().QueryRow(r.Context(), `SELECT jsonb_build_object(
	 'as_of',now(),
	 'requests_24h',count(*),
	 'active_users_24h',count(DISTINCT user_id),
	 'active_tokens_24h',count(DISTINCT api_key_id),
	 'active_ips_24h',count(DISTINCT NULLIF(ip_address,'')),
	 'cost_24h',COALESCE(sum(actual_cost),0)
	) FROM usage_logs WHERE created_at>=now()-interval '24 hours'`).Scan(&raw)
	if err != nil {
		return err
	}
	var users, keys int64
	if err := a.sourceDB.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE deleted_at IS NULL`).Scan(&users); err != nil {
		return err
	}
	if err := a.sourceDB.QueryRow(r.Context(), `SELECT count(*) FROM api_keys WHERE deleted_at IS NULL`).Scan(&keys); err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	payload["total_users"] = users
	payload["total_tokens"] = keys
	writeData(w, http.StatusOK, payload)
	return nil
}

func (a *App) sourceBillingHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		page = 100000
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}
	var raw []byte
	err := a.sourceDB.QueryRow(r.Context(), `SELECT jsonb_build_object('items',COALESCE(jsonb_agg(v),'[]'),'total',(SELECT count(*) FROM payment_orders),'page',$1::int,'page_size',$2::int,'summary',jsonb_build_object('count_by_status',COALESCE((SELECT jsonb_object_agg(status,n) FROM (SELECT status,count(*) n FROM payment_orders GROUP BY status)s),'{}'),'amount_by_status',COALESCE((SELECT jsonb_object_agg(status,total) FROM (SELECT status,COALESCE(sum(amount),0) total FROM payment_orders GROUP BY status)s),'{}'))) FROM (SELECT id,user_id,status AS normalized_status,amount,NULL::text AS currency,created_at,completed_at FROM payment_orders ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET (($1-1)*$2)) v`, page, pageSize).Scan(&raw)
	if err != nil {
		return err
	}
	var response sourceBillingResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	normalizeBillingResponse(&response)
	writeData(w, http.StatusOK, response)
	return nil
}

func (a *App) sourceRedeemHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if size < 1 || size > 100 {
		size = 50
	}
	var raw []byte
	err := a.sourceDB.QueryRow(r.Context(), `SELECT jsonb_build_object('items',COALESCE(jsonb_agg(v),'[]'),'total',(SELECT count(*) FROM redeem_codes)) FROM (SELECT id,code,type,status,value,used_by,expires_at,created_at FROM redeem_codes ORDER BY created_at DESC,id DESC LIMIT $1 OFFSET (($2-1)*$1)) v`, size, page).Scan(&raw)
	if err != nil {
		return err
	}
	var response struct {
		Items []redeemCodeView `json:"items"`
		Total int64            `json:"total"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	for i := range response.Items {
		response.Items[i].Code = maskRedeemCode(response.Items[i].Code)
	}
	writeData(w, http.StatusOK, response)
	return nil
}

func (a *App) serviceUserDetailHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	id, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil || id <= 0 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_USER_ID", Message: "服务用户 ID 无效"}
	}
	var raw []byte
	err = a.sourceDB.QueryRow(r.Context(), `SELECT jsonb_build_object('user',u,'keys',COALESCE((SELECT jsonb_agg(k) FROM (SELECT id,name,status,last_used_at,quota,quota_used FROM api_keys WHERE user_id=$1 AND deleted_at IS NULL ORDER BY id DESC) k),'[]'::jsonb)) FROM (SELECT id,username,email,role,status,balance,concurrency,created_at,last_login_at,last_active_at FROM users WHERE id=$1 AND deleted_at IS NULL) u`, id).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &apiError{Status: http.StatusNotFound, Code: "SERVICE_USER_NOT_FOUND", Message: "服务用户不存在"}
		}
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	var usageRaw []byte
	err = a.sourceLogPool().QueryRow(r.Context(), `SELECT jsonb_build_object('requests',count(*),'input_tokens',COALESCE(sum(input_tokens),0),'output_tokens',COALESCE(sum(output_tokens),0),'cost',COALESCE(sum(actual_cost),0),'ips',count(DISTINCT NULLIF(ip_address,'')),'tokens',count(DISTINCT api_key_id),'last_seen',max(created_at)) FROM usage_logs WHERE user_id=$1 AND created_at>=now()-interval '24 hours'`, id).Scan(&usageRaw)
	if err != nil {
		return sourceCapabilityError(err, "Sub2API 用量日志不可用")
	}
	var usage any
	if err := json.Unmarshal(usageRaw, &usage); err != nil {
		return err
	}
	payload["usage"] = usage
	writeData(w, http.StatusOK, payload)
	return nil
}

func (a *App) sourceRiskHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	metric := r.URL.Query().Get("metric")
	if metric != "cost" {
		metric = "requests"
	}
	if a.sourceLogDB != nil {
		return a.sourceRiskFromLogDB(w, r, metric)
	}
	var raw []byte
	err := a.sourceDB.QueryRow(r.Context(), `WITH windows(hours) AS (VALUES(1),(3),(24))
	 SELECT jsonb_build_object('source','sub2api.usage_logs','as_of',now(),'metric',$1::text,'windows',jsonb_agg(v ORDER BY v.hours)) FROM (
	 SELECT hours,COALESCE((SELECT jsonb_agg(r) FROM (
	 SELECT l.user_id,COALESCE(NULLIF(u.username,''),u.email,'用户 #'||l.user_id) AS name,
	 count(*) AS requests,count(DISTINCT NULLIF(l.ip_address,'')) AS ips,count(DISTINCT l.api_key_id) AS tokens,
	 COALESCE(sum(l.actual_cost),0) AS cost,max(l.created_at) AS last_seen
	 FROM usage_logs l LEFT JOIN users u ON u.id=l.user_id
	 WHERE l.created_at>=now()-hours*interval '1 hour' AND l.created_at<=now()
	 GROUP BY l.user_id,u.username,u.email
	 ORDER BY CASE WHEN $1='cost' THEN COALESCE(sum(l.actual_cost),0) ELSE count(*) END DESC,l.user_id LIMIT 10
	 ) r),'[]') AS items FROM windows
	 ) v`, metric).Scan(&raw)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, json.RawMessage(raw))
	return nil
}

func (a *App) sourceIPsHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours != 1 && hours != 3 && hours != 24 {
		hours = 24
	}
	var raw []byte
	err := a.sourceLogPool().QueryRow(r.Context(), `SELECT jsonb_build_object('hours',$1::int,'as_of',now(),'items',COALESCE(jsonb_agg(v),'[]')) FROM (
	 SELECT ip_address AS ip,count(*) AS requests,count(DISTINCT user_id) AS users,count(DISTINCT api_key_id) AS tokens,
	 COALESCE(sum(actual_cost),0) AS cost,max(created_at) AS last_seen
	 FROM usage_logs WHERE created_at>=now()-$1::int*interval '1 hour' AND created_at<=now() AND NULLIF(ip_address,'') IS NOT NULL
	 GROUP BY ip_address ORDER BY count(*) DESC,ip_address LIMIT 100
	) v`, hours).Scan(&raw)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, json.RawMessage(raw))
	return nil
}

func (a *App) sourceSharedIPsHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	var raw []byte
	err := a.sourceLogPool().QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM (
	 SELECT ip_address AS ip,count(*) AS requests,count(DISTINCT user_id) AS users,count(DISTINCT api_key_id) AS tokens,
	 jsonb_agg(DISTINCT user_id) FILTER (WHERE user_id IS NOT NULL) AS user_ids,max(created_at) AS last_seen
	 FROM usage_logs WHERE created_at>=now()-interval '24 hours' AND NULLIF(ip_address,'') IS NOT NULL
	 GROUP BY ip_address HAVING count(DISTINCT user_id)>=2 ORDER BY count(DISTINCT user_id) DESC,count(*) DESC LIMIT 100
	) v`).Scan(&raw)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, json.RawMessage(raw))
	return nil
}

func (a *App) sourceTokensHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	var raw []byte
	err := a.sourceLogPool().QueryRow(r.Context(), `SELECT jsonb_build_object('hours',24,'as_of',now(),'items',COALESCE(jsonb_agg(v),'[]')) FROM (
	 SELECT l.api_key_id AS token_id,count(*) AS requests,count(DISTINCT l.user_id) AS users,count(DISTINCT NULLIF(l.ip_address,'')) AS ips,COALESCE(sum(l.actual_cost),0) AS cost,max(l.created_at) AS last_seen
	 FROM usage_logs l WHERE l.created_at>=now()-interval '24 hours' AND l.created_at<=now() AND l.api_key_id IS NOT NULL
	 GROUP BY l.api_key_id ORDER BY count(*) DESC,l.api_key_id LIMIT 100
	) v`).Scan(&raw)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, json.RawMessage(raw))
	return nil
}

func (a *App) sourceTokenRotationHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	var raw []byte
	err := a.sourceLogPool().QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM (
	 SELECT user_id,count(DISTINCT api_key_id) AS token_count,count(*) AS requests,
	 count(DISTINCT NULLIF(ip_address,'')) AS ip_count,
	 max(created_at) AS last_seen,
	 CASE WHEN count(DISTINCT api_key_id)>=5 OR count(DISTINCT NULLIF(ip_address,''))>=5 THEN 'high' WHEN count(DISTINCT api_key_id)>=3 OR count(DISTINCT NULLIF(ip_address,''))>=3 THEN 'review' ELSE 'normal' END AS risk
	 FROM usage_logs WHERE created_at>=now()-interval '24 hours' AND api_key_id IS NOT NULL AND user_id IS NOT NULL
	 GROUP BY user_id HAVING count(DISTINCT api_key_id)>=3 OR count(DISTINCT NULLIF(ip_address,''))>=3
	 ORDER BY count(DISTINCT api_key_id) DESC,count(*) DESC LIMIT 100
	) v`).Scan(&raw)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, json.RawMessage(raw))
	return nil
}

func (a *App) sourceAuditHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	var raw []byte
	err := a.sourceDB.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM (SELECT id,created_at,actor_user_id,actor_email,actor_role,action,method,path,request_id,client_ip,status_code,latency_ms FROM audit_logs ORDER BY created_at DESC,id DESC LIMIT 200) v`).Scan(&raw)
	if err != nil {
		return sourceCapabilityError(err, "Sub2API 数据库未启用活动审计日志")
	}
	writeData(w, http.StatusOK, json.RawMessage(raw))
	return nil
}

func (a *App) sourceModerationHandler(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireSource(r); err != nil {
		return err
	}
	var raw []byte
	err := a.sourceDB.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM (SELECT id,created_at,request_id,user_id,user_email,api_key_id,model,action,flagged,highest_category,highest_score,violation_count,auto_banned,error FROM content_moderation_logs ORDER BY created_at DESC,id DESC LIMIT 200) v`).Scan(&raw)
	if err != nil {
		return sourceCapabilityError(err, "Sub2API 数据库未启用内容审计日志")
	}
	writeData(w, http.StatusOK, json.RawMessage(raw))
	return nil
}

type sourceRiskItem struct {
	UserID   int64     `json:"user_id"`
	Name     string    `json:"name"`
	Requests int64     `json:"requests"`
	IPs      int64     `json:"ips"`
	Tokens   int64     `json:"tokens"`
	Cost     float64   `json:"cost"`
	LastSeen time.Time `json:"last_seen"`
}

func (a *App) sourceRiskFromLogDB(w http.ResponseWriter, r *http.Request, metric string) error {
	type window struct {
		Hours int              `json:"hours"`
		Items []sourceRiskItem `json:"items"`
	}
	windows := make([]window, 0, 3)
	for _, hours := range []int{1, 3, 24} {
		rows, err := a.sourceLogDB.Query(r.Context(), `SELECT user_id,count(*),count(DISTINCT NULLIF(ip_address,'')),count(DISTINCT api_key_id),COALESCE(sum(actual_cost),0),max(created_at) FROM usage_logs WHERE created_at>=now()-$1::int*interval '1 hour' AND created_at<=now() GROUP BY user_id ORDER BY CASE WHEN $2='cost' THEN COALESCE(sum(actual_cost),0) ELSE count(*) END DESC,user_id LIMIT 10`, hours, metric)
		if err != nil {
			return err
		}
		items := []sourceRiskItem{}
		ids := []int64{}
		for rows.Next() {
			var v sourceRiskItem
			if err := rows.Scan(&v.UserID, &v.Requests, &v.IPs, &v.Tokens, &v.Cost, &v.LastSeen); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, v.UserID)
			items = append(items, v)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(ids) > 0 {
			users, err := a.sourceUsersByID(r.Context(), ids)
			if err != nil {
				return err
			}
			for i := range items {
				items[i].Name = users[items[i].UserID]
			}
		}
		windows = append(windows, window{Hours: hours, Items: items})
	}
	writeData(w, http.StatusOK, map[string]any{"source": "sub2api.usage_logs", "as_of": time.Now().UTC(), "metric": metric, "windows": windows})
	return nil
}

func (a *App) sourceUsersByID(ctx context.Context, ids []int64) (map[int64]string, error) {
	rows, err := a.sourceDB.Query(ctx, `SELECT id,COALESCE(NULLIF(username,''),email,'用户 #'||id) FROM users WHERE id=ANY($1) AND deleted_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
