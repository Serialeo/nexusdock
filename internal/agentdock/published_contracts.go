package agentdock

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PublishedToolContract 记录当前已准入 release 的工具目录；启动时清空，不能恢复节点发布。
// AcceptedSemanticHashes 仅记录当前平台输入来源，不参与跨版本兼容或调用回退。
type PublishedToolContract struct {
	ToolName               string
	Descriptor             ToolDescriptor
	AcceptedSemanticHashes []string
	SourceNodeID           string
	SourceVersion          string
	UpdatedAt              time.Time
}

func (s *Store) ListPublishedToolContracts(ctx context.Context) ([]PublishedToolContract, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tool_name, descriptor_json, source_node_id, source_version, updated_at
		FROM agentdock_published_tool_contracts ORDER BY tool_name`)
	if err != nil {
		return nil, fmt.Errorf("列出 AgentDock 公开工具契约: %w", err)
	}

	contracts := make([]PublishedToolContract, 0)
	contractIndexes := make(map[string]int)
	for rows.Next() {
		var contract PublishedToolContract
		var descriptorJSON, updatedAt string
		if err := rows.Scan(&contract.ToolName, &descriptorJSON, &contract.SourceNodeID, &contract.SourceVersion, &updatedAt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("读取 AgentDock 公开工具契约: %w", err)
		}
		if err := json.Unmarshal([]byte(descriptorJSON), &contract.Descriptor); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("解析 AgentDock 公开工具契约 %s: %w", contract.ToolName, err)
		}
		if contract.Descriptor.Name != contract.ToolName {
			_ = rows.Close()
			return nil, fmt.Errorf("AgentDock 公开工具契约名称不一致: %s", contract.ToolName)
		}
		contract.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("解析 AgentDock 公开工具契约更新时间: %w", err)
		}
		contractIndexes[contract.ToolName] = len(contracts)
		contracts = append(contracts, contract)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("遍历 AgentDock 公开工具契约: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("关闭 AgentDock 公开工具契约结果集: %w", err)
	}

	variantRows, err := s.db.QueryContext(ctx, `SELECT tool_name, semantic_hash
		FROM agentdock_published_tool_variants ORDER BY tool_name, semantic_hash`)
	if err != nil {
		return nil, fmt.Errorf("列出 AgentDock 公开工具变体: %w", err)
	}
	defer variantRows.Close()
	for variantRows.Next() {
		var toolName, semanticHash string
		if err := variantRows.Scan(&toolName, &semanticHash); err != nil {
			return nil, fmt.Errorf("读取 AgentDock 公开工具变体: %w", err)
		}
		if index, ok := contractIndexes[toolName]; ok {
			contracts[index].AcceptedSemanticHashes = append(contracts[index].AcceptedSemanticHashes, semanticHash)
		}
	}
	if err := variantRows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 AgentDock 公开工具变体: %w", err)
	}
	return contracts, nil
}

func (s *Store) SavePublishedToolContract(ctx context.Context, contract PublishedToolContract) error {
	contract.ToolName = strings.TrimSpace(contract.ToolName)
	if contract.ToolName == "" || contract.Descriptor.Name != contract.ToolName {
		return invalid("公开工具契约名称无效")
	}
	descriptorJSON, err := json.Marshal(contract.Descriptor)
	if err != nil {
		return fmt.Errorf("编码 AgentDock 公开工具契约 %s: %w", contract.ToolName, err)
	}

	acceptedHashes := normalizeSemanticHashes(contract.AcceptedSemanticHashes)
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始保存 AgentDock 公开工具契约 %s: %w", contract.ToolName, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `INSERT INTO agentdock_published_tool_contracts(
		tool_name, descriptor_json, source_node_id, source_version, updated_at
	) VALUES(?, ?, ?, ?, ?) ON CONFLICT(tool_name) DO UPDATE SET
		descriptor_json = excluded.descriptor_json,
		source_node_id = excluded.source_node_id,
		source_version = excluded.source_version,
		updated_at = excluded.updated_at`,
		contract.ToolName, string(descriptorJSON), strings.TrimSpace(contract.SourceNodeID), strings.TrimSpace(contract.SourceVersion), now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("保存 AgentDock 公开工具契约 %s: %w", contract.ToolName, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agentdock_published_tool_variants WHERE tool_name = ?`, contract.ToolName); err != nil {
		return fmt.Errorf("清理 AgentDock 公开工具变体 %s: %w", contract.ToolName, err)
	}
	for _, semanticHash := range acceptedHashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agentdock_published_tool_variants(tool_name, semantic_hash) VALUES(?, ?)`, contract.ToolName, semanticHash); err != nil {
			return fmt.Errorf("保存 AgentDock 公开工具变体 %s: %w", contract.ToolName, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 AgentDock 公开工具契约 %s: %w", contract.ToolName, err)
	}
	return nil
}

func (s *Store) DeletePublishedToolContract(ctx context.Context, toolName string) error {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return invalid("公开工具契约名称无效")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM agentdock_published_tool_contracts WHERE tool_name = ?`, toolName); err != nil {
		return fmt.Errorf("删除 AgentDock 公开工具契约 %s: %w", toolName, err)
	}
	return nil
}

func normalizeSemanticHashes(hashes []string) []string {
	unique := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		hash = strings.TrimSpace(hash)
		if hash != "" {
			unique[hash] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(unique))
	for hash := range unique {
		normalized = append(normalized, hash)
	}
	sort.Strings(normalized)
	return normalized
}
