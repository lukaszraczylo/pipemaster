//go:build behave

// Package behave runs converted pipelines on real CI engines and compares what they do with the source run.
package behave

import (
	"errors"
	"fmt"
	"time"
)

// Event is one simulated trigger. Branch is empty for a tag.
type Event struct {
	Name   string
	Kind   string
	Branch string
	Target string
	Tag    string
}

// Ref returns the full git ref of the event.
func (e Event) Ref() string {
	if e.Tag != "" {
		return "refs/tags/" + e.Tag
	}
	return "refs/heads/" + e.Branch
}

// Event names a fixture can list.
const (
	EvPushMain   = "push-main"
	EvPushBranch = "push-branch"
	EvTag        = "tag"
	EvPR         = "pr"
)

// Event kinds.
const (
	kindPush = "push"
	kindTag  = "tag"
	kindPR   = "pull_request"
)

const (
	mainBranch    = "main"
	featureBranch = "feature/x"
	tagName       = "v1.0.0"
)

var events = map[string]Event{
	EvPushMain:   {Name: EvPushMain, Kind: kindPush, Branch: mainBranch},
	EvPushBranch: {Name: EvPushBranch, Kind: kindPush, Branch: featureBranch},
	EvTag:        {Name: EvTag, Kind: kindTag, Tag: tagName},
	EvPR:         {Name: EvPR, Kind: kindPR, Branch: featureBranch, Target: mainBranch},
}

// Trace is what one engine run did.
type Trace struct {
	Log      string
	Markers  []string
	Duration time.Duration
	Exit     int
}

// BlockedError reports that an engine cannot run a pair, with the reason.
type BlockedError struct{ Reason string }

func (e *BlockedError) Error() string { return "blocked: " + e.Reason }

func blocked(format string, args ...any) error {
	return &BlockedError{Reason: fmt.Sprintf(format, args...)}
}

func asBlocked(err error) (*BlockedError, bool) {
	var b *BlockedError
	if errors.As(err, &b) {
		return b, true
	}
	return nil, false
}

// Status values of one matrix cell.
const (
	StatusPass      = "pass"
	StatusExplained = "diff-explained"
	StatusFail      = "FAIL"
	StatusBlocked   = "blocked"
)

// Result is one source x target x fixture x event cell.
type Result struct {
	Source, Target, Fixture, Event string
	Status                         string
	Detail                         string
	Explained                      string
	SourceTime, TargetTime         time.Duration
}
