package monitor

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/storage/sqlite"
)

func boolp(b bool) *bool { return &b }

func testSvc(id string, enabled bool) model.Service {
	return model.Service{
		ID: id, Name: id, Protocol: model.ProtocolHTTP, BaseURL: "http://example.com",
		IntervalSec: 60, TimeoutSec: 5, Enabled: boolp(enabled),
	}
}

func defaultPage() model.PageConfig {
	return model.PageConfig{HistoryLen: 60, RefreshSec: 5, ShowUptime: true}
}

func openTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("打开测试 store 失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type deleteHookRepository struct {
	Repository
	afterDelete func()
}

func (r *deleteHookRepository) DeleteHistories(ctx context.Context, serviceIDs []string) (int64, error) {
	deleted, err := r.Repository.DeleteHistories(ctx, serviceIDs)
	if err == nil && r.afterDelete != nil {
		r.afterDelete()
	}
	return deleted, err
}
