package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	pluginrepo "github.com/Mininglamp-OSS/octo-marketplace/internal/repository/plugin"
	"github.com/go-sql-driver/mysql"
)

// Pause both transactions after their application name checks and before the
// statement that makes the name globally visible. No scheduler timing is used.
type globalNameBarrierConnector struct {
	driver.Connector
	statement string
	arrived   chan<- struct{}
	resume    <-chan struct{}
}

func (c globalNameBarrierConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return globalNameBarrierConn{Conn: conn, gate: c}, nil
}

type globalNameBarrierConn struct {
	driver.Conn
	gate globalNameBarrierConnector
}

func (c globalNameBarrierConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.gate.statement != "" && strings.HasPrefix(query, c.gate.statement) {
		select {
		case c.gate.arrived <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-c.gate.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func TestGlobalNamesConcurrentAcrossSpaces(t *testing.T) {
	for _, kind := range []string{"skill", "expert", "expert_team"} {
		for _, operation := range []string{"create", "publish"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				database := reviewDB(t)
				var databaseName string
				if err := database.QueryRow(`SELECT DATABASE()`).Scan(&databaseName); err != nil {
					t.Fatal(err)
				}
				cfg, err := mysql.ParseDSN(testDSN(t))
				if err != nil {
					t.Fatal(err)
				}
				cfg.DBName = databaseName
				if cfg.Params == nil {
					cfg.Params = map[string]string{}
				}
				// BeginTx(nil) inherits the session isolation. Name integrity must not
				// depend on REPEATABLE READ gap locks or a particular query plan.
				cfg.Params["transaction_isolation"] = "'READ-COMMITTED'"
				connector, err := mysql.NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				statement := "INSERT INTO plugins ("
				if operation == "publish" {
					// Publishing already locks each resource. The legacy-name scan
					// may deadlock while visiting the other draft, so do not hold
					// either transaction at an artificial post-check barrier.
					statement = ""
				}
				arrived, resume := make(chan struct{}, 2), make(chan struct{})
				var release sync.Once
				unblock := func() { release.Do(func() { close(resume) }) }
				defer unblock()
				guarded := sql.OpenDB(globalNameBarrierConnector{Connector: connector, statement: statement, arrived: arrived, resume: resume})
				defer guarded.Close()
				repo := pluginrepo.New(guarded).WithVersionManagement(true)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				mutations := []pluginrepo.Mutation{
					uploadNameMutation("global-a", "Official", kind, "space-a"),
					uploadNameMutation("global-b", " ＯＦＦＩＣＩＡＬ ", kind, "space-b"),
				}
				versions := make([]string, 2)
				for i := range mutations {
					mutations[i].Plugin.Visibility = "system"
					if i == 1 {
						mutations[i].Plugin.Visibility = "public"
					}
					if operation == "create" {
						mutations[i].Plugin.ListingState = "published"
						continue
					}
					label := "1.0.0"
					mutations[i].Plugin.CurrentVersion = &label
					mutations[i].SnapshotVersion = true
					result, err := pluginrepo.New(database).WithVersionManagement(true).Create(ctx, pluginrepo.Scope{Admin: true}, mutations[i])
					if err != nil {
						t.Fatal(err)
					}
					versions[i] = result.NewVersionID
				}
				results := make(chan error, 2)
				start := make(chan struct{})
				var writers sync.WaitGroup
				defer func() { cancel(); writers.Wait() }()
				for i := range mutations {
					writers.Add(1)
					go func(i int) {
						defer writers.Done()
						<-start
						var err error
						if operation == "create" {
							_, err = repo.Create(ctx, pluginrepo.Scope{Admin: true}, mutations[i])
						} else {
							_, err = repo.PublishManaged(ctx, pluginrepo.Scope{Admin: true}, pluginrepo.PublishParams{PluginID: mutations[i].Plugin.ID, ExpectedVersionID: versions[i], OperatorID: "owner"})
						}
						results <- err
					}(i)
				}
				close(start)
				if operation == "create" {
					for i := 0; i < 2; i++ {
						select {
						case <-arrived:
						case err := <-results:
							t.Fatalf("write returned before the post-check barrier: %v", err)
						case <-ctx.Done():
							t.Fatal("transactions did not both reach the post-check barrier")
						}
					}
				}
				unblock()
				success := 0
				for i := 0; i < 2; i++ {
					err := <-results
					if err == nil {
						success++
					} else if !errors.Is(err, pluginrepo.ErrNameTaken) && !(operation == "publish" && errors.Is(err, pluginrepo.ErrDeadlock)) {
						t.Errorf("collision must return ErrNameTaken: %v", err)
					}
				}
				if success != 1 {
					t.Errorf("%d concurrent global names committed, want one", success)
				}
				var listed int
				if err := database.QueryRow(`SELECT COUNT(*) FROM plugins WHERE listing_state='published'`).Scan(&listed); err != nil || listed != 1 {
					t.Errorf("published=%d err=%v, want one", listed, err)
				}
				if operation == "publish" {
					var releases int
					if err := database.QueryRow(`SELECT COUNT(*) FROM plugin_version_releases`).Scan(&releases); err != nil || releases != 1 {
						t.Errorf("releases=%d err=%v, want one; failed publication must roll back", releases, err)
					}
					var loser string
					if err := database.QueryRow(`SELECT plugin_id FROM plugins WHERE listing_state='draft'`).Scan(&loser); err != nil {
						t.Fatal(err)
					}
					index := 0
					if loser == mutations[1].Plugin.ID {
						index = 1
					}
					if _, err := repo.PublishManaged(ctx, pluginrepo.Scope{Admin: true}, pluginrepo.PublishParams{PluginID: loser, ExpectedVersionID: versions[index], OperatorID: "owner"}); !errors.Is(err, pluginrepo.ErrNameTaken) {
						t.Errorf("publication retry must report the occupied global name: %v", err)
					}
				}
			})
		}
	}
}
