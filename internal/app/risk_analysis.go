package app

import "net/http"

type riskSummaryRow struct {
	SiteID   string `json:"site_id"`
	SiteName string `json:"site_name"`
	Success  int    `json:"success"`
	Failure  int    `json:"failure"`
	Unknown  int    `json:"unknown"`
	Conflict int    `json:"conflict"`
}

func (a *App) riskSummaryHandler(w http.ResponseWriter, r *http.Request) error {
	if !a.config.OperationsExtensionsEnabled || !a.config.RiskAnalysisEnabled {
		return &apiError{Status: http.StatusNotFound, Code: "RISK_DISABLED", Message: "运营风控未启用"}
	}
	rows, err := a.db.Query(r.Context(), `SELECT s.id::text,s.name,count(*) FILTER (WHERE o.outcome='success'),count(*) FILTER (WHERE o.outcome='failure'),count(*) FILTER (WHERE o.outcome='unknown'),count(*) FILTER (WHERE o.outcome='conflict') FROM request_outcome_observations o JOIN sites s ON s.id=o.site_id WHERE s.owner_id=$1 AND o.seen_at>now()-interval '24 hours' GROUP BY s.id,s.name ORDER BY s.name`, identityFrom(r).ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	result := []riskSummaryRow{}
	for rows.Next() {
		var v riskSummaryRow
		if err := rows.Scan(&v.SiteID, &v.SiteName, &v.Success, &v.Failure, &v.Unknown, &v.Conflict); err != nil {
			return err
		}
		result = append(result, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeData(w, http.StatusOK, result)
	return nil
}
