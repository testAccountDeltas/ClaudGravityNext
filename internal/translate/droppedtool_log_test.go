package translate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claudgravity/internal/logx"
)

func TestLogDroppedServerTool_OncePerNameAndType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gw.log")
	logx.InitLogger(path)
	droppedToolLogged.Delete("advisor_t|advisor_20260301")
	droppedToolLogged.Delete("advisor_t|other_type")

	for i := 0; i < 5; i++ {
		logDroppedServerTool("advisor_t", "advisor_20260301")
	}
	logDroppedServerTool("advisor_t", "other_type")

	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), "dropping unsupported server tool"); n != 2 {
		t.Fatalf("want 2 log lines (one per name+type), got %d:\n%s", n, data)
	}
}
