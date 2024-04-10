package ansible

const (
	// https://github.com/ansible/awx/blob/devel/docs/job_events.md#job-event-relationships
	// outlines various event types and the relationships between them
	eventTypeRunnerFailed      = "runner_on_failed"
	eventTypeRunnerUnreachable = "runner_on_unreachable"
)

// jobEvent represents [ansible-runner's job events](https://ansible.readthedocs.io/projects/runner/en/stable/intro/#artifactevents)
type jobEvent struct {
	UUID      string         `json:"uuid"`
	Stdout    string         `json:"stdout"`
	Event     string         `json:"event"`
	EventData map[string]any `json:"event_data"`
}

type runnerEventData struct {
	Play         string       `json:"play"`
	Task         string       `json:"task"`
	Host         string       `json:"host"`
	Result       runnerResult `json:"res"`
	IgnoreErrors bool         `json:"ignore_errors"`
}

type runnerResult struct {
	Msg string `json:"msg"`
}
