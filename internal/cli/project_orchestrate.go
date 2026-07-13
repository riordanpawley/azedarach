package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func resolveCLIOrchestrationScope(explicitRoot string) (domain.OrchestrationScope, error) {
	return domain.ResolveOrchestrationScope(explicitRoot, os.Getenv("AZEDARACH_ISSUE_ID"))
}

func applyOrchestrationProjectOverride(deps *Dependencies, project string) func() {
	project = normalizeIssueProject(project)
	if project == "" {
		return func() {}
	}
	if _, ok := findSessionProjectCandidate(deps, project); ok {
		return applyExplicitSessionProjectOverride(deps, project)
	}
	return applyIssueProjectOverride(deps, project)
}

func orchestrationSnapshot(ctx context.Context, deps *Dependencies, scope domain.OrchestrationScope, limit int, cursor int64) (protocol.OrchestrationSnapshot, error) {
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return protocol.OrchestrationSnapshot{}, err
	}
	return deps.DaemonClient.OrchestrationSnapshot(ctx, protocol.OrchestrationSnapshotRequest{
		Scope: scope, ActorID: orchestrateOwnerID(), Limit: limit, ObservedCursor: cursor,
	})
}

func projectOrchestrateStatusCommand(deps *Dependencies, opts OrchestrateStatusOptions, scope domain.OrchestrationScope) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	snapshot, err := orchestrationSnapshot(ctx, deps, scope, opts.Limit, opts.SinceSeq)
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(snapshot)
	}
	printProjectOrchestrationSnapshot(snapshot)
	return nil
}

func printProjectOrchestrationSnapshot(s protocol.OrchestrationSnapshot) {
	fmt.Printf("Orchestration scope: %s\n", s.Scope.Kind)
	if s.Lifecycle != "" {
		fmt.Printf("State: %s\n", s.Lifecycle)
	} else {
		fmt.Println("State: not-started")
	}
	fmt.Printf("Capacity: workers=%d/%d runnable=%d wave-limit=%d\n", s.Capacity.TotalCountingCapacityCount, s.Constraints.AgentCapacity, len(s.Runnable), s.Constraints.StartLimit)
	fmt.Printf("Queues: ready=%d review=%d waiting-human=%d owned-elsewhere=%d\n", len(s.Runnable), len(s.Reviews), len(s.Interactions), len(s.OwnershipConflicts))
	fmt.Printf("Board health: healthy=%t open=%d inspected=%d/%d\n", s.Health.Healthy, s.Health.OpenIssueCount, s.Health.InspectedCount, s.Health.InspectLimit)
	for _, diagnostic := range s.Health.Diagnostics {
		fmt.Printf("- health: %s\n", diagnostic)
	}
	if len(s.Candidates) > 0 {
		fmt.Println("Explain:")
		for _, candidate := range s.Candidates {
			fmt.Printf("- %s: %s (%s)\n", candidate.IssueID, candidate.Classification, candidate.Reason)
		}
	}
	if s.Cursor > 0 {
		fmt.Printf("Last action cursor: %d\n", s.Cursor)
	}
	if len(s.RecentEvents) > 0 {
		last := s.RecentEvents[len(s.RecentEvents)-1]
		fmt.Printf("Last action: %s issue=%s\n", last.Type, last.IssueID)
	}
}

func projectOrchestrateStartCommand(deps *Dependencies, opts OrchestrateStartOptions, scope domain.OrchestrationScope) error {
	ctx, cancel := context.WithTimeout(context.Background(), sessionStartCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	request := protocol.OrchestrationIntentRequest{
		Scope: scope, Kind: protocol.OrchestrationIntentStart,
		IntentKey: fmt.Sprintf("cli:%d", time.Now().UTC().UnixNano()), ActorID: orchestrateOwnerID(),
		IssueIDs: opts.IssueIDs, Limit: opts.Limit, RepoDir: deps.RepoDir,
		BaseBranch: opts.BaseBranchOverride, OverrideBoardHealth: opts.OverrideBoardHealth,
	}
	result, err := deps.DaemonClient.ApplyOrchestrationIntent(ctx, request)
	if err != nil {
		return err
	}
	if opts.JSON {
		if err := printJSON(result); err != nil {
			return err
		}
	} else {
		fmt.Printf("Project orchestration start: requested=%d started=%d limit=%d\n", len(result.Requested), len(result.Started), opts.Limit)
		for _, id := range result.Started {
			fmt.Printf("- started: %s\n", id)
		}
		for _, id := range sortedKeys(result.Skipped) {
			fmt.Printf("- skipped %s: %s\n", id, result.Skipped[id])
		}
		for _, id := range sortedKeys(result.Failed) {
			reason := result.Failed[id]
			fmt.Printf("- failed %s: %s\n", id, reason)
		}
	}
	if len(result.Failed) > 0 {
		return fmt.Errorf("orchestrate start completed with failures")
	}
	return nil
}

func projectOrchestrateWatchCommand(deps *Dependencies, opts OrchestrateWatchOptions, scope domain.OrchestrationScope) error {
	watchCtx, stop := newWatchCommandContext("orchestrate watch")
	defer stop()
	var previous string
	cursor := opts.SinceSeq
	for {
		snapshot, err := watchDaemonCommandContext(watchCtx, deps, func(segmentCtx context.Context) (protocol.OrchestrationSnapshot, error) {
			ctx, cancel := context.WithTimeout(segmentCtx, daemonCommandTimeout)
			defer cancel()
			return orchestrationSnapshot(ctx, deps, scope, 0, cursor)
		})
		if err != nil {
			if isWatchContextDone(watchCtx, err) {
				return nil
			}
			return err
		}
		key, err := projectOrchestrationWatchKey(snapshot)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return fmt.Errorf("encode orchestration watch snapshot: %w", err)
		}
		if opts.Once || key != previous {
			if opts.JSONL {
				fmt.Println(string(encoded))
			} else {
				printProjectOrchestrationSnapshot(snapshot)
			}
			previous = key
		}
		if snapshot.Cursor > cursor {
			cursor = snapshot.Cursor
		}
		if opts.Once {
			return nil
		}
		if err := sleepWatchPoll(watchCtx, opts.PollInterval); err != nil {
			return nil
		}
	}
}

func projectOrchestrationWatchKey(snapshot protocol.OrchestrationSnapshot) (string, error) {
	snapshot.GeneratedAt = time.Time{}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("encode stable orchestration watch snapshot: %w", err)
	}
	return string(encoded), nil
}

func projectOrchestrateCompleteCheckCommand(deps *Dependencies, opts OrchestrateCompleteCheckOptions, scope domain.OrchestrationScope) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	snapshot, err := orchestrationSnapshot(ctx, deps, scope, 0, 0)
	if err != nil {
		return err
	}
	result := snapshot.Completion
	if opts.JSON {
		if err := printJSON(result); err != nil {
			return err
		}
	} else if result.Pass {
		fmt.Println("Project orchestration is complete.")
	} else {
		fmt.Println("Project orchestration is NOT complete:")
		for _, reason := range result.Reasons {
			fmt.Printf("- %s\n", reason)
		}
	}
	if !result.Pass {
		return fmt.Errorf("orchestration completion gate failed")
	}
	return nil
}
