package conversations

// TaskProvider exposes an ordered iterator over the tasks it provides.
type TaskProvider interface {
	// Tasks visits each provided task in provider order, calling fn once
	// per task
	Tasks(fn func(Task) error) error
}

type Task interface {
	ID() string
	Prompt() (string, error)
}
