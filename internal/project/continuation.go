package project

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/core"
)

const continuationLease = 90 * time.Second
const continuationClaimLease = 30 * time.Second
const continuationDispatchTimeout = 45 * time.Second
const continuationSettlementTimeout = 5 * time.Minute

type ContinuationError struct {
	Code    string
	Message string
}

func (e *ContinuationError) Error() string         { return e.Code + ": " + e.Message }
func continuationError(code, message string) error { return &ContinuationError{code, message} }

type continuationAttempt struct {
	Value       protocol.WorkWakeAttempt `json:"value"`
	BindingID   string                   `json:"binding_id"`
	ConsumeHash string                   `json:"consume_hash"`
}
type continuationDocument struct {
	State       protocol.WorkContinuation         `json:"state"`
	Endpoint    protocol.WorkContinuationEndpoint `json:"endpoint"`
	BindingHash string                            `json:"binding_hash"`
	ViewConsent bool                              `json:"view_consent"`
	Wakes       []protocol.WorkWake               `json:"wakes"`
	Attempts    []continuationAttempt             `json:"attempts"`
}

// BEGIN IMMEDIATE 在读取状态前获得 SQLite 写锁；不同 Store 实例也不能同时越过 dispatch fence。
func (s *Store) continuationTransaction(ctx context.Context, fn func(*sql.Conn) error) error {
	if s == nil || s.db == nil {
		return errors.New("Project store 未初始化")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("开始 continuation 事务: %w", err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if err = fn(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}
func loadContinuation(ctx context.Context, conn *sql.Conn, owner, ws string) (continuationDocument, error) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(ws) == "" {
		return continuationDocument{}, continuationError("CONTINUATION_DENIED", "owner and work_session_id are required")
	}
	var actual string
	if err := conn.QueryRowContext(ctx, "SELECT owner_key FROM work_sessions WHERE id = ?", ws).Scan(&actual); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return continuationDocument{}, ErrWorkSessionNotFound
		}
		return continuationDocument{}, err
	}
	if actual != owner {
		return continuationDocument{}, continuationError("CONTINUATION_DENIED", "WorkSession belongs to another owner")
	}
	var raw []byte
	err := conn.QueryRowContext(ctx, "SELECT document_json FROM work_continuations WHERE work_session_id = ?", ws).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return continuationDocument{State: protocol.WorkContinuation{WorkSessionID: ws, Phase: "disabled", MaxRounds: 8, MaxFailures: 3, Sources: []protocol.ContinuationSource{}}}, nil
	}
	if err != nil {
		return continuationDocument{}, err
	}
	var d continuationDocument
	if err = json.Unmarshal(raw, &d); err != nil {
		return d, fmt.Errorf("读取 continuation 状态: %w", err)
	}
	return d, nil
}
func saveContinuation(ctx context.Context, conn *sql.Conn, d *continuationDocument, now time.Time) error {
	if err := compactContinuation(ctx, conn, d); err != nil {
		return err
	}
	d.State.UpdatedAt = formatTime(now)
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO work_continuations(work_session_id,document_json,updated_at) VALUES(?,?,?) ON CONFLICT(work_session_id) DO UPDATE SET document_json=excluded.document_json,updated_at=excluded.updated_at`, d.State.WorkSessionID, raw, formatTime(now))
	return err
}
func (s *Store) changeContinuation(ctx context.Context, owner, ws string, fn func(*sql.Conn, *continuationDocument, time.Time) (protocol.WorkContinuationResult, error)) (protocol.WorkContinuationResult, error) {
	var result protocol.WorkContinuationResult
	err := s.continuationTransaction(ctx, func(conn *sql.Conn) error {
		d, err := loadContinuation(ctx, conn, owner, ws)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		expireContinuation(&d, now)
		d.State.UpdatedAt = formatTime(now)
		result, err = fn(conn, &d, now)
		if err != nil {
			return err
		}
		return saveContinuation(ctx, conn, &d, now)
	})
	if err != nil {
		return protocol.WorkContinuationResult{}, err
	}
	return result, nil
}

// 轮询只读快照；需要过期转换时重新加写锁并重读，不能保存旧快照。
func (s *Store) readContinuation(ctx context.Context, owner, ws string, validate func(*continuationDocument, time.Time) error) (protocol.WorkContinuationResult, error) {
	if s == nil || s.db == nil {
		return protocol.WorkContinuationResult{}, errors.New("Project store 未初始化")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return protocol.WorkContinuationResult{}, err
	}
	d, err := loadContinuation(ctx, conn, owner, ws)
	conn.Close()
	if err != nil {
		return protocol.WorkContinuationResult{}, err
	}
	now := s.now().UTC()
	if validate != nil {
		if err := validate(&d, now); err != nil {
			return protocol.WorkContinuationResult{}, err
		}
	}
	if !expireContinuation(&d, now) {
		return continuationResult(&d), nil
	}
	return s.changeContinuation(ctx, owner, ws, func(_ *sql.Conn, current *continuationDocument, now time.Time) (protocol.WorkContinuationResult, error) {
		if validate != nil {
			if err := validate(current, now); err != nil {
				return protocol.WorkContinuationResult{}, err
			}
		}
		return continuationResult(current), nil
	})
}

func continuationSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func secretHash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func validSecret(value, hash string) bool {
	return value != "" && hash != "" && subtle.ConstantTimeCompare([]byte(secretHash(value)), []byte(hash)) == 1
}
func isExpired(raw string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339Nano, raw)
	return err != nil || !now.Before(t)
}
func wakeState(w *protocol.WorkWake, state string, now time.Time) {
	w.State = protocol.WorkWakeState(state)
	w.UpdatedAt = formatTime(now)
}
func attemptForWake(d *continuationDocument, wakeID string) *continuationAttempt {
	for i := len(d.Attempts) - 1; i >= 0; i-- {
		if d.Attempts[i].Value.WakeID == wakeID {
			return &d.Attempts[i]
		}
	}
	return nil
}
func findWake(d *continuationDocument, id string) *protocol.WorkWake {
	for i := range d.Wakes {
		if d.Wakes[i].WakeID == id {
			return &d.Wakes[i]
		}
	}
	return nil
}
func blockedWake(d *continuationDocument) bool {
	for i := range d.Wakes {
		switch string(d.Wakes[i].State) {
		case "prepared", "dispatch_accepted", "delivery_unknown", "delivery_rejected", "consumed", "needs_attention":
			return true
		}
	}
	return false
}
func failContinuation(d *continuationDocument, reason string) {
	d.State.FailureCount++
	d.State.LastError = reason
	if d.State.FailureCount >= d.State.MaxFailures {
		d.State.Enabled = false
		d.State.Phase = "circuit_open"
	}
}
func expireContinuation(d *continuationDocument, now time.Time) bool {
	changed := false
	for i := range d.Wakes {
		w := &d.Wakes[i]
		a := attemptForWake(d, w.WakeID)
		if a == nil {
			continue
		}
		switch string(w.State) {
		case "claimed":
			if isExpired(a.Value.LeaseExpiresAt, now) {
				changed = true
				wakeState(w, "pending", now)
				a.Value.State = protocol.WorkWakeState("delivery_rejected")
				a.Value.LastError = "claim expired before dispatch"
			}
		case "prepared", "dispatch_accepted":
			if isExpired(a.Value.LeaseExpiresAt, now) {
				changed = true
				wakeState(w, "delivery_unknown", now)
				a.Value.State = w.State
				failContinuation(d, "dispatch outcome requires inspection")
				if d.State.Enabled {
					d.State.Phase = "needs_attention"
				}
			}
		case "consumed":
			t, err := time.Parse(time.RFC3339Nano, a.Value.ConsumedAt)
			if err != nil || !now.Before(t.Add(continuationSettlementTimeout)) {
				changed = true
				wakeState(w, "needs_attention", now)
				a.Value.State = w.State
				d.State.Phase = "needs_attention"
				d.State.LastError = "consumed work has not been settled"
			}
		}
	}
	return changed
}

func continuationResult(d *continuationDocument) protocol.WorkContinuationResult {
	selected := ""
	for _, w := range d.Wakes {
		if string(w.State) == "settled" {
			continue
		}
		if selected == "" {
			selected = w.WakeID
		}
		if string(w.State) != "pending" {
			selected = w.WakeID
			break
		}
	}
	return continuationWakeResult(d, selected)
}
func continuationWakeResult(d *continuationDocument, wakeID string) protocol.WorkContinuationResult {
	r := protocol.WorkContinuationResult{WorkSessionID: d.State.WorkSessionID, EndpointID: d.Endpoint.EndpointID, ControllerGeneration: d.Endpoint.ControllerGeneration, BindingID: d.Endpoint.BindingID, LeaseExpiresAt: d.Endpoint.LeaseExpiresAt, State: d.State}
	if w := findWake(d, wakeID); w != nil {
		copyWake := *w
		r.Wake = &copyWake
		r.WakeID = w.WakeID
		if a := attemptForWake(d, w.WakeID); a != nil {
			copyAttempt := a.Value
			r.Attempt = &copyAttempt
			r.AttemptID = a.Value.AttemptID
		}
	}
	return r
}
func validateContinuationTarget(ctx context.Context, conn *sql.Conn, ws, target string, current bool) (protocol.ExecutionContext, string, error) {
	var e protocol.ExecutionContext
	var node, targetState, sessionState, desired, applied, applyState string
	var projectEnabled, deploymentEnabled, nodeEnabled, fullAccess int
	var permissionsJSON string
	if !current {
		err := conn.QueryRowContext(ctx, `SELECT work_session_id,id,project_id,deployment_id,deployment_revision,context_revision,node_id FROM work_targets WHERE work_session_id=? AND id=?`, ws, target).Scan(&e.WorkSessionID, &e.TargetID, &e.ProjectID, &e.DeploymentID, &e.DeploymentRevision, &e.ContextRevision, &node)
		return e, node, err
	}
	err := conn.QueryRowContext(ctx, `SELECT t.work_session_id,t.id,t.project_id,t.deployment_id,t.deployment_revision,t.context_revision,t.node_id,t.status,s.status,p.enabled,d.desired_revision,d.applied_revision,d.apply_status,d.enabled,n.enabled,n.full_access,t.permissions_json FROM work_targets t JOIN work_sessions s ON s.id=t.work_session_id LEFT JOIN projects p ON p.id=t.project_id LEFT JOIN project_deployments d ON d.id=t.deployment_id LEFT JOIN agentdock_devices n ON n.id=t.node_id WHERE t.work_session_id=? AND t.id=?`, ws, target).Scan(&e.WorkSessionID, &e.TargetID, &e.ProjectID, &e.DeploymentID, &e.DeploymentRevision, &e.ContextRevision, &node, &targetState, &sessionState, &projectEnabled, &desired, &applied, &applyState, &deploymentEnabled, &nodeEnabled, &fullAccess, &permissionsJSON)
	if err != nil {
		return e, node, continuationError("CONTINUATION_TARGET_DENIED", "Target identity or deployment is unavailable")
	}
	var permissions protocol.DeploymentPermissions
	if err := json.Unmarshal([]byte(permissionsJSON), &permissions); err != nil {
		return e, node, err
	}
	if current && (permissions.FullAccess != (fullAccess == 1) || targetState != "ready" && targetState != "running" && targetState != "idle" || sessionState == "cancelled" || sessionState == "completed" || sessionState == "failed" || projectEnabled != 1 || deploymentEnabled != 1 || nodeEnabled != 1 || applyState != "applied" || desired != applied || e.DeploymentRevision != "rev-"+applied) {
		return e, node, continuationError("CONTINUATION_TARGET_DENIED", "Target configuration is no longer current")
	}
	return e, node, nil
}
func validateWakeTargets(ctx context.Context, conn *sql.Conn, w *protocol.WorkWake) error {
	for _, o := range w.Sources {
		current, _, err := validateContinuationTarget(ctx, conn, w.WorkSessionID, o.ExecutionContext.TargetID, true)
		if err != nil {
			return err
		}
		if current.DeploymentRevision != o.ExecutionContext.DeploymentRevision || current.ContextRevision != o.ExecutionContext.ContextRevision {
			return continuationError("CONTINUATION_TARGET_DENIED", "source context is no longer the current Target context; inspect and explicitly recover")
		}

	}
	return nil
}

func (s *Store) ControlContinuation(ctx context.Context, owner string, in protocol.WorkContinuationInput) (protocol.WorkContinuationResult, error) {
	if in.Action == "status" {
		return s.readContinuation(ctx, owner, in.WorkSessionID, nil)
	}
	return s.changeContinuation(ctx, owner, in.WorkSessionID, func(conn *sql.Conn, d *continuationDocument, now time.Time) (protocol.WorkContinuationResult, error) {
		switch in.Action {
		case "status":
		case "enable":
			if !in.Confirmed {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_CONFIRMATION_REQUIRED", "enable requires explicit confirmation")
			}
			if d.State.FailureCount >= d.State.MaxFailures || d.State.RoundsUsed >= d.State.MaxRounds {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_RECOVERY_REQUIRED", "explicit recovery is required")
			}
			if in.MaxRounds != 0 {
				if in.MaxRounds < 1 || in.MaxRounds > 100 {
					return protocol.WorkContinuationResult{}, invalid("max_rounds must be 1..100")
				}
				d.State.MaxRounds = in.MaxRounds
			}
			if in.MaxFailures != 0 {
				if in.MaxFailures < 1 || in.MaxFailures > 10 {
					return protocol.WorkContinuationResult{}, invalid("max_failures must be 1..10")
				}
				d.State.MaxFailures = in.MaxFailures
			}
			d.State.Enabled = true
			d.State.Phase = "ready"
		case "pause":
			d.State.Enabled = false
			d.State.Phase = "paused"
		case "recover":
			if !in.Confirmed {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_CONFIRMATION_REQUIRED", "recovery requires explicit confirmation")
			}
			// 未知投递不能转回 pending；显式恢复只能在未结算来源已核对后恢复预算。
			if in.WakeID != "" {
				w := findWake(d, in.WakeID)
				if w == nil || strings.TrimSpace(in.Checkpoint) == "" {
					return protocol.WorkContinuationResult{}, invalid("manual wake recovery requires existing wake_id and checkpoint")
				}
				if string(w.State) != "settled" {
					wakeState(w, "settled", now)
					if a := attemptForWake(d, w.WakeID); a != nil {
						a.Value.State = w.State
						a.Value.LastError = "manually resolved after explicit confirmation: " + in.Checkpoint
						a.ConsumeHash = ""
					}
					d.State.Checkpoint = in.Checkpoint
				}
			}
			if blockedWake(d) {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_NEEDS_ATTENTION", "resolve every outstanding wake before recovery")
			}
			d.State.RoundsUsed = 0
			d.State.FailureCount = 0
			d.State.LastError = ""
			d.State.Enabled = true
			d.State.Phase = "ready"
		case "await":
			if !d.State.Enabled {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_PAUSED", "continuation is not enabled")
			}
			if len(in.Sources) == 0 || len(in.Sources) > protocol.MaxWorkContinuationSources {
				return protocol.WorkContinuationResult{}, invalid("await requires 1..32 sources")
			}
			seen := map[string]bool{}
			for _, source := range in.Sources {
				key := source.TargetID + "\x00" + source.CommandSessionID
				if source.CommandSessionID == "" || seen[key] {
					return protocol.WorkContinuationResult{}, invalid("await sources must have distinct target and command session IDs")
				}
				seen[key] = true
				if _, _, err := validateContinuationTarget(ctx, conn, in.WorkSessionID, source.TargetID, true); err != nil {
					return protocol.WorkContinuationResult{}, err
				}
			}
			d.State.Sources = append([]protocol.ContinuationSource(nil), in.Sources...)
			d.State.Phase = "waiting"
			if err := mergeStoredContinuationOutcomes(ctx, conn, d, now); err != nil {
				return protocol.WorkContinuationResult{}, err
			}
		case "settle":
			w := findWake(d, in.WakeID)
			if w == nil {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_WAKE_NOT_FOUND", "wake not found")
			}
			if string(w.State) == "settled" {
				return continuationResult(d), nil
			}
			a := attemptForWake(d, w.WakeID)
			if a == nil || a.Value.ConsumedAt == "" {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_NOT_CONSUMED", "consume must precede settlement")
			}
			if strings.TrimSpace(in.Checkpoint) == "" {
				return protocol.WorkContinuationResult{}, invalid("settlement requires checkpoint")
			}
			wakeState(w, "settled", now)
			a.Value.State = w.State
			d.State.Checkpoint = in.Checkpoint
			d.State.FailureCount = 0
			d.State.LastError = ""
			if d.State.Enabled {
				d.State.Phase = "ready"
			}
		default:
			return protocol.WorkContinuationResult{}, invalid("unknown continuation action")
		}
		return continuationResult(d), nil
	})
}

func (s *Store) PresentContinuation(ctx context.Context, owner, ws string, recover bool) (protocol.WorkContinuationResult, error) {
	return s.changeContinuation(ctx, owner, ws, func(conn *sql.Conn, d *continuationDocument, now time.Time) (protocol.WorkContinuationResult, error) {
		if d.Endpoint.EndpointID != "" && !recover {
			return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_RECOVERY_REQUIRED", "an endpoint already exists; explicitly recover presentation")
		}
		if blockedWake(d) {
			return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_NEEDS_ATTENTION", "cannot replace endpoint while dispatch or consumed work is unresolved")
		}
		id, err := core.NewID("ep")
		if err != nil {
			return protocol.WorkContinuationResult{}, err
		}
		secret, err := continuationSecret()
		if err != nil {
			return protocol.WorkContinuationResult{}, err
		}
		generation := d.Endpoint.ControllerGeneration + 1
		d.Endpoint = protocol.WorkContinuationEndpoint{WorkSessionID: ws, EndpointID: id, ControllerGeneration: generation}
		d.BindingHash = secretHash(secret)
		d.ViewConsent = false
		for i := range d.Wakes {
			w := &d.Wakes[i]
			if string(w.State) == "claimed" {
				wakeState(w, "pending", now)
			}
			if string(w.State) == "pending" {
				w.EndpointID = id
				w.ControllerGeneration = generation
			}
		}
		r := continuationResult(d)
		r.BindingSecret = secret
		return r, nil
	})
}

func controllerIdentity(d *continuationDocument, in protocol.ContinuationControllerInput, now time.Time, lease bool) error {
	if in.EndpointID == "" || in.EndpointID != d.Endpoint.EndpointID || in.ControllerGeneration != d.Endpoint.ControllerGeneration || !validSecret(in.BindingSecret, d.BindingHash) {
		return continuationError("CONTINUATION_BINDING_DENIED", "endpoint, generation or presentation secret does not match")
	}
	if in.BindingID == "" || len(in.BindingID) > 160 {
		return continuationError("CONTINUATION_BINDING_DENIED", "binding_id is required")
	}
	if lease && (in.BindingID != d.Endpoint.BindingID || isExpired(d.Endpoint.LeaseExpiresAt, now)) {
		return continuationError("CONTINUATION_LEASE_EXPIRED", "view does not hold the current lease")
	}
	return nil
}
func (s *Store) ControllerContinuation(ctx context.Context, owner, action string, in protocol.ContinuationControllerInput) (protocol.WorkContinuationResult, error) {
	if action == "state" {
		return s.readContinuation(ctx, owner, in.WorkSessionID, func(d *continuationDocument, now time.Time) error { return controllerIdentity(d, in, now, true) })
	}
	return s.changeContinuation(ctx, owner, in.WorkSessionID, func(conn *sql.Conn, d *continuationDocument, now time.Time) (protocol.WorkContinuationResult, error) {
		if err := controllerIdentity(d, in, now, action != "bind"); err != nil {
			return protocol.WorkContinuationResult{}, err
		}
		switch action {
		case "bind":
			if d.Endpoint.BindingID != "" && d.Endpoint.BindingID != in.BindingID && !isExpired(d.Endpoint.LeaseExpiresAt, now) {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_VIEW_BUSY", "another view holds the lease")
			}
			if d.Endpoint.BindingID != in.BindingID {
				d.ViewConsent = false
			}
			d.Endpoint.BindingID = in.BindingID
			d.Endpoint.LeaseExpiresAt = formatTime(now.Add(continuationLease))
			d.ViewConsent = d.ViewConsent || in.UserEnabled
		case "state":
		case "heartbeat":
			d.Endpoint.LeaseExpiresAt = formatTime(now.Add(continuationLease))
		case "pause":
			d.State.Enabled = false
			d.State.Phase = "paused"
			d.ViewConsent = false
		case "acquire":
			if !d.State.Enabled || !d.ViewConsent {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_PAUSED", "enabled continuation and active view consent are required")
			}
			if d.State.RoundsUsed >= d.State.MaxRounds || d.State.FailureCount >= d.State.MaxFailures {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_BUDGET_EXHAUSTED", "automatic continuation budget is exhausted")
			}
			if in.WakeID != "" {
				requested := findWake(d, in.WakeID)
				if requested == nil {
					return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_WAKE_NOT_FOUND", "requested wake not found")
				}
				if string(requested.State) != "pending" && string(requested.State) != "claimed" {
					return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_DISPATCH_FENCED", "requested wake is not available for acquisition")
				}
			}
			if blockedWake(d) {
				if in.WakeID != "" {
					return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_NEEDS_ATTENTION", "another dispatch remains unresolved")
				}
				return continuationResult(d), nil
			}
			for _, existing := range d.Wakes {
				if string(existing.State) == "claimed" {
					a := attemptForWake(d, existing.WakeID)
					if a != nil && a.BindingID == in.BindingID && (in.WakeID == "" || in.WakeID == existing.WakeID) {
						return continuationWakeResult(d, existing.WakeID), nil
					}
					return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_VIEW_BUSY", "another wake already owns the active claim")
				}
			}
			if in.WakeID != "" && findWake(d, in.WakeID) == nil {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_WAKE_NOT_FOUND", "requested wake not found")
			}
			for i := range d.Wakes {
				w := &d.Wakes[i]
				if in.WakeID != "" && w.WakeID != in.WakeID {
					continue
				}
				if string(w.State) == "claimed" {
					a := attemptForWake(d, w.WakeID)
					if a != nil && a.BindingID == in.BindingID {
						return continuationResult(d), nil
					}
					return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_VIEW_BUSY", "wake is claimed")
				}
				if string(w.State) != "pending" {
					continue
				}
				if err := validateWakeTargets(ctx, conn, w); err != nil {
					return protocol.WorkContinuationResult{}, err
				}
				id, err := core.NewID("attempt")
				if err != nil {
					return protocol.WorkContinuationResult{}, err
				}
				wakeState(w, "claimed", now)
				d.Attempts = append(d.Attempts, continuationAttempt{Value: protocol.WorkWakeAttempt{AttemptID: id, WakeID: w.WakeID, State: w.State, LeaseExpiresAt: formatTime(now.Add(continuationClaimLease))}, BindingID: in.BindingID})
				return continuationWakeResult(d, w.WakeID), nil
			}
		case "prepare":
			w := findWake(d, in.WakeID)
			if w == nil {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_WAKE_NOT_FOUND", "wake not found")
			}
			a := attemptForWake(d, w.WakeID)
			if a == nil || a.Value.AttemptID != in.AttemptID || a.BindingID != in.BindingID {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_ATTEMPT_DENIED", "attempt does not belong to this view")
			}
			if string(w.State) != "claimed" || a.Value.PreparedAt != "" {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_DISPATCH_FENCED", "dispatch fence already crossed or claim expired; no message can be returned again")
			}
			if blockedWake(d) {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_NEEDS_ATTENTION", "another dispatch remains unresolved")
			}
			if !d.State.Enabled || !d.ViewConsent || d.State.RoundsUsed >= d.State.MaxRounds || d.State.FailureCount >= d.State.MaxFailures {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_PAUSED", "continuation cannot dispatch")
			}
			if err := validateWakeTargets(ctx, conn, w); err != nil {
				return protocol.WorkContinuationResult{}, err
			}
			token, err := continuationSecret()
			if err != nil {
				return protocol.WorkContinuationResult{}, err
			}
			wakeState(w, "prepared", now)
			a.Value.State = w.State
			a.Value.PreparedAt = formatTime(now)
			a.Value.LeaseExpiresAt = formatTime(now.Add(continuationDispatchTimeout))
			a.ConsumeHash = secretHash(token)
			d.Endpoint.LeaseExpiresAt = formatTime(now.Add(continuationLease))
			d.State.RoundsUsed++
			d.State.Phase = "dispatching"
			envelope := protocol.ResumeEnvelope{ProtocolVersion: 1, WorkSessionID: in.WorkSessionID, EndpointID: in.EndpointID, ControllerGeneration: in.ControllerGeneration, WakeID: w.WakeID, AttemptID: a.Value.AttemptID, ConsumeToken: token}
			message, err := envelope.AutomaticMessage()
			if err != nil {
				return protocol.WorkContinuationResult{}, err
			}
			r := continuationWakeResult(d, w.WakeID)
			r.AutomaticMessage = message
			return r, nil
		case "finish":
			if in.DeliveryStatus != "dispatch_accepted" && in.DeliveryStatus != "delivery_rejected" && in.DeliveryStatus != "delivery_unknown" {
				return protocol.WorkContinuationResult{}, invalid("invalid delivery_status")
			}
			w := findWake(d, in.WakeID)
			if w == nil {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_WAKE_NOT_FOUND", "wake not found")
			}
			a := attemptForWake(d, w.WakeID)
			if a == nil || a.Value.AttemptID != in.AttemptID || a.BindingID != in.BindingID || a.Value.PreparedAt == "" {
				return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_ATTEMPT_DENIED", "prepared attempt does not match")
			}
			// consume 可先于 Host ACK；迟到 finish 只补记回执，绝不能降级 consumed/settled。
			if a.Value.FinishedAt != "" {
				return continuationWakeResult(d, w.WakeID), nil
			}
			a.Value.FinishedAt = formatTime(now)
			if a.Value.ConsumedAt == "" {
				if string(w.State) == "prepared" || string(w.State) == "dispatch_accepted" || (string(w.State) == "delivery_unknown" && in.DeliveryStatus == "delivery_rejected") {
					wakeState(w, in.DeliveryStatus, now)
					a.Value.State = w.State
					if in.DeliveryStatus != "dispatch_accepted" && d.State.Phase != "needs_attention" && d.State.Phase != "circuit_open" {
						failContinuation(d, in.DeliveryStatus)
						if d.State.Enabled {
							d.State.Phase = "needs_attention"
						}
					}
				}
			}
			return continuationWakeResult(d, w.WakeID), nil
		default:
			return protocol.WorkContinuationResult{}, invalid("unknown controller action")
		}
		return continuationResult(d), nil
	})
}

func (s *Store) ConsumeWorkWake(ctx context.Context, owner string, in protocol.ResumeEnvelope) (protocol.WorkContinuationResult, error) {
	return s.changeContinuation(ctx, owner, in.WorkSessionID, func(conn *sql.Conn, d *continuationDocument, now time.Time) (protocol.WorkContinuationResult, error) {
		if in.ProtocolVersion != 1 || in.EndpointID != d.Endpoint.EndpointID || in.ControllerGeneration != d.Endpoint.ControllerGeneration {
			return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_BINDING_DENIED", "consume envelope endpoint or generation does not match")
		}
		w := findWake(d, in.WakeID)
		if w == nil || w.EndpointID != in.EndpointID || w.ControllerGeneration != in.ControllerGeneration {
			return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_WAKE_NOT_FOUND", "wake does not match envelope")
		}
		a := attemptForWake(d, w.WakeID)
		if a == nil || a.Value.AttemptID != in.AttemptID || a.Value.PreparedAt == "" || !validSecret(in.ConsumeToken, a.ConsumeHash) {
			return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_CONSUME_DENIED", "consume credential does not match prepared attempt")
		}
		if err := validateWakeTargets(ctx, conn, w); err != nil {
			return protocol.WorkContinuationResult{}, err
		}
		if string(w.State) != "prepared" && string(w.State) != "dispatch_accepted" && string(w.State) != "delivery_unknown" {
			return protocol.WorkContinuationResult{}, continuationError("CONTINUATION_CONSUME_DENIED", "wake is not eligible for first consumption")
		}
		if a.Value.ConsumedAt == "" {
			a.ConsumeHash = ""
			wakeState(w, "consumed", now)
			a.Value.State = w.State
			a.Value.ConsumedAt = formatTime(now)
			d.State.Phase = "processing"
		}
		r := continuationResult(d)
		copyWake := *w
		copyAttempt := a.Value
		r.Wake = &copyWake
		r.Attempt = &copyAttempt
		r.WakeID = w.WakeID
		r.AttemptID = a.Value.AttemptID
		r.Outcomes = append([]protocol.CommandOutcome(nil), w.Sources...)
		return r, nil
	})
}

func mergeStoredContinuationOutcomes(ctx context.Context, conn *sql.Conn, d *continuationDocument, now time.Time) error {
	for _, source := range d.State.Sources {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM command_source_receipts WHERE work_session_id=? AND target_id=? AND command_session_id=? AND eligible=2`, d.State.WorkSessionID, source.TargetID, source.CommandSessionID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return continuationError("CONTINUATION_SOURCE_EXPIRED", "unawaited result exceeded the Nexus retention window; inspect the node's durable result explicitly")
		}
	}
	rows, err := conn.QueryContext(ctx, "SELECT outcome_json FROM command_source_receipts WHERE work_session_id = ? AND eligible = 1 ORDER BY received_at,event_id", d.State.WorkSessionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		var outcome protocol.CommandOutcome
		if err = json.Unmarshal(raw, &outcome); err != nil {
			return err
		}
		if err = mergeContinuationOutcome(d, outcome, now); err != nil {
			return err
		}
	}
	return rows.Err()
}
func mergeContinuationOutcome(d *continuationDocument, outcome protocol.CommandOutcome, now time.Time) error {
	wanted := false
	for _, source := range d.State.Sources {
		if source.TargetID == outcome.ExecutionContext.TargetID && source.CommandSessionID == outcome.CommandSessionID {
			wanted = true
			break
		}
	}
	if !wanted {
		return nil
	}
	for _, w := range d.Wakes {
		for _, existing := range w.Sources {
			if existing.ExecutionContext.TargetID == outcome.ExecutionContext.TargetID && existing.CommandSessionID == outcome.CommandSessionID {
				return nil
			}
		}
	}
	for i := range d.Wakes {
		w := &d.Wakes[i]
		if string(w.State) == "pending" || string(w.State) == "claimed" {
			if len(w.Sources) >= protocol.MaxWorkContinuationSources {
				continue
			}
			candidate := append(append([]protocol.CommandOutcome(nil), w.Sources...), outcome)
			encoded, err := json.Marshal(candidate)
			if err != nil {
				return err
			}
			if len(encoded) > protocol.MaxCommandOutcomeBatchBytes {
				continue
			}
			w.Sources = candidate
			w.UpdatedAt = formatTime(now)
			return nil
		}
	}
	id, err := core.NewID("wake")
	if err != nil {
		return err
	}
	d.Wakes = append(d.Wakes, protocol.WorkWake{WorkSessionID: d.State.WorkSessionID, EndpointID: d.Endpoint.EndpointID, ControllerGeneration: d.Endpoint.ControllerGeneration, WakeID: id, State: protocol.WorkWakeState("pending"), Sources: []protocol.CommandOutcome{outcome}, CreatedAt: formatTime(now), UpdatedAt: formatTime(now)})
	return nil
}

// 返回的 event IDs 只有在收据和对应 Wake 同一事务提交后才可向已认证节点 ACK。
func (s *Store) RecordCommandOutcomes(ctx context.Context, nodeID string, outcomes []protocol.CommandOutcome) ([]string, error) {
	if nodeID == "" || len(outcomes) > protocol.MaxCommandOutcomesPerRead {
		return nil, invalid("authenticated node and bounded outcome batch required")
	}
	batch, err := json.Marshal(outcomes)
	if err != nil {
		return nil, err
	}
	if len(batch) > protocol.MaxCommandOutcomeBatchBytes {
		return nil, invalid("outcome batch exceeds size limit")
	}
	acks := make([]string, 0, len(outcomes))
	err = s.continuationTransaction(ctx, func(conn *sql.Conn) error {
		for _, o := range outcomes {
			switch string(o.State) {
			case "completed", "failed", "interrupted", "outcome_unknown":
			default:
				continue
			}
			if len(o.Output)+len(o.Stdout)+len(o.Stderr) > protocol.MaxCommandOutcomeOutputBytes {
				return invalid("outcome output exceeds size limit")
			}
			if o.EventID == "" || o.CommandSessionID == "" || o.ExecutionContext.DeploymentRevision == "" || o.ExecutionContext.ContextRevision == "" {
				return invalid("terminal outcome identity is incomplete")
			}
			e, node, err := validateContinuationTarget(ctx, conn, o.ExecutionContext.WorkSessionID, o.ExecutionContext.TargetID, false)
			eligible := 1
			if errors.Is(err, sql.ErrNoRows) {
				eligible = 0
				e = o.ExecutionContext
			} else if err != nil {
				return err
			}
			if eligible == 1 && (node != nodeID || e.ProjectID != o.ExecutionContext.ProjectID || e.DeploymentID != o.ExecutionContext.DeploymentID) {
				return continuationError("CONTINUATION_SOURCE_DENIED", "outcome does not belong to authenticated node and Target")
			}
			// 已删除的会话/Target 作为隔离收据确认，不能毒化整个节点补报队列。

			// PendingReport 是节点传输状态，不属于不可变执行事实。
			o.PendingReport = false
			raw, err := json.Marshal(o)
			if err != nil {
				return err
			}
			hash := secretHash(string(raw))
			var previous string
			err = conn.QueryRowContext(ctx, "SELECT outcome_hash FROM command_source_receipts WHERE node_id=? AND event_id=?", nodeID, o.EventID).Scan(&previous)
			if err == nil {
				if previous != hash {
					return continuationError("CONTINUATION_RECEIPT_CONFLICT", "event ID was reused for different execution facts")
				}
				acks = append(acks, o.EventID)
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			now := s.now().UTC()
			_, err = conn.ExecContext(ctx, `INSERT INTO command_source_receipts(node_id,event_id,work_session_id,target_id,command_session_id,outcome_hash,outcome_json,received_at,eligible) VALUES(?,?,?,?,?,?,?,?,?)`, nodeID, o.EventID, e.WorkSessionID, e.TargetID, o.CommandSessionID, hash, raw, formatTime(now), eligible)
			if err != nil {
				return err
			}
			if eligible == 0 {
				if _, err = conn.ExecContext(ctx, "UPDATE command_source_receipts SET outcome_json='{}' WHERE node_id=? AND event_id=?", nodeID, o.EventID); err != nil {
					return err
				}
				acks = append(acks, o.EventID)
				continue
			}
			var owner string
			if err = conn.QueryRowContext(ctx, "SELECT owner_key FROM work_sessions WHERE id=?", e.WorkSessionID).Scan(&owner); err != nil {
				return err
			}
			d, err := loadContinuation(ctx, conn, owner, e.WorkSessionID)
			if err != nil {
				return err
			}
			expireContinuation(&d, now)
			if err = mergeContinuationOutcome(&d, o, now); err != nil {
				return err
			}
			if err = saveContinuation(ctx, conn, &d, now); err != nil {
				return err
			}
			acks = append(acks, o.EventID)
		}
		return expireUnawaitedReceipts(ctx, conn, nodeID, s.now().UTC())
	})
	if err != nil {
		return nil, err
	}
	return acks, nil
}

// 已结算来源只保留紧凑去重凭据，避免 ACK 丢失/旧事件重放重新生成工作；未结算结果永不清理。
func compactContinuation(ctx context.Context, conn *sql.Conn, d *continuationDocument) error {
	settled := 0
	for _, w := range d.Wakes {
		if string(w.State) == "settled" {
			settled++
			for _, o := range w.Sources {
				if _, err := conn.ExecContext(ctx, `UPDATE command_source_receipts SET outcome_json='{}',eligible=0 WHERE work_session_id=? AND target_id=? AND command_session_id=? AND eligible=1`, w.WorkSessionID, o.ExecutionContext.TargetID, o.CommandSessionID); err != nil {
					return err
				}
			}
		}
	}
	keep := make(map[string]bool)
	wakes := make([]protocol.WorkWake, 0, len(d.Wakes))
	for _, w := range d.Wakes {
		if string(w.State) == "settled" && settled > 32 {
			settled--
			continue
		}
		wakes = append(wakes, w)
		keep[w.WakeID] = true
	}
	d.Wakes = wakes
	latest := make(map[string]int)
	for i, a := range d.Attempts {
		latest[a.Value.WakeID] = i
	}
	attempts := make([]continuationAttempt, 0, len(d.Attempts))
	for i, a := range d.Attempts {
		if keep[a.Value.WakeID] && latest[a.Value.WakeID] == i {
			attempts = append(attempts, a)
		}
	}
	d.Attempts = attempts
	return nil
}

// 普通命令未注册 await 时，全文最多保留 30 天/每节点 1024 条；已交接或未结算来源不参与淘汰。
// eligible=2 留下过期墓碑，使迟到 await 明确报错而不是静默等待已删除的结果。
func expireUnawaitedReceipts(ctx context.Context, conn *sql.Conn, nodeID string, now time.Time) error {
	_, err := conn.ExecContext(ctx, `UPDATE command_source_receipts SET outcome_json='{}',eligible=2 WHERE rowid IN (
 SELECT r.rowid FROM command_source_receipts r WHERE r.node_id=? AND r.eligible=1
 AND (r.received_at<? OR r.rowid NOT IN (SELECT rowid FROM command_source_receipts WHERE node_id=? AND eligible=1 ORDER BY received_at DESC,rowid DESC LIMIT 1024))
 AND NOT EXISTS(SELECT 1 FROM work_continuations c,json_each(c.document_json,'$.state.sources') s WHERE c.work_session_id=r.work_session_id AND json_extract(s.value,'$.target_id')=r.target_id AND json_extract(s.value,'$.command_session_id')=r.command_session_id)
 AND NOT EXISTS(SELECT 1 FROM work_continuations c,json_each(c.document_json,'$.wakes') w,json_each(w.value,'$.sources') o WHERE c.work_session_id=r.work_session_id AND json_extract(w.value,'$.state')!='settled' AND json_extract(o.value,'$.event_id')=r.event_id AND json_extract(o.value,'$.execution_context.target_id')=r.target_id)
 )`, nodeID, formatTime(now.Add(-30*24*time.Hour)), nodeID)
	return err
}
