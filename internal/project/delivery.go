package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
)

var ErrContextDeliveryNotFound = errors.New("Project Context delivery 不存在")

type ContextDeliveryRevisionConflictError struct {
	ContextRevision string
}

func (e *ContextDeliveryRevisionConflictError) Error() string {
	return "Project Context delivery revision 已变化"
}

type ContextDelivery struct {
	WorkSessionID   string                                `json:"work_session_id"`
	TargetID        string                                `json:"target_id,omitempty"`
	ContextRevision string                                `json:"context_revision"`
	Status          protocol.ProjectContextDeliveryStatus `json:"status"`
	ReturnedAt      time.Time                             `json:"returned_at"`
	HostConsumedAt  *time.Time                            `json:"host_consumed_at,omitempty"`
	UpdatedAt       time.Time                             `json:"updated_at"`
}

func (s *Store) RecordContextReturned(ctx context.Context, ownerKey, workSessionID, targetID, contextRevision string) (ContextDelivery, error) {
	ownerKey = strings.TrimSpace(ownerKey)
	workSessionID = strings.TrimSpace(workSessionID)
	targetID = strings.TrimSpace(targetID)
	contextRevision = strings.TrimSpace(contextRevision)
	if ownerKey == "" || workSessionID == "" || contextRevision == "" {
		return ContextDelivery{}, invalid("Context delivery owner/work_session_id/context_revision 不能为空")
	}
	if err := s.validateContextDeliveryOwner(ctx, ownerKey, workSessionID, targetID); err != nil {
		return ContextDelivery{}, err
	}
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_context_deliveries(
		work_session_id, target_id, context_revision, status, returned_at, host_consumed_at, updated_at
	) VALUES(?, ?, ?, ?, ?, '', ?)
	ON CONFLICT(work_session_id, target_id) DO UPDATE SET
		context_revision = excluded.context_revision,
		status = CASE
			WHEN project_context_deliveries.context_revision = excluded.context_revision AND project_context_deliveries.status = 'host_consumed'
			THEN 'host_consumed' ELSE 'returned' END,
		returned_at = excluded.returned_at,
		host_consumed_at = CASE
			WHEN project_context_deliveries.context_revision = excluded.context_revision AND project_context_deliveries.status = 'host_consumed'
			THEN project_context_deliveries.host_consumed_at ELSE '' END,
		updated_at = excluded.updated_at`,
		workSessionID, targetID, contextRevision, string(protocol.ProjectContextReturned), formatTime(now), formatTime(now))
	if err != nil {
		return ContextDelivery{}, fmt.Errorf("记录 Project Context returned: %w", err)
	}
	return s.GetContextDelivery(ctx, ownerKey, workSessionID, targetID)
}

func (s *Store) AcknowledgeContextConsumed(ctx context.Context, ownerKey string, ack protocol.ProjectContextAcknowledgment) (ContextDelivery, error) {
	ownerKey = strings.TrimSpace(ownerKey)
	ack.WorkSessionID = strings.TrimSpace(ack.WorkSessionID)
	ack.TargetID = strings.TrimSpace(ack.TargetID)
	ack.ContextRevision = strings.TrimSpace(ack.ContextRevision)
	if ownerKey == "" || ack.WorkSessionID == "" || ack.ContextRevision == "" {
		return ContextDelivery{}, invalid("Context acknowledgment owner/work_session_id/context_revision 不能为空")
	}
	if err := s.validateContextDeliveryOwner(ctx, ownerKey, ack.WorkSessionID, ack.TargetID); err != nil {
		return ContextDelivery{}, err
	}
	current, err := s.getContextDelivery(ctx, ack.WorkSessionID, ack.TargetID)
	if err != nil {
		return ContextDelivery{}, err
	}
	if current.ContextRevision != ack.ContextRevision {
		return ContextDelivery{}, &ContextDeliveryRevisionConflictError{ContextRevision: current.ContextRevision}
	}
	now := s.now().UTC()
	_, err = s.db.ExecContext(ctx, `UPDATE project_context_deliveries SET
		status = ?,
		host_consumed_at = CASE WHEN host_consumed_at = '' THEN ? ELSE host_consumed_at END,
		updated_at = ?
		WHERE work_session_id = ? AND target_id = ? AND context_revision = ?`,
		string(protocol.ProjectContextHostConsumed), formatTime(now), formatTime(now), ack.WorkSessionID, ack.TargetID, ack.ContextRevision)
	if err != nil {
		return ContextDelivery{}, fmt.Errorf("确认 Project Context host_consumed: %w", err)
	}
	return s.GetContextDelivery(ctx, ownerKey, ack.WorkSessionID, ack.TargetID)
}

func (s *Store) GetContextDelivery(ctx context.Context, ownerKey, workSessionID, targetID string) (ContextDelivery, error) {
	ownerKey = strings.TrimSpace(ownerKey)
	workSessionID = strings.TrimSpace(workSessionID)
	targetID = strings.TrimSpace(targetID)
	if err := s.validateContextDeliveryOwner(ctx, ownerKey, workSessionID, targetID); err != nil {
		return ContextDelivery{}, err
	}
	return s.getContextDelivery(ctx, workSessionID, targetID)
}

func (s *Store) GetProjectContextDelivery(ctx context.Context, projectID, workSessionID, targetID string) (ContextDelivery, error) {
	if _, err := s.GetProjectWorkSession(ctx, projectID, workSessionID); err != nil {
		return ContextDelivery{}, err
	}
	if strings.TrimSpace(targetID) != "" {
		targets, err := s.ListProjectWorkTargets(ctx, projectID, workSessionID)
		if err != nil {
			return ContextDelivery{}, err
		}
		found := false
		for _, target := range targets {
			if target.Target.ID == strings.TrimSpace(targetID) {
				found = true
				break
			}
		}
		if !found {
			return ContextDelivery{}, ErrWorkTargetNotFound
		}
	}
	return s.getContextDelivery(ctx, strings.TrimSpace(workSessionID), strings.TrimSpace(targetID))
}

func (s *Store) validateContextDeliveryOwner(ctx context.Context, ownerKey, workSessionID, targetID string) error {
	if s == nil || s.db == nil {
		return errors.New("Project store 未初始化")
	}
	if _, err := s.GetWorkSession(ctx, ownerKey, workSessionID); err != nil {
		return err
	}
	if targetID != "" {
		if _, err := s.GetWorkTarget(ctx, ownerKey, workSessionID, targetID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) getContextDelivery(ctx context.Context, workSessionID, targetID string) (ContextDelivery, error) {
	row := s.db.QueryRowContext(ctx, `SELECT work_session_id, target_id, context_revision, status, returned_at, host_consumed_at, updated_at
		FROM project_context_deliveries WHERE work_session_id = ? AND target_id = ?`, workSessionID, targetID)
	var item ContextDelivery
	var status, returnedAt, consumedAt, updatedAt string
	if err := row.Scan(&item.WorkSessionID, &item.TargetID, &item.ContextRevision, &status, &returnedAt, &consumedAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ContextDelivery{}, ErrContextDeliveryNotFound
		}
		return ContextDelivery{}, err
	}
	item.Status = protocol.ProjectContextDeliveryStatus(status)
	parsedReturned, err := parseTime(returnedAt)
	if err != nil {
		return ContextDelivery{}, err
	}
	item.ReturnedAt = parsedReturned
	parsedUpdated, err := parseTime(updatedAt)
	if err != nil {
		return ContextDelivery{}, err
	}
	item.UpdatedAt = parsedUpdated
	if consumedAt != "" {
		parsedConsumed, err := parseTime(consumedAt)
		if err != nil {
			return ContextDelivery{}, err
		}
		item.HostConsumedAt = &parsedConsumed
	}
	return item, nil
}
