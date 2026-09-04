package app

import (
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func (a *App) sampleSiteCacheHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	siteID, err := requiredID(r, "siteID")
	if err != nil {
		return err
	}
	if err := a.sampleCacheRates(r.Context(), siteID, identity.ID, "", true); err != nil {
		if err == pgx.ErrNoRows {
			return err
		}
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "CACHE_SAMPLE_FAILED", Message: fmt.Sprintf("缓存率采样失败：%v", err)}
	}
	site, err := a.getSite(r.Context(), siteID, identity.ID)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, site)
	return nil
}

func (a *App) sampleAccountCacheHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	accountID, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	work, err := a.loadAccountWork(r.Context(), accountID, identity.ID)
	if err != nil {
		return err
	}
	if err := a.sampleCacheRates(r.Context(), work.SiteID, identity.ID, work.ID, true); err != nil {
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "CACHE_SAMPLE_FAILED", Message: fmt.Sprintf("缓存率采样失败：%v", err)}
	}
	account, err := a.getAccount(r.Context(), accountID, identity.ID)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, map[string]any{"account": account})
	return nil
}
