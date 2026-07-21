package health

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/xsaveopt/recodarr/internal/agent"
	"github.com/xsaveopt/recodarr/internal/arr"
	"github.com/xsaveopt/recodarr/internal/handbrake"
	"github.com/xsaveopt/recodarr/internal/notify"
	"github.com/xsaveopt/recodarr/internal/qbit"
	"github.com/xsaveopt/recodarr/internal/store"
)

const (
	cacheTTL = 30 * time.Second

	probeTimeout = 6 * time.Second

	tickInterval = 2 * time.Minute
)

type Level string

const (
	LevelError Level = "error"
	LevelWarn  Level = "warn"
)

type Issue struct {
	Level  Level  `json:"level"`
	Source string `json:"source"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

type Snapshot struct {
	OK        bool      `json:"ok"`
	Issues    []Issue   `json:"issues"`
	CheckedAt time.Time `json:"checkedAt"`
}

type Checker struct {
	st *store.Store

	mu    sync.Mutex
	last  Snapshot
	lastT time.Time

	prev map[string]Issue
}

func New(st *store.Store) *Checker { return &Checker{st: st, prev: map[string]Issue{}} }

func (c *Checker) Run(ctx context.Context) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()

	c.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.tick(ctx)
		}
	}
}

func (c *Checker) tick(ctx context.Context) {
	snap := c.probe(ctx)

	c.mu.Lock()
	c.last = snap
	c.lastT = time.Now()
	prev := c.prev
	curr := indexIssues(snap.Issues)
	c.prev = curr
	c.mu.Unlock()

	c.diffAndNotify(ctx, prev, curr)
}

func (c *Checker) Snapshot(ctx context.Context) Snapshot {
	c.mu.Lock()
	if time.Since(c.lastT) < cacheTTL && !c.lastT.IsZero() {
		s := c.last
		c.mu.Unlock()
		return s
	}
	c.mu.Unlock()

	snap := c.probe(ctx)

	c.mu.Lock()
	c.last = snap
	c.lastT = time.Now()
	c.mu.Unlock()
	return snap
}

func (c *Checker) diffAndNotify(ctx context.Context, prev, curr map[string]Issue) {
	for k, iss := range curr {
		if _, existed := prev[k]; !existed {
			notify.SendHealth(ctx, c.st, iss.Source, iss.Title, iss.Detail, string(iss.Level), "opened")
			slog.Info("health issue opened", "source", iss.Source, "title", iss.Title)
		}
	}
	for k, iss := range prev {
		if _, stillThere := curr[k]; !stillThere {
			notify.SendHealth(ctx, c.st, iss.Source, iss.Title, "", string(iss.Level), "resolved")
			slog.Info("health issue resolved", "source", iss.Source, "title", iss.Title)
		}
	}
}

func issueKey(i Issue) string { return i.Source + "|" + i.Title }

func indexIssues(issues []Issue) map[string]Issue {
	out := make(map[string]Issue, len(issues))
	for _, i := range issues {
		out[issueKey(i)] = i
	}
	return out
}

func (c *Checker) probe(ctx context.Context) Snapshot {
	issues := []Issue{}

	if strings.HasPrefix(handbrake.VersionString(), "(HandBrakeCLI not found)") {
		issues = append(issues, Issue{
			Level:  LevelError,
			Source: "handbrake",
			Title:  "HandBrakeCLI not installed",
			Detail: "Encodes will fail until HandBrakeCLI is on PATH. The official Docker image bundles it; if you're running outside Docker, install it.",
		})
	}

	if mappings, err := c.st.ListTagMappings(ctx); err == nil && len(mappings) == 0 {
		issues = append(issues, Issue{
			Level:  LevelWarn,
			Source: "config",
			Title:  "No tag mappings configured",
			Detail: "Recodarr only queues library items whose tag has a mapping. Add a mapping in Settings → Mappings to start encoding.",
		})
	}

	arrRows, _ := c.st.ListArrInstances(ctx)
	qbitRows, _ := c.st.ListQbitInstances(ctx)

	type probeResult struct{ issue *Issue }
	results := make(chan probeResult, len(arrRows)+len(qbitRows)+1)
	var wg sync.WaitGroup

	for _, row := range arrRows {
		if !row.Enabled {
			continue
		}
		row := row
		wg.Add(1)
		go func() {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			if err := arr.New(arr.Kind(row.Kind), row.URL, row.APIKey).Ping(pctx); err != nil {
				results <- probeResult{&Issue{
					Level:  LevelError,
					Source: fmt.Sprintf("arr:%d", row.ID),
					Title:  fmt.Sprintf("%s (%s): unreachable", row.Name, row.Kind),
					Detail: err.Error(),
				}}
			} else {
				results <- probeResult{}
			}
		}()
	}

	for _, row := range qbitRows {
		row := row
		wg.Add(1)
		go func() {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			client, err := qbit.New(row.URL, row.Username, row.Password)
			if err != nil {
				results <- probeResult{&Issue{
					Level:  LevelError,
					Source: fmt.Sprintf("qbit:%d", row.ID),
					Title:  fmt.Sprintf("qBittorrent (%s): bad config", row.Name),
					Detail: err.Error(),
				}}
				return
			}
			if err := client.Login(pctx); err != nil {
				results <- probeResult{&Issue{
					Level:  LevelError,
					Source: fmt.Sprintf("qbit:%d", row.ID),
					Title:  fmt.Sprintf("qBittorrent (%s): login failed", row.Name),
					Detail: err.Error(),
				}}
				return
			}
			results <- probeResult{}
		}()
	}

	wg.Wait()
	close(results)
	for r := range results {
		if r.issue != nil {
			issues = append(issues, *r.issue)
		}
	}

	cfg, _ := c.st.LoadAppSettings(ctx)
	if cfg.AgentEnabled && cfg.AgentURL != "" && cfg.AgentToken != "" {
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		client := agent.NewClient(cfg.AgentURL, cfg.AgentToken)
		if _, err := client.Ping(pctx); err != nil {
			issues = append(issues, Issue{
				Level:  LevelError,
				Source: "agent",
				Title:  fmt.Sprintf("Remote agent %s: unreachable", cfg.AgentURL),
				Detail: err.Error(),
			})
		}
		cancel()
	} else if cfg.AgentEnabled {
		issues = append(issues, Issue{
			Level:  LevelWarn,
			Source: "agent",
			Title:  "Remote agent enabled but URL or token missing",
			Detail: "Add both in Settings → Remote Agent, or disable the toggle.",
		})
	}

	if len(qbitRows) == 0 {
		stats, err := c.st.GetJobStats(ctx)
		if err == nil && stats.WaitingForSeed > 0 {
			issues = append(issues, Issue{
				Level:  LevelWarn,
				Source: "qbit",
				Title:  "No qBittorrent configured",
				Detail: fmt.Sprintf("%d job(s) are stuck in waiting_for_seed because there's no qBit to poll. Add one in Settings → qBittorrent, or those jobs will never run.", stats.WaitingForSeed),
			})
		}
	}

	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Level != issues[j].Level {
			return issues[i].Level == LevelError
		}
		return issues[i].Source < issues[j].Source
	})

	return Snapshot{
		OK:        len(issues) == 0,
		Issues:    issues,
		CheckedAt: time.Now(),
	}
}
