package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/riordanpawley/azedarach/internal/cli"
	clitext "github.com/riordanpawley/azedarach/internal/cli/text"
)

func maybePrintCommandHelp(args []string) bool {
	path, ok := helpPath(args)
	if !ok {
		return false
	}
	return printHelpForPath(path)
}

func helpPath(args []string) ([]string, bool) {
	if len(args) == 0 {
		return nil, false
	}
	if strings.EqualFold(args[0], "help") {
		if len(args) == 1 || len(args) == 2 && isHelpArg(args[1]) {
			return []string{}, true
		}
		return args[1:], true
	}
	if len(args) == 1 && isHelpArg(args[0]) {
		return []string{}, true
	}
	if isHelpArg(args[len(args)-1]) {
		return args[:len(args)-1], true
	}
	return nil, false
}

func printHelpForPath(path []string) bool {
	legacyIssuePath := len(path) > 0 && path[0] == "issue"
	// Ticket is the canonical command family. Internally the mature command
	// implementation still uses its legacy issue path while the compatibility
	// window remains open, so normalize ticket subcommand help through it.
	if len(path) > 1 && path[0] == "ticket" {
		path = append([]string{"issue"}, path[1:]...)
	}
	key := strings.Join(path, " ")
	switch key {
	case "":
		printRootUsage()
	case "help":
		printRootUsage()
	case "version", "-v", "--version":
		fmt.Println("Usage: az version")
	case "session":
		printSessionUsage()
	case "session start", "start":
		printSessionCommandUsage("start", key == "session start")
	case "session attach", "attach":
		printSessionCommandUsage("attach", key == "session attach")
	case "session stop", "stop":
		printSessionCommandUsage("stop", key == "session stop")
	case "session kill", "kill":
		printSessionCommandUsage("kill", key == "session kill")
	case "session status", "status":
		printSessionCommandUsage("status", key == "session status")
	case "session capture":
		printSessionCommandUsage("capture", true)
	case "session restart-all":
		printSessionCommandUsage("restart-all", true)
	case "session resolve-conflict":
		printSessionCommandUsage("resolve-conflict", true)
	case "branch":
		fmt.Println("Usage: az branch <merge|agent-merge> [arguments]")
	case "branch merge", "branch merge-to-base":
		fmt.Println("usage: az branch merge [--project <project-id>] --source <issue-id> --target base|<issue-id>")
	case "branch agent-merge":
		fmt.Println("usage: az branch agent-merge [--project <project-id>] <issue-id> [--target base|<issue-id>]")
	case "board":
		printBoardUsage()
	case "board view":
		printBoardViewUsage()
	case "board view list":
		printBoardViewCommandUsage("list")
	case "board view get":
		printBoardViewCommandUsage("get")
	case "board view select":
		printBoardViewCommandUsage("select")
	case "board view create":
		printBoardViewCommandUsage("create")
	case "board view update":
		printBoardViewCommandUsage("update")
	case "board view delete":
		printBoardViewCommandUsage("delete")
	case "board view explain":
		printBoardViewCommandUsage("explain")
	case "view":
		printBoardViewUsage()
	case "view list", "view get", "view select", "view create", "view update", "view delete", "view explain":
		printBoardViewCommandUsage(path[1])
	case "worktree":
		printWorktreeUsage()
	case "worktree create":
		fmt.Println("Usage: az worktree create [--project <project-id>] [--base <branch>] [--json] <issue-id>")
	case "worktree delete", "worktree remove":
		fmt.Println("Usage: az worktree delete [--project <project-id>] [--force] [--delete-branch] [--json] <issue-id>")
	case "operation":
		fmt.Println("Usage: az operation <get|list|queue|logs|cancel> [arguments]")
	case "operation get":
		fmt.Println("Usage: az operation get --id <operation-id> [--wait]")
	case "operation list":
		fmt.Println("Usage: az operation list [--issue-id <issue-id>] [--state <state>] [--kind <kind>] [--limit N]")
	case "operation queue":
		fmt.Println("Usage: az operation queue [--issue <issue-id>] [--state <state>] [--kind <kind>] [--limit N] [--tree] [--json]")
	case "operation logs":
		fmt.Println("Usage: az operation logs --id <operation-id>")
	case "operation cancel":
		fmt.Println("Usage: az operation cancel --id <operation-id> [--reason <reason>]")
	case "export":
		fmt.Println("Usage: az export --format json [--out <path>]")
	case "log":
		fmt.Println("Usage: az log [--source daemon,tui,cli] [--lines N] [--follow|--no-follow] [daemon|tui|cli ...]")
	case "config":
		fmt.Println("Usage: az config set <key> <value> [--project-dir <dir>]")
	case "config set":
		fmt.Println("Usage: az config set <key> <value> [--project-dir <dir>]")
	case "spec":
		cli.PrintSpecUsage()
	case "spec req":
		cli.PrintSpecReqUsage()
	case "spec req list":
		fmt.Println("Usage: az spec req list [--json] [--issue <issue-id>] [--status <open|accepted|superseded>] [--query <text>] [--match <all|any>] [--limit <n>] [--id <req-id> ...] [--ids a,b,c]")
	case "spec req get":
		fmt.Println("Usage: az spec req get --id <req-id> [--json]")
	case "spec req create":
		fmt.Println("Usage: az spec req create --id <req-id> --title <text> [--description <text>] [--issue <issue-id>] [--json]")
	case "spec req update":
		fmt.Println("Usage: az spec req update --id <req-id> [--title <text>] [--description <text>] [--status <open|accepted|superseded>] [--json]")
	case "spec req delete":
		fmt.Println("Usage: az spec req delete --id <req-id> --confirm [--json]")
	case "spec link":
		cli.PrintSpecLinkUsage()
	case "spec link list":
		fmt.Println("Usage: az spec link list [--json] [--issue <issue-id>] [--req <req-id>] [--id <link-id> ...] [--ids a,b,c]")
	case "spec link add":
		fmt.Println("Usage: az spec link add --issue <issue-id> --req <req-id> [--role <implements|verifies|relates>] [--note <text>] [--json]")
	case "spec link remove":
		fmt.Println("Usage: az spec link remove --issue <issue-id> --req <req-id> [--json]")
	case "spec read":
		cli.PrintSpecReadUsage()
	case "spec pack":
		cli.PrintSpecPackUsage()
	case "spec graph":
		cli.PrintSpecGraphUsage()
	case "spec slice", "spec slice gate":
		cli.PrintSpecSliceUsage()
	case "spec lint":
		cli.PrintSpecLintUsage()
	case "spec parity":
		cli.PrintSpecParityUsage()
	case "decision":
		printDecisionUsage()
	case "decision list":
		fmt.Println("Usage: az decision list [--json] [--issue <issue-id>] [--req <req-id>] [--id <id> ...] [--query <text>]")
	case "decision get":
		fmt.Println("Usage: az decision get --id <id> [--with-links] [--json]")
	case "decision record":
		fmt.Println("Usage: az decision record --title <text> --rationale <text> [--context <text>] [--consequences <text>] [--issue <id> ...] [--req <id> ...] [--json]")
	case "decision update":
		fmt.Println("Usage: az decision update --id <id> [--title <text>] [--rationale <text>] [--context <text>] [--consequences <text>] [--json]")
	case "decision delete":
		fmt.Println("Usage: az decision delete --id <id> --confirm [--json]")
	case "decision revisit":
		fmt.Println("Usage: az decision revisit --id <old-id> (--new <existing-id> | --title <text> --rationale <text>) [--context <text>] [--note <text>] [--json]")
	case "decision sync":
		fmt.Println("Usage: az decision sync [--check] [--project-dir <dir>] [--json]")
	case "decision import":
		fmt.Println("Usage: az decision import [--check] [--force] [--project-dir <dir>] [--json]")
	case "decision link":
		printDecisionLinkUsage()
	case "decision link list":
		fmt.Println("Usage: az decision link list [--json] [--id <decision-id>] [--kind <issue|requirement|decision>] [--target <id>]")
	case "decision link add":
		fmt.Println("Usage: az decision link add --id <decision-id> (--issue <id> | --req <id> | --decision <id>) [--relation <applies-to|revises|informs>] [--note <text>] [--json]")
	case "decision link remove":
		fmt.Println("Usage: az decision link remove --id <decision-id> (--issue <id> | --req <id> | --decision <id>) [--json]")
	case "learn":
		printLearnUsage()
	case "interaction", "interaction list", "interaction get", "interaction discuss", "interaction answer", "interaction resolve", "interaction withdraw":
		cli.PrintInteractionUsage()
	case "learn add":
		fmt.Println("Usage: az learn add --evidence <text> [--summary <text>] [--private] [--issue <id>] [--req <id>] [--tag <tag> ...] [--file <path> ...] [--json]")
	case "learn recall":
		fmt.Println("Usage: az learn recall [--query <text>] [--issue <id>] [--req <id>] [--status <status> ...] [--tag <tag> ...] [--file <path> ...] [--limit N] [--include-evidence] [--include-private] [--json]")
	case "learn show":
		fmt.Println("Usage: az learn show <learning-id> [--json]")
	case "learn review":
		fmt.Println("Usage: az learn review [--queue-status <status> ...] [--issue <id>] [--req <id>] [--tag <tag> ...] [--file <path> ...] [--target-state active|retired|drifted|missing ...] [--older-than 30d] [--limit N] [--json]")
	case "learn stale":
		fmt.Println("Usage: az learn stale --note <text> <learning-id> [--json]")
	case "learn demote":
		fmt.Println("Usage: az learn demote --note <text> <learning-id> [--json]")
	case "learn promote":
		fmt.Println("Usage: az learn promote --target rulesync|agents|skill|spec|decision [--target-id <id-or-path>] <learning-id> [--create-target] [--target-title <text>] [--target-description <text>] [--target-issue <id>] [--decision-rationale <text>] [--decision-context <text>] [--decision-consequences <text>] [--note <text>] [--target-hash <hash>] [--target-meta key=value ...] [--json]")
	case "learn retire":
		fmt.Println("Usage: az learn retire --note <text> <learning-id> [--json]")
	case "learn relate":
		fmt.Println("Usage: az learn relate --type supersedes|conflicts --note <text> [--scope-issue <id>] [--scope-req <id>] [--scope-session <id>] [--scope-tag <tag> ...] [--scope-file <path> ...] <source-learning-id> <target-learning-id> [--json]")
	case "learn supersede":
		fmt.Println("Usage: az learn supersede --note <text> [--scope-issue <id>] [--scope-req <id>] [--scope-session <id>] [--scope-tag <tag> ...] [--scope-file <path> ...] <new-learning-id> <old-learning-id> [--json]")
	case "learn doctor":
		fmt.Println("Usage: az learn doctor [--candidate-older-than-days N] [--inactive-older-than-days N] [--limit N] [--json]")
	case "learn gc":
		fmt.Println("Usage: az learn gc [--confirm] [--candidate-older-than-days N] [--inactive-older-than-days N] [--limit N] [--json]")
	case "sync":
		fmt.Println("Usage: az sync [conflicts] [--all] [<directory>] [--project-dir <dir>] [--json]")
	case "githooks":
		cli.PrintGitHooksUsage()
	case "githooks install":
		fmt.Println("Usage: az githooks install [--project-dir <dir>] [--verbose]")
	case "githooks update":
		fmt.Println("Usage: az githooks update [--project-dir <dir>] [--verbose]")
	case "githooks run":
		fmt.Println("Usage: az githooks run [--project-dir <dir>] [--verbose] [-- <hook-args>...]")
	case "githooks notify":
		fmt.Println("Usage: az githooks notify [--project-dir <dir>] [--hook <name>] [--verbose] [-- <hook-args>...]")
	case "githooks hook":
		fmt.Println("Usage: az githooks hook --hook <name> [--project-dir <dir>] [--verbose] [-- <hook-args>...]")
	case "gate":
		cli.PrintGateUsage()
	case "dev":
		printDevUsage()
	case "dev gate":
		fmt.Println("Usage: az dev gate <issue-id> [--project-dir <dir>] [--verbose] [--fix]")
	case "dev start":
		printDevStartUsage()
	case "dev stop":
		printDevStopUsage()
	case "dev restart":
		printDevRestartUsage()
	case "dev status":
		printDevStatusUsage()
	case "dev list":
		printDevListUsage()
	case "project":
		printProjectUsage()
	case "project list":
		printProjectListUsage()
	case "project add":
		printProjectAddUsage()
	case "project remove":
		printProjectRemoveUsage()
	case "project scripts":
		printProjectScriptsUsage()
	case "project scripts status":
		printProjectScriptsStatusUsage()
	case "impl":
		printImplUsage()
	case "impl list":
		fmt.Println("Usage: az impl list")
	case "impl delete":
		fmt.Println("Usage: az impl delete --confirm <implementation>")
	case "impl migrate":
		fmt.Println("Usage: az impl migrate <from-implementation> <to-implementation>")
	case "ai":
		cli.PrintAIUsage()
		cli.PrintAIInstallUsage()
		cli.PrintAIStatusUsage()
		cli.PrintAIUninstallUsage()
		cli.PrintAIMigrateUsage()
		cli.PrintAIAccountUsage()
	case "ai install":
		cli.PrintAIInstallUsage()
	case "ai account", "ai account backup", "ai account list", "ai account status", "ai account activate", "ai account delete":
		cli.PrintAIAccountUsage()
	case "ai status":
		cli.PrintAIStatusUsage()
	case "ai uninstall":
		cli.PrintAIUninstallUsage()
	case "ai migrate":
		cli.PrintAIMigrateUsage()
	case "ai hook":
		fmt.Println("Usage: az ai hook run --agent=<claude|codex|opencode> [--json] <event>")
	case "ai hook run":
		fmt.Println("Usage: az ai hook run --agent=<claude|codex|opencode> [--json] <event>")
	case "tmux":
		cli.PrintTmuxUsage()
	case "tmux selector":
		fmt.Println("Usage: az tmux selector")
	case "tmux install-selector":
		fmt.Println("Usage: az tmux install-selector [--config <path>] [--project-dir <dir>] [--key <key>] [--az-command <command>] [--verbose]")
	case "tmux uninstall-selector":
		fmt.Println("Usage: az tmux uninstall-selector [--config <path>] [--verbose]")
	case "prime":
		fmt.Println("Usage: az prime")
	case "ticket", "issue":
		printIssueHelp()
	case "issue list":
		fmt.Println(issueListUsage)
	case "issue search":
		fmt.Println(issueSearchUsage)
	case "issue get":
		fmt.Println(issueGetUsage)
	case "issue claim":
		fmt.Println(issueClaimUsage)
	case "issue takeover":
		fmt.Println(issueTakeoverUsage)
	case "issue release":
		fmt.Println(issueReleaseUsage)
	case "issue events":
		fmt.Println(issueEventsUsage)
	case "issue record":
		fmt.Println(issueRecordUsage)
	case "issue context-risk":
		fmt.Println(issueContextRiskUsage)
	case "issue get-many":
		fmt.Println(issueGetManyUsage)
	case "issue check":
		fmt.Println(issueCheckUsage)
	case "issue doctor":
		fmt.Println(issueDoctorUsage)
	case "issue create":
		printIssueCreateUsage(os.Stdout)
	case "issue split":
		printIssueSplitUsage(os.Stdout)
	case "issue update":
		printIssueUpdateUsage(os.Stdout)
	case "issue close":
		printIssueCloseUsage(os.Stdout)
	case "issue cleanup":
		fmt.Println("Usage: az ticket cleanup [--project <project-id>] [--id <ticket-id> ...|--ids a,b] [--status <status> ...] [--query text] [--updated-before date] [--limit N] [--action closed|cancelled] [--dry-run] [--per-ticket-timeout duration] [--json]")
	case "issue delete":
		fmt.Println(issueDeleteUsage)
	case "issue unarchive":
		fmt.Println(issueUnarchiveUsage)
	case "issue image":
		fmt.Println("Usage: az ticket image <add|remove> [arguments]")
	case "issue image add":
		fmt.Println(issueImageAddUsage)
	case "issue image remove":
		fmt.Println(issueImageRemoveUsage)
	case "issue document":
		fmt.Println("Usage: az ticket document <add|list|remove> [arguments]")
	case "issue document add":
		fmt.Println(issueDocumentAddUsage)
	case "issue document list":
		fmt.Println(issueDocumentListUsage)
	case "issue document remove":
		fmt.Println(issueDocumentRemoveUsage)
	case "issue dep":
		fmt.Println("Usage: az ticket dep <add|remove|bulk> [arguments]")
	case "issue dep add":
		fmt.Println(issueDepAddUsage)
	case "issue dep remove":
		fmt.Println(issueDepRemoveUsage)
	case "issue dep bulk":
		fmt.Println("Usage: az ticket dep bulk apply [--project <project-id>] --input <path> [--dry-run] [--json]")
	case "issue dep bulk apply":
		fmt.Println(issueDepBulkApplyUsage)
	case "issue bulk-create":
		fmt.Println(issueBulkCreateUsage)
	case "issue bulk-update":
		fmt.Println(issueBulkUpdateUsage)
	case "issue fanout":
		fmt.Println(issueFanoutUsage)
	case "issue fanout ready":
		fmt.Println(issueFanoutReadyUsage)
	case "issue fanout drift":
		fmt.Println(issueFanoutDriftUsage)
	case "mail":
		fmt.Println("Usage: az mail <send|list|watch|validate-evidence> [arguments]")
	case "mail send":
		fmt.Println(mailSendUsage)
	case "mail list":
		fmt.Println(mailListUsage)
	case "mail watch":
		fmt.Println(mailWatchUsage)
	case "mail validate-evidence":
		fmt.Println(mailValidateEvidenceUsage)
	case "evidence":
		fmt.Println("Usage: az evidence <validate> [arguments]")
	case "evidence validate":
		fmt.Println(evidenceValidateUsage)
	case "observe":
		fmt.Println(observeUsage)
	case "orchestrate":
		fmt.Println("Usage: az orchestrate <status|start|group|watch|observe|prompt|message|capture|complete-check|integrate|close-session> [arguments]")
	case "orchestrator-session":
		fmt.Println(orchestratorSessionUsage)
	case "orchestrator-session start", "orchestrator-session attach", "orchestrator-session status":
		fmt.Println(orchestratorSessionUsage)
	case "orchestrate status":
		fmt.Println(orchestrateStatusUsage)
	case "orchestrate start":
		fmt.Println(orchestrateStartUsage)
	case "orchestrate group":
		fmt.Println(orchestrateGroupUsage)
	case "orchestrate watch":
		fmt.Println(orchestrateWatchUsage)
	case "orchestrate observe":
		fmt.Println(orchestrateObserveUsage)
	case "orchestrate prompt":
		fmt.Println(orchestratePromptUsage)
	case "orchestrate message":
		fmt.Println(orchestrateMessageUsage)
	case "orchestrate capture":
		fmt.Println(orchestrateCaptureUsage)
	case "orchestrate complete-check":
		fmt.Println(orchestrateCompleteCheckUsage)
	case "orchestrate integrate":
		fmt.Println(orchestrateIntegrateUsage)
	case "orchestrate close-session":
		fmt.Println(orchestrateCloseSessionUsage)
	case "daemon":
		fmt.Println("Usage: az daemon <start|stop|restart|watch-clients>")
	case "daemon start":
		fmt.Println("Usage: az daemon start")
	case "daemon stop":
		fmt.Println("Usage: az daemon stop")
	case "daemon restart":
		fmt.Println("Usage: az daemon restart")
	case "daemon watch-clients":
		fmt.Println("Usage: az daemon watch-clients [--json] [--all]")
	default:
		return false
	}
	if legacyIssuePath {
		fmt.Println("Compatibility: `az issue` is deprecated; use `az ticket`.")
	}
	return true
}

func printIssueHelp() {
	helpText, err := clitext.Render("issue_help", nil)
	if err != nil {
		fmt.Println("Usage: az ticket <list|search|get|claim|takeover|release|events|record|context-risk|get-many|check|doctor|create|split|update|close|cleanup|delete|unarchive|image|document|dep|bulk-create|bulk-update|fanout> [arguments]")
		return
	}
	fmt.Print(helpText)
}

func printImplUsage() {
	fmt.Println("Usage:")
	fmt.Println("  az impl list")
	fmt.Println("  az impl delete --confirm <implementation>")
	fmt.Println("  az impl migrate <from-implementation> <to-implementation>")
}

func printIssueUpdateUsage(w *os.File) {
	fmt.Fprintln(w, issueUpdateUsage)
	fmt.Fprintln(w, "Note: setting --status closed integrates the issue branch, cleans session/worktree attachments, then closes; --status cancelled runs close cleanup without integration; --force-worktree only applies to terminal close statuses.")
	fmt.Fprintln(w, "Note: --cascade-children only applies to --status in_review and moves open/in_progress descendants to in_review first.")
	fmt.Fprintln(w, "Note: --update-impl is only for changing implementation assignments; normal field updates do not require it.")
}

func printIssueCloseUsage(w *os.File) {
	fmt.Fprintln(w, issueCloseUsage)
	fmt.Fprintln(w, "Note: close integrates the issue branch, cleans session/worktree attachments, then writes closed status.")
	fmt.Fprintln(w, "Note: --force-worktree forces worktree removal after integration.")
}

const (
	issueListUsage                = "Usage: az ticket list [--project <project-id>] [--json] [--deps] [--query <text>|-q <text>] [--created-after YYYY-MM-DD] [--created-before YYYY-MM-DD] [--updated-after YYYY-MM-DD] [--updated-before YYYY-MM-DD] [--status <status> ...] [--statuses a,b,c] [--limit N] [--id <id> ...] [--ids a,b,c] [--parent <id> ...] [--parents a,b,c] [--depends-on <id> ...] [--depends-on-ids a,b,c]"
	issueSearchUsage              = "Usage: az ticket search [--project <project-id>] [--json] [--deps] [--created-after YYYY-MM-DD] [--created-before YYYY-MM-DD] [--updated-after YYYY-MM-DD] [--updated-before YYYY-MM-DD] [--status <status> ...] [--statuses a,b,c] [--limit N] [--id <id> ...] [--ids a,b,c] [--parent <id> ...] [--parents a,b,c] [--depends-on <id> ...] [--depends-on-ids a,b,c] (--query <text>|-q <text>|<query>)"
	issueGetUsage                 = "Usage: az ticket get [--project <project-id>] [--id <ticket-id>] [--json] [--with-notes] [<ticket-id>]"
	issueClaimUsage               = "Usage: az ticket claim [--project <project-id>] [--id <ticket-id>] [--owner <owner-id>] [--kind human|agent|orchestrator] [--ttl 2h] [--force] [--json] [<ticket-id>]"
	issueTakeoverUsage            = "Usage: az ticket takeover [--project <project-id>] [--id <ticket-id>] [--owner <owner-id>] [--kind human|agent|orchestrator] [--ttl 2h] [--json] [<ticket-id>]"
	issueReleaseUsage             = "Usage: az ticket release [--project <project-id>] [--id <ticket-id>] [--owner <owner-id>] [--force] [--json] [<ticket-id>]"
	issueEventsUsage              = "Usage: az ticket events [--project <project-id>] [--id <ticket-id>] [--json] [--jq-help] [--type <event-type> ...] [--types a,b] [--limit N] [<ticket-id>]"
	issueRecordUsage              = "Usage: az ticket record [--project <project-id>] [--id <ticket-id>] [--type <event-type>] [--summary <text>] [--body <text>] [--data <json-object>] [--follow-up <ticket-id> ...] [--json] [<ticket-id>]"
	issueContextRiskUsage         = "Usage: az ticket context-risk [--project <project-id>] [--id <ticket-id>] [--since 14d] [--summary|--full] [--json] [<ticket-id>]"
	issueGetManyUsage             = "Usage: az ticket get-many [--project <project-id>] --id <ticket-id> [--id <ticket-id> ...] [--ids a,b,c] [--json] [--with-notes]"
	issueCheckUsage               = "Usage: az ticket check [--project <project-id>] [--id <ticket-id>] [--json] [<ticket-id>]"
	issueDoctorUsage              = "Usage: az ticket doctor [--project <project-id>] [--id <ticket-id>] [--checkpoint-wal] [--truncate-wal] [--json] [<ticket-id>]"
	issueUpdateUsage              = "Usage: az ticket update [--project <project-id>] [--id <ticket-id>] [--json] [<ticket-id>] [--title text] [--description text] [--notes text] [--append-notes text] [--status backlog|open|in_progress|in_review|closed|cancelled] [--cascade-children] [--force-worktree] [--type task|bug|feature|epic|chore|investigation] [--priority P0|P1|P2|P3|P4] [--update-impl <impl> ...]"
	issueCloseUsage               = "Usage: az ticket close [--project <project-id>] [--id <ticket-id>|-i <ticket-id>] [--json] [--force-worktree] [--close-clean-children] [<ticket-id>]"
	issueDeleteUsage              = "Usage: az ticket delete [--project <project-id>] --confirm [--id <ticket-id>] [--json] [<ticket-id>]"
	issueUnarchiveUsage           = "Usage: az ticket unarchive [--project <project-id>] [--id <ticket-id>] [--json] [--with-parents] [--cascade-children] [<ticket-id>]"
	issueImageAddUsage            = "Usage: az ticket image add [--project <project-id>] [--ticket-id <ticket-id>] [--path <file>] [<ticket-id> <file>] [--json]"
	issueImageRemoveUsage         = "Usage: az ticket image remove [--project <project-id>] [--ticket-id <ticket-id>] [--attachment-id <attachment-id>] [<ticket-id> <attachment-id>] [--json]"
	issueDocumentAddUsage         = "Usage: az ticket document add [--project <project-id>] [--ticket-id <ticket-id>] [--path <file>] [<ticket-id> <file>] [--json]"
	issueDocumentListUsage        = "Usage: az ticket document list [--project <project-id>] [--ticket-id <ticket-id>] [<ticket-id>] [--json]"
	issueDocumentRemoveUsage      = "Usage: az ticket document remove [--project <project-id>] [--ticket-id <ticket-id>] [--attachment-id <attachment-id>] [<ticket-id> <attachment-id>] [--json]"
	issueDepAddUsage              = "Usage: az ticket dep add [--project <project-id>] [--ticket-id <ticket-id>] [--depends-on-id <depends-on-id>] [<ticket-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--force-parent-change] [--json]"
	issueDepRemoveUsage           = "Usage: az ticket dep remove [--project <project-id>] [--ticket-id <ticket-id>] [--depends-on-id <depends-on-id>] [<ticket-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--confirm] [--confirm-parent-orphan] [--json]"
	issueDepBulkApplyUsage        = "Usage: az ticket dep bulk apply [--project <project-id>] --input <path> [--dry-run] [--json]"
	issueBulkCreateUsage          = "Usage: az ticket bulk-create [--project <project-id>] [--impl <implementation>] --input <path> [--dry-run] [--json]"
	issueBulkUpdateUsage          = "Usage: az ticket bulk-update [--project <project-id>] [--impl <implementation>] --input <path> [--dry-run] [--json]"
	issueFanoutUsage              = "Usage: az ticket fanout [--project <project-id>] --input <path> [--apply] [--json]"
	issueFanoutReadyUsage         = "Usage: az ticket fanout ready [--project <project-id>] --root <ticket-id> [--json]"
	issueFanoutDriftUsage         = "Usage: az ticket fanout drift [--project <project-id>] --ticket <ticket-id> [--worktree <path>] [--json] [--fail-on-out]"
	mailSendUsage                 = "Usage: az mail send --parent <issue-id> --type <event-type> --body <text> [--issue <issue-id>] [--from <actor>] [--to <actor>] [--json]"
	mailListUsage                 = "Usage: az mail list --parent <issue-id> [--since <seq>] [--limit <n>] [--json]"
	mailWatchUsage                = "Usage: az mail watch --parent <issue-id> [--since <seq>] [--jsonl] [--once]"
	mailValidateEvidenceUsage     = "Usage: az mail validate-evidence [--body <json>|--file <path>] [--fix] [--template] [--json]"
	evidenceValidateUsage         = "Usage: az evidence validate [--body <json>|--file <path>] [--fix] [--template] [--json]"
	observeUsage                  = "Usage: az observe [--root <issue-id>] [--project <project-id>] [--json]"
	orchestrateStatusUsage        = "Usage: az orchestrate status [--root <issue-id>] [--project <project-id>] [--since <seq>] [--limit <n>] [--json] [--summary|--full]"
	orchestrateStartUsage         = "Usage: az orchestrate start [--root <issue-id>] [--project <project-id>] [--limit <n>] [--issue <issue-id> ...] [--override-board-health] [--json]"
	orchestrateGroupUsage         = "Usage: az orchestrate group --root <issue-id> --nested <issue-id> --issue <issue-id> ... [--project <project-id>] [--json]"
	orchestrateWatchUsage         = "Usage: az orchestrate watch [--root <issue-id>] [--project <project-id>] [--since <seq>] [--jsonl] [--once] [--verbose|--full]"
	orchestrateObserveUsage       = "Usage: az orchestrate observe --root <issue-id> [--project <project-id>] [--json]"
	orchestratePromptUsage        = "Usage: az orchestrate prompt --issue <issue-id> [--root <issue-id>] [--coordination native|mailbox] [--project <project-id>] [--json]"
	orchestrateMessageUsage       = "Usage: az orchestrate message --root <issue-id> --issue <issue-id> --body <text> [--type <event-type>] [--force-self-delivery] [--project <project-id>] [--json]"
	orchestrateCaptureUsage       = "Usage: az orchestrate capture --issue <issue-id> [--project <project-id>] [--lines N] [--raw] [--json]"
	orchestrateCompleteCheckUsage = "Usage: az orchestrate complete-check [--root <issue-id>] [--project <project-id>] [--json]"
	orchestratorSessionUsage      = "Usage: az orchestrator-session <start|attach|status> [--root <issue-id>] [--project <project-id>] [--json]"
	orchestrateIntegrateUsage     = "Usage: az orchestrate integrate --issue <issue-id> [--apply] [--project <project-id>] [--json]"
	orchestrateCloseSessionUsage  = "Usage: az orchestrate close-session --issue <issue-id> [--project <project-id>] [--json]"
)
