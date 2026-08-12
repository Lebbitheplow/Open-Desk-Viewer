package httpx

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// PaginationRequest holds pagination parameters
type PaginationRequest struct {
	Current    int64  `json:"current" query:"current"`
	PageSize   int64  `json:"pageSize" query:"pageSize"`
	Total      int64  `json:"total"`
	TotalPages int64  `json:"totalPages"`
	Data       []byte `json:"data"`
}

// ParsePagination extracts pagination parameters, preferring the query string
// and falling back to a JSON body.
//
// The query string has to win regardless of method: the RustDesk client POSTs
// its paged reads with an empty body and puts current and pageSize in the query.
// Reading only the body there would silently page at the default size while the
// client advanced its cursor by its own, and it would never see the tail of the
// list.
func ParsePagination(r *http.Request) (int64, int64, error) {
	current := int64(1)
	pageSize := int64(10)

	query := r.URL.Query()
	fromQuery := false

	if c := query.Get("current"); c != "" {
		if parsed, err := strconv.ParseInt(c, 10, 64); err == nil && parsed > 0 {
			current = parsed
			fromQuery = true
		}
	}
	if ps := query.Get("pageSize"); ps != "" {
		if parsed, err := strconv.ParseInt(ps, 10, 64); err == nil && parsed > 0 {
			pageSize = parsed
			fromQuery = true
		}
	}

	if !fromQuery && r.Method != http.MethodGet && r.Body != nil {
		var req struct {
			Current  int64 `json:"current"`
			PageSize int64 `json:"pageSize"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			if req.Current > 0 {
				current = req.Current
			}
			if req.PageSize > 0 {
				pageSize = req.PageSize
			}
		}
	}

	return current, pageSize, nil
}

// PaginatedResponse represents a paginated API response
type PaginatedResponse struct {
	Total int64       `json:"total"`
	Data  interface{} `json:"data"`
}

// WritePaginatedResponse writes a paginated response with metadata
func WritePaginatedResponse(w http.ResponseWriter, total int64, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(PaginatedResponse{
		Total: total,
		Data:  data,
	})
}

// WritePaginatedResponsePage writes a paginated response with page metadata
func WritePaginatedResponsePage(w http.ResponseWriter, current, pageSize, total int64, data interface{}) {
	totalPages := total / pageSize
	if total%pageSize != 0 {
		totalPages++
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"total":      total,
		"current":    current,
		"pageSize":   pageSize,
		"totalPages": totalPages,
		"data":       data,
	})
}
