package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/openchat/openchat/server/store"
)

// 应用目录管理 API（设计 §3.3-9 目录与订阅；网页 cats 的目录/管理界面数据面）。
//
//	GET    /api/service-agents/catalog              全部 active agent + 本账号清单状态
//	PUT    /api/service-agents/{slug}/subscription  添加（恢复默认可见）
//	DELETE /api/service-agents/{slug}/subscription  删除（屏蔽：tools/list 隐去、调用拒绝）
//
// 语义=模式二（默认开放 + 个人清单）：无行=默认可见；删除写屏蔽行；添加=删行。
// 清单跟账号（uid）；发布权仅内部（本 API 不提供创建/发布）。
type ServiceAgentCatalogHandler struct {
	store    store.ServiceAgentStore
	notifier serviceSubscriptionNotifier
}

// serviceSubscriptionNotifier 清单变更后的在线 MCP 会话同步器（MCP handler 实现）。
type serviceSubscriptionNotifier interface {
	NotifySubscriptionChanged(callerUID int64)
}

func NewServiceAgentCatalogHandler(st store.ServiceAgentStore) *ServiceAgentCatalogHandler {
	return &ServiceAgentCatalogHandler{store: st}
}

// SetSubscriptionNotifier 注入在线会话同步器（未注入=仅数据面生效，在线会话等客户端重查）。
func (h *ServiceAgentCatalogHandler) SetSubscriptionNotifier(n serviceSubscriptionNotifier) {
	h.notifier = n
}

type serviceCatalogEntry struct {
	Slug                  string `json:"slug"`
	Name                  string `json:"name"`
	Version               int64  `json:"version"`
	AsyncThresholdSeconds int    `json:"async_threshold_seconds,omitempty"`
	Hidden                bool   `json:"hidden"`
}

func (h *ServiceAgentCatalogHandler) HandleCatalog(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	agents, err := h.store.ListServiceAgents(r.Context())
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "catalog_unavailable"})
		return
	}
	hiddenIDs, err := h.store.ListHiddenServiceAgentIDs(r.Context(), uid)
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "catalog_unavailable"})
		return
	}
	hidden := make(map[int64]struct{}, len(hiddenIDs))
	for _, id := range hiddenIDs {
		hidden[id] = struct{}{}
	}
	entries := make([]serviceCatalogEntry, 0, len(agents))
	for _, agent := range agents {
		_, blocked := hidden[agent.ID]
		entries = append(entries, serviceCatalogEntry{
			Slug:                  agent.Slug,
			Name:                  agent.Name,
			Version:               agent.Version,
			AsyncThresholdSeconds: agent.AsyncThresholdSeconds,
			Hidden:                blocked,
		})
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"agents": entries})
}

// HandleAdd 添加（恢复默认可见）：PUT /api/service-agents/{slug}/subscription。幂等。
func (h *ServiceAgentCatalogHandler) HandleAdd(w http.ResponseWriter, r *http.Request) {
	h.setHidden(w, r, false)
}

// HandleRemove 删除（屏蔽）：DELETE /api/service-agents/{slug}/subscription。幂等。
func (h *ServiceAgentCatalogHandler) HandleRemove(w http.ResponseWriter, r *http.Request) {
	h.setHidden(w, r, true)
}

func (h *ServiceAgentCatalogHandler) setHidden(w http.ResponseWriter, r *http.Request, hidden bool) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	slug := strings.TrimSpace(r.PathValue("slug"))
	if slug == "" {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "slug_required"})
		return
	}
	agentID, _, _, _, err := h.store.GetServiceAgentBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrServiceAgentNotFound) {
			writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "agent_not_found"})
			return
		}
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "update_failed"})
		return
	}
	if err := h.store.SetServiceAgentSubscriptionHidden(r.Context(), uid, agentID, hidden); err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "update_failed"})
		return
	}
	if h.notifier != nil {
		// 在线 MCP 会话增量同步（list_changed 通知）；离线会话下次连接即生效。
		h.notifier.NotifySubscriptionChanged(uid)
	}
	writeServiceJSON(w, http.StatusOK, map[string]any{"slug": slug, "hidden": hidden})
}
