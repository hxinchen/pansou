package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/credential"
	"pansou/model"
	"pansou/sourceconfig"
	"pansou/storage"
)

type CredentialHandler struct {
	service       *credential.Service
	adapters      map[string]any
	sourceManager *sourceconfig.Manager
}

func NewCredentialHandler(s *credential.Service, a map[string]any, managers ...*sourceconfig.Manager) *CredentialHandler {
	h := &CredentialHandler{service: s, adapters: a}
	if len(managers) > 0 {
		h.sourceManager = managers[0]
	}
	return h
}

func (h *CredentialHandler) acquireAdapter(key string) (any, func(), bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	if h != nil && h.sourceManager != nil {
		lease, err := h.sourceManager.Acquire()
		if err != nil {
			return nil, func() {}, false
		}
		snapshot := lease.Snapshot()
		if snapshot != nil && snapshot.PluginManager != nil {
			for _, instance := range snapshot.PluginManager.GetPlugins() {
				if strings.EqualFold(instance.Name(), key) {
					return instance, lease.Release, true
				}
			}
		}
		lease.Release()
		return nil, func() {}, false
	}
	adapter, ok := h.adapters[key]
	return adapter, func() {}, ok
}

func (h *CredentialHandler) adapterKeys() []string {
	if h != nil && h.sourceManager != nil {
		lease, err := h.sourceManager.Acquire()
		if err == nil {
			defer lease.Release()
			if snapshot := lease.Snapshot(); snapshot != nil && snapshot.PluginManager != nil {
				keys := make([]string, 0)
				for _, instance := range snapshot.PluginManager.GetPlugins() {
					if _, password := instance.(credential.PasswordLoginAdapter); password {
						keys = append(keys, strings.ToLower(instance.Name()))
						continue
					}
					if _, qr := instance.(credential.QRLoginAdapter); qr {
						keys = append(keys, strings.ToLower(instance.Name()))
					}
				}
				return keys
			}
		}
	}
	keys := make([]string, 0, len(h.adapters))
	for key := range h.adapters {
		keys = append(keys, key)
	}
	return keys
}
func (h *CredentialHandler) RegisterUser(g *gin.RouterGroup) {
	g.GET("/plugins", h.userPlugins)
	g.GET("/plugin-credentials", h.listUser)
	g.POST("/plugin-credentials", h.createUser)
	g.PATCH("/plugin-credentials/:id", h.patchUser)
	g.POST("/plugin-credentials/:id/toggle", h.toggleUser)
	g.DELETE("/plugin-credentials/:id", h.deleteUser)
	g.POST("/plugin-credentials/login-flows", h.beginUserFlow)
	g.GET("/plugin-credentials/login-flows/:id", h.pollUserFlow)
}
func (h *CredentialHandler) RegisterAdmin(g *gin.RouterGroup) {
	g.GET("/plugin-credentials", h.listAdmin)
	g.POST("/plugin-credentials", h.createAdmin)
	g.POST("/plugin-credentials/:id/relogin", h.reloginAdmin)
	g.POST("/plugin-credentials/:id/toggle", h.toggleAdmin)
	g.PATCH("/plugin-credentials/:id", h.patchAdmin)
	g.PUT("/plugin-credentials/:id/scope", h.scopeAdmin)
	g.DELETE("/plugin-credentials/:id", h.deleteAdmin)
	g.POST("/plugin-credentials/login-flows", h.beginAdminFlow)
	g.GET("/plugin-credentials/login-flows/:id", h.pollAdminFlow)
	g.PATCH("/user-plugin-credentials/:id", h.patchUserAsAdmin)
	g.DELETE("/user-plugin-credentials/:id", h.deleteUserAsAdmin)
}

type credentialDTO struct {
	PublicID            string         `json:"public_id"`
	PluginKey           string         `json:"plugin_key"`
	Scope               string         `json:"scope"`
	OwnerUserID         *int64         `json:"owner_user_id,omitempty"`
	DisplayName         string         `json:"display_name"`
	PublicMetadata      map[string]any `json:"public_metadata"`
	OwnerEnabled        bool           `json:"owner_enabled"`
	Status              string         `json:"status"`
	ExpiresAt           any            `json:"expires_at,omitempty"`
	AdminSuspended      bool           `json:"admin_suspended"`
	ConsecutiveFailures int            `json:"consecutive_failures"`
	LastErrorCode       string         `json:"last_error_code,omitempty"`
	LastSuccessAt       any            `json:"last_success_at,omitempty"`
	CreatedAt           any            `json:"created_at,omitempty"`
	UpdatedAt           any            `json:"updated_at,omitempty"`
}

func safeCredential(v storage.PluginCredential) credentialDTO {
	return credentialDTO{PublicID: v.PublicID, PluginKey: v.PluginKey, Scope: v.Scope, OwnerUserID: v.OwnerUserID, DisplayName: v.DisplayName, PublicMetadata: redactMetadata(v.PublicMetadata), OwnerEnabled: v.OwnerEnabled, Status: v.Status, ExpiresAt: v.ExpiresAt, AdminSuspended: v.AdminSuspendedAt != nil, ConsecutiveFailures: v.ConsecutiveFailures, LastErrorCode: v.LastErrorCode, LastSuccessAt: v.LastSuccessAt, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}
func safeCredentialPage(p storage.PluginCredentialPage) gin.H {
	items := make([]credentialDTO, len(p.Items))
	for i := range p.Items {
		items[i] = safeCredential(p.Items[i])
	}
	return gin.H{"items": items, "total": p.Total, "page": p.Page, "page_size": p.PageSize}
}
func (h *CredentialHandler) ready(c *gin.Context) bool {
	if h == nil || h.service == nil {
		c.JSON(503, gin.H{"error": "凭证服务不可用", "code": "credential_unavailable"})
		return false
	}
	return true
}
func principalID(c *gin.Context) (int64, bool) {
	p, ok := currentPrincipal(c)
	if !ok || p.UserID <= 0 {
		c.JSON(401, gin.H{"error": "未授权", "code": "AUTH_TOKEN_MISSING"})
		return 0, false
	}
	return p.UserID, true
}
func (h *CredentialHandler) listUser(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	id, ok := principalID(c)
	if !ok {
		return
	}
	p, e := h.service.ListUser(c, id, credentialFilter(c))
	if e != nil {
		h.respond(c, nil, e)
		return
	}
	h.respond(c, safeCredentialPage(p), nil)
}
func (h *CredentialHandler) listAdmin(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	p, e := h.service.ListAdmin(c, credentialFilter(c))
	if e != nil {
		h.respond(c, nil, e)
		return
	}
	h.respond(c, safeCredentialPage(p), nil)
}

func (h *CredentialHandler) userPlugins(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	id, ok := principalID(c)
	if !ok {
		return
	}
	page, e := h.service.ListUser(c.Request.Context(), id, storage.PluginCredentialFilter{Page: 1, PageSize: 500})
	if e != nil {
		h.respond(c, nil, e)
		return
	}
	byPlugin := map[string]int{}
	for _, v := range page.Items {
		if v.IsUsableAt(h.serviceTime()) {
			byPlugin[v.PluginKey]++
		}
	}
	keys := h.adapterKeys()
	items := make([]gin.H, 0, len(keys))
	for _, key := range keys {
		sharedCount := 0
		sharedStatus := "unavailable"
		if layers, err := h.service.Resolve(c.Request.Context(), credential.Identity{Actor: credential.ActorUser, UserID: id}, key, 500); err == nil {
			sharedCount = len(layers.Shared)
			if sharedCount > 0 {
				sharedStatus = "available"
			}
		}
		descriptor := credentialPluginDescriptor(key)
		descriptor["enabled"] = true
		descriptor["private_count"] = byPlugin[key]
		descriptor["credential_count"] = byPlugin[key]
		descriptor["active_credential_count"] = byPlugin[key]
		descriptor["shared"] = gin.H{"status": sharedStatus, "available_count": sharedCount}
		items = append(items, descriptor)
	}
	h.respond(c, gin.H{"items": items}, nil)
}
func credentialPluginDescriptor(key string) gin.H {
	display := map[string]string{"qqpd": "QQ频道", "gying": "观影", "panlian": "盘链", "weibo": "微博"}[key]
	loginType := "password"
	if key == "qqpd" || key == "weibo" {
		loginType = "qr"
	}
	return gin.H{"key": key, "plugin_key": key, "display_name": display, "requires_account": true, "supports_multiple": true, "login_type": loginType}
}
func (h *CredentialHandler) serviceTime() time.Time { return time.Now() }

type credentialRequest struct {
	PluginKey      string         `json:"plugin_key"`
	Scope          string         `json:"scope"`
	DisplayName    string         `json:"display_name"`
	Username       string         `json:"username"`
	Password       string         `json:"password"`
	Enabled        *bool          `json:"enabled"`
	CredentialID   string         `json:"credential_id"`
	Suspended      *bool          `json:"suspended"`
	Metadata       map[string]any `json:"metadata"`
	PublicMetadata map[string]any `json:"public_metadata"`
}

func (r credentialRequest) metadataPatch() (map[string]any, bool) {
	if r.Metadata == nil && r.PublicMetadata == nil {
		return nil, false
	}
	return mergeCredentialMetadata(r.Metadata, r.PublicMetadata), true
}

func (h *CredentialHandler) createUser(c *gin.Context) {
	id, ok := principalID(c)
	if !ok || !h.ready(c) {
		return
	}
	h.create(c, id, storage.CredentialScopeUserPrivate)
}
func (h *CredentialHandler) createAdmin(c *gin.Context) {
	id, ok := principalID(c)
	if !ok || !h.ready(c) {
		return
	}
	var r credentialRequest
	if c.ShouldBindJSON(&r) != nil {
		badCredential(c)
		return
	}
	if r.Scope == "" {
		r.Scope = storage.CredentialScopeAdminPrivate
	}
	h.loginCreate(c, id, nil, r, "")
}

func (h *CredentialHandler) reloginAdmin(c *gin.Context) {
	id, ok := principalID(c)
	if !ok || !h.ready(c) {
		return
	}
	var r credentialRequest
	if c.ShouldBindJSON(&r) != nil {
		badCredential(c)
		return
	}
	h.loginCreate(c, id, nil, r, c.Param("id"))
}
func (h *CredentialHandler) create(c *gin.Context, id int64, scope string) {
	var r credentialRequest
	if c.ShouldBindJSON(&r) != nil {
		badCredential(c)
		return
	}
	r.Scope = scope
	h.loginCreate(c, id, &id, r, r.CredentialID)
}
func (h *CredentialHandler) loginCreate(c *gin.Context, actor int64, owner *int64, r credentialRequest, replaceID string) {
	adapter, release, found := h.acquireAdapter(r.PluginKey)
	defer release()
	a, ok := adapter.(credential.PasswordLoginAdapter)
	if !found || !ok {
		c.JSON(400, gin.H{"error": "插件不支持密码登录", "code": "credential_login_unsupported"})
		return
	}
	m, e := a.LoginWithPassword(c, r.Username, r.Password)
	if e != nil {
		h.respond(c, nil, e)
		return
	}
	metadata, _ := r.metadataPatch()
	m.PublicMetadata = mergeCredentialMetadata(m.PublicMetadata, metadata)
	if strings.TrimSpace(r.DisplayName) != "" {
		m.DisplayName = strings.TrimSpace(r.DisplayName)
	}
	var v storage.PluginCredential
	if strings.TrimSpace(replaceID) != "" {
		if owner != nil {
			v, e = h.service.ReplaceUser(c, actor, replaceID, m)
		} else {
			v, e = h.service.ReplaceAdmin(c, replaceID, m)
		}
	} else {
		v, e = h.service.Create(c, credential.CreateInput{PluginKey: r.PluginKey, Scope: r.Scope, OwnerUserID: owner, CreatedByUserID: &actor, DisplayName: m.DisplayName, PublicMetadata: m.PublicMetadata, Secret: m.Secret, StableID: m.StableID, ConfigBinding: m.ConfigBinding, Status: m.Status, ExpiresAt: m.ExpiresAt})
	}
	if e != nil {
		h.respond(c, nil, e)
		return
	}
	h.respond(c, safeCredential(v), nil)
}
func (h *CredentialHandler) patchUser(c *gin.Context) {
	id, ok := principalID(c)
	if !ok || !h.ready(c) {
		return
	}
	var r credentialRequest
	if c.ShouldBindJSON(&r) != nil {
		badCredential(c)
		return
	}
	if r.Enabled != nil {
		h.respond(c, gin.H{}, h.service.SetUserEnabled(c, id, c.Param("id"), *r.Enabled))
		return
	}
	metadata, hasMetadata := r.metadataPatch()
	if r.DisplayName != "" || hasMetadata {
		value, err := h.service.UpdateUserMetadata(c, id, c.Param("id"), r.DisplayName, metadata)
		if err != nil {
			h.respond(c, nil, err)
			return
		}
		h.respond(c, safeCredential(value), nil)
		return
	}
	badCredential(c)
}
func (h *CredentialHandler) toggleUser(c *gin.Context) {
	id, ok := principalID(c)
	if !ok || !h.ready(c) {
		return
	}
	var r credentialRequest
	if c.ShouldBindJSON(&r) != nil || r.Enabled == nil {
		badCredential(c)
		return
	}
	h.respond(c, gin.H{}, h.service.SetUserEnabled(c, id, c.Param("id"), *r.Enabled))
}
func (h *CredentialHandler) toggleAdmin(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var r credentialRequest
	if c.ShouldBindJSON(&r) != nil || r.Enabled == nil {
		badCredential(c)
		return
	}
	h.respond(c, gin.H{}, h.service.SetAdminEnabled(c, c.Param("id"), *r.Enabled))
}
func (h *CredentialHandler) deleteUser(c *gin.Context) {
	id, ok := principalID(c)
	if !ok || !h.ready(c) {
		return
	}
	h.respond(c, gin.H{}, h.service.DeleteUser(c, id, c.Param("id")))
}
func (h *CredentialHandler) deleteAdmin(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	h.respond(c, gin.H{}, h.service.DeleteAdmin(c, c.Param("id")))
}
func (h *CredentialHandler) scopeAdmin(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var r credentialRequest
	if c.ShouldBindJSON(&r) != nil {
		badCredential(c)
		return
	}
	v, e := h.service.ChangeAdminScope(c, c.Param("id"), r.Scope)
	if e != nil {
		h.respond(c, nil, e)
		return
	}
	h.respond(c, safeCredential(v), nil)
}
func (h *CredentialHandler) patchAdmin(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var r credentialRequest
	if c.ShouldBindJSON(&r) != nil {
		badCredential(c)
		return
	}
	if r.Scope != "" {
		v, e := h.service.ChangeAdminScope(c.Request.Context(), c.Param("id"), r.Scope)
		if e != nil {
			h.respond(c, nil, e)
			return
		}
		h.respond(c, safeCredential(v), nil)
		return
	}
	if r.Enabled != nil {
		h.respond(c, gin.H{}, h.service.SetAdminEnabled(c.Request.Context(), c.Param("id"), *r.Enabled))
		return
	}
	metadata, hasMetadata := r.metadataPatch()
	if r.DisplayName != "" || hasMetadata {
		value, err := h.service.UpdateAdminMetadata(c.Request.Context(), c.Param("id"), r.DisplayName, metadata)
		if err != nil {
			h.respond(c, nil, err)
			return
		}
		h.respond(c, safeCredential(value), nil)
		return
	}
	badCredential(c)
}

func (h *CredentialHandler) patchUserAsAdmin(c *gin.Context) {
	adminID, ok := principalID(c)
	if !ok || !h.ready(c) {
		return
	}
	var r credentialRequest
	if c.ShouldBindJSON(&r) != nil || r.Suspended == nil {
		badCredential(c)
		return
	}
	h.respond(c, gin.H{}, h.service.Suspend(c, adminID, c.Param("id"), *r.Suspended))
}

func (h *CredentialHandler) deleteUserAsAdmin(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	h.respond(c, gin.H{}, h.service.DeleteAnyAsAdmin(c, c.Param("id")))
}

type flowRequest struct {
	PluginKey      string         `json:"plugin_key"`
	Scope          string         `json:"scope"`
	CredentialID   string         `json:"credential_id"`
	Metadata       map[string]any `json:"metadata"`
	PublicMetadata map[string]any `json:"public_metadata"`
}

type credentialFlowState struct {
	AdapterState any
	CredentialID string
	Metadata     map[string]any
}

func (h *CredentialHandler) beginUserFlow(c *gin.Context) {
	id, ok := principalID(c)
	if !ok || !h.ready(c) {
		return
	}
	h.beginFlow(c, id, storage.CredentialScopeUserPrivate)
}
func (h *CredentialHandler) beginAdminFlow(c *gin.Context) {
	id, ok := principalID(c)
	if !ok || !h.ready(c) {
		return
	}
	var r flowRequest
	if c.ShouldBindJSON(&r) != nil {
		badCredential(c)
		return
	}
	if r.Scope == "" {
		r.Scope = storage.CredentialScopeAdminPrivate
	}
	r.Metadata = mergeCredentialMetadata(r.Metadata, r.PublicMetadata)
	h.beginFlowWith(c, id, r)
}
func (h *CredentialHandler) beginFlow(c *gin.Context, id int64, scope string) {
	var r flowRequest
	if c.ShouldBindJSON(&r) != nil {
		badCredential(c)
		return
	}
	r.Scope = scope
	r.Metadata = mergeCredentialMetadata(r.Metadata, r.PublicMetadata)
	h.beginFlowWith(c, id, r)
}
func (h *CredentialHandler) beginFlowWith(c *gin.Context, id int64, r flowRequest) {
	adapter, release, found := h.acquireAdapter(r.PluginKey)
	defer release()
	a, ok := adapter.(credential.QRLoginAdapter)
	if !found || !ok {
		c.JSON(400, gin.H{"error": "插件不支持二维码登录", "code": "credential_login_unsupported"})
		return
	}
	q, e := a.BeginQRLogin(c)
	if e != nil {
		h.respond(c, nil, e)
		return
	}
	value := any(q.State)
	if strings.TrimSpace(r.CredentialID) != "" || len(r.Metadata) > 0 {
		value = credentialFlowState{AdapterState: q.State, CredentialID: strings.TrimSpace(r.CredentialID), Metadata: r.Metadata}
	}
	f, e := h.service.Flows().Create(id, strings.ToLower(r.PluginKey), r.Scope, value)
	h.respond(c, gin.H{"id": f.ID, "flow_id": f.ID, "public_id": f.ID, "plugin_key": strings.ToLower(r.PluginKey), "status": "waiting_scan", "qr_code": q.QRCodeData, "qr_code_data": q.QRCodeData, "expires_at": f.ExpiresAt}, e)
}
func (h *CredentialHandler) pollUserFlow(c *gin.Context)  { h.pollFlow(c) }
func (h *CredentialHandler) pollAdminFlow(c *gin.Context) { h.pollFlow(c) }
func (h *CredentialHandler) pollFlow(c *gin.Context) {
	id, ok := principalID(c)
	if !ok || !h.ready(c) {
		return
	}
	f, e := h.service.Flows().Get(c.Param("id"), id)
	if e != nil {
		h.respond(c, nil, e)
		return
	}
	adapter, release, found := h.acquireAdapter(f.PluginKey)
	defer release()
	a, ok := adapter.(credential.QRLoginAdapter)
	if !found || !ok {
		badCredential(c)
		return
	}
	adapterState := f.Value
	replaceID := ""
	var metadata map[string]any
	if state, ok := f.Value.(credentialFlowState); ok {
		adapterState = state.AdapterState
		replaceID = state.CredentialID
		metadata = state.Metadata
	}
	r, e := a.PollQRLogin(c, adapterState)
	if e != nil {
		h.respond(c, nil, e)
		return
	}
	if r.Material == nil || r.Status != "success" {
		h.respond(c, r, nil)
		return
	}
	owner := (*int64)(nil)
	if f.Scope == storage.CredentialScopeUserPrivate {
		owner = &id
	}
	m := r.Material
	m.PublicMetadata = mergeCredentialMetadata(m.PublicMetadata, metadata)
	var v storage.PluginCredential
	if replaceID != "" {
		if owner != nil {
			v, e = h.service.ReplaceUser(c, id, replaceID, *m)
		} else {
			v, e = h.service.ReplaceAdmin(c, replaceID, *m)
		}
	} else {
		v, e = h.service.Create(c.Request.Context(), credential.CreateInput{PluginKey: f.PluginKey, Scope: f.Scope, OwnerUserID: owner, CreatedByUserID: &id, DisplayName: m.DisplayName, PublicMetadata: m.PublicMetadata, Secret: m.Secret, StableID: m.StableID, ConfigBinding: m.ConfigBinding, Status: m.Status, ExpiresAt: m.ExpiresAt})
	}
	if e != nil {
		h.respond(c, nil, e)
		return
	}
	h.service.Flows().Delete(f.ID)
	h.respond(c, gin.H{"status": "success", "credential": safeCredential(v)}, nil)
}

func mergeCredentialMetadata(base, extra map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range extra {
		result[key] = value
	}
	return result
}
func credentialFilter(c *gin.Context) storage.PluginCredentialFilter {
	p, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	s, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	return storage.PluginCredentialFilter{PluginKeys: queryList(c, "plugin_key", "plugin"), Scopes: queryList(c, "scope", "scopes"), Statuses: queryList(c, "status", "statuses"), Page: p, PageSize: s}
}
func badCredential(c *gin.Context) {
	c.JSON(400, gin.H{"error": "请求参数无效", "code": "credential_invalid"})
}
func (h *CredentialHandler) respond(c *gin.Context, v any, e error) {
	if e == nil {
		c.JSON(http.StatusOK, model.NewSuccessResponse(v))
		return
	}
	switch {
	case errors.Is(e, storage.ErrInvalid):
		badCredential(c)
	case errors.Is(e, storage.ErrNotFound), errors.Is(e, credential.ErrFlowNotFound):
		c.JSON(404, gin.H{"error": "凭证不存在", "code": "credential_not_found"})
	case errors.Is(e, credential.ErrRateLimited):
		c.JSON(429, gin.H{"error": "请求过于频繁", "code": "rate_limited"})
	default:
		c.JSON(500, gin.H{"error": "凭证操作失败", "code": "credential_error"})
	}
}
