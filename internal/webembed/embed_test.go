package webembed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// HARD-03 负测：路径穿越请求不得泄露任何宿主文件内容。嵌入文件系统本身
// 只含前端产物，穿越尝试必须以 400/404 结束，绝不返回 200。
func TestHandlerRejectsPathTraversal(t *testing.T) {
	handler := Handler()
	attempts := []string{
		"/../../etc/passwd",
		"/..%2f..%2f..%2fetc/passwd",
		"/%2e%2e/%2e%2e/etc/passwd",
		"/..\\..\\windows\\win.ini",
	}
	for _, target := range attempts {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			t.Fatalf("traversal %q returned 200: %s", target, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "root:") || strings.Contains(response.Body.String(), "[extensions]") {
			t.Fatalf("traversal %q leaked file content: %s", target, response.Body.String())
		}
	}
}
