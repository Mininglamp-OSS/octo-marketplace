package response

import (
	"net/http"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/api/errcode"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/logging"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Data is the standard successful response envelope.
type Data[T any] struct {
	Data T `json:"data"`
}

// EmptyResp represents a successful operation without response fields.
type EmptyResp struct{}

// CursorPagination describes cursor-based pagination.
type CursorPagination struct {
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// CursorList is the standard cursor-list response envelope.
type CursorList[T any] struct {
	Data       []T              `json:"data"`
	Pagination CursorPagination `json:"pagination"`
}

// OffsetPagination describes offset-based pagination.
type OffsetPagination struct {
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

// OffsetList is the standard offset-list response envelope.
type OffsetList[T any] struct {
	Data       []T              `json:"data"`
	Pagination OffsetPagination `json:"pagination"`
}

// ErrorBody is the stable machine-readable error payload.
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
	Hint    string         `json:"hint,omitempty"`
}

// Error is the standard failed response envelope.
type Error struct {
	Error ErrorBody `json:"error"`
}

func OK[T any](c *gin.Context, value T) {
	c.JSON(http.StatusOK, Data[T]{Data: value})
}

func Created[T any](c *gin.Context, value T) {
	c.JSON(http.StatusCreated, Data[T]{Data: value})
}

func Empty(c *gin.Context) {
	c.JSON(http.StatusOK, Data[EmptyResp]{Data: EmptyResp{}})
}

func Cursor[T any](c *gin.Context, items []T, hasMore bool, nextCursor string) {
	if items == nil {
		items = []T{}
	}
	c.JSON(http.StatusOK, CursorList[T]{
		Data: items,
		Pagination: CursorPagination{
			HasMore:    hasMore,
			NextCursor: nextCursor,
		},
	})
}

func Offset[T any](c *gin.Context, items []T, total, page, pageSize int) {
	if items == nil {
		items = []T{}
	}
	c.JSON(http.StatusOK, OffsetList[T]{
		Data: items,
		Pagination: OffsetPagination{
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

func Fail(c *gin.Context, status int, code, message string, details map[string]any, hint string) {
	if details == nil {
		details = map[string]any{}
	}
	c.AbortWithStatusJSON(status, Error{Error: ErrorBody{
		Code:    code,
		Message: message,
		Details: details,
		Hint:    hint,
	}})
}

func Internal(c *gin.Context, err error, operation string, fields ...zap.Field) {
	logFields := append(logging.RequestFields(c),
		zap.String("operation", operation),
		zap.Int("status", http.StatusInternalServerError),
		zap.String("code", errcode.InternalError),
		logging.ErrorField(err),
	)
	logFields = append(logFields, fields...)
	logging.Error("internal_api_error", logFields...)
	Fail(c, http.StatusInternalServerError, errcode.InternalError, "internal error", nil, "")
}
