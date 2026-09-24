package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type capabilityCountingTracker struct {
	fakeTracker
	calls int
}

func (f *capabilityCountingTracker) TrackInstall(ctx context.Context, resourceType, resourceID string) error {
	f.calls++
	return f.fakeTracker.TrackInstall(ctx, resourceType, resourceID)
}

func TestCapabilityCountsEverySuccessfulCall(t *testing.T) {
	for _, pluginID := range []string{"expert-1", "team-1"} {
		for _, metadata := range []struct {
			name          string
			known, replay bool
		}{
			{name: "unknown"},
			{name: "fresh", known: true},
			{name: "replay", known: true, replay: true},
		} {
			t.Run(pluginID+"/"+metadata.name, func(t *testing.T) {
				svc, _, installer := capabilityFixture()
				installer.replayKnown, installer.replay = metadata.known, metadata.replay
				tracker := &capabilityCountingTracker{}
				svc.WithMetrics(tracker)
				for i := 1; i <= 2; i++ {
					out, err := svc.CreateInstallation(context.Background(), testCaller, pluginID, installationParams())
					if err != nil || out.Replayed != metadata.replay || tracker.calls != i || tracker.typ != "plugin" || tracker.id != pluginID {
						t.Fatalf("out=%+v err=%v tracker=%+v", out, err, tracker)
					}
				}
				installer.err = errors.New("uncertain outcome")
				if _, err := svc.CreateInstallation(context.Background(), testCaller, pluginID, installationParams()); err == nil || tracker.calls != 2 {
					t.Fatalf("failed invocation changed metrics: err=%v calls=%d", err, tracker.calls)
				}
			})
		}
	}
}

func TestCapabilityBotRejectedBeforeFleetAndMetrics(t *testing.T) {
	svc, _, installer := capabilityFixture()
	tracker := &capabilityCountingTracker{}
	svc.WithMetrics(tracker)
	bot := testCaller
	bot.BotUID = "bot-1"
	for _, pluginID := range []string{"expert-1", "team-1", "missing"} {
		_, err := svc.CreateInstallation(context.Background(), bot, pluginID, installationParams())
		if !errors.Is(err, ErrDependencyHidden) || installer.calls != 0 || tracker.calls != 0 {
			t.Fatalf("err=%v Fleet=%d metrics=%d", err, installer.calls, tracker.calls)
		}
	}
}

func TestCapabilityExplicitStdioTypeAndHeadersRemainUnsupported(t *testing.T) {
	for _, pluginID := range []string{"expert-1", "team-1"} {
		for _, fields := range []string{`"type":"stdio"`, `"headers":{"Authorization":"placeholder"}`} {
			svc, store, installer := capabilityFixture()
			store.plugins["expert-1"].Package = packageWith(rawAtt("AGENTS.md", "work"), rawAtt("mcp.json", `{"mcpServers":{"test":{"command":"npx",`+fields+`}}}`))
			_, err := svc.CreateInstallation(context.Background(), testCaller, pluginID, installationParams())
			var invalid *InstallationValidationError
			if !errors.As(err, &invalid) || invalid.Reason != "invalid_stdio" || installer.calls != 0 {
				t.Fatalf("plugin=%s err=%v calls=%d", pluginID, err, installer.calls)
			}
		}
	}
}

func TestCapabilityLegacySkillReplacementChargesOnlyFinalContent(t *testing.T) {
	for _, size := range []int{1200, 2700, 2701} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			svc, store, _ := capabilityFixture()
			zipKey, mdKey := "plugins/space-a/attachments/legacy.zip", "plugins/space-a/attachments/entry.md"
			content := strings.Repeat("x", size)
			refContent := strings.Repeat("r", 300)
			data := zipWith(t, map[string][]byte{"pkg/SKILL.md": []byte(strings.Repeat("s", 2000)), "pkg/ref.md": []byte(refContent)})
			store.plugins["skill-1"].Package = packageWith(rawAtt("skill/ref.json", fmt.Sprintf(`{"zip_object_key":%q,"object_key":%q}`, zipKey, mdKey)))
			svc.storage = &importStorage{objects: map[string][]byte{zipKey: data, mdKey: []byte(content)}}
			builder := capabilityBuilder{service: svc, remaining: 3000}
			files, err := builder.skillFiles(context.Background(), store.plugins["skill-1"])
			if size > 2700 {
				if !errors.Is(err, ErrTooLarge) {
					t.Fatalf("oversize replacement accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 2 || files["SKILL.md"] != content || files["ref.md"] != refContent || builder.remaining != int64(2700-size) || builder.totalFiles != 1 {
				t.Fatalf("replacement accounting failed: remaining=%d files=%d", builder.remaining, builder.totalFiles)
			}
		})
	}
}
