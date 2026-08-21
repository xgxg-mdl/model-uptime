package notification

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

func TestBuiltInTemplateLanguageSelection(t *testing.T) {
	t.Parallel()
	context := NewTemplateContext(time.Now(), []model.StatusChange{{ServiceUID: "a", Model: "alpha", Error: "timeout", Status: "down", PreviousStatus: "up"}})
	english, err := RenderTemplate(TemplateForLanguage(LanguageEnglish), context)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(english, "Model incident alert") || !strings.Contains(english, "<code>alpha</code>") {
		t.Fatalf("英文内置模板渲染错误:\n%s", english)
	}
	if got := TemplateForLanguage(""); got != DefaultTemplate {
		t.Fatal("空语言必须使用中文默认模板")
	}
}

func TestNormalizeConfigDefaultsToChineseAndMigratesLegacyTemplate(t *testing.T) {
	t.Parallel()
	config := Config{Subscriptions: []Subscription{
		{ID: "empty"},
		{ID: "legacy", Template: EnglishTemplate},
		{ID: "english", Language: LanguageEnglish},
		{ID: "custom", Template: "custom English content"},
		{ID: "old-chinese", Language: DefaultLanguage, Template: legacyChineseTemplate},
		{ID: "old-english", Language: LanguageEnglish, Template: legacyEnglishTemplate},
		{ID: "stats-chinese", Language: DefaultLanguage, Template: legacyStatisticsTemplate(DefaultLanguage)},
		{ID: "stats-english", Language: LanguageEnglish, Template: legacyStatisticsTemplate(LanguageEnglish)},
		{ID: "verbose-chinese", Language: DefaultLanguage, Template: legacyVerboseChineseTemplate},
		{ID: "verbose-english", Language: LanguageEnglish, Template: legacyVerboseEnglishTemplate},
		{ID: "compact-chinese", Language: DefaultLanguage, Template: legacyCompactChineseTemplate},
		{ID: "compact-english", Language: LanguageEnglish, Template: legacyCompactEnglishTemplate},
		{ID: "card-chinese", Language: DefaultLanguage, Template: legacyCardChineseTemplate},
		{ID: "card-english", Language: LanguageEnglish, Template: legacyCardEnglishTemplate},
	}}
	NormalizeConfig(&config)
	for _, index := range []int{0, 1} {
		if config.Subscriptions[index].Language != DefaultLanguage || config.Subscriptions[index].Template != DefaultTemplate {
			t.Fatalf("订阅未迁移为中文默认模板: %+v", config.Subscriptions[index])
		}
	}
	if config.Subscriptions[2].Language != LanguageEnglish || config.Subscriptions[2].Template != EnglishTemplate {
		t.Fatalf("英文订阅未使用英文内置模板: %+v", config.Subscriptions[2])
	}
	if config.Subscriptions[3].Language != DefaultLanguage || config.Subscriptions[3].Template != "custom English content" {
		t.Fatalf("自定义模板不应被覆盖: %+v", config.Subscriptions[3])
	}
	if config.Subscriptions[4].Template != DefaultTemplate || config.Subscriptions[5].Template != EnglishTemplate {
		t.Fatalf("旧版内置模板应升级为新的统计模板: %+v %+v", config.Subscriptions[4], config.Subscriptions[5])
	}
	if config.Subscriptions[6].Template != DefaultTemplate || config.Subscriptions[7].Template != EnglishTemplate {
		t.Fatalf("带错误详情的默认模板应升级为脱敏模板: %+v %+v", config.Subscriptions[6], config.Subscriptions[7])
	}
	if config.Subscriptions[8].Template != DefaultTemplate || config.Subscriptions[9].Template != EnglishTemplate {
		t.Fatalf("冗长默认模板应升级为紧凑模板: %+v %+v", config.Subscriptions[8], config.Subscriptions[9])
	}
	if config.Subscriptions[10].Template != DefaultTemplate || config.Subscriptions[11].Template != EnglishTemplate {
		t.Fatalf("v0.10 紧凑模板应升级为新版模板: %+v %+v", config.Subscriptions[10], config.Subscriptions[11])
	}
	if config.Subscriptions[12].Template != DefaultTemplate || config.Subscriptions[13].Template != EnglishTemplate {
		t.Fatalf("v0.10.1 卡片模板应升级为分类模板: %+v %+v", config.Subscriptions[12], config.Subscriptions[13])
	}
}

func TestValidateConfigRejectsUnsupportedLanguage(t *testing.T) {
	t.Parallel()
	err := ValidateConfig(Config{Subscriptions: []Subscription{{ID: "ops", Language: "fr-FR"}}})
	if err == nil || !strings.Contains(err.Error(), "language") {
		t.Fatalf("未知语言应被拒绝: %v", err)
	}
}

func TestCompileConfigOwnsServiceUIDsAndUsesStableDeliveryFingerprint(t *testing.T) {
	t.Parallel()
	serviceIDs := []string{" b ", "a", "a"}
	config := Config{BotToken: " token ", Subscriptions: []Subscription{{
		ID: "ops", Enabled: true, ChatID: "chat", ServiceUIDs: serviceIDs, Template: "message",
	}}}
	compiled, err := compileConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if serviceIDs[0] != " b " {
		t.Fatalf("编译配置修改了调用方切片: %q", serviceIDs)
	}
	serviceIDs[0] = "changed"
	if got := compiled.subscriptions[0].ServiceUIDs[0]; got != "b" {
		t.Fatalf("运行时配置仍引用调用方切片: %q", got)
	}

	reordered, err := compileConfig(Config{BotToken: "token", Subscriptions: []Subscription{{
		ID: "ops", Enabled: true, ChatID: "chat", ServiceUIDs: []string{"a", "b"}, Template: "message",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if compiled.subscriptions[0].fingerprint != reordered.subscriptions[0].fingerprint {
		t.Fatal("仅调整服务顺序或重复项不应改变投递配置指纹")
	}
}

type reactivationTrackingRepository struct {
	*MemoryOutbox
	calls int
}

func (repository *reactivationTrackingRepository) ReactivateFailures(
	ctx context.Context,
	fingerprints map[string]string,
	availableAt time.Time,
) error {
	repository.calls++
	return repository.MemoryOutbox.ReactivateFailures(ctx, fingerprints, availableAt)
}

func TestEquivalentConfigUpdateHasNoPersistenceSideEffects(t *testing.T) {
	t.Parallel()
	repository := &reactivationTrackingRepository{MemoryOutbox: NewMemoryOutbox()}
	config := validConfig("token", "chat")
	notifier, err := New(Options{Repository: repository, Logger: discardLogger()}, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, notifier)
	if repository.calls != 1 {
		t.Fatalf("启动恢复次数 = %d，期望 1", repository.calls)
	}
	if err := notifier.UpdateConfig(config); err != nil {
		t.Fatal(err)
	}
	if repository.calls != 1 {
		t.Fatalf("等价配置产生了持久化副作用: calls=%d", repository.calls)
	}
}

func TestUpdateConfigIsUsedByFollowingNotifications(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	n := newTestNotifier(t, server.URL, validConfig("old-token", "old-chat"))
	if err := n.UpdateConfig(validConfig("new-token", "new-chat")); err != nil {
		t.Fatal(err)
	}
	if err := n.SendTest(context.Background(), "ops", ""); err != nil {
		t.Fatal(err)
	}
	closeNotifier(t, n)
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 || paths[0] != "/botnew-token/sendMessage" || paths[1] != "/botnew-token/sendMessage" {
		t.Fatalf("热更新未生效: %v", paths)
	}
}

func TestUpdateConfigKeepsPreviousSnapshotOnInvalidTemplate(t *testing.T) {
	t.Parallel()
	n := newTestNotifier(t, "http://unused", validConfig("token", "chat"))
	defer closeNotifier(t, n)
	err := n.UpdateConfig(Config{BotToken: "new", Subscriptions: []Subscription{{ID: "ops", Enabled: true, ChatID: "chat", Template: "{{"}}})
	if err == nil {
		t.Fatal("期望模板校验失败")
	}
	n.configMu.RLock()
	defer n.configMu.RUnlock()
	if n.config.botToken != "token" {
		t.Fatalf("无效热更新覆盖了旧配置: %q", n.config.botToken)
	}
}
