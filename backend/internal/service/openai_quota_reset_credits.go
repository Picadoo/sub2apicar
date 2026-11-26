package service

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OpenAIQuotaResetCreditsExtraKey 是账号 extra 中保存脱敏重置卡快照的键名。
const OpenAIQuotaResetCreditsExtraKey = openaiQuotaResetCreditsKey

// AccountResetCreditSummary 是向拼车/配额界面暴露的脱敏重置卡摘要（不含上游卡 ID）。
type AccountResetCreditSummary struct {
	AvailableCount int                                `json:"available_count"`
	Credits        []OpenAIRateLimitResetCreditDetail `json:"credits,omitempty"`
}

// ParseAccountResetCreditSnapshot 解析 accounts.extra.codex_reset_credit_snapshot，
// 过滤已过期卡并按过期时间升序排列。
// 若快照缺失/损坏，或声称有卡但列表内全部已过期，返回 nil（由调用方按“未查询/未知”处理，不伪造 0 张）。
func ParseAccountResetCreditSnapshot(raw []byte, now time.Time) *AccountResetCreditSummary {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || trimmed[0] != '{' {
		return nil
	}
	var payload struct {
		AvailableCount *int                               `json:"available_count"`
		Credits        []OpenAIRateLimitResetCreditDetail `json:"credits"`
	}
	if err := json.Unmarshal(trimmed, &payload); err != nil || payload.AvailableCount == nil {
		return nil
	}
	rawCount := *payload.AvailableCount
	if rawCount < 0 {
		return nil
	}

	type parsedCredit struct {
		expiresAt string
		parsed    time.Time
		hasTime   bool
	}
	valid := make([]parsedCredit, 0, len(payload.Credits))
	for _, credit := range payload.Credits {
		exp := strings.TrimSpace(credit.ExpiresAt)
		if exp == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, exp); err == nil {
			if !t.After(now) {
				continue
			}
			valid = append(valid, parsedCredit{expiresAt: exp, parsed: t, hasTime: true})
		} else {
			// 与前端 OpenAIQuotaResetCell 口径一致：无法解析的时间戳原样保留，避免静默低估可用张数。
			valid = append(valid, parsedCredit{expiresAt: exp})
		}
	}

	sort.Slice(valid, func(i, j int) bool {
		if valid[i].hasTime && valid[j].hasTime {
			if !valid[i].parsed.Equal(valid[j].parsed) {
				return valid[i].parsed.Before(valid[j].parsed)
			}
		} else if valid[i].hasTime != valid[j].hasTime {
			return valid[i].hasTime
		}
		return valid[i].expiresAt < valid[j].expiresAt
	})

	availableCount := rawCount
	if availableCount > len(valid) {
		availableCount = len(valid)
	}
	if rawCount > 0 && availableCount <= 0 {
		return nil
	}

	credits := make([]OpenAIRateLimitResetCreditDetail, 0, len(valid))
	for _, item := range valid {
		credits = append(credits, OpenAIRateLimitResetCreditDetail{ExpiresAt: item.expiresAt})
	}
	return &AccountResetCreditSummary{
		AvailableCount: availableCount,
		Credits:        credits,
	}
}

// ExtractAccountResetCreditsSummary 从内存中的 account.Extra 提取脱敏重置卡摘要。
func ExtractAccountResetCreditsSummary(extra map[string]any, now time.Time) *AccountResetCreditSummary {
	if extra == nil {
		return nil
	}
	snapshot, ok := extra[OpenAIQuotaResetCreditsExtraKey]
	if !ok || snapshot == nil {
		return nil
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	return ParseAccountResetCreditSnapshot(raw, now)
}

type openAIRateLimitResetCreditDetailPayload struct {
	ID             string `json:"id,omitempty"`
	CreditID       string `json:"credit_id,omitempty"`
	CreditIDCamel  string `json:"creditId,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	ExpiresAtCamel string `json:"expiresAt,omitempty"`
	ResetType      string `json:"reset_type,omitempty"`
	ResetTypeCamel string `json:"resetType,omitempty"`
	Status         string `json:"status,omitempty"`
}

type openAIRateLimitResetCreditDetailsPayload struct {
	AvailableCount        json.RawMessage `json:"available_count,omitempty"`
	AvailableCountCamel   json.RawMessage `json:"availableCount,omitempty"`
	Credits               json.RawMessage `json:"credits,omitempty"`
	RateLimitResetCredits json.RawMessage `json:"rate_limit_reset_credits,omitempty"`
	Items                 json.RawMessage `json:"items,omitempty"`
	Data                  json.RawMessage `json:"data,omitempty"`
}

type openAIRateLimitResetCreditDetails struct {
	AvailableCount       *int
	AvailableCreditCount int
	CreditListPresent    bool
	Credits              []OpenAIRateLimitResetCreditDetail
	AutoResetCandidates  []openAIAutoResetCreditCandidate
}

// openAIAutoResetCreditCandidate 仅在服务内部流转。上游卡 ID 不进入 API DTO、
// 账号 extra 或日志，避免管理端响应扩大敏感标识暴露面。
type openAIAutoResetCreditCandidate struct {
	ID        string
	ExpiresAt string
}

func parseOpenAIRateLimitResetCreditDetails(body []byte) (openAIRateLimitResetCreditDetails, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return openAIRateLimitResetCreditDetails{}, nil
	}

	var rawCredits []*openAIRateLimitResetCreditDetailPayload
	var availableCount *int
	var creditListPresent bool
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &rawCredits); err != nil {
			return openAIRateLimitResetCreditDetails{}, err
		}
		creditListPresent = true
	} else {
		var payload openAIRateLimitResetCreditDetailsPayload
		if err := json.Unmarshal(trimmed, &payload); err != nil {
			return openAIRateLimitResetCreditDetails{}, err
		}
		availableCount = parseOpenAIResetCreditAvailableCount(payload.AvailableCount, payload.AvailableCountCamel)
		var err error
		rawCredits, creditListPresent, err = firstPresentResetCreditPayload(
			payload.Credits,
			payload.RateLimitResetCredits,
			payload.Items,
			payload.Data,
		)
		if err != nil {
			return openAIRateLimitResetCreditDetails{AvailableCount: availableCount}, err
		}
	}

	credits := make([]OpenAIRateLimitResetCreditDetail, 0, len(rawCredits))
	autoResetCandidates := make([]openAIAutoResetCreditCandidate, 0, len(rawCredits))
	availableCreditCount := 0
	for _, raw := range rawCredits {
		if raw == nil {
			continue
		}
		resetType := strings.TrimSpace(raw.ResetType)
		if resetType == "" {
			resetType = strings.TrimSpace(raw.ResetTypeCamel)
		}
		if resetType != "" && !strings.EqualFold(resetType, "codex_rate_limits") {
			continue
		}
		if status := strings.TrimSpace(raw.Status); status != "" && !strings.EqualFold(status, "available") {
			continue
		}
		availableCreditCount++
		expiresAt := strings.TrimSpace(raw.ExpiresAt)
		if expiresAt == "" {
			expiresAt = strings.TrimSpace(raw.ExpiresAtCamel)
		}
		if expiresAt == "" {
			continue
		}
		credits = append(credits, OpenAIRateLimitResetCreditDetail{ExpiresAt: expiresAt})
		creditID := strings.TrimSpace(raw.ID)
		if creditID == "" {
			creditID = strings.TrimSpace(raw.CreditID)
		}
		if creditID == "" {
			creditID = strings.TrimSpace(raw.CreditIDCamel)
		}
		autoResetCandidates = append(autoResetCandidates, openAIAutoResetCreditCandidate{
			ID:        creditID,
			ExpiresAt: expiresAt,
		})
	}
	return openAIRateLimitResetCreditDetails{
		AvailableCount:       availableCount,
		AvailableCreditCount: availableCreditCount,
		CreditListPresent:    creditListPresent,
		Credits:              credits,
		AutoResetCandidates:  autoResetCandidates,
	}, nil
}

func parseOpenAIResetCreditAvailableCount(values ...json.RawMessage) *int {
	for _, value := range values {
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			continue
		}

		var count int
		if trimmed[0] == '"' {
			var text string
			if err := json.Unmarshal(trimmed, &text); err != nil {
				continue
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(text))
			if err != nil {
				continue
			}
			count = parsed
		} else if err := json.Unmarshal(trimmed, &count); err != nil {
			continue
		}
		if count >= 0 {
			return &count
		}
	}
	return nil
}

func firstPresentResetCreditPayload(values ...json.RawMessage) ([]*openAIRateLimitResetCreditDetailPayload, bool, error) {
	for _, value := range values {
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			continue
		}
		var credits []*openAIRateLimitResetCreditDetailPayload
		if err := json.Unmarshal(trimmed, &credits); err != nil {
			return nil, false, err
		}
		return credits, true, nil
	}
	return nil, false, nil
}
