package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (a *App) listRiskReviews(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	if err := requireAdmin(identity); err != nil {
		return err
	}
	rows, err := a.db.Query(r.Context(), `SELECT id::text,subject_type,subject_id,status,note,created_at,updated_at FROM risk_reviews WHERE owner_id=$1 ORDER BY updated_at DESC`, identity.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	result := make([]map[string]any, 0)
	for rows.Next() {
		var id, typ, sid, status, note string
		var created, updated any
		if err := rows.Scan(&id, &typ, &sid, &status, &note, &created, &updated); err != nil {
			return err
		}
		result = append(result, map[string]any{"id": id, "subject_type": typ, "subject_id": sid, "status": status, "note": note, "created_at": created, "updated_at": updated})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeData(w, http.StatusOK, result)
	return nil
}

func (a *App) saveRiskReview(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	if err := requireAdmin(identity); err != nil {
		return err
	}
	var input struct {
		SubjectType string `json:"subject_type"`
		SubjectID   string `json:"subject_id"`
		Status      string `json:"status"`
		Note        string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return err
	}
	input.SubjectType = strings.TrimSpace(input.SubjectType)
	input.SubjectID = strings.TrimSpace(input.SubjectID)
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = "open"
	}
	switch input.SubjectType {
	case "user", "ip", "token", "moderation":
	default:
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_RISK_REVIEW", Message: "风险复核对象类型无效"}
	}
	switch input.Status {
	case "open", "confirmed", "dismissed":
	default:
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_RISK_REVIEW", Message: "风险复核状态无效"}
	}
	if input.SubjectID == "" {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_RISK_REVIEW", Message: "风险复核对象和状态不能为空"}
	}
	id := uuid.NewString()
	var out map[string]any
	var created, updated time.Time
	err := a.db.QueryRow(r.Context(), `INSERT INTO risk_reviews(id,owner_id,subject_type,subject_id,status,note) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(owner_id,subject_type,subject_id) DO UPDATE SET status=EXCLUDED.status,note=EXCLUDED.note,updated_at=now() RETURNING id::text,subject_type,subject_id,status,note,created_at,updated_at`, id, identity.ID, input.SubjectType, input.SubjectID, input.Status, input.Note).Scan(&id, &input.SubjectType, &input.SubjectID, &input.Status, &input.Note, &created, &updated)
	if err != nil {
		return err
	}
	out = map[string]any{"id": id, "subject_type": input.SubjectType, "subject_id": input.SubjectID, "status": input.Status, "note": input.Note, "created_at": created, "updated_at": updated}
	_ = a.audit(r.Context(), identity.ID, identity.ID, "", "", "risk.review", "success", out)
	writeData(w, http.StatusOK, out)
	return nil
}
