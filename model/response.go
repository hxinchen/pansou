package model

import "time"

// Link 网盘链接
type Link struct {
	Type      string    `json:"type" sonic:"type"`
	URL       string    `json:"url" sonic:"url"`
	Password  string    `json:"password" sonic:"password"`
	Datetime  time.Time `json:"datetime,omitempty" sonic:"datetime,omitempty"`     // 链接更新时间（可选）
	WorkTitle string    `json:"work_title,omitempty" sonic:"work_title,omitempty"` // 作品标题（用于区分同一消息中多个作品的链接）
}

// SearchResult 搜索结果
type SearchResult struct {
	MessageID string    `json:"message_id" sonic:"message_id"`
	UniqueID  string    `json:"unique_id" sonic:"unique_id"` // 全局唯一ID
	Channel   string    `json:"channel" sonic:"channel"`
	SubSource string    `json:"sub_source,omitempty" sonic:"sub_source,omitempty"` // 插件内部可识别的来源站点
	Datetime  time.Time `json:"datetime" sonic:"datetime"`
	Title     string    `json:"title" sonic:"title"`
	Content   string    `json:"content" sonic:"content"`
	Links     []Link    `json:"links" sonic:"links"`
	Tags      []string  `json:"tags,omitempty" sonic:"tags,omitempty"`
	Images    []string  `json:"images,omitempty" sonic:"images,omitempty"` // TG消息中的图片链接
}

// MergedLink 合并后的网盘链接
type MergedLink struct {
	URL       string    `json:"url" sonic:"url"`
	Password  string    `json:"password" sonic:"password"`
	Note      string    `json:"note" sonic:"note"`
	Datetime  time.Time `json:"datetime" sonic:"datetime"`
	Source    string    `json:"source,omitempty" sonic:"source,omitempty"`         // 数据来源：tg:频道名 或 plugin:插件名
	SubSource string    `json:"sub_source,omitempty" sonic:"sub_source,omitempty"` // 插件内部可识别的来源站点
	Images    []string  `json:"images,omitempty" sonic:"images,omitempty"`         // TG消息中的图片链接
}

// MergedLinks 按网盘类型分组的合并链接
type MergedLinks map[string][]MergedLink

// SearchCompletion describes whether every requested source finished before
// the response was returned. Partial responses may still contain useful data.
type SearchCompletion string

const (
	SearchCompletionComplete   SearchCompletion = "complete"
	SearchCompletionPartial    SearchCompletion = "partial"
	SearchCompletionProcessing SearchCompletion = "processing"
)

// SourceStatus exposes useful progress for a source that returned a partial
// result. It is omitted for normal complete sources to keep responses compact.
type SourceStatus struct {
	Completion SearchCompletion `json:"completion" sonic:"completion"`
	Candidates int              `json:"candidates,omitempty" sonic:"candidates,omitempty"`
	Attempted  int              `json:"attempted,omitempty" sonic:"attempted,omitempty"`
	Succeeded  int              `json:"succeeded,omitempty" sonic:"succeeded,omitempty"`
	Failed     int              `json:"failed,omitempty" sonic:"failed,omitempty"`
	Message    string           `json:"message,omitempty" sonic:"message,omitempty"`
}

// SearchResponse 搜索响应
type SearchResponse struct {
	Total          int                     `json:"total" sonic:"total"`
	Results        []SearchResult          `json:"results,omitempty" sonic:"results,omitempty"`
	MergedByType   MergedLinks             `json:"merged_by_type,omitempty" sonic:"merged_by_type,omitempty"`
	Completion     SearchCompletion        `json:"completion,omitempty" sonic:"completion,omitempty"`
	PartialSources []string                `json:"partial_sources,omitempty" sonic:"partial_sources,omitempty"`
	SourceStatuses map[string]SourceStatus `json:"source_statuses,omitempty" sonic:"source_statuses,omitempty"`
}

func (r SearchResponse) IsPartial() bool {
	return r.Completion == SearchCompletionPartial
}

// Response API通用响应
type Response struct {
	Code    int         `json:"code" sonic:"code"`
	Message string      `json:"message" sonic:"message"`
	Data    interface{} `json:"data,omitempty" sonic:"data,omitempty"`
}

// NewSuccessResponse 创建成功响应
func NewSuccessResponse(data interface{}) Response {
	return Response{
		Code:    0,
		Message: "success",
		Data:    data,
	}
}

// NewErrorResponse 创建错误响应
func NewErrorResponse(code int, message string) Response {
	return Response{
		Code:    code,
		Message: message,
	}
}
