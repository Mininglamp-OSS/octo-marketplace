package db

import (
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	pluginsvc "github.com/Mininglamp-OSS/octo-marketplace/internal/service/plugin"
)

func TestManagedDraftCanOmitVersionButPublicationCannot(t *testing.T) {
	for _, typ := range []model.PluginType{model.PluginTypeSkill, model.PluginTypeExpert, model.PluginTypeExpertTeam} {
		t.Run(string(typ), func(t *testing.T) {
			database := reviewDB(t)
			ctx, owner := context.Background(), managedOwner()
			repo := pluginrepo.New(database)
			svc := pluginsvc.New(repo).WithVersionManagement(true)
			req := managedWrite("", "draft", model.PluginVisibilityPrivate)
			if typ == model.PluginTypeSkill {
				req = managedSkillWrite("", "Draft Skill", model.PluginVisibilityPrivate)
			} else if typ == model.PluginTypeExpertTeam {
				req = compatibilitySourceWrite("expert", "")
			}
			created, err := svc.Create(ctx, owner, req)
			if err != nil {
				t.Fatal(err)
			}
			if created.Plugin.CurrentVersion == nil || *created.Plugin.CurrentVersion != "1.0.0" {
				t.Fatal("versionless draft create did not default to 1.0.0")
			}
			id := created.Plugin.ID
			req.Version = "2.4.0"
			if _, err = svc.Update(ctx, owner, id, req); err != nil {
				t.Fatal(err)
			}
			req.Version = ""
			draft, err := svc.Update(ctx, owner, id, req)
			if err != nil {
				t.Fatal(err)
			}
			if draft.Plugin.CurrentVersion == nil || *draft.Plugin.CurrentVersion != "2.4.0" {
				t.Fatal("versionless draft update reset the saved label")
			}
			before, err := repo.GetVersionState(ctx, tenantScope(), id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id}); err == nil {
				t.Fatal("publication without explicit version succeeded")
			} else {
				var field *pluginsvc.ReviewFieldError
				if !errors.As(err, &field) || field.Field != "version" || field.Reason != "required" {
					t.Fatalf("publication: %v", err)
				}
			}
			after, err := repo.GetVersionState(ctx, tenantScope(), id)
			if err != nil {
				t.Fatal(err)
			}
			if after.PublishedVersionID != nil || before.CurrentVersionID == nil || after.CurrentVersionID == nil || *before.CurrentVersionID != *after.CurrentVersionID {
				t.Fatal("refused publication changed the draft or release pointer")
			}
			if _, err = svc.Publish(ctx, owner, pluginsvc.PublishParams{PluginID: id, Version: "2.4.0"}); err != nil {
				t.Fatal(err)
			}
			released, err := repo.GetVersionState(ctx, tenantScope(), id)
			if err != nil || released.PublishedVersionID == nil || *released.PublishedVersionID != *after.CurrentVersionID {
				t.Fatalf("explicit publication did not release the saved draft: %+v %v", released, err)
			}
		})
	}
}
