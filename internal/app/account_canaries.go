package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
)

// Direct account profiles use the supplier's model name and Pilot's network
// path. They do not claim to reproduce Sub2API proxy/header/model rewriting.
func (a *App) accountCanarySource(ctx context.Context, p serviceProfileWork) (string, string, error) {
	w, err := a.loadAccountWork(ctx, p.AccountID, p.OwnerID)
	if err != nil {
		return "", "", err
	}
	if w.AccountType != "apikey" {
		return "", "", fmt.Errorf("直接协议档案仅支持 API Key 账号；其他类型请使用原生账号探测或分组入口探测")
	}
	works, err := a.loadAccountBalanceWork(ctx, p.OwnerID, []string{w.ID})
	if err != nil {
		return "", "", err
	}
	if len(works) != 1 || works[0].ObservedSourceBaseURL == nil || works[0].ObservedSourceCredentialFingerprint == "" {
		return "", "", fmt.Errorf("库存缺少可核对的来源身份，请先同步站点并确认导出权限")
	}
	client, err := a.clientForWork(w)
	if err != nil {
		return "", "", err
	}
	credentials, err := client.AccountUsageCredentials(ctx, []int64{w.RemoteID})
	if err != nil {
		return "", "", fmt.Errorf("无法读取此账号的来源凭据，请检查导出权限")
	}
	credential, ok := credentials[w.RemoteID]
	if !ok {
		return "", "", fmt.Errorf("导出数据未提供对应账号的 API Key 来源")
	}
	key := strings.TrimSpace(credential.APIKey)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	if fingerprint != works[0].ObservedSourceCredentialFingerprint || canonicalBalanceSourceURL(credential.BaseURL, "sub2api") != canonicalBalanceSourceURL(*works[0].ObservedSourceBaseURL, "sub2api") {
		_, _ = a.db.Exec(ctx, `UPDATE sites SET next_inventory_at=LEAST(next_inventory_at,now()) WHERE id=$1`, w.SiteID)
		return "", "", fmt.Errorf("来源身份已变化，等待库存同步后再探测")
	}
	fresh, err := a.loadServiceProfile(ctx, p.ID, p.OwnerID)
	if err != nil {
		return "", "", err
	}
	if fresh.Generation != p.Generation {
		return "", "", errEngineReplan
	}
	return credential.BaseURL, key, nil
}

func (a *App) probeTargetsHandler(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.db.Query(r.Context(), `SELECT g.id::text,g.name,s.name,'group',COALESCE(g.platform,'') FROM upstream_groups g JOIN sites s ON s.id=g.site_id WHERE s.owner_id=$1 AND g.deleted_at IS NULL UNION ALL SELECT a.id::text,a.name,s.name,'account',a.platform FROM upstream_accounts a JOIN sites s ON s.id=a.site_id WHERE s.owner_id=$1 AND a.account_type='apikey' AND a.deleted_at IS NULL ORDER BY 4,2`, identityFrom(r).ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type target struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		SiteName string `json:"site_name"`
		Kind     string `json:"kind"`
		Platform string `json:"platform"`
	}
	result := []target{}
	for rows.Next() {
		var v target
		if err = rows.Scan(&v.ID, &v.Name, &v.SiteName, &v.Kind, &v.Platform); err != nil {
			return err
		}
		result = append(result, v)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeData(w, 200, result)
	return nil
}
