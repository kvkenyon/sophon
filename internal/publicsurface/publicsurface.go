// Package publicsurface owns every value Sophon may write to a public Git or
// forge surface. Callers render through this package and run Preflight before
// any push or forge write.
package publicsurface

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"

	"sophon/internal/domain"
)

const (
	MaxTitleLength  = 120
	MaxBranchLength = 200
	MaxBodyLength   = 60_000
)

var (
	internalID  = regexp.MustCompile(`(?i)\b(?:mission|task)_[0-9a-f]{12,}\b`)
	attemptRef  = regexp.MustCompile(`(?i)(?:^|[/_-])attempt[-_]?[0-9]+(?:$|[/_-])`)
	leaseRef    = regexp.MustCompile(`(?i)\b(?:lease[_ -]?id|lease[_ -]?holder)\b`)
	runtimeRef  = regexp.MustCompile(`(?i)\b(?:agent[_ -]?runtime|(?:pane|tab|workspace|session)[_ -]?id)\b|\bHERDR_[A-Z_]+\b`)
	localPath   = regexp.MustCompile(`(?i)(?:^|[\s"'=(])(?:/Users/|/home/|/var/folders/|/private/var/|/tmp/|[A-Z]:\\)`)
	localLink   = regexp.MustCompile(`(?i)\b(?:file://|https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0)(?::[0-9]+)?/)`)
	privateFile = regexp.MustCompile(`(?i)(?:^|[/\s])\.sophon(?:[/\s]|$)|\b(?:spawn|outcome|delivery|release|commander|wake|report)\.json\b|\bstate/[^\s]+\.status\b`)
	promptRef   = regexp.MustCompile(`(?i)\b(?:generated (?:task )?brief|generated prompt|completion command|worker complete)\b`)
)

// TaskTitle validates the concise public title recorded at task intake.
func TaskTitle(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("public title must be non-empty with no surrounding whitespace")
	}
	if len([]rune(value)) > MaxTitleLength {
		return fmt.Errorf("public title exceeds %d characters", MaxTitleLength)
	}
	if strings.ContainsAny(value, "\r\n") || hasControl(value, false) {
		return errors.New("public title must be one printable line")
	}
	return Validate("public title", value)
}

// Branch validates an explicit public delivery branch using Git ref rules and
// the same leak checks applied to every outbound surface.
func Branch(value string) error {
	if err := branchSyntax(value); err != nil {
		return err
	}
	return Validate("public delivery branch", value)
}

// ExistingBranch validates only Git ref safety for an already-public branch
// whose exact identity is immutable correction input. This compatibility path
// cannot create or rename a branch; every newly created task still uses Branch
// and the full leak detector.
func ExistingBranch(value string) error {
	return branchSyntax(value)
}

func branchSyntax(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("public delivery branch must be non-empty with no surrounding whitespace")
	}
	if len(value) > MaxBranchLength {
		return fmt.Errorf("public delivery branch exceeds %d bytes", MaxBranchLength)
	}
	if value == "@" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.Contains(value, "//") ||
		strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.ContainsAny(value, " ~^:?*[\\") ||
		hasControl(value, false) {
		return fmt.Errorf("public delivery branch %q is not a valid Git branch", value)
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("public delivery branch %q is not a valid Git branch", value)
		}
	}
	return nil
}

// Preflight validates the complete set of values that one delivery may make
// public. Field names remain local diagnostics and are never posted.
func Preflight(branch, title, body string, commitMessages []string) error {
	if err := Branch(branch); err != nil {
		return err
	}
	return preflightContent(title, body, commitMessages)
}

// PreflightExistingBranch applies the full outbound content preflight while
// retaining an exact already-public branch identity for same-PR correction.
func PreflightExistingBranch(branch, title, body string, commitMessages []string) error {
	if err := ExistingBranch(branch); err != nil {
		return err
	}
	return preflightContent(title, body, commitMessages)
}

func preflightContent(title, body string, commitMessages []string) error {
	if err := TaskTitle(title); err != nil {
		return err
	}
	if body == "" || len(body) > MaxBodyLength || hasControl(body, true) {
		return errors.New("public PR body must be non-empty printable text within the size limit")
	}
	if err := Validate("public PR body", body); err != nil {
		return err
	}
	for index, message := range commitMessages {
		if strings.TrimSpace(message) == "" {
			return fmt.Errorf("public commit message %d is empty", index+1)
		}
		if hasControl(message, true) {
			return fmt.Errorf("public commit message %d contains control characters", index+1)
		}
		if err := Validate(fmt.Sprintf("public commit message %d", index+1), message); err != nil {
			return err
		}
	}
	return nil
}

// Validate is the bounded defense-in-depth leak detector shared by intake,
// evidence curation, and final delivery preflight.
func Validate(field, value string) error {
	lower := strings.ToLower(value)
	var reason string
	switch {
	case strings.Contains(lower, "sophon"):
		reason = "product branding"
	case strings.Contains(lower, "firstmate") || strings.Contains(lower, "first mate") || strings.Contains(lower, "crewmate"):
		reason = "orchestrator identity"
	case internalID.MatchString(value):
		reason = "internal record identifier"
	case attemptRef.MatchString(value):
		reason = "attempt identity"
	case leaseRef.MatchString(value):
		reason = "lease identity"
	case strings.Contains(lower, "treehouse"):
		reason = "worktree allocator identity"
	case strings.Contains(lower, "herdr") || runtimeRef.MatchString(value):
		reason = "agent runtime identity"
	case strings.Contains(lower, "data_home") || strings.Contains(lower, "data-home") || strings.Contains(lower, "data home"):
		reason = "private data-home detail"
	case localPath.MatchString(value):
		reason = "local filesystem path"
	case localLink.MatchString(value):
		reason = "local-only link"
	case privateFile.MatchString(value):
		reason = "internal record path"
	case promptRef.MatchString(value):
		reason = "prompt mechanics"
	}
	if reason != "" {
		return fmt.Errorf("%s contains %s", field, reason)
	}
	return nil
}

// PullRequestBody renders maintainer-facing product evidence. Unsafe private
// result fragments are omitted; verification retains a safe command when one
// can be isolated and otherwise records only the successful outcome.
func PullRequestBody(title string, result domain.WorkerResult) string {
	var body strings.Builder
	body.WriteString("## Summary\n\n")
	summaryTitle := cleanLine(title)
	body.WriteString(summaryTitle)
	if summaryTitle != "" && !strings.ContainsAny(summaryTitle[len(summaryTitle)-1:], ".!?") {
		body.WriteByte('.')
	}
	body.WriteByte('\n')

	implementation := make([]string, 0, 1+len(result.ChangedFiles))
	if summary := safeLine(result.Summary); summary != "" && !strings.EqualFold(summary, title) {
		implementation = append(implementation, summary)
	}
	for _, changed := range result.ChangedFiles {
		clean := path.Clean(changed)
		if clean == "." || strings.HasPrefix(clean, "../") || hasControl(clean, false) || Validate("result changed file", clean) != nil {
			continue
		}
		implementation = append(implementation, "Updated `"+strings.ReplaceAll(clean, "`", "")+"`")
		if len(implementation) == 12 {
			break
		}
	}
	if len(implementation) > 0 {
		body.WriteString("\n## Implementation\n\n")
		for _, item := range implementation {
			body.WriteString("- ")
			body.WriteString(item)
			body.WriteByte('\n')
		}
	}

	body.WriteString("\n## Verification\n\n")
	for _, check := range result.Verification {
		command := safeVerification(check.Command)
		if command == "" {
			body.WriteString("- Verification completed successfully.\n")
		} else {
			body.WriteString("- `")
			body.WriteString(strings.ReplaceAll(command, "`", ""))
			body.WriteString("` (passed)\n")
		}
	}

	body.WriteString("\n## Risks\n\n")
	risks := 0
	for _, risk := range result.Risks {
		if safe := safeLine(risk); safe != "" {
			body.WriteString("- ")
			body.WriteString(safe)
			body.WriteByte('\n')
			risks++
		}
	}
	if risks == 0 {
		body.WriteString("- None identified.\n")
	}
	return body.String()
}

func safeVerification(command string) string {
	parts := strings.Split(command, "&&")
	for index := len(parts) - 1; index >= 0; index-- {
		fields := strings.Fields(parts[index])
		for len(fields) > 0 && strings.Contains(fields[0], "=") {
			fields = fields[1:]
		}
		candidate := strings.Join(fields, " ")
		if candidate != "" && !hasControl(candidate, false) && Validate("result verification", candidate) == nil {
			return candidate
		}
	}
	return ""
}

func safeLine(value string) string {
	value = cleanLine(value)
	if value == "" || hasControl(value, false) || Validate("result evidence", value) != nil {
		return ""
	}
	return value
}

func cleanLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func hasControl(value string, allowLayout bool) bool {
	for _, r := range value {
		if unicode.IsControl(r) && !(allowLayout && (r == '\n' || r == '\t')) {
			return true
		}
	}
	return false
}
