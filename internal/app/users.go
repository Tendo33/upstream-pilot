package app

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (a *App) beginAdminUserMutation(ctx context.Context, identity Identity) (pgx.Tx, error) {
	if err := requireAdmin(identity); err != nil {
		return nil, err
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (pgx.Tx, error) {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, userAdminLockID); err != nil {
		return fail(err)
	}
	var role string
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT role,enabled FROM users WHERE id=$1`, identity.ID).Scan(&role, &enabled); err != nil {
		if err == pgx.ErrNoRows {
			return fail(&apiError{Status: http.StatusForbidden, Code: "ADMIN_REQUIRED", Message: "此操作仅限管理员"})
		}
		return fail(err)
	}
	if role != "admin" || !enabled {
		return fail(&apiError{Status: http.StatusForbidden, Code: "ADMIN_REQUIRED", Message: "此操作仅限管理员"})
	}
	return tx, nil
}

func (a *App) listUsers(w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(identityFrom(r)); err != nil {
		return err
	}
	rows, err := a.db.Query(r.Context(), `SELECT id,email,role,enabled,created_at,last_login_at FROM users ORDER BY created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	users := make([]User, 0)
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.Email, &user.Role, &user.Enabled, &user.CreatedAt, &user.LastLoginAt); err != nil {
			return err
		}
		users = append(users, user)
	}
	writeData(w, http.StatusOK, users)
	return rows.Err()
}

func (a *App) createUser(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	if err := requireAdmin(identity); err != nil {
		return err
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return err
	}
	if err := validatePassword(input.Password); err != nil {
		return err
	}
	if input.Role == "" {
		input.Role = "user"
	}
	if input.Role != "user" && input.Role != "admin" {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_ROLE", Message: "用户角色无效"}
	}
	hash, err := hashPassword(input.Password)
	if err != nil {
		return err
	}
	user := User{ID: uuid.NewString(), Email: email, Role: input.Role, Enabled: true}
	tx, err := a.beginAdminUserMutation(r.Context(), identity)
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	err = tx.QueryRow(r.Context(), `INSERT INTO users(id,email,password_hash,role) VALUES($1,$2,$3,$4) RETURNING created_at`, user.ID, user.Email, hash, user.Role).Scan(&user.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "users_email_unique") {
			return &apiError{Status: http.StatusConflict, Code: "EMAIL_EXISTS", Message: "该邮箱已存在"}
		}
		return err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	_ = a.audit(r.Context(), identity.ID, identity.ID, "", "", "user.create", "success", map[string]any{"user_id": user.ID, "role": user.Role})
	writeData(w, http.StatusCreated, user)
	return nil
}

func (a *App) updateUser(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	if err := requireAdmin(identity); err != nil {
		return err
	}
	userID, err := requiredID(r, "userID")
	if err != nil {
		return err
	}
	var input struct {
		Role     *string `json:"role"`
		Enabled  *bool   `json:"enabled"`
		Password string  `json:"password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	if userID == identity.ID && ((input.Enabled != nil && !*input.Enabled) || (input.Role != nil && *input.Role != "admin")) {
		return &apiError{Status: http.StatusBadRequest, Code: "SELF_LOCKOUT", Message: "不能停用或降级当前管理员"}
	}
	if input.Role != nil && *input.Role != "user" && *input.Role != "admin" {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_ROLE", Message: "用户角色无效"}
	}
	passwordHash := ""
	if input.Password != "" {
		if err := validatePassword(input.Password); err != nil {
			return err
		}
		hash, err := hashPassword(input.Password)
		if err != nil {
			return err
		}
		passwordHash = hash
	}
	tx, err := a.beginAdminUserMutation(r.Context(), identity)
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var currentRole string
	var currentEnabled bool
	if err := tx.QueryRow(r.Context(), `SELECT role,enabled FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&currentRole, &currentEnabled); err != nil {
		return err
	}
	resultRole := currentRole
	if input.Role != nil {
		resultRole = *input.Role
	}
	resultEnabled := currentEnabled
	if input.Enabled != nil {
		resultEnabled = *input.Enabled
	}
	if currentRole == "admin" && currentEnabled && (resultRole != "admin" || !resultEnabled) {
		var anotherAdmin bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id<>$1 AND role='admin' AND enabled)`, userID).Scan(&anotherAdmin); err != nil {
			return err
		}
		if !anotherAdmin {
			return &apiError{Status: http.StatusConflict, Code: "LAST_ADMIN", Message: "系统必须至少保留一个启用的管理员"}
		}
	}
	var user User
	err = tx.QueryRow(r.Context(), `
		UPDATE users SET
		 role=CASE WHEN $2::boolean THEN $3 ELSE role END,
		 enabled=CASE WHEN $4::boolean THEN $5 ELSE enabled END,
		 password_hash=CASE WHEN $6<>'' THEN $6 ELSE password_hash END,
		 updated_at=now()
		WHERE id=$1
		RETURNING id,email,role,enabled,created_at,last_login_at`,
		userID, input.Role != nil, stringValue(input.Role), input.Enabled != nil, boolValue(input.Enabled), passwordHash,
	).Scan(&user.ID, &user.Email, &user.Role, &user.Enabled, &user.CreatedAt, &user.LastLoginAt)
	if err != nil {
		return err
	}
	if input.Password != "" || (input.Enabled != nil && !*input.Enabled) {
		if _, err := tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
			return err
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	_ = a.audit(r.Context(), identity.ID, identity.ID, "", "", "user.update", "success", map[string]any{"user_id": userID})
	writeData(w, http.StatusOK, user)
	return nil
}

func (a *App) deleteUser(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	if err := requireAdmin(identity); err != nil {
		return err
	}
	userID, err := requiredID(r, "userID")
	if err != nil {
		return err
	}
	if userID == identity.ID {
		return &apiError{Status: http.StatusBadRequest, Code: "SELF_DELETE", Message: "不能删除当前管理员"}
	}
	tx, err := a.beginAdminUserMutation(r.Context(), identity)
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var role string
	var enabled bool
	if err := tx.QueryRow(r.Context(), `SELECT role,enabled FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&role, &enabled); err != nil {
		return err
	}
	if role == "admin" && enabled {
		var anotherAdmin bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id<>$1 AND role='admin' AND enabled)`, userID).Scan(&anotherAdmin); err != nil {
			return err
		}
		if !anotherAdmin {
			return &apiError{Status: http.StatusConflict, Code: "LAST_ADMIN", Message: "系统必须至少保留一个启用的管理员"}
		}
	}
	command, err := tx.Exec(r.Context(), `DELETE FROM users WHERE id=$1`, userID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return &apiError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "用户不存在"}
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	_ = a.audit(r.Context(), identity.ID, identity.ID, "", "", "user.delete", "success", map[string]any{"user_id": userID})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func boolValue(value *bool) bool {
	return value != nil && *value
}
