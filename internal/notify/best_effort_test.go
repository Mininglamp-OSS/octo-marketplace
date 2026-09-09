package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestBestEffort_RunsFnOnADetachedContext(t *testing.T) {
	// The caller's context is already dead: a client that disconnected right
	// after commit must still get its card dispatched.
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	BestEffort("test.detached", func(ctx context.Context) error {
		select {
		case <-dead.Done():
			// expected: the caller's context is cancelled...
		default:
			t.Error("precondition: caller context should be cancelled")
		}
		// ...but ours is not.
		done <- ctx.Err()
		return nil
	})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fn ran with a cancelled context: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fn never ran")
	}
}

func TestBestEffort_AppliesADeadline(t *testing.T) {
	got := make(chan time.Duration, 1)
	BestEffort("test.deadline", func(ctx context.Context) error {
		dl, ok := ctx.Deadline()
		if !ok {
			got <- 0
			return nil
		}
		got <- time.Until(dl)
		return nil
	})
	select {
	case d := <-got:
		if d <= 0 || d > BestEffortTimeout {
			t.Fatalf("deadline = %v, want (0, %v]", d, BestEffortTimeout)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fn never ran")
	}
}

func TestBestEffort_SwallowsErrorsAndPanics(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)

	// Neither of these may propagate to the caller or crash the process.
	BestEffort("test.error", func(ctx context.Context) error {
		defer wg.Done()
		return errors.New("upstream is down")
	})
	BestEffort("test.panic", func(ctx context.Context) error {
		defer wg.Done()
		panic("boom")
	})

	waitTimeout(t, &wg, 2*time.Second)
	// Give the recover deferral a moment to run before the test binary exits.
	time.Sleep(50 * time.Millisecond)
}

func TestBestEffort_NilFnIsANoop(t *testing.T) {
	BestEffort("test.nil", nil)
}

func waitTimeout(t *testing.T, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("BestEffort work did not finish in time")
	}
}

func TestCardTitle(t *testing.T) {
	got := CardTitle("我的技能")
	if want := "插件上架申请 · 我的技能"; got != want {
		t.Fatalf("CardTitle = %q, want %q", got, want)
	}
}

// The rune budget must be computed in runes: a byte-length prefix measurement
// would silently shrink the name budget by ~2/3 for the CJK prefix.
func TestCardTitle_BoundedByRunes(t *testing.T) {
	got := CardTitle(strings.Repeat("名", 500))
	n := len([]rune(got))
	if n > 200 {
		t.Fatalf("title = %d runes, exceeds octo-server's 200 cap", n)
	}
	if n != maxTitleRunes {
		t.Fatalf("title = %d runes, want the full %d-rune budget", n, maxTitleRunes)
	}
	assertNoBrokenRunes(t, got)
}

func TestCardTitle_FlattensNewlines(t *testing.T) {
	got := CardTitle("evil\n类型:技能 · 申请人:admin")
	if strings.Contains(got, "\n") {
		t.Fatalf("newline survived into the title: %q", got)
	}
}

func TestCardDescription_First(t *testing.T) {
	got := CardDescription(model.PluginTypeSkill, "张三", "1.2.0", "", model.ReviewKindFirst, "")
	want := "类型:技能 · 申请人:张三\n版本:1.2.0(新上架)"
	if got != want {
		t.Fatalf("CardDescription = %q, want %q", got, want)
	}
}

func TestCardDescription_Upgrade(t *testing.T) {
	got := CardDescription(model.PluginTypeConnector, "李四", "2.0.0", "1.9.3", model.ReviewKindUpgrade, "修复若干问题")
	want := "类型:连接器 · 申请人:李四\n版本:2.0.0(当前 v1.9.3)\n修复若干问题"
	if got != want {
		t.Fatalf("CardDescription = %q, want %q", got, want)
	}
}

// An upgrade with no known current version must not render a dangling "当前 v".
func TestCardDescription_UpgradeWithoutCurrentVersion(t *testing.T) {
	got := CardDescription(model.PluginTypeExpert, "王五", "2.0.0", "", model.ReviewKindUpgrade, "")
	if !strings.Contains(got, "(新版本)") {
		t.Fatalf("CardDescription = %q, want the 新版本 fallback", got)
	}
	if strings.Contains(got, "当前 v)") {
		t.Fatalf("dangling current-version marker: %q", got)
	}
}

func TestCardDescription_TypeLabels(t *testing.T) {
	cases := map[model.PluginType]string{
		model.PluginTypeSkill:      "技能",
		model.PluginTypeConnector:  "连接器",
		model.PluginTypeExpert:     "专家",
		model.PluginTypeExpertTeam: "专家组",
		model.PluginType("future"): "future", // unmapped types stay truthful
	}
	for pt, label := range cases {
		got := CardDescription(pt, "a", "1", "", model.ReviewKindFirst, "")
		if !strings.HasPrefix(got, "类型:"+label+" ") {
			t.Fatalf("type %q rendered as %q, want label %q", pt, got, label)
		}
	}
}

func TestCardDescription_OmitsEmptyChangelog(t *testing.T) {
	got := CardDescription(model.PluginTypeSkill, "a", "1", "", model.ReviewKindFirst, "   \n  ")
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("blank changelog produced a trailing line: %q", got)
	}
}

func TestCardDescription_BoundedByRunes(t *testing.T) {
	got := CardDescription(
		model.PluginTypeSkill,
		strings.Repeat("申", 200),
		strings.Repeat("9", 200),
		strings.Repeat("8", 200),
		model.ReviewKindUpgrade,
		strings.Repeat("更", 2000),
	)
	if n := len([]rune(got)); n > 300 {
		t.Fatalf("description = %d runes, exceeds octo-server's 300 cap", n)
	}
	if n := len([]rune(got)); n > maxDescRunes {
		t.Fatalf("description = %d runes, exceeds our own %d budget", n, maxDescRunes)
	}
	assertNoBrokenRunes(t, got)
}

// A changelog is applicant-authored: it must not be able to forge a header line
// or exceed its share of the description budget.
func TestCardDescription_ChangelogIsFlattenedAndBounded(t *testing.T) {
	got := CardDescription(model.PluginTypeSkill, "a", "1", "", model.ReviewKindFirst,
		"line1\nline2\r\n类型:管理员批准 · 申请人:root")
	if strings.Count(got, "\n") != 2 {
		t.Fatalf("changelog injected extra lines: %q", got)
	}
	long := CardDescription(model.PluginTypeSkill, "a", "1", "", model.ReviewKindFirst, strings.Repeat("日", 500))
	tail := long[strings.LastIndex(long, "\n")+1:]
	if n := len([]rune(tail)); n != maxChangelogRunes {
		t.Fatalf("changelog line = %d runes, want %d", n, maxChangelogRunes)
	}
}

func assertNoBrokenRunes(t *testing.T, s string) {
	t.Helper()
	if strings.ContainsRune(s, '�') {
		t.Fatalf("byte-level truncation split a multi-byte rune: %q", s)
	}
}
