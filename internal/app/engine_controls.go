package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/quality"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
)

type controlIntent struct {
	ControlScope     string         `json:"control_scope,omitempty"`
	SourceGeneration int64          `json:"source_generation"`
	PlanID           string         `json:"plan_id,omitempty"`
	ConfigGeneration int64          `json:"config_generation,omitempty"`
	From             map[string]any `json:"from"`
	To               map[string]any `json:"to"`
}

func remoteControls(r upstream.Sub2Account) map[string]any {
	m := map[string]any{"priority": float64(r.Priority), "schedulable": r.Schedulable, "load_factor": nil}
	if r.LoadFactor != nil {
		m["load_factor"] = float64(*r.LoadFactor)
	}
	if r.Concurrency != nil {
		m["concurrency"] = float64(*r.Concurrency)
	}
	return m
}
func controlEqual(a, b any) bool { return reflect.DeepEqual(a, b) }
func controlEnabled(p quality.Policy, k string) bool {
	switch k {
	case "priority":
		return p.Mode == "priority"
	case "schedulable":
		return p.AutoPause
	case "load_factor":
		return p.AutoLoadFactor
	case "concurrency":
		return p.AutoConcurrency
	}
	return false
}

func (a *App) applyEngineControls(ctx context.Context, p *engineWork, works []engineWork, policies map[string]quality.GroupPolicy) (int, bool, error) {
	s := &p.Decision.State
	w := p.Work
	before := w.Priority
	if p.PreflightError != nil {
		return before, false, p.PreflightError
	}
	if s.Conflict {
		return before, false, nil
	}
	var generation int64
	if err := a.db.QueryRow(ctx, `SELECT config_generation FROM upstream_accounts WHERE id=$1 AND source_generation=$2`, w.ID, w.SourceGeneration).Scan(&generation); err != nil {
		return before, false, err
	}
	if generation != w.ConfigGeneration {
		return before, false, errEngineReplan
	}
	client, err := a.clientForWork(w)
	if err != nil {
		return before, false, err
	}
	remote, err := client.GetAccount(ctx, w.RemoteID)
	if err != nil {
		return before, false, err
	}
	before = remote.Priority
	current := remoteControls(remote)
	baseline := map[string]any{}
	applied := map[string]any{}
	pending := controlIntent{}
	var b, c, i []byte
	var pendingPriority *int
	var pendingSince *time.Time
	if err = a.db.QueryRow(ctx, `SELECT baseline_control,applied_control,pending_control,pending_priority,pending_since FROM quality_states WHERE account_id=$1`, w.ID).Scan(&b, &c, &i, &pendingPriority, &pendingSince); err != nil {
		return before, false, err
	}
	if err = json.Unmarshal(b, &baseline); err != nil {
		return before, false, err
	}
	if err = json.Unmarshal(c, &applied); err != nil {
		return before, false, err
	}
	if err = json.Unmarshal(i, &pending); err != nil {
		return before, false, err
	}
	if p.Policy.Mode != "priority" && len(applied) == 0 && len(pending.To) == 0 && pendingPriority == nil && s.LastApplied == nil && !s.OwnedPause {
		return before, false, nil
	}
	baseline["priority"] = float64(s.Baseline)
	if s.LastApplied != nil {
		applied["priority"] = float64(*s.LastApplied)
	}
	if pendingPriority != nil && len(pending.To) == 0 {
		from := baseline["priority"]
		if v, ok := applied["priority"]; ok {
			from = v
		}
		pending = controlIntent{From: map[string]any{"priority": from}, To: map[string]any{"priority": float64(*pendingPriority)}}
	}
	conflict := func(k string) (int, bool, error) {
		s.Conflict = true
		s.Status = "conflict"
		s.Reason = fmt.Sprintf("上游 %s 已被人工或其他工具修改，停止自动覆盖", k)
		return before, false, nil
	}
	if len(pending.To) > 0 && pending.ControlScope != "" && pending.ControlScope != controlScope(w) {
		return conflict("控制目标站点已变化；旧意图无法在新站点确认")
	}
	for k, to := range pending.To {
		if !controlEqual(current[k], to) && !controlEqual(current[k], pending.From[k]) {
			return conflict(k)
		}
	}
	for k, expected := range baseline {
		if v, ok := applied[k]; ok {
			expected = v
		}
		if _, ok := pending.To[k]; ok {
			continue
		}
		if !controlEqual(current[k], expected) {
			return conflict(k)
		}
	}
	if len(pending.To) > 0 {
		observed := 0
		for k, to := range pending.To {
			if controlEqual(current[k], to) {
				applied[k] = to
				observed++
			}
		}
		// Settle observed side effects and cancel only unapplied parts in one local
		// transaction before replacing an old intent. Never blindly replay it.
		checkpoint := *p
		checkpoint.Decision = quality.Decision{State: p.Old}
		checkpoint.AppliedControl = applied
		checkpoint.ControlsSettled = true
		checkpoint.Decision.State.Reason = fmt.Sprintf("旧待办已核对：确认 %d 项，撤销未执行目标 %d 项", observed, len(pending.To)-observed)
		recordOwnedControls(&checkpoint, applied)
		if err = a.persistEngineDecision(ctx, w, checkpoint, before, observed > 0, nil); err != nil {
			return before, false, err
		}
		p.Old = checkpoint.Decision.State
		recordOwnedControls(p, applied)
	}
	if p.Policy.Mode != "priority" || s.Status == "unknown" || s.PlanError != "" || statusText(remote.Status) != "active" {
		return before, false, nil
	}
	if err = a.requireCurrentEvidence(ctx, p); err != nil {
		return before, false, err
	}
	desired := map[string]any{"priority": float64(s.Desired)}
	for _, k := range []string{"load_factor", "concurrency"} {
		if !controlEnabled(p.Policy, k) {
			continue
		}
		base, owned := baseline[k]
		if !owned {
			base = current[k]
		}
		n, known := base.(float64)
		if !known || n < 1 {
			s.Reason += "；" + k + " 未提供有效基准，跳过"
			continue
		}
		baseline[k] = base
		desired[k] = math.Max(1, math.Floor(n*math.Pow(float64(p.Policy.CapacityPercent)/100, float64(s.Tier))))
	}
	if p.Policy.AutoPause {
		if p.Decision.HardFailure && remote.Schedulable {
			if safeToPause(*p, works, policies) {
				baseline["schedulable"] = true
				desired["schedulable"] = false
			} else {
				s.Reason += "；保留调度：分组缺少已验证健康备用"
			}
		} else if s.OwnedPause && s.Tier == 0 && s.Status == "healthy" {
			baseline["schedulable"] = true
			desired["schedulable"] = true
		}
	}
	intent := controlIntent{ControlScope: controlScope(w), SourceGeneration: w.SourceGeneration, PlanID: p.PlanID, ConfigGeneration: w.ConfigGeneration, From: map[string]any{}, To: map[string]any{}}
	for k, v := range desired {
		if !controlEqual(current[k], v) {
			intent.From[k] = current[k]
			intent.To[k] = v
		}
	}
	if len(intent.To) == 0 {
		return before, false, nil
	}
	if pending.SourceGeneration != w.SourceGeneration || !reflect.DeepEqual(pending.To, intent.To) {
		pendingSince = nil
	}
	raw, _ := json.Marshal(intent)
	baseRaw, _ := json.Marshal(baseline)
	var priority any
	if v, ok := intent.To["priority"]; ok {
		priority = int(v.(float64))
	}
	if _, err = a.db.Exec(ctx, `UPDATE quality_states SET pending_control=$2,pending_since=COALESCE($5,now()),baseline_control=$3,pending_priority=$4 WHERE account_id=$1`, w.ID, raw, baseRaw, priority, pendingSince); err != nil {
		return before, false, err
	}
	// Account reads and backup checks may take time. Recheck the whole dependent
	// component at the action boundary, not only at the beginning of the cycle.
	if err = validateEngineEvidence(works, engineComponents(works), w.ID, time.Now().UTC()); err != nil {
		return before, false, err
	}
	fields := map[string]any{}
	for k, v := range intent.To {
		if k != "schedulable" {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		if _, err = client.UpdateCapacity(ctx, w.RemoteID, fields); err != nil {
			return before, false, err
		}
	}
	if value, ok := intent.To["schedulable"]; ok {
		if err = a.requireCurrentEvidence(ctx, p); err != nil {
			return before, false, err
		}
		if !value.(bool) && (!p.Decision.HardFailure || !a.verifyBackups(ctx, *p, works, policies)) {
			return before, false, errEngineReplan
		}
		if err = a.requireCurrentEvidence(ctx, p); err != nil {
			return before, false, err
		}
		if _, err = client.SetSchedulable(ctx, w.RemoteID, value.(bool)); err != nil {
			return before, false, err
		}
	}
	updated, err := client.GetAccount(ctx, w.RemoteID)
	if err != nil {
		return before, false, err
	}
	readback := remoteControls(updated)
	for k, v := range intent.To {
		if !controlEqual(readback[k], v) {
			return before, false, fmt.Errorf("上游未保存目标 %s", k)
		}
		applied[k] = v
	}
	recordOwnedControls(p, applied)
	return before, true, nil
}

func recordOwnedControls(p *engineWork, applied map[string]any) {
	p.AppliedControl = applied
	if v, ok := applied["priority"].(float64); ok {
		n := int(v)
		p.Decision.State.LastApplied = &n
	}
	if v, ok := applied["schedulable"].(bool); ok {
		p.Decision.State.OwnedPause = !v
		p.Work.Schedulable = v
	}
}

// Read back the relevant candidates and reload their evidence, then expire all
// timestamps again after the last network request before authorizing a pause.
func (a *App) verifyBackups(ctx context.Context, target engineWork, works []engineWork, policies map[string]quality.GroupPolicy) bool {
	checked := append([]engineWork(nil), works...)
	relevant := map[string]bool{}
	for _, pool := range target.Pools {
		relevant[pool] = true
	}
	for i := range checked {
		p := &checked[i]
		shared := false
		for _, pool := range p.Pools {
			shared = shared || relevant[pool]
		}
		if p.Work.ID == target.Work.ID || !shared || !p.Decision.Eligible || p.Decision.State.Conflict || p.Decision.State.PlanError != "" {
			p.Decision.Eligible = false
			continue
		}
		client, err := a.clientForWork(p.Work)
		if err != nil {
			p.Decision.Eligible = false
			continue
		}
		remote, err := client.GetAccount(ctx, p.Work.RemoteID)
		if err != nil {
			p.Decision.Eligible = false
			continue
		}
		p.Work.Schedulable = remote.Schedulable
		p.Work.RemoteStatus = statusText(remote.Status)
		p.Work.NativeConstraints = remote.Native
		checkedAt := time.Now().UTC()
		p.Work.NativeCheckedAt = &checkedAt
		p.Native = assessWorkNative(p.Work, p.RemoteGroups, checkedAt)
		p.Old.PlanError = p.Decision.State.PlanError
		p.Snapshot, err = a.qualitySnapshot(ctx, p.Work, p.Policy)
		if err != nil {
			p.Decision.Eligible = false
			continue
		}
		p.Decision = quality.Evaluate(p.Policy, p.Old, p.Snapshot, time.Now().UTC())
	}
	now := time.Now().UTC()
	for i := range checked {
		p := &checked[i]
		p.Native = assessWorkNative(p.Work, p.RemoteGroups, now)
		if p.Decision.Eligible {
			p.Decision = quality.Evaluate(p.Policy, p.Old, p.Snapshot, now)
		}
	}
	return safeToPause(target, checked, policies)
}

func (a *App) restoreEngineControls(ctx context.Context, w AccountWork, s quality.State, client *upstream.Sub2Client, remote upstream.Sub2Account) (upstream.Sub2Account, error) {
	var b, c, i []byte
	var pendingPriority *int
	if err := a.db.QueryRow(ctx, `SELECT baseline_control,applied_control,pending_control,pending_priority FROM quality_states WHERE account_id=$1`, w.ID).Scan(&b, &c, &i, &pendingPriority); err != nil {
		return remote, err
	}
	baseline := map[string]any{}
	applied := map[string]any{}
	intent := controlIntent{}
	if err := json.Unmarshal(b, &baseline); err != nil {
		return remote, err
	}
	if err := json.Unmarshal(c, &applied); err != nil {
		return remote, err
	}
	if err := json.Unmarshal(i, &intent); err != nil {
		return remote, err
	}
	baseline["priority"] = float64(s.Baseline)
	if s.LastApplied != nil {
		applied["priority"] = float64(*s.LastApplied)
	}
	if pendingPriority != nil && len(intent.To) == 0 {
		intent.To = map[string]any{"priority": float64(*pendingPriority)}
	}
	current := remoteControls(remote)
	restore := controlIntent{From: map[string]any{}, To: map[string]any{}}
	for k, base := range baseline {
		if k == "schedulable" {
			continue
		} // Explicit separate scheduling action remains required.
		expected := base
		if v, ok := applied[k]; ok {
			expected = v
		}
		accepted := controlEqual(current[k], expected)
		if v, ok := intent.To[k]; ok {
			accepted = accepted || controlEqual(current[k], v) || controlEqual(current[k], intent.From[k])
		}
		if !accepted {
			return remote, &apiError{Status: 409, Code: "MANUAL_CHANGE", Message: k + " 已被外部修改，请选择保留当前值并停止接管"}
		}
		if !controlEqual(current[k], base) {
			restore.From[k] = current[k]
			restore.To[k] = base
		}
	}
	if len(restore.To) == 0 {
		return remote, nil
	}
	raw, _ := json.Marshal(restore)
	if _, err := a.db.Exec(ctx, `UPDATE quality_states SET pending_control=$2,pending_since=COALESCE(pending_since,now()),pending_priority=$3 WHERE account_id=$1`, w.ID, raw, s.Baseline); err != nil {
		return remote, err
	}
	if _, err := client.UpdateCapacity(ctx, w.RemoteID, restore.To); err != nil {
		return remote, err
	}
	updated, err := client.GetAccount(ctx, w.RemoteID)
	if err != nil {
		return remote, err
	}
	check := remoteControls(updated)
	for k, v := range restore.To {
		if !controlEqual(check[k], v) {
			return remote, fmt.Errorf("上游未恢复 %s", k)
		}
	}
	return updated, nil
}

func controlScope(w AccountWork) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%d", w.SiteBaseURL, w.RemoteID))))
}
