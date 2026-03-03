package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Bowl42/maxx-next/internal/adapter/provider/antigravity"
	"github.com/Bowl42/maxx-next/internal/domain"
	"github.com/Bowl42/maxx-next/internal/repository"
	"github.com/Bowl42/maxx-next/internal/service"
)

// AntigravityHandler handles Antigravity-specific API requests
type AntigravityHandler struct {
	svc       *service.AdminService
	quotaRepo repository.AntigravityQuotaRepository
}

// NewAntigravityHandler creates a new Antigravity handler
func NewAntigravityHandler(svc *service.AdminService, quotaRepo repository.AntigravityQuotaRepository) *AntigravityHandler {
	return &AntigravityHandler{svc: svc, quotaRepo: quotaRepo}
}

// ServeHTTP routes Antigravity requests
// Routes:
//   POST /antigravity/validate-token - 验证单个 refresh token
//   POST /antigravity/validate-tokens - 批量验证 refresh tokens
//   GET  /antigravity/providers/{id}/quota - 获取 provider 的配额信息
func (h *AntigravityHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/antigravity")
	path = strings.TrimSuffix(path, "/")

	parts := strings.Split(path, "/")

	// POST /antigravity/validate-token
	if len(parts) >= 2 && parts[1] == "validate-token" && r.Method == http.MethodPost {
		h.handleValidateToken(w, r)
		return
	}

	// POST /antigravity/validate-tokens
	if len(parts) >= 2 && parts[1] == "validate-tokens" && r.Method == http.MethodPost {
		h.handleValidateTokens(w, r)
		return
	}

	// GET /antigravity/providers/{id}/quota
	if len(parts) >= 4 && parts[1] == "providers" && parts[3] == "quota" {
		id, _ := strconv.ParseUint(parts[2], 10, 64)
		if id > 0 {
			h.handleGetQuota(w, r, id)
			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

// handleValidateToken 验证单个 refresh token
func (h *AntigravityHandler) handleValidateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if req.RefreshToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "refreshToken is required"})
		return
	}

	result, err := antigravity.ValidateRefreshToken(r.Context(), req.RefreshToken)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// 保存配额到数据库（基于邮箱）
	if result.Valid && result.UserInfo != nil && result.UserInfo.Email != "" {
		h.saveQuotaToDB(result.UserInfo.Email, result.UserInfo.Name, result.UserInfo.Picture, result.ProjectID, result.Quota)
	}

	writeJSON(w, http.StatusOK, result)
}

// saveQuotaToDB 保存配额到数据库
func (h *AntigravityHandler) saveQuotaToDB(email, name, picture, projectID string, quota *antigravity.QuotaData) {
	if h.quotaRepo == nil || email == "" {
		return
	}

	var models []domain.AntigravityModelQuota
	var subscriptionTier string
	var isForbidden bool
	var lastUpdated int64

	if quota != nil {
		models = make([]domain.AntigravityModelQuota, len(quota.Models))
		for i, m := range quota.Models {
			models[i] = domain.AntigravityModelQuota{
				Name:       m.Name,
				Percentage: m.Percentage,
				ResetTime:  m.ResetTime,
			}
		}
		subscriptionTier = quota.SubscriptionTier
		isForbidden = quota.IsForbidden
		lastUpdated = quota.LastUpdated
	} else {
		lastUpdated = time.Now().Unix()
	}

	domainQuota := &domain.AntigravityQuota{
		Email:            email,
		Name:             name,
		Picture:          picture,
		ProjectID:        projectID,
		SubscriptionTier: subscriptionTier,
		IsForbidden:      isForbidden,
		Models:           models,
		LastUpdated:      lastUpdated,
	}

	h.quotaRepo.Upsert(domainQuota)
}

// handleValidateTokens 批量验证 refresh tokens
func (h *AntigravityHandler) handleValidateTokens(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// 可以是 tokens 数组或多行文本
		Tokens    []string `json:"tokens,omitempty"`
		TokenText string   `json:"tokenText,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	var tokens []string
	if len(req.Tokens) > 0 {
		tokens = req.Tokens
	} else if req.TokenText != "" {
		tokens = antigravity.ParseRefreshTokens(req.TokenText)
	}

	if len(tokens) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no valid tokens provided"})
		return
	}

	// 限制批量验证数量
	if len(tokens) > 50 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too many tokens (max 50)"})
		return
	}

	results := antigravity.BatchValidateRefreshTokens(r.Context(), tokens)

	// 保存每个有效的验证结果到数据库
	for _, result := range results {
		if result.Valid && result.UserInfo != nil && result.UserInfo.Email != "" {
			h.saveQuotaToDB(result.UserInfo.Email, result.UserInfo.Name, result.UserInfo.Picture, result.ProjectID, result.Quota)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"total":   len(results),
	})
}

// handleGetQuota 获取 provider 的配额信息
func (h *AntigravityHandler) handleGetQuota(w http.ResponseWriter, r *http.Request, providerID uint64) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	// 获取 provider
	provider, err := h.svc.GetProvider(providerID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "provider not found"})
		return
	}

	// 检查是否为 Antigravity provider
	if provider.Type != "antigravity" || provider.Config == nil || provider.Config.Antigravity == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not an Antigravity provider"})
		return
	}

	config := provider.Config.Antigravity
	email := config.Email

	// 检查 refresh 参数 - 是否强制刷新
	forceRefresh := r.URL.Query().Get("refresh") == "true"

	// 尝试从数据库获取缓存的配额（如果不是强制刷新）
	if !forceRefresh && email != "" && h.quotaRepo != nil {
		cachedQuota, err := h.quotaRepo.GetByEmail(email)
		if err == nil && cachedQuota != nil {
			// 检查是否过期（5分钟）
			if time.Now().Unix()-cachedQuota.LastUpdated < 300 {
				writeJSON(w, http.StatusOK, h.domainQuotaToResponse(cachedQuota))
				return
			}
		}
	}

	// 从 API 获取最新配额
	quota, err := antigravity.FetchQuotaForProvider(r.Context(), config.RefreshToken, config.ProjectID)
	if err != nil {
		// 如果 API 失败，尝试返回缓存数据
		if email != "" && h.quotaRepo != nil {
			cachedQuota, _ := h.quotaRepo.GetByEmail(email)
			if cachedQuota != nil {
				writeJSON(w, http.StatusOK, h.domainQuotaToResponse(cachedQuota))
				return
			}
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// 保存到数据库（从缓存获取已有的 name 和 picture，因为 FetchQuotaForProvider 不返回这些）
	if email != "" {
		// 尝试保留已有的用户信息
		var name, picture string
		if cachedQuota, _ := h.quotaRepo.GetByEmail(email); cachedQuota != nil {
			name = cachedQuota.Name
			picture = cachedQuota.Picture
		}
		h.saveQuotaToDB(email, name, picture, config.ProjectID, quota)
	}

	writeJSON(w, http.StatusOK, quota)
}

// domainQuotaToResponse 将数据库模型转换为 API 响应
func (h *AntigravityHandler) domainQuotaToResponse(quota *domain.AntigravityQuota) *antigravity.QuotaData {
	models := make([]antigravity.ModelQuota, len(quota.Models))
	for i, m := range quota.Models {
		models[i] = antigravity.ModelQuota{
			Name:       m.Name,
			Percentage: m.Percentage,
			ResetTime:  m.ResetTime,
		}
	}

	return &antigravity.QuotaData{
		Models:           models,
		LastUpdated:      quota.LastUpdated,
		IsForbidden:      quota.IsForbidden,
		SubscriptionTier: quota.SubscriptionTier,
	}
}
